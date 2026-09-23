package offline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/api"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/pauserules"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// Get returns an item or offline.notFound.
func (e *Engine) Get(id string) (store.OfflineItem, error) {
	it, err := e.st.OfflineItem(id)
	if errors.Is(err, store.ErrNotFound) {
		return it, api.Errorf("offline.notFound", "Offline Item %s not found", id)
	}
	return it, err
}

// List returns all items.
func (e *Engine) List() ([]DTO, error) {
	items, err := e.st.OfflineItems()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]DTO, 0, len(items))
	for _, it := range items {
		out = append(out, e.dtoLocked(it))
	}
	return out, nil
}

// PreflightParams are offline.preflight params.
type PreflightParams struct {
	ConnectionID string   `json:"connectionId"`
	Kind         string   `json:"kind"`
	RemotePath   string   `json:"remotePath"`
	Files        []string `json:"files"`
	StoragePath  string   `json:"storagePath"`
}

// PreflightResult is offline.preflight's result.
type PreflightResult struct {
	RemoteBytes     int64  `json:"remoteBytes"`
	FreeBytes       uint64 `json:"freeBytes"`
	StorageNonEmpty bool   `json:"storageNonEmpty"`
	StoragePath     string `json:"storagePath"`
}

func (e *Engine) resolveStorage(conn store.Connection, remotePath, given string) (string, error) {
	if given == "" {
		return DefaultStoragePath(e.paths.Expand(e.settings().BaseFolder), conn.Name, remotePath), nil
	}
	p := filepath.Clean(e.paths.Expand(given))
	if !filepath.IsAbs(p) {
		return "", api.Invalid("storage location must be an absolute path")
	}
	return p, nil
}

// Preflight measures the remote size and the local free space.
func (e *Engine) Preflight(ctx context.Context, p PreflightParams) (PreflightResult, error) {
	conn, err := e.Conns.Get(p.ConnectionID)
	if err != nil {
		return PreflightResult{}, err
	}
	if e.Vaults != nil && e.Vaults.IsLocked(conn.ID) {
		return PreflightResult{}, api.Errorf("offline.vaultLocked", "Unlock the Vault first")
	}
	remotePath := strings.Trim(p.RemotePath, "/")
	sp, err := e.resolveStorage(conn, remotePath, p.StoragePath)
	if err != nil {
		return PreflightResult{}, err
	}
	res := PreflightResult{StoragePath: sp}
	res.RemoteBytes, err = remoteSize(conn.RcloneRemote, p.Kind, remotePath, p.Files)
	if err != nil {
		return res, api.Errorf("connection.testFailed", "%v", err)
	}
	res.FreeBytes = freeBytesNear(sp)
	res.StorageNonEmpty = storageNonEmpty(sp, p.Kind, p.Files)
	return res, nil
}

func remoteSize(remote, kind, remotePath string, files []string) (int64, error) {
	if kind == "files" {
		var res struct {
			List []struct {
				Name  string `json:"Name"`
				Size  int64  `json:"Size"`
				IsDir bool   `json:"IsDir"`
			} `json:"list"`
		}
		if err := rcl.CallInto("operations/list", map[string]any{"fs": remote + ":", "remote": remotePath}, &res); err != nil {
			return 0, err
		}
		var n int64
		for _, e := range res.List {
			if !e.IsDir && slices.Contains(files, e.Name) && e.Size > 0 {
				n += e.Size
			}
		}
		return n, nil
	}
	var res struct {
		Bytes int64 `json:"bytes"`
	}
	if err := rcl.CallInto("operations/size", map[string]any{"fs": remote + ":" + remotePath}, &res); err != nil {
		return 0, err
	}
	return max(res.Bytes, 0), nil
}

func freeBytesNear(p string) uint64 {
	for d := p; ; d = filepath.Dir(d) {
		if n, err := platform.FreeBytes(d); err == nil {
			return n
		}
		if d == "/" || d == "." {
			return 0
		}
	}
}

