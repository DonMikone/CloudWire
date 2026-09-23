package offline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/notify"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/pauserules"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// Item states.
const (
	StatePending           = "pending"
	StateSyncing           = "syncing"
	StateIdle              = "idle"
	StatePaused            = "paused"
	StateError             = "error"
	StateNeedsConfirmation = "needsConfirmation"
)

// Pause reasons that are not Pause Rules.
const (
	ReasonLocationMissing = "location-missing"
	ReasonVaultLocked     = "vault-locked"
)

// Publisher pushes events.
type Publisher interface {
	Publish(eventType string, data any)
}

// Notifier queues notifications.
type Notifier interface {
	Notify(kind string, params any)
}

// Connections resolves Connections and their rclone config.
type Connections interface {
	Get(id string) (store.Connection, error)
	Config(c store.Connection) (map[string]string, error)
}

// Mounts refreshes Mount caches after a sync.
type Mounts interface {
	Forget(connID, remotePath string)
}

// Vaults reports whether a Vault pseudo-Connection is locked.
type Vaults interface {
	IsLocked(connID string) bool
}

// Job is a running worker (sv.Worker in production).
type Job interface {
	Done() <-chan struct{}
	Outcome() sv.Msg
	Stop(grace time.Duration)
	Send(sv.Command) error
}

// Runner starts workers.
type Runner func(job sv.Job, h sv.Handlers) (Job, error)

// Clock is replaceable in tests.
type Clock func() time.Time

// MigrationRequest is a queued Vault migration.
type MigrationRequest struct {
	JobID       string
	VaultID     string
	Job         sv.MigrateJob
	IgnorePause bool
	// OnResult receives the final worker result.
	OnResult func(sv.Msg)
	// OnStart is called when the job starts running.
	OnStart func()
}

type queued struct {
	itemID      string // bisync
	migration   *MigrationRequest
	ignorePause bool
	force       bool
}

func (q queued) key() string {
	if q.migration != nil {
		return "m:" + q.migration.JobID
	}
	return "i:" + q.itemID
}

type running struct {
	q         queued
	job       Job
	runID     int64
	started   time.Time
	stopping  bool
	files     map[string]string // path -> action
	resyncGen int               // item resync generation when the run started
}

type itemRT struct {
	due            time.Time // zero = not due
	quietUntil     time.Time // local changes postpone every run until this time
	nextRemote     time.Time
	nextSafety     time.Time
	retryAt        time.Time
	pendingPaths   map[string]bool // absolute paths changed locally since the last ok run
	duringRun      map[string]bool // relative paths changed while syncing
	progress       *Progress
	resyncGen      int    // bumped whenever a resync is requested
	busy           bool   // relocation or removal in progress: never schedule
	seenETag       string // latest remote ETag observed by a check
	runETag        string // ETag observed before the running sync started
	watch          *watcher
	remote         *remoteWatch
	lastNotifiedEr bool
}

// Progress is the offline.progress event payload.
type Progress struct {
	ID         string   `json:"id"`
	Bytes      int64    `json:"bytes"`
	TotalBytes int64    `json:"totalBytes"`
	Transfers  int64    `json:"transfers"`
	ETA        *float64 `json:"eta"`
}

// Engine runs the Offline Items.
type Engine struct {
	st         *store.Store
	log        *activity.Logger
	notify     Notifier
	pub        Publisher
	paths      paths.Paths
	configPass string
	logLevel   func() string

	Conns  Connections
	Mounts Mounts
	Vaults Vaults
	Eval   *pauserules.Evaluator
	Run    Runner
	Now    Clock
	// Watch enables FSEvents and remote change detection (off in unit tests).
	Watch bool

	mu          sync.Mutex
	rt          map[string]*itemRT
	queue       []queued
	cur         *running
	manualUntil *time.Time // nil = none; zero time = until resumed
	lastRules   []pauserules.ActiveRule
	nextCPU     time.Time
	nextPause   time.Time
	wake        chan struct{}
	quit        chan struct{}
	done        chan struct{}
	closed      bool
	started     bool
}

