package offline

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsevents"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/cache"
	"github.com/rclone/rclone/fs/config/obscure"

	"github.com/DonMikone/CloudWire/core/internal/sharing/nextcloud"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

type watcher struct {
	es   *fsevents.EventStream
	root string // storage path with symlinks resolved
	once sync.Once
	done chan struct{}
}

func (w *watcher) stop() {
	w.once.Do(func() {
		close(w.done)
		w.es.Stop()
	})
}

const (
	remoteGeneric = iota
	remoteNotify
	remoteETag
)

type remoteWatch struct {
	kind   int
	cancel context.CancelFunc
	client *nextcloud.Client
	path   string // OCS path for ETag checks
}

func (e *Engine) startWatching(it store.OfflineItem) {
	e.startFSEvents(it)
	go e.startRemote(it)
}

func (e *Engine) stopWatchingLocked(id string) {
	rt := e.rt[id]
	if rt == nil {
		return
	}
	if rt.watch != nil {
		go rt.watch.stop()
		rt.watch = nil
	}
	if rt.remote != nil && rt.remote.cancel != nil {
		rt.remote.cancel()
	}
	rt.remote = nil
}

// startFSEvents watches the Storage Location (no-op if it does not exist yet).
func (e *Engine) startFSEvents(it store.OfflineItem) {
	root, err := filepath.EvalSymlinks(it.StoragePath)
	if err != nil {
		return
	}
	es := &fsevents.EventStream{
		Paths:   []string{root},
		Latency: time.Second,
		Flags:   fsevents.FileEvents | fsevents.WatchRoot,
	}
	if err := es.Start(); err != nil {
		slog.Warn("offline: fsevents", "path", root, "err", err)
		return
	}
	w := &watcher{es: es, root: root, done: make(chan struct{})}
	e.mu.Lock()
	rt := e.itemRT(it.ID)
	if rt.watch != nil || e.closed {
		e.mu.Unlock()
		w.stop()
		return
	}
	rt.watch = w
	e.mu.Unlock()
	go func() {
		// fsevents never closes Events, so the reader also ends on stop.
		for {
			select {
			case <-w.done:
				return
			case msg, ok := <-es.Events:
				if !ok {
					return
				}
				for _, ev := range msg {
					e.onLocalEvent(it.ID, w, ev)
				}
			}
		}
	}()
}

func (e *Engine) onLocalEvent(id string, w *watcher, ev fsevents.Event) {
	e.mu.Lock()
	rt := e.rt[id]
	current := rt != nil && rt.watch == w
	e.mu.Unlock()
	if !current {
		return
	}
	if ev.Flags&(fsevents.MustScanSubDirs|fsevents.RootChanged) != 0 {
		e.mu.Lock()
		rt.due = minTime(rt.due, e.Now())
		e.mu.Unlock()
		e.poke()
		return
	}
	p := ev.Path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if p == w.root || !strings.HasPrefix(p, w.root+"/") {
		return
	}
	e.localChange(id, strings.TrimPrefix(p, w.root+"/"), e.Now())
}

// localChange records a local modification of rel (relative to the Storage
// Location) at now: it restarts the Quiet Period and marks the path pending.
func (e *Engine) localChange(id, rel string, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rt := e.rt[id]
	if rt == nil {
		return
	}
	it, err := e.st.OfflineItem(id)
	if err != nil || rel == "" || Excluded(rel, it.Excludes) {
		return
	}
	if it.Kind == "files" && !slices.Contains(it.Files, strings.SplitN(rel, "/", 2)[0]) {
		return
	}
	if e.cur != nil && e.cur.q.itemID == id {
		rt.duringRun[rel] = true
		return
	}
	quiet := time.Duration(e.settings().QuietPeriodSeconds) * time.Second
	rt.pendingPaths[filepath.Join(it.StoragePath, rel)] = true
	rt.quietUntil = now.Add(quiet)
	rt.due = minTime(rt.due, rt.quietUntil)
	e.poke()
}

// startRemote sets up remote change detection for an item.
func (e *Engine) startRemote(it store.OfflineItem) {
	conn, err := e.Conns.Get(it.ConnectionID)
	if err != nil {
		return
	}
	cfg, err := e.Conns.Config(conn)
	if err != nil {
		return
	}
	rw := &remoteWatch{kind: remoteGeneric}
	if conn.Provider == "webdav" && (cfg["vendor"] == "nextcloud" || cfg["vendor"] == "owncloud") {
		base, root, err := nextcloud.DAVLocation(cfg["url"])
		if err == nil {
			pass, _ := obscure.Reveal(cfg["pass"])
			rw.kind = remoteETag
			rw.client = &nextcloud.Client{Base: base, User: cfg["user"], Pass: pass, DAVURL: cfg["url"]}
			rw.path = nextcloud.JoinPath(root, it.RemotePath)
		}
	} else if nw := e.changeNotify(conn, it); nw != nil {
		rw = nw
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	rt := e.rt[it.ID]
	if rt == nil || e.closed {
		if rw.cancel != nil {
			rw.cancel()
		}
		return
	}
	if rt.remote != nil && rt.remote.cancel != nil {
		rt.remote.cancel()
	}
	rt.remote = rw
	e.scheduleChecks(it, rt, e.Now())
}

// changeNotify subscribes to rclone ChangeNotify when the backend supports it.
func (e *Engine) changeNotify(conn store.Connection, it store.OfflineItem) *remoteWatch {
	ctx, cancel := context.WithCancel(context.Background())
	f, err := cache.Get(ctx, conn.RcloneRemote+":"+it.RemotePath)
	if (err != nil && err != fs.ErrorIsFile) || f.Features().ChangeNotify == nil {
		cancel()
		return nil
	}
	poll := make(chan time.Duration, 1)
	poll <- time.Duration(e.settings().PollIntervalSeconds) * time.Second
	f.Features().ChangeNotify(ctx, func(p string, _ fs.EntryType) { e.onRemoteChange(it.ID, p) }, poll)
	return &remoteWatch{kind: remoteNotify, cancel: func() {
		cancel()
		close(poll)
	}}
}

func (e *Engine) onRemoteChange(id, p string) {
	now := e.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	rt := e.rt[id]
	if rt == nil {
		return
	}
	// Even during a run (or right after one, when the change may be our own
	// upload) the item is marked due: one extra run is cheaper than a missed change.
	it, err := e.st.OfflineItem(id)
	if err != nil {
		return
	}
	if it.Kind == "files" && !slices.Contains(it.Files, strings.SplitN(strings.Trim(p, "/"), "/", 2)[0]) {
		return
	}
	rt.due = minTime(rt.due, now)
	e.poke()
}

// checkETag compares the remote root ETag with the stored one.
func (e *Engine) checkETag(it store.OfflineItem) {
	e.mu.Lock()
	rt := e.rt[it.ID]
	var rw *remoteWatch
	if rt != nil {
		rw = rt.remote
	}
	e.mu.Unlock()
	if rw == nil || rw.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	etag, err := rw.client.ETag(ctx, rw.path)
	if err != nil {
		slog.Debug("offline: etag check", "item", it.ID, "err", err)
		return
	}
	e.mu.Lock()
	if rt := e.rt[it.ID]; rt != nil {
		rt.seenETag = etag
	}
	e.mu.Unlock()
	fresh, err := e.st.OfflineItem(it.ID)
	if err != nil || fresh.RemoteETag == etag {
		return
	}
	e.onRemoteChange(it.ID, "")
}