func storageNonEmpty(p, kind string, files []string) bool {
	if kind == "files" {
		for _, f := range files {
			if _, err := os.Lstat(filepath.Join(p, f)); err == nil {
				return true
			}
		}
		return false
	}
	d, err := os.Open(p)
	if err != nil {
		return false
	}
	defer d.Close()
	for {
		names, err := d.Readdirnames(32)
		for _, n := range names {
			if n != ".DS_Store" && n != ".localized" {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}

// CreateParams are offline.create params.
type CreateParams struct {
	ConnectionID  string    `json:"connectionId"`
	Kind          string    `json:"kind"`
	RemotePath    string    `json:"remotePath"`
	Files         []string  `json:"files"`
	StoragePath   string    `json:"storagePath"`
	Excludes      *[]string `json:"excludes"`
	MergeExisting bool      `json:"mergeExisting"`
}

// MountPoints lists the Mount points (storage locations must not overlap them).
func (e *Engine) mountPoints() []string {
	ms, _ := e.st.Mounts()
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.MountPoint)
	}
	return out
}

// Create adds an Offline Item (or appends files to an existing files item).
func (e *Engine) Create(ctx context.Context, p CreateParams) (DTO, error) {
	conn, err := e.Conns.Get(p.ConnectionID)
	if err != nil {
		return DTO{}, err
	}
	if e.Vaults != nil && e.Vaults.IsLocked(conn.ID) {
		return DTO{}, api.Errorf("offline.vaultLocked", "Unlock the Vault first")
	}
	it := store.OfflineItem{ID: store.NewID(), ConnectionID: conn.ID, Kind: p.Kind, RemotePath: strings.Trim(p.RemotePath, "/"),
		NeedsResync: true, State: StatePending, CreatedAt: store.Now(), Advanced: map[string]any{}}
	switch p.Kind {
	case "folder":
	case "files":
		if len(p.Files) == 0 {
			return DTO{}, api.Invalid("files must not be empty")
		}
		for _, f := range p.Files {
			if f == "" || strings.Contains(f, "/") || f == "." || f == ".." {
				return DTO{}, api.Invalid("invalid file name %q", f)
			}
			if !slices.Contains(it.Files, f) {
				it.Files = append(it.Files, f)
			}
		}
	default:
		return DTO{}, api.Invalid("kind must be folder or files")
	}
	if it.StoragePath, err = e.resolveStorage(conn, it.RemotePath, p.StoragePath); err != nil {
		return DTO{}, err
	}
	existing, err := e.st.OfflineItems()
	if err != nil {
		return DTO{}, err
	}
	v, err := validateNew(it, existing, e.mountPoints(), e.paths)
	if err != nil {
		return DTO{}, err
	}
	if v.mergeInto != nil {
		return e.mergeFiles(*v.mergeInto, it.Files)
	}
	pre, err := e.Preflight(ctx, PreflightParams{ConnectionID: conn.ID, Kind: it.Kind, RemotePath: it.RemotePath, Files: it.Files, StoragePath: it.StoragePath})
	if err != nil {
		return DTO{}, err
	}
	if float64(pre.FreeBytes) < 1.1*float64(pre.RemoteBytes) {
		return DTO{}, api.Errorf("offline.insufficientSpace", "Not enough free space: %s needed, %s available",
			humanBytes(uint64(float64(pre.RemoteBytes)*1.1)), humanBytes(pre.FreeBytes)).
			WithData("remoteBytes", pre.RemoteBytes).WithData("freeBytes", pre.FreeBytes)
	}
	if pre.StorageNonEmpty && !p.MergeExisting {
		return DTO{}, api.Errorf("offline.storageNotEmpty", "The folder %s is not empty", it.StoragePath)
	}
	if p.Excludes != nil {
		it.Excludes = *p.Excludes
	} else {
		it.Excludes = e.settings().DefaultExcludes
	}
	if err := os.MkdirAll(it.StoragePath, 0o755); err != nil {
		return DTO{}, api.Errorf("offline.locationMissing", "Cannot create %s: %v", it.StoragePath, err)
	}
	if err := e.st.InsertOfflineItem(it); err != nil {
		return DTO{}, err
	}
	if err := WriteFilters(e.paths, it); err != nil {
		return DTO{}, err
	}
	e.log.Info("offline", it.ID, fmt.Sprintf("Offline Item %q added at %s", itemName(it), it.StoragePath), nil)
	now := e.Now()
	e.mu.Lock()
	rt := e.itemRT(it.ID)
	rt.due = now
	e.scheduleChecks(it, rt, now)
	d := e.dtoLocked(it)
	e.mu.Unlock()
	if e.Watch {
		e.startWatching(it)
	}
	e.pub.Publish("offline.status", d)
	e.poke()
	return d, nil
}

func (e *Engine) mergeFiles(snapshot store.OfflineItem, files []string) (DTO, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ex, err := e.Get(snapshot.ID)
	if err != nil {
		return DTO{}, err
	}
	for _, f := range files {
		if !slices.Contains(ex.Files, f) {
			ex.Files = append(ex.Files, f)
		}
	}
	e.requestResyncLocked(&ex)
	if err := e.st.UpdateOfflineItem(ex); err != nil {
		return DTO{}, err
	}
	if err := WriteFilters(e.paths, ex); err != nil {
		return DTO{}, err
	}
	e.itemRT(ex.ID).due = e.Now()
	e.log.Info("offline", ex.ID, fmt.Sprintf("Added %d file(s) to Offline Item %q", len(files), itemName(ex)), files)
	d := e.dtoLocked(ex)
	e.pub.Publish("offline.status", d)
	e.poke()
	return d, nil
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// UpdateParams are offline.update params.
type UpdateParams struct {
	ID       string          `json:"id"`
	Excludes *[]string       `json:"excludes"`
	Advanced *map[string]any `json:"advanced"`
}

// Update changes excludes or advanced options; bisync then needs a resync.
func (e *Engine) Update(ctx context.Context, p UpdateParams) (DTO, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, err := e.Get(p.ID)
	if err != nil {
		return DTO{}, err
	}
	changed := false
	if p.Excludes != nil && !slices.Equal(*p.Excludes, it.Excludes) {
		it.Excludes, changed = *p.Excludes, true
	}
	if p.Advanced != nil {
		it.Advanced, changed = *p.Advanced, true
	}
	if changed {
		e.requestResyncLocked(&it)
		if err := e.st.UpdateOfflineItem(it); err != nil {
			return DTO{}, err
		}
		if err := WriteFilters(e.paths, it); err != nil {
			return DTO{}, err
		}
		e.itemRT(it.ID).due = e.Now()
		e.log.Info("offline", it.ID, fmt.Sprintf("Settings of %q changed", itemName(it)), nil)
		e.poke()
	}
	d := e.dtoLocked(it)
	e.pub.Publish("offline.status", d)
	return d, nil
}

// stopItem removes an item from the queue and stops its running job.
func (e *Engine) stopItem(id string) {
	e.mu.Lock()
	e.queue = slices.DeleteFunc(e.queue, func(q queued) bool { return q.itemID == id })
	e.itemRT(id).busy = true // until restartItem or removal
	var job Job
	if e.cur != nil && e.cur.q.itemID == id {
		e.cur.stopping = true
		job = e.cur.job
	}
	e.stopWatchingLocked(id)
	e.mu.Unlock()
	if job != nil {
		job.Stop(30 * time.Second)
		<-job.Done()
		// The finisher may have re-queued it.
		e.mu.Lock()
		e.queue = slices.DeleteFunc(e.queue, func(q queued) bool { return q.itemID == id })
		e.mu.Unlock()
	}
}

// Relocate moves an item's local files to newPath.
func (e *Engine) Relocate(ctx context.Context, id, newPath string) (DTO, error) {
	it, err := e.Get(id)
	if err != nil {
		return DTO{}, err
	}
	dst := filepath.Clean(e.paths.Expand(newPath))
	if dst == it.StoragePath {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.dtoLocked(it), nil
	}
	existing, err := e.st.OfflineItems()
	if err != nil {
		return DTO{}, err
	}
	others := slices.DeleteFunc(slices.Clone(existing), func(x store.OfflineItem) bool { return x.ID == id })
	probe := it
	probe.StoragePath = dst
	if _, err := validateNew(probe, others, e.mountPoints(), e.paths); err != nil {
		return DTO{}, err
	}
	if storageNonEmpty(dst, it.Kind, it.Files) {
		return DTO{}, api.Errorf("offline.storageNotEmpty", "The folder %s is not empty", dst)
	}
	e.stopItem(id)
	leftovers, err := moveItem(it, dst)
	if err != nil {
		e.restartItem(it)
		return DTO{}, api.Errorf("offline.locationMissing", "Moving the files failed: %v", err)
	}
	if len(leftovers) > 0 {
		// The data is complete at dst; the item must point there even if
		// cleaning up the old location failed.
		e.log.Warn("offline", it.ID, fmt.Sprintf("Moved %q, but some files could not be removed from %s", itemName(it), it.StoragePath),
			errorStrings(leftovers))
	}
	e.mu.Lock()
	fresh, err := e.Get(id)
	if err != nil {
		e.mu.Unlock()
		return DTO{}, err
	}
	fresh.StoragePath = dst
	e.requestResyncLocked(&fresh)
	if fresh.State == StatePaused && fresh.LastError == ReasonLocationMissing {
		fresh.State, fresh.LastError = StatePending, ""
	}
	err = e.st.UpdateOfflineItem(fresh)
	e.mu.Unlock()
	if err != nil {
		return DTO{}, err
	}
	it = fresh
	e.log.Info("offline", it.ID, fmt.Sprintf("Moved %q to %s", itemName(it), dst), nil)
	return e.restartItem(it), nil
}

func (e *Engine) restartItem(it store.OfflineItem) DTO {
	e.mu.Lock()
	rt := e.itemRT(it.ID)
	rt.busy = false
	rt.due = e.Now()
	rt.pendingPaths = map[string]bool{}
	d := e.dtoLocked(it)
	e.mu.Unlock()
	if e.Watch {
		e.startWatching(it)
	}
	e.pub.Publish("offline.status", d)
	e.poke()
	return d
}

// moveItem moves the local files: the whole folder, or each listed file. err
// means the data is not (completely) at dst; leftovers are sources that were
// copied but could not be deleted afterwards.
func moveItem(it store.OfflineItem, dst string) (leftovers []error, err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if it.Kind == "files" {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return nil, err
		}
		for _, f := range it.Files {
			src := filepath.Join(it.StoragePath, f)
			if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
				continue
			}
			left, err := movePath(src, filepath.Join(dst, f))
			if err != nil {
				return leftovers, err
			}
			if left != nil {
				leftovers = append(leftovers, left)
			}
		}
		return leftovers, nil
	}
	if _, err := os.Lstat(dst); err == nil {
		// An empty target directory exists: replace it.
		if err := os.Remove(dst); err != nil {
			return nil, err
		}
	}
	left, err := movePath(it.StoragePath, dst)
	if left != nil {
		leftovers = append(leftovers, left)
	}
	return leftovers, err
}