// New creates the engine. Start launches its loop.
func New(st *store.Store, log *activity.Logger, n Notifier, pub Publisher, p paths.Paths, configPass string, logLevel func() string) *Engine {
	return &Engine{st: st, log: log, notify: n, pub: pub, paths: p, configPass: configPass, logLevel: logLevel,
		rt: map[string]*itemRT{}, wake: make(chan struct{}, 1), quit: make(chan struct{}), done: make(chan struct{}),
		Now: time.Now,
		Run: func(job sv.Job, h sv.Handlers) (Job, error) {
			return sv.Spawn(job, sv.Secrets{ConfigPass: configPass}, h)
		}}
}

func (e *Engine) settings() store.Settings {
	s, err := e.st.Settings()
	if err != nil {
		return store.DefaultSettings()
	}
	return s
}

func (e *Engine) label() string {
	if l := e.settings().ConflictLabel; l != "" {
		return l
	}
	return "conflict"
}

// Start loads the items, starts watchers and the scheduler loop. Every item
// gets a sync at start (catch up with changes made while the Core was off).
func (e *Engine) Start() error {
	items, err := e.st.OfflineItems()
	if err != nil {
		return err
	}
	now := e.Now()
	e.mu.Lock()
	for _, it := range items {
		rt := e.itemRT(it.ID)
		if it.State != StateNeedsConfirmation {
			rt.due = now
		}
		if it.State == StateSyncing {
			it.State = StatePending
			_ = e.st.UpdateOfflineItem(it)
		}
		e.scheduleChecks(it, rt, now)
	}
	e.mu.Unlock()
	if e.Watch {
		for _, it := range items {
			e.startWatching(it)
		}
	}
	e.mu.Lock()
	e.started = true
	e.mu.Unlock()
	go e.loop()
	return nil
}

func (e *Engine) itemRT(id string) *itemRT {
	rt := e.rt[id]
	if rt == nil {
		rt = &itemRT{pendingPaths: map[string]bool{}, duringRun: map[string]bool{}}
		e.rt[id] = rt
	}
	return rt
}

func (e *Engine) scheduleChecks(it store.OfflineItem, rt *itemRT, now time.Time) {
	st := e.settings()
	rt.nextSafety = now.Add(time.Duration(st.SafetyFullSyncMinutes) * time.Minute)
	rt.nextRemote = now.Add(time.Duration(e.remoteInterval(it, st)) * time.Second)
}

// remoteInterval is the polling interval for remote changes of an item: the
// Nextcloud ETag interval, or the generic full-run interval. Items with
// ChangeNotify are notified and only need the safety run.
func (e *Engine) remoteInterval(it store.OfflineItem, st store.Settings) int {
	if rt := e.rt[it.ID]; rt != nil && rt.remote != nil {
		switch rt.remote.kind {
		case remoteNotify:
			return 0
		case remoteETag:
			return st.NextcloudEtagSeconds
		}
	}
	return st.GenericCheckSeconds
}