// movePath renames, or copies (preserving modification times) and then
// deletes the source when the target is on another volume. A failure to
// delete the source is returned as leftover, not as err.
func movePath(src, dst string) (leftover, err error) {
	err = os.Rename(src, dst)
	if err == nil {
		return nil, nil
	}
	var le *os.LinkError
	if !errors.As(err, &le) || !isCrossDevice(le.Err) {
		return nil, err
	}
	if err := copyTree(src, dst); err != nil {
		_ = os.RemoveAll(dst)
		return nil, err
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove %s: %w", src, err), nil
	}
	return nil, nil
}

func errorStrings(errs []error) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}

func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

func copyTree(src, dst string) error {
	var dirs []string
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case fi.IsDir():
			dirs = append(dirs, p)
			return os.MkdirAll(target, fi.Mode().Perm()|0o700)
		default:
			return copyFile(p, target, fi)
		}
	})
	if err != nil {
		return err
	}
	// Directory times last, after their contents were written.
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil {
			rel, _ := filepath.Rel(src, d)
			_ = os.Chtimes(filepath.Join(dst, rel), fi.ModTime(), fi.ModTime())
		}
	}
	return nil
}

func copyFile(src, dst string, fi os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, fi.ModTime(), fi.ModTime())
}

// Remove deletes an item. The cloud is never touched; the local copy goes to
// the Trash or stays.
func (e *Engine) Remove(ctx context.Context, id, localCopy string) error {
	it, err := e.Get(id)
	if err != nil {
		return err
	}
	if localCopy != "trash" && localCopy != "keep" {
		return api.Invalid("localCopy must be trash or keep")
	}
	e.stopItem(id)
	if localCopy == "trash" {
		targets := []string{it.StoragePath}
		if it.Kind == "files" {
			targets = nil
			for _, f := range it.Files {
				targets = append(targets, filepath.Join(it.StoragePath, f))
			}
		}
		for _, t := range targets {
			if _, err := os.Lstat(t); err != nil {
				continue
			}
			if err := platform.MoveToTrash(t); err != nil {
				e.restartItem(it)
				return api.Errorf("offline.locationMissing", "%v", err)
			}
		}
	}
	_ = os.RemoveAll(e.paths.BisyncWorkdir(id))
	_ = os.Remove(e.paths.FiltersFile(id))
	if err := e.st.DeleteOfflineItem(id); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.rt, id)
	e.mu.Unlock()
	how := "kept"
	if localCopy == "trash" {
		how = "moved to the Trash"
	}
	e.log.Info("offline", id, fmt.Sprintf("Offline Item %q removed; local copy %s", itemName(it), how), nil)
	it.State = "removed"
	e.pub.Publish("offline.status", DTO{OfflineItem: it})
	return nil
}

// SyncNow queues a run that ignores the Pause Rules (keeps background
// priority and the bandwidth limit). An empty id means every item.
func (e *Engine) SyncNow(id string) error {
	items, err := e.st.OfflineItems()
	if err != nil {
		return err
	}
	found := false
	e.mu.Lock()
	for _, it := range items {
		if id != "" && it.ID != id {
			continue
		}
		found = true
		e.enqueueLocked(queued{itemID: it.ID, ignorePause: true})
	}
	e.mu.Unlock()
	if id != "" && !found {
		return api.Errorf("offline.notFound", "Offline Item %s not found", id)
	}
	e.poke()
	return nil
}

// enqueueLocked adds or upgrades a queued run of an item.
func (e *Engine) enqueueLocked(q queued) {
	if q.migration == nil && e.itemRT(q.itemID).busy {
		return
	}
	for i := range e.queue {
		if e.queue[i].key() == q.key() {
			e.queue[i].ignorePause = e.queue[i].ignorePause || q.ignorePause
			e.queue[i].force = e.queue[i].force || q.force
			return
		}
	}
	if e.cur != nil && e.cur.q.key() == q.key() {
		// Running now: queue one more run afterwards.
		e.queue = append(e.queue, q)
		return
	}
	e.queue = append(e.queue, q)
}