func (e *Engine) poke() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *Engine) loop() {
	defer close(e.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		next := e.step(e.Now())
		d := time.Until(next)
		if d < 100*time.Millisecond {
			d = 100 * time.Millisecond
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(d)
		select {
		case <-e.quit:
			return
		case <-e.wake:
		case <-timer.C:
		}
	}
}

// step performs all due scheduling work at now and returns when it wants to
// run again.
func (e *Engine) step(now time.Time) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return now.Add(time.Hour)
	}
	next := now.Add(time.Hour)
	earliest := func(t time.Time) {
		if !t.IsZero() && t.Before(next) {
			next = t
		}
	}
	items, err := e.st.OfflineItems()
	if err != nil {
		slog.Error("offline: list items", "err", err)
		return now.Add(time.Minute)
	}
	st := e.settings()
	for _, it := range items {
		rt := e.itemRT(it.ID)
		if rt.busy {
			continue
		}
		// Periodic triggers.
		if !rt.nextSafety.IsZero() && !rt.nextSafety.After(now) {
			rt.due = minTime(rt.due, now)
			rt.nextSafety = now.Add(time.Duration(st.SafetyFullSyncMinutes) * time.Minute)
		}
		if iv := e.remoteInterval(it, st); iv > 0 && !rt.nextRemote.IsZero() && !rt.nextRemote.After(now) {
			rt.nextRemote = now.Add(time.Duration(iv) * time.Second)
			if rt.remote != nil && rt.remote.kind == remoteETag {
				go e.checkETag(it)
			} else {
				rt.due = minTime(rt.due, now)
			}
		}
		if !rt.retryAt.IsZero() && !rt.retryAt.After(now) {
			rt.retryAt = time.Time{}
			rt.due = minTime(rt.due, now)
		}
		earliest(rt.nextSafety)
		if e.remoteInterval(it, st) > 0 {
			earliest(rt.nextRemote)
		}
		earliest(rt.retryAt)
		// Due items join the queue.
		if !rt.due.IsZero() {
			ready := rt.due
			if rt.quietUntil.After(ready) {
				ready = rt.quietUntil
			}
			if ready.After(now) {
				earliest(ready)
			} else if it.State != StateNeedsConfirmation && !e.isQueuedOrRunning("i:"+it.ID) {
				rt.due = time.Time{}
				e.queue = append(e.queue, queued{itemID: it.ID})
			} else if it.State == StateNeedsConfirmation {
				rt.due = time.Time{}
			}
		}
	}
	// CPU sampling only while something is pending or running.
	if len(e.queue) > 0 || e.cur != nil {
		if !e.nextCPU.After(now) {
			e.Eval.SampleCPU()
			e.nextCPU = now.Add(10 * time.Second)
		}
		earliest(e.nextCPU)
	} else if !e.nextCPU.IsZero() {
		e.Eval.ResetCPU()
		e.nextCPU = time.Time{}
	}
	// A running job is re-checked against the Pause Rules every 10 s.
	if e.cur != nil {
		if !e.cur.q.ignorePause && !e.cur.stopping && !e.nextPause.After(now) {
			e.nextPause = now.Add(10 * time.Second)
			if reason, paused := e.pausedLocked(now); paused {
				e.stopCurrentLocked(reason)
			}
		}
		earliest(e.nextPause)
		return next
	}
	// Start the next job (FIFO, one at a time).
	for len(e.queue) > 0 {
		q := e.queue[0]
		if q.migration == nil {
			it, err := e.st.OfflineItem(q.itemID)
			if err != nil {
				e.queue = e.queue[1:]
				continue
			}
			if reason := e.blockedReason(it); reason != "" {
				e.queue = e.queue[1:]
				e.setStateLocked(it, StatePaused, reason)
				e.itemRT(it.ID).retryAt = now.Add(60 * time.Second)
				earliest(now.Add(60 * time.Second))
				continue
			}
		}
		if !q.ignorePause {
			if reason, paused := e.pausedLocked(now); paused {
				e.markQueuedPausedLocked(reason)
				earliest(now.Add(30 * time.Second))
				return next
			}
		}
		e.queue = e.queue[1:]
		if err := e.startLocked(q, now); err != nil {
			slog.Error("offline: start job", "err", err)
			continue
		}
		e.nextPause = now.Add(10 * time.Second)
		earliest(e.nextPause)
		break
	}
	return next
}