// ConfirmMassDelete resolves a Mass-Delete Guard stop.
func (e *Engine) ConfirmMassDelete(ctx context.Context, id, action string) (DTO, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	it, err := e.Get(id)
	if err != nil {
		return DTO{}, err
	}
	switch action {
	case "delete":
		it.State, it.LastError = StatePending, ""
		if err := e.st.UpdateOfflineItem(it); err != nil {
			return DTO{}, err
		}
		e.enqueueLocked(queued{itemID: id, ignorePause: true, force: true})
		e.log.Info("offline", id, fmt.Sprintf("Deletions in %q confirmed", itemName(it)), nil)
	case "restore":
		it.State, it.LastError = StatePending, ""
		e.requestResyncLocked(&it)
		if err := e.st.UpdateOfflineItem(it); err != nil {
			return DTO{}, err
		}
		e.enqueueLocked(queued{itemID: id, ignorePause: true})
		e.log.Info("offline", id, fmt.Sprintf("Deleted files of %q will be restored", itemName(it)), nil)
	default:
		return DTO{}, api.Invalid("action must be delete or restore")
	}
	d := e.dtoLocked(it)
	e.pub.Publish("offline.status", d)
	e.poke()
	return d, nil
}

// StatusForPaths returns Finder badge states.
func (e *Engine) StatusForPaths(pathsIn []string) (map[string]string, error) {
	items, err := e.st.OfflineItems()
	if err != nil {
		return nil, err
	}
	label := e.label()
	marker := ConflictMarker(label)
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(pathsIn))
	for _, p := range pathsIn {
		cp := filepath.Clean(p)
		var it *store.OfflineItem
		for i := range items {
			if paths.IsWithin(cp, items[i].StoragePath) {
				it = &items[i]
				break
			}
		}
		if it == nil {
			out[p] = "none"
			continue
		}
		if it.Kind == "files" && cp != it.StoragePath {
			rel := strings.SplitN(strings.TrimPrefix(cp, it.StoragePath+"/"), "/", 2)[0]
			if !slices.Contains(it.Files, rel) && !strings.Contains(rel, marker) {
				out[p] = "none"
				continue
			}
		}
		rt := e.rt[it.ID]
		pending := rt != nil && rt.pendingPaths[cp]
		root := cp == it.StoragePath
		switch {
		case strings.Contains(filepath.Base(cp), marker):
			out[p] = "conflict"
		case it.State == StateError && (root || pending):
			out[p] = "error"
		case pending || (it.State == StateSyncing && root):
			out[p] = "syncing"
		default:
			out[p] = "synced"
		}
	}
	return out, nil
}

// Runs lists the sync runs of an item.
func (e *Engine) Runs(itemID string, limit int) ([]store.SyncRun, error) {
	return e.st.Runs(itemID, limit)
}

// RunFiles lists the files of a run.
func (e *Engine) RunFiles(runID int64) ([]store.SyncRunFile, error) {
	return e.st.RunFiles(runID)
}