func minTime(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func (e *Engine) isQueuedOrRunning(key string) bool {
	if e.cur != nil && e.cur.q.key() == key {
		return true
	}
	for _, q := range e.queue {
		if q.key() == key {
			return true
		}
	}
	return false
}

// blockedReason reports item-specific blockers: missing storage location or
// locked Vault.
func (e *Engine) blockedReason(it store.OfflineItem) string {
	if e.Vaults != nil && e.Vaults.IsLocked(it.ConnectionID) {
		return ReasonVaultLocked
	}
	if fi, err := os.Stat(it.StoragePath); err != nil || !fi.IsDir() {
		// The Storage Location of a new item is created at its first sync. An
		// item that synced before, or whose external volume is absent, pauses.
		if it.LastSyncAt == nil && volumePresent(it.StoragePath) {
			if err := os.MkdirAll(it.StoragePath, 0o755); err == nil {
				return ""
			}
		}
		return ReasonLocationMissing
	}
	return ""
}

// volumePresent reports whether the volume holding p is mounted: paths below
// /Volumes/<name> need that volume; everything else is on the startup disk.
func volumePresent(p string) bool {
	if !strings.HasPrefix(p, "/Volumes/") {
		return true
	}
	name := strings.SplitN(strings.TrimPrefix(p, "/Volumes/"), "/", 2)[0]
	if name == "" {
		return false
	}
	vol := filepath.Join("/Volumes", name)
	fi, err := os.Stat(vol)
	if err != nil || !fi.IsDir() {
		return false
	}
	// A mounted volume has a different device than /Volumes itself.
	var a, b unix.Stat_t
	if unix.Stat(vol, &a) != nil || unix.Stat("/Volumes", &b) != nil {
		return false
	}
	return a.Dev != b.Dev
}

// pausedLocked evaluates manual pause and Pause Rules.
func (e *Engine) pausedLocked(now time.Time) (string, bool) {
	if e.manualUntil != nil {
		if e.manualUntil.IsZero() || now.Before(*e.manualUntil) {
			return pauserules.RuleManual, true
		}
		e.manualUntil = nil
		e.publishPauseLocked()
	}
	rules := e.Eval.Evaluate()
	changed := !sameRules(rules, e.lastRules)
	e.lastRules = rules
	if changed {
		if len(rules) > 0 {
			e.log.Info("offline", "", fmt.Sprintf("Syncing paused: %s (%s)", rules[0].ID, rules[0].Detail), rules)
		} else {
			e.log.Info("offline", "", "Pause Rules no longer active; syncing resumes", nil)
		}
		e.publishPauseLocked()
	}
	if len(rules) > 0 {
		return rules[0].ID, true
	}
	return "", false
}

func sameRules(a, b []pauserules.ActiveRule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

func (e *Engine) markQueuedPausedLocked(reason string) {
	for _, q := range e.queue {
		if q.migration != nil {
			continue
		}
		it, err := e.st.OfflineItem(q.itemID)
		if err == nil && (it.State != StatePaused || it.LastError != reason) {
			e.setStateLocked(it, StatePaused, reason)
		}
	}
}

// requestResyncLocked marks an item for a resync (filters, location or
// restore changed) so a run in progress cannot clear the request.
func (e *Engine) requestResyncLocked(it *store.OfflineItem) {
	it.NeedsResync = true
	e.itemRT(it.ID).resyncGen++
}

func (e *Engine) setStateLocked(it store.OfflineItem, state, reason string) {
	it.State, it.LastError = state, reason
	if err := e.st.UpdateOfflineItem(it); err != nil {
		slog.Error("offline: update item", "err", err)
	}
	e.publishItemLocked(it)
}

// DTO is the contract's OfflineItem object.
type DTO struct {
	store.OfflineItem
	Progress *Progress `json:"progress"`
	IsVault  bool      `json:"isVault"`
}

func (e *Engine) dtoLocked(it store.OfflineItem) DTO {
	d := DTO{OfflineItem: it}
	if rt := e.rt[it.ID]; rt != nil && it.State == StateSyncing {
		d.Progress = rt.progress
	}
	if c, err := e.st.Connection(it.ConnectionID); err == nil {
		d.IsVault = c.Kind == "vault"
	}
	return d
}

func (e *Engine) publishItemLocked(it store.OfflineItem) {
	e.pub.Publish("offline.status", e.dtoLocked(it))
}

// PauseStatus is the pause.status result.
type PauseStatus struct {
	ManualUntil *int64                  `json:"manualUntil"`
	ActiveRules []pauserules.ActiveRule `json:"activeRules"`
	Effective   bool                    `json:"effective"`
}

func (e *Engine) pauseStatusLocked() PauseStatus {
	ps := PauseStatus{ActiveRules: e.lastRules}
	if ps.ActiveRules == nil {
		ps.ActiveRules = []pauserules.ActiveRule{}
	}
	if e.manualUntil != nil {
		v := int64(-1)
		if !e.manualUntil.IsZero() {
			v = e.manualUntil.UnixMilli()
		}
		ps.ManualUntil = &v
	}
	ps.Effective = ps.ManualUntil != nil || len(ps.ActiveRules) > 0
	return ps
}

func (e *Engine) publishPauseLocked() {
	e.pub.Publish("pause.status", e.pauseStatusLocked())
}

// ---- running jobs ----

func (e *Engine) logLevelRclone() string {
	if e.logLevel != nil && e.logLevel() == "debug" {
		return "DEBUG"
	}
	return "INFO"
}

func (e *Engine) startLocked(q queued, now time.Time) error {
	st := e.settings()
	job := sv.Job{Background: true, LogLevel: e.logLevelRclone(), BwLimit: pauserules.BwLimit(st.Bandwidth)}
	r := &running{q: q, started: now, files: map[string]string{}}
	if q.migration != nil {
		job.Type, job.ID = sv.JobVaultMigrate, q.migration.JobID
		m := q.migration.Job
		job.Migrate = &m
		runID, _ := e.st.StartRun(q.migration.JobID, "vault-migrate")
		r.runID = runID
	} else {
		it, err := e.st.OfflineItem(q.itemID)
		if err != nil {
			return err
		}
		conn, err := e.Conns.Get(it.ConnectionID)
		if err != nil {
			return err
		}
		if err := WriteFilters(e.paths, it); err != nil {
			return err
		}
		if err := os.MkdirAll(e.paths.BisyncWorkdir(it.ID), 0o700); err != nil {
			return err
		}
		b, err := json.Marshal(BisyncParams(it, conn.RcloneRemote, e.paths, e.label(), q.force))
		if err != nil {
			return err
		}
		job.Type, job.ID, job.Bisync = sv.JobBisync, it.ID, b
		runID, _ := e.st.StartRun(it.ID, "bisync")
		r.runID = runID
		rt := e.itemRT(it.ID)
		r.resyncGen = rt.resyncGen
		rt.progress = &Progress{ID: it.ID}
		rt.duringRun = map[string]bool{}
		// A remote change after this point must still be noticed, so the run
		// is recorded against the ETag seen before it started.
		rt.runETag = rt.seenETag
		e.setStateLocked(it, StateSyncing, "")
	}
	label := e.label()
	w, err := e.Run(job, sv.Handlers{
		OnMsg: func(m sv.Msg) { e.onMsg(r, m) },
		OnLog: func(l sv.LogLine) { e.onLog(r, l, label) },
	})
	if err != nil {
		e.finishLocked(r, sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return err
	}
	r.job = w
	e.cur = r
	if q.migration != nil && q.migration.OnStart != nil {
		go q.migration.OnStart()
	}
	go func() {
		<-w.Done()
		out := w.Outcome()
		e.mu.Lock()
		if e.cur == r {
			e.cur = nil
		}
		e.finishLocked(r, out)
		e.mu.Unlock()
		e.poke()
	}()
	return nil
}

func (e *Engine) onMsg(r *running, m sv.Msg) {
	if m.Type != "progress" || r.q.migration != nil {
		return
	}
	p := &Progress{ID: r.q.itemID, Bytes: m.Bytes, TotalBytes: m.TotalBytes, Transfers: m.Transfers, ETA: m.ETA}
	e.mu.Lock()
	e.itemRT(r.q.itemID).progress = p
	e.mu.Unlock()
	e.pub.Publish("offline.progress", p)
}

// ClassifyLog maps an rclone log line to a sync file action ("" = none).
func ClassifyLog(l sv.LogLine, label string) string {
	if l.Object == "" {
		return ""
	}
	if strings.Contains(l.Object, ConflictMarker(label)) && (strings.HasPrefix(l.Msg, "Copied") || strings.HasPrefix(l.Msg, "Moved")) {
		return "conflict"
	}
	switch {
	case strings.HasPrefix(l.Msg, "Copied"):
		return "transferred"
	case strings.HasPrefix(l.Msg, "Deleted"):
		return "deleted"
	}
	return ""
}

func (e *Engine) onLog(r *running, l sv.LogLine, label string) {
	subject := r.q.itemID
	if r.q.migration != nil {
		subject = r.q.migration.JobID
	}
	if action := ClassifyLog(l, label); action != "" {
		e.mu.Lock()
		if prev := r.files[l.Object]; prev != "conflict" {
			r.files[l.Object] = action
		}
		e.mu.Unlock()
	}
	switch l.Level {
	case "error", "critical":
		e.log.Error("sync", subject, l.Msg, map[string]string{"object": l.Object})
	default:
		if e.log.Enabled("debug") {
			e.log.Debug("sync", subject, l.Msg, map[string]string{"object": l.Object, "level": l.Level})
		}
	}
}

// stopCurrentLocked stops the running job gracefully (30 s, then SIGKILL)
// and puts it back at the head of the queue.
func (e *Engine) stopCurrentLocked(reason string) {
	r := e.cur
	if r == nil || r.stopping {
		return
	}
	r.stopping = true
	e.queue = append([]queued{r.q}, e.queue...)
	if r.q.migration == nil {
		if it, err := e.st.OfflineItem(r.q.itemID); err == nil {
			e.setStateLocked(it, StatePaused, reason)
		}
	}
	go r.job.Stop(30 * time.Second)
}

func (e *Engine) finishLocked(r *running, out sv.Msg) {
	status := out.Status
	if status == "" {
		status = sv.StatusError
	}
	var files []store.SyncRunFile
	counts := map[string]int{}
	for p, a := range r.files {
		files = append(files, store.SyncRunFile{Action: a, Path: p})
		counts[a]++
	}
	var stats struct {
		Bytes int64 `json:"bytes"`
	}
	_ = json.Unmarshal(out.Stats, &stats)
	if r.runID != 0 {
		if err := e.st.FinishRun(store.SyncRun{ID: r.runID, Status: status, Transferred: counts["transferred"],
			Deleted: counts["deleted"], Conflicts: counts["conflict"], Bytes: stats.Bytes, Error: out.Error}, files); err != nil {
			slog.Error("offline: finish run", "err", err)
		}
	}
	if r.q.migration != nil {
		if r.stopping && status == sv.StatusStopped {
			return // re-queued
		}
		if cb := r.q.migration.OnResult; cb != nil {
			go cb(out)
		}
		return
	}
	it, err := e.st.OfflineItem(r.q.itemID)
	if err != nil {
		return // removed meanwhile
	}
	rt := e.itemRT(it.ID)
	rt.progress = nil
	name := itemName(it)
	now := e.Now()
	switch status {
	case sv.StatusOK:
		ts := now.UnixMilli()
		it.LastSyncAt, it.State, it.LastError = &ts, StateIdle, ""
		if r.resyncGen == rt.resyncGen {
			// Only clear the flag if no new resync was requested during the run
			// (e.g. filters changed); otherwise the next run must resync.
			it.NeedsResync = false
		}
		rt.pendingPaths = map[string]bool{}
		rt.retryAt = time.Time{}
		rt.lastNotifiedEr = false
		// Local changes during the run that were not caused by the sync itself.
		if leftover := foreignChanges(rt.duringRun, r.files); len(leftover) > 0 {
			for _, rel := range leftover {
				rt.pendingPaths[filepath.Join(it.StoragePath, rel)] = true
			}
			rt.due = minTime(rt.due, now.Add(time.Duration(e.settings().QuietPeriodSeconds)*time.Second))
		}
		rt.duringRun = map[string]bool{}
		if err := e.st.UpdateOfflineItem(it); err != nil {
			slog.Error("offline: update item", "err", err)
		}
		if e.Watch && rt.watch == nil {
			// The Storage Location exists now (e.g. the external volume came back).
			go e.startFSEvents(it)
		}
		if counts["transferred"]+counts["deleted"]+counts["conflict"] > 0 {
			e.log.Info("sync", it.ID, fmt.Sprintf("Synced %q: %d transferred, %d deleted, %d conflicts", name,
				counts["transferred"], counts["deleted"], counts["conflict"]), map[string]any{"runId": r.runID})
		} else {
			e.log.Debug("sync", it.ID, fmt.Sprintf("Synced %q: no changes", name), map[string]any{"runId": r.runID})
		}
		if counts["conflict"] > 0 {
			var cf []string
			for p, a := range r.files {
				if a == "conflict" {
					cf = append(cf, p)
				}
			}
			e.log.Warn("sync", it.ID, fmt.Sprintf("%d conflict copies created in %q", len(cf), name), cf)
			e.notify.Notify(notify.KindConflict, map[string]any{"itemId": it.ID, "itemName": name, "files": cf})
		}
		if e.Mounts != nil {
			go e.Mounts.Forget(it.ConnectionID, it.RemotePath)
		}
		if rt.runETag != "" {
			if err := e.st.SetOfflineETag(it.ID, rt.runETag); err != nil {
				slog.Error("offline: store etag", "err", err)
			}
		}
		e.publishItemLocked(it)
	case sv.StatusMassDelete:
		it.State, it.LastError = StateNeedsConfirmation, out.Error
		_ = e.st.UpdateOfflineItem(it)
		e.log.Warn("sync", it.ID, fmt.Sprintf("Mass-Delete Guard stopped %q: %s", name, out.Error), map[string]any{"runId": r.runID})
		e.notify.Notify(notify.KindMassDelete, map[string]any{"itemId": it.ID, "itemName": name, "message": out.Error})
		e.publishItemLocked(it)
	case sv.StatusStopped:
		if !r.stopping {
			// Stopped without our request: retry soon.
			rt.retryAt = now.Add(time.Minute)
		}
		if it.State == StateSyncing {
			it.State = StatePending
			_ = e.st.UpdateOfflineItem(it)
			e.publishItemLocked(it)
		}
	default:
		it.State, it.LastError = StateError, out.Error
		_ = e.st.UpdateOfflineItem(it)
		rt.retryAt = now.Add(5 * time.Minute)
		e.log.Error("sync", it.ID, fmt.Sprintf("Sync of %q failed: %s", name, out.Error), map[string]any{"runId": r.runID})
		if !rt.lastNotifiedEr {
			rt.lastNotifiedEr = true
			e.notify.Notify(notify.KindError, map[string]any{"title": name, "message": out.Error, "subjectId": it.ID})
		}
		e.publishItemLocked(it)
	}
}

// foreignChanges returns the relative paths changed during a run that the
// run itself did not touch (and that are not parent folders of touched files).
func foreignChanges(during map[string]bool, touched map[string]string) []string {
	var out []string
	for rel := range during {
		if _, ok := touched[rel]; ok {
			continue
		}
		parent := false
		for t := range touched {
			if strings.HasPrefix(t, rel+"/") {
				parent = true
				break
			}
		}
		if !parent {
			out = append(out, rel)
		}
	}
	return out
}