// PauseStatus returns the manual pause and the currently active rules.
// It evaluates the rules fresh without recording them, so the scheduler still
// notices (logs and publishes) the transition itself.
func (e *Engine) PauseStatus() PauseStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	ps := e.pauseStatusLocked()
	if rules := e.Eval.Evaluate(); rules != nil {
		ps.ActiveRules = rules
	} else {
		ps.ActiveRules = []pauserules.ActiveRule{}
	}
	ps.Effective = ps.ManualUntil != nil || len(ps.ActiveRules) > 0
	return ps
}

// SetPause sets (until/indefinite) or clears the manual pause.
func (e *Engine) SetPause(until *int64, indefinite bool) PauseStatus {
	e.mu.Lock()
	switch {
	case indefinite:
		t := time.Time{}
		e.manualUntil = &t
		e.log.Info("offline", "", "Syncing paused until resumed", nil)
	case until != nil && *until > e.Now().UnixMilli():
		t := time.UnixMilli(*until)
		e.manualUntil = &t
		e.log.Info("offline", "", "Syncing paused until "+t.Format("2006-01-02 15:04"), nil)
	default:
		e.manualUntil = nil
		e.log.Info("offline", "", "Syncing resumed", nil)
	}
	if e.manualUntil != nil && e.cur != nil && !e.cur.q.ignorePause {
		e.stopCurrentLocked(pauserules.RuleManual)
	}
	ps := e.pauseStatusLocked()
	e.publishPauseLocked()
	e.mu.Unlock()
	e.poke()
	return ps
}

// EnqueueMigration queues a Vault migration job (runs like a sync job).
func (e *Engine) EnqueueMigration(req *MigrationRequest) {
	e.mu.Lock()
	e.queue = append(e.queue, queued{migration: req, ignorePause: req.IgnorePause})
	e.mu.Unlock()
	e.poke()
}

// PauseForConnection pauses the items of a (locked) Vault connection.
func (e *Engine) PauseForConnection(connID, reason string) {
	items, _ := e.st.OfflineItems()
	for _, it := range items {
		if it.ConnectionID == connID {
			e.stopItem(it.ID)
			e.mu.Lock()
			if fresh, err := e.st.OfflineItem(it.ID); err == nil {
				e.setStateLocked(fresh, StatePaused, reason)
			}
			e.mu.Unlock()
		}
	}
}

// ResumeForConnection re-activates the items of an unlocked Vault.
func (e *Engine) ResumeForConnection(connID string) {
	items, _ := e.st.OfflineItems()
	for _, it := range items {
		if it.ConnectionID == connID {
			e.restartItem(it)
		}
	}
}

// Sleep stops the running job gracefully before the Mac sleeps; it runs
// again (bisync --recover) after wake.
func (e *Engine) Sleep() {
	e.mu.Lock()
	r := e.cur
	if r != nil && !r.stopping {
		r.stopping = true
		e.queue = append([]queued{r.q}, e.queue...)
	}
	e.mu.Unlock()
	if r != nil {
		r.job.Stop(4 * time.Second)
	}
}

// Wake marks every item for a remote check (wake, network regained).
func (e *Engine) Wake() {
	now := e.Now()
	items, _ := e.st.OfflineItems()
	e.mu.Lock()
	for _, it := range items {
		rt := e.itemRT(it.ID)
		if rt.remote != nil && rt.remote.kind == remoteETag {
			rt.nextRemote = now
		} else {
			rt.due = minTime(rt.due, now)
		}
	}
	e.mu.Unlock()
	e.poke()
}

// Stop ends the scheduler and stops the running job gracefully.
func (e *Engine) Stop() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	r := e.cur
	if r != nil {
		r.stopping = true
	}
	for id := range e.rt {
		e.stopWatchingLocked(id)
	}
	started := e.started
	e.mu.Unlock()
	close(e.quit)
	if r != nil {
		r.job.Stop(30 * time.Second)
	}
	if started {
		<-e.done
	}
}

// BandwidthChanged applies a new bandwidth limit to the running job.
func (e *Engine) BandwidthChanged() {
	rate := pauserules.BwLimit(e.settings().Bandwidth)
	e.mu.Lock()
	r := e.cur
	e.mu.Unlock()
	if r != nil && r.job != nil {
		_ = r.job.Send(sv.Command{Cmd: "bwlimit", Rate: rate})
	}
}
