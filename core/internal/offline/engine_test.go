package offline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/pauserules"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/store"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

type fakePub struct{}

func (fakePub) Publish(string, any) {}

type fakeNotify struct {
	mu    sync.Mutex
	kinds []string
}

func (f *fakeNotify) Notify(kind string, _ any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kinds = append(f.kinds, kind)
}

type fakeConns struct{}

func (fakeConns) Get(id string) (store.Connection, error) {
	return store.Connection{ID: id, Name: "NC", Kind: "remote", RcloneRemote: "cw-" + id, Provider: "local"}, nil
}
func (fakeConns) Config(store.Connection) (map[string]string, error) { return map[string]string{}, nil }

type fakeVaults map[string]bool

func (f fakeVaults) IsLocked(id string) bool { return f[id] }

type fakeProbes struct {
	mu      sync.Mutex
	battery bool
}

func (f *fakeProbes) RunningExecutables() ([]string, error) { return nil, nil }
func (f *fakeProbes) OnBattery() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.battery
}
func (f *fakeProbes) setBattery(b bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.battery = b
}
func (f *fakeProbes) LowPowerMode() bool                { return false }
func (f *fakeProbes) Network() (bool, bool, bool)       { return false, false, true }
func (f *fakeProbes) CPUTicks() (uint64, uint64, error) { return 0, 0, nil }

type fakeJob struct {
	job     sv.Job
	h       sv.Handlers
	done    chan struct{}
	mu      sync.Mutex
	out     sv.Msg
	stopped bool
	sent    []sv.Command
}

func (j *fakeJob) Done() <-chan struct{} { return j.done }
func (j *fakeJob) Outcome() sv.Msg {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.out
}
func (j *fakeJob) Stop(time.Duration) {
	j.mu.Lock()
	j.stopped = true
	j.out = sv.Msg{Type: "result", Status: sv.StatusStopped}
	j.mu.Unlock()
	j.finish()
}
func (j *fakeJob) Send(c sv.Command) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.sent = append(j.sent, c)
	return nil
}
func (j *fakeJob) finish() {
	select {
	case <-j.done:
	default:
		close(j.done)
	}
}
func (j *fakeJob) complete(status, errText string) {
	j.mu.Lock()
	j.out = sv.Msg{Type: "result", Status: status, Error: errText}
	j.mu.Unlock()
	j.finish()
}

type harness struct {
	t      *testing.T
	e      *Engine
	st     *store.Store
	probes *fakeProbes
	notify *fakeNotify
	mu     sync.Mutex
	jobs   []*fakeJob
	now    time.Time
	dir    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_ = st.InsertConnection(store.Connection{ID: "c1", Name: "NC", Kind: "remote", RcloneRemote: "cw-c1", Provider: "local", CreatedAt: 1})
	_, _ = st.UpdateSettings(map[string]any{"conflictLabel": "Konflikt"})
	h := &harness{t: t, st: st, probes: &fakeProbes{}, notify: &fakeNotify{}, dir: dir, now: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	pub := fakePub{}
	e := New(st, activity.New(st, pub), h.notify, pub, paths.ForHome(dir), "pass", nil)
	e.Conns = fakeConns{}
	e.Vaults = fakeVaults{"vault-conn": true}
	e.Eval = &pauserules.Evaluator{Probes: h.probes, Settings: func() store.Settings { s, _ := st.Settings(); return s }}
	e.Now = func() time.Time { return h.now }
	e.Run = func(job sv.Job, hs sv.Handlers) (Job, error) {
		j := &fakeJob{job: job, h: hs, done: make(chan struct{})}
		h.mu.Lock()
		h.jobs = append(h.jobs, j)
		h.mu.Unlock()
		return j, nil
	}
	h.e = e
	return h
}

func (h *harness) addItem(id string) store.OfflineItem {
	h.t.Helper()
	ts := int64(1)
	it := store.OfflineItem{ID: id, ConnectionID: "c1", Kind: "folder", RemotePath: id, StoragePath: filepath.Join(h.dir, "store", id),
		Excludes: store.DefaultSettings().DefaultExcludes, State: StateIdle, LastSyncAt: &ts, CreatedAt: 1}
	if err := h.st.InsertOfflineItem(it); err != nil {
		h.t.Fatal(err)
	}
	if err := mkdir(it.StoragePath); err != nil {
		h.t.Fatal(err)
	}
	h.e.mu.Lock()
	h.e.itemRT(id)
	h.e.mu.Unlock()
	return it
}

func (h *harness) step() { h.e.step(h.now) }

func (h *harness) started() []*fakeJob {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*fakeJob{}, h.jobs...)
}

// waitIdle waits until the engine processed a finished job.
func (h *harness) waitIdle() {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		h.e.mu.Lock()
		busy := h.e.cur != nil
		h.e.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("job did not finish")
}

func (h *harness) state(id string) store.OfflineItem {
	it, err := h.st.OfflineItem(id)
	if err != nil {
		h.t.Fatal(err)
	}
	return it
}

func TestQuietPeriodRestartsOnEachChange(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	t0 := h.now
	h.e.localChange("a", "Song.wav", 0, false, t0)
	h.step()
	h.now = t0.Add(30 * time.Second)
	h.e.localChange("a", "Song.wav", 0, false, h.now)
	h.now = t0.Add(60 * time.Second)
	h.step()
	if n := len(h.started()); n != 0 {
		t.Fatalf("sync started %d jobs before the quiet period after the last change ended", n)
	}
	h.now = t0.Add(89 * time.Second)
	h.step()
	if len(h.started()) != 0 {
		t.Fatal("started one second early")
	}
	h.now = t0.Add(90 * time.Second)
	h.step()
	jobs := h.started()
	if len(jobs) != 1 || jobs[0].job.Type != sv.JobBisync || jobs[0].job.ID != "a" || !jobs[0].job.Background {
		t.Fatalf("expected one background bisync job for a at t0+90s, got %+v", jobs)
	}
	// Excluded names never trigger a sync.
	h.e.localChange("a", ".DS_Store", 0, false, h.now)
	if h.e.rt["a"].pendingPaths[filepath.Join(h.dir, "store", "a", ".DS_Store")] {
		t.Fatal("excluded change recorded")
	}
}

func TestPauseRuleBlocksAndSyncNowBypasses(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.probes.setBattery(true)
	h.e.rt["a"].due = h.now
	h.step()
	if len(h.started()) != 0 {
		t.Fatal("job started although a Pause Rule is active")
	}
	if it := h.state("a"); it.State != StatePaused || it.LastError != pauserules.RuleBattery {
		t.Fatalf("item must be paused by battery rule: %+v", it)
	}
	if err := h.e.SyncNow("a"); err != nil {
		t.Fatal(err)
	}
	h.step()
	jobs := h.started()
	if len(jobs) != 1 {
		t.Fatalf("Jetzt synchronisieren must ignore Pause Rules, got %d jobs", len(jobs))
	}
	if jobs[0].job.BwLimit != "5M:20M" || !jobs[0].job.Background {
		t.Fatalf("syncNow keeps background priority and bandwidth limit: %+v", jobs[0].job)
	}
}

func TestRunningJobStopsWhenRuleActivates(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.e.rt["a"].due = h.now
	h.step()
	jobs := h.started()
	if len(jobs) != 1 {
		t.Fatal("job not started")
	}
	h.probes.setBattery(true)
	h.now = h.now.Add(10 * time.Second)
	h.step()
	h.waitIdle()
	jobs[0].mu.Lock()
	stopped := jobs[0].stopped
	jobs[0].mu.Unlock()
	if !stopped {
		t.Fatal("running job not stopped by the Pause Rule")
	}
	if it := h.state("a"); it.State != StatePaused {
		t.Fatalf("item state %s, want paused", it.State)
	}
	h.probes.setBattery(false)
	h.now = h.now.Add(30 * time.Second)
	h.step()
	if len(h.started()) != 2 {
		t.Fatal("paused job must resume when the rule ends")
	}
}

func TestFIFOOneJobAtATime(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.addItem("b")
	h.e.rt["b"].due = h.now.Add(-time.Second)
	h.step()
	h.e.rt["a"].due = h.now
	h.step()
	jobs := h.started()
	if len(jobs) != 1 || jobs[0].job.ID != "b" {
		t.Fatalf("expected only b running, got %d jobs", len(jobs))
	}
	jobs[0].complete(sv.StatusOK, "")
	h.waitIdle()
	h.step()
	jobs = h.started()
	if len(jobs) != 2 || jobs[1].job.ID != "a" {
		t.Fatalf("a must run after b finished, got %d jobs", len(jobs))
	}
	if it := h.state("b"); it.State != StateIdle || it.LastSyncAt == nil || it.NeedsResync {
		t.Fatalf("b after ok run: %+v", it)
	}
}

func TestMassDeleteConfirmFlow(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.e.rt["a"].due = h.now
	h.step()
	// rclone logs the abort reason before the job fails.
	h.started()[0].h.OnLog(sv.LogLine{Level: "error", Object: "Safety abort",
		Msg: `too many deletes (>50%, 5 of 9) on Path2 "cw-c1:a/". Run with --force if desired.`})
	h.started()[0].complete(sv.StatusMassDelete, "too many deletes")
	h.waitIdle()
	if it := h.state("a"); it.State != StateNeedsConfirmation || !strings.HasPrefix(it.LastError, "Safety abort: too many deletes (>50%, 5 of 9) on Path2") {
		t.Fatalf("item after Mass-Delete Guard stop: %+v", it)
	}
	if md := massDeleteOf(t, h.e, "a"); md == nil || *md != (MassDeleteInfo{Reason: "tooManyDeletes", Side: "cloud", Deletes: 5, Total: 9}) {
		t.Fatalf("massDelete %+v", md)
	}
	if len(h.notify.kinds) != 1 || h.notify.kinds[0] != "massDelete" {
		t.Fatalf("notifications %v", h.notify.kinds)
	}
	// While waiting for confirmation, triggers do not start runs.
	h.e.rt["a"].due = h.now
	h.step()
	if len(h.started()) != 1 {
		t.Fatal("item ran without confirmation")
	}
	if _, err := h.e.ConfirmMassDelete(context.Background(), "a", "delete"); err != nil {
		t.Fatal(err)
	}
	h.step()
	jobs := h.started()
	var params map[string]any
	_ = json.Unmarshal(jobs[1].job.Bisync, &params)
	if params["force"] != true {
		t.Fatalf("confirmed delete must run with force: %v", params)
	}
	jobs[1].complete(sv.StatusMassDelete, "too many deletes")
	h.waitIdle()
	// Without a logged reason the worker's error is kept; no details then.
	if it := h.state("a"); it.LastError != "too many deletes" || massDeleteOf(t, h.e, "a") != nil {
		t.Fatalf("fallback reason: %+v", it)
	}
	if _, err := h.e.ConfirmMassDelete(context.Background(), "a", "restore"); err != nil {
		t.Fatal(err)
	}
	h.step()
	jobs = h.started()
	params = nil
	_ = json.Unmarshal(jobs[2].job.Bisync, &params)
	if params["force"] != nil || params["resync"] != true || params["resyncMode"] != "newer" {
		t.Fatalf("restore must resync (newer) without force: %v", params)
	}
}

func TestLostListingsResyncs(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.e.rt["a"].due = h.now
	h.step()
	outOfSync := sv.LogLine{Level: "error", Msg: "Bisync critical error: path1 and path2 are out of sync, run --resync to recover"}
	h.started()[0].h.OnLog(outOfSync)
	h.started()[0].complete(sv.StatusError, "sync/bisync: bisync aborted")
	h.waitIdle()
	if it := h.state("a"); it.State != StatePending || it.LastError != "" || !it.NeedsResync {
		t.Fatalf("item after lost listings: %+v", it)
	}
	if len(h.notify.kinds) != 0 {
		t.Fatalf("self-healing must not notify: %v", h.notify.kinds)
	}
	// The resync run starts right away, without the 5-minute retry delay.
	h.step()
	jobs := h.started()
	if len(jobs) != 2 {
		t.Fatalf("resync run did not start, got %d jobs", len(jobs))
	}
	var params map[string]any
	_ = json.Unmarshal(jobs[1].job.Bisync, &params)
	if params["resync"] != true || params["resyncMode"] != "newer" {
		t.Fatalf("recovery must resync (newer): %v", params)
	}
	// A failing resync takes the normal error path (with retry backoff): no
	// resync loop. NeedsResync stays set, so the next attempt resyncs again,
	// only at an escalating interval.
	jobs[1].h.OnLog(outOfSync)
	jobs[1].complete(sv.StatusError, "sync/bisync: bisync aborted")
	h.waitIdle()
	if it := h.state("a"); it.State != StateError || !it.NeedsResync {
		t.Fatalf("item after failed resync: %+v", it)
	}
	if len(h.notify.kinds) != 1 || h.notify.kinds[0] != "error" {
		t.Fatalf("notifications %v", h.notify.kinds)
	}
	// First consecutive failure: 1-minute backoff instead of the fixed 5.
	if got := h.e.rt["a"].retryAt; !got.Equal(h.now.Add(h.e.backoff(1))) {
		t.Fatalf("retryAt %v", got)
	}
}

// Consecutive sync failures escalate the retry delay 1, 2, 5 minutes;
// the first successful run resets the count.
func TestFailedRunsBackOff(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	run := func() *fakeJob {
		if err := h.e.SyncNow("a"); err != nil {
			t.Fatal(err)
		}
		h.step()
		jobs := h.started()
		return jobs[len(jobs)-1]
	}
	// three failing runs in a row
	for i, want := range []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute} {
		jobs := run()
		jobs.complete(sv.StatusError, "boom")
		h.waitIdle()
		wantAt := h.now.Add(want)
		if got := h.e.rt["a"].retryAt; !got.Equal(wantAt) {
			t.Fatalf("after failure %d: retryAt %v, want %v (retryCount %d)", i+1, got, wantAt, h.e.rt["a"].retryCount)
		}
		if got := h.e.rt["a"].retryCount; got != i+1 {
			t.Fatalf("after failure %d: retryCount %d", i+1, got)
		}
		if it := h.state("a"); it.State != StateError {
			t.Fatalf("after failure %d: state %s", i+1, it.State)
		}
	}
	// the run after a success resets count and retryAt
	jobs := run()
	if len(h.started()) != 4 {
		t.Fatalf("4th run did not start, %d jobs", len(h.started()))
	}
	jobs.complete(sv.StatusOK, "")
	h.waitIdle()
	if got := h.e.rt["a"].retryCount; got != 0 {
		t.Fatalf("retryCount after success %d, want 0", got)
	}
	if got := h.e.rt["a"].retryAt; !got.IsZero() {
		t.Fatalf("retryAt after success %v, want zero", got)
	}
	if it := h.state("a"); it.State != StateIdle {
		t.Fatalf("state after success %s", it.State)
	}
	// a single new failure retries in 1 minute again
	jobs = run()
	if len(h.started()) != 5 {
		t.Fatalf("5th run did not start, %d jobs", len(h.started()))
	}
	jobs.complete(sv.StatusError, "boom again")
	h.waitIdle()
	if got := h.e.rt["a"].retryAt; !got.Equal(h.now.Add(time.Minute)) {
		t.Fatalf("retryAt after reset %v, want +1 minute", got)
	}
}

func massDeleteOf(t *testing.T, e *Engine, id string) *MassDeleteInfo {
	t.Helper()
	items, err := e.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range items {
		if d.ID == id {
			return d.MassDelete
		}
	}
	t.Fatalf("item %s not listed", id)
	return nil
}

// migrationHarness queues a migration whose callbacks report to channels.
func migrationHarness(t *testing.T, h *harness, jobID string) (results chan sv.Msg, progress chan sv.Msg) {
	t.Helper()
	results, progress = make(chan sv.Msg, 4), make(chan sv.Msg, 4)
	h.e.EnqueueMigration(&MigrationRequest{JobID: jobID, VaultID: "v1", Job: sv.MigrateJob{SrcFs: "cw-c1:", SrcPath: "Docs"},
		OnResult:   func(m sv.Msg) { results <- m },
		OnProgress: func(m sv.Msg) { progress <- m },
	})
	return results, progress
}

func noResult(t *testing.T, results chan sv.Msg) {
	t.Helper()
	select {
	case m := <-results:
		t.Fatalf("OnResult called for a canceled job: %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCancelQueuedMigration(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.e.rt["a"].due = h.now
	h.step()
	results, _ := migrationHarness(t, h, "m1")
	if !h.e.CancelMigration("m1") {
		t.Fatal("queued migration not found")
	}
	if h.e.CancelMigration("m1") {
		t.Fatal("a canceled migration must not be cancelable again")
	}
	h.started()[0].complete(sv.StatusOK, "")
	h.waitIdle()
	h.step()
	if jobs := h.started(); len(jobs) != 1 {
		t.Fatalf("canceled migration started: %d jobs", len(jobs))
	}
	noResult(t, results)
}

func TestCancelRunningMigration(t *testing.T) {
	h := newHarness(t)
	results, progress := migrationHarness(t, h, "m1")
	h.step()
	jobs := h.started()
	if len(jobs) != 1 || jobs[0].job.Type != sv.JobVaultMigrate {
		t.Fatalf("migration not started: %+v", jobs)
	}
	jobs[0].h.OnMsg(sv.Msg{Type: "progress", Bytes: 10, TotalBytes: 40, Transfers: 1})
	if p := <-progress; p.Bytes != 10 || p.TotalBytes != 40 || p.Transfers != 1 {
		t.Fatalf("progress %+v", p)
	}
	// A Pause Rule stop underway would re-queue the job; the cancel wins.
	h.probes.setBattery(true)
	h.now = h.now.Add(10 * time.Second)
	h.step()
	if !h.e.CancelMigration("m1") {
		t.Fatal("running migration not found")
	}
	h.waitIdle()
	jobs[0].mu.Lock()
	stopped := jobs[0].stopped
	jobs[0].mu.Unlock()
	if !stopped {
		t.Fatal("running migration not stopped")
	}
	h.probes.setBattery(false)
	h.now = h.now.Add(time.Minute)
	h.step()
	if n := len(h.started()); n != 1 {
		t.Fatalf("canceled migration re-queued: %d jobs", n)
	}
	noResult(t, results)
}

func TestCancelRunningMigrationWithoutPause(t *testing.T) {
	h := newHarness(t)
	results, _ := migrationHarness(t, h, "m1")
	h.step()
	if !h.e.CancelMigration("m1") {
		t.Fatal("running migration not found")
	}
	h.waitIdle()
	h.step()
	if n := len(h.started()); n != 1 {
		t.Fatalf("canceled migration re-queued: %d jobs", n)
	}
	noResult(t, results)
	if h.e.CancelMigration("m1") {
		t.Fatal("a finished migration must not be cancelable")
	}
}

func TestLocationMissingPausesOnlyThatItem(t *testing.T) {
	h := newHarness(t)
	a := h.addItem("a")
	h.addItem("b")
	if err := removeAll(a.StoragePath); err != nil {
		t.Fatal(err)
	}
	h.e.rt["a"].due = h.now
	h.e.rt["b"].due = h.now
	h.step()
	if it := h.state("a"); it.State != StatePaused || it.LastError != ReasonLocationMissing {
		t.Fatalf("a: %+v", it)
	}
	jobs := h.started()
	if len(jobs) != 1 || jobs[0].job.ID != "b" {
		t.Fatalf("b must still sync: %d jobs", len(jobs))
	}
}

func TestCreateRejectsLockedVault(t *testing.T) {
	h := newHarness(t)
	_, err := h.e.Create(context.Background(), CreateParams{ConnectionID: "vault-conn", Kind: "folder", RemotePath: "x"})
	if appCode(err) != "offline.vaultLocked" {
		t.Fatalf("got %v", err)
	}
}

func TestConflictStatusForPaths(t *testing.T) {
	h := newHarness(t)
	a := h.addItem("a")
	h.e.localChange("a", "Sub/Changed.wav", 0, false, h.now)
	got, err := h.e.StatusForPaths([]string{
		filepath.Join(a.StoragePath, "Bassline.Konflikt 2026-09-23 1405.wav"),
		filepath.Join(a.StoragePath, "Sub/Changed.wav"),
		filepath.Join(a.StoragePath, "Clean.wav"),
		"/elsewhere/file",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		filepath.Join(a.StoragePath, "Bassline.Konflikt 2026-09-23 1405.wav"): "conflict",
		filepath.Join(a.StoragePath, "Sub/Changed.wav"):                       "syncing",
		filepath.Join(a.StoragePath, "Clean.wav"):                             "synced",
		"/elsewhere/file": "none",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}
}

func TestResyncRequestedDuringRunSurvives(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.e.rt["a"].due = h.now
	h.step()
	// While the run is in progress the excludes change: bisync must resync next time.
	if _, err := h.e.Update(context.Background(), UpdateParams{ID: "a", Excludes: &[]string{"*.tmp"}}); err != nil {
		t.Fatal(err)
	}
	h.started()[0].complete(sv.StatusOK, "")
	h.waitIdle()
	if it := h.state("a"); !it.NeedsResync || it.LastSyncAt == nil {
		t.Fatalf("finishing an older run cleared a newer resync request: %+v", it)
	}
	h.step()
	jobs := h.started()
	var params map[string]any
	_ = json.Unmarshal(jobs[len(jobs)-1].job.Bisync, &params)
	if params["resync"] != true {
		t.Fatalf("next run must resync: %v", params)
	}
	jobs[len(jobs)-1].complete(sv.StatusOK, "")
	h.waitIdle()
	if h.state("a").NeedsResync {
		t.Fatal("a completed resync run must clear the flag")
	}
}

func TestPauseTransitionLoggedAfterStatusQuery(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.probes.setBattery(true)
	// The App asking for the pause state must not hide the transition from the scheduler.
	if ps := h.e.PauseStatus(); !ps.Effective || len(ps.ActiveRules) != 1 {
		t.Fatalf("pause status %+v", ps)
	}
	h.e.rt["a"].due = h.now
	h.step()
	got, _ := h.st.QueryActivity(store.ActivityFilter{Search: "Syncing paused"})
	if len(got) != 1 {
		t.Fatalf("pause transition not logged: %+v", got)
	}
}

func TestNoRunsWhileRelocatingOrRemoving(t *testing.T) {
	h := newHarness(t)
	it := h.addItem("a")
	h.e.stopItem("a") // what Relocate and Remove do before touching files
	h.e.rt["a"].due = h.now
	h.e.rt["a"].nextSafety = h.now
	h.step()
	_ = h.e.SyncNow("a")
	h.step()
	if n := len(h.started()); n != 0 {
		t.Fatalf("%d run(s) started on an item that is being moved", n)
	}
	h.e.restartItem(it)
	h.step()
	if len(h.started()) != 1 {
		t.Fatal("item must run again after the move finished")
	}
}

func TestSetSelectionTrashesDeselected(t *testing.T) {
	h := newHarness(t)
	a := h.addItem("a")
	writeFiles(t, a.StoragePath, "x/1.txt", "y/2.txt")
	var trashed []string
	moveToTrash = func(p string) error { trashed = append(trashed, p); return os.RemoveAll(p) }
	t.Cleanup(func() { moveToTrash = platform.MoveToTrash })
	if _, err := h.e.SetSelection(context.Background(), SelectionParams{ID: "a", Kind: "files", Files: []string{"x/"}}); err != nil {
		t.Fatal(err)
	}
	it := h.state("a")
	if it.Kind != "files" || !slices.Equal(it.Files, []string{"x"}) || !it.NeedsResync {
		t.Fatalf("stored item %+v", it)
	}
	filters, _ := os.ReadFile(paths.ForHome(h.dir).FiltersFile("a"))
	if !strings.Contains(string(filters), "+ /x\n+ /x/**\n- **\n") {
		t.Fatalf("filters %q", filters)
	}
	if !slices.Equal(trashed, []string{filepath.Join(a.StoragePath, "y")}) {
		t.Fatalf("trashed %v", trashed)
	}
	if _, err := os.Stat(filepath.Join(a.StoragePath, "x/1.txt")); err != nil {
		t.Fatalf("selected local copy gone: %v", err)
	}
	// Widening back to the whole root trashes nothing.
	if _, err := h.e.SetSelection(context.Background(), SelectionParams{ID: "a", Kind: "folder"}); err != nil {
		t.Fatal(err)
	}
	if it := h.state("a"); it.Kind != "folder" || len(it.Files) != 0 || len(trashed) != 1 {
		t.Fatalf("widened item %+v, trashed %v", it, trashed)
	}
}

func TestLocalAdditionsJoinTheSelection(t *testing.T) {
	h := newHarness(t)
	inner := h.addItem("in") // folder item at store/in: another item's Storage Location
	it := store.OfflineItem{ID: "t", ConnectionID: "c1", Kind: "files", Files: []string{"T/B", "T/C"},
		StoragePath: filepath.Join(h.dir, "store"), Excludes: store.DefaultSettings().DefaultExcludes, State: StateIdle}
	if err := h.st.InsertOfflineItem(it); err != nil {
		t.Fatal(err)
	}
	h.e.mu.Lock()
	h.e.itemRT("t")
	h.e.mu.Unlock()
	writeFiles(t, it.StoragePath, "T/B/b", "T/New/n", "T/.DS_Store", "T/~$Doc.docx", "T/x.Konflikt 2026-09-23 1200.txt", "top.txt")
	writeFiles(t, inner.StoragePath, "song.wav")

	// The watcher starts a run for a new child of a partial folder only.
	h.e.localChange("t", "T/New/n", 0, false, h.now)
	h.e.localChange("t", "T/~$Doc.docx", 0, false, h.now)
	h.e.localChange("t", "in/song.wav", 0, false, h.now)
	pending := h.e.rt["t"].pendingPaths
	if !pending[filepath.Join(it.StoragePath, "T/New/n")] || len(pending) != 1 {
		t.Fatalf("pending %v", pending)
	}

	// After a successful run: T/New and top.txt join, the missing T/C leaves.
	h.e.rt["t"].due = h.now
	h.now = h.now.Add(time.Hour)
	h.step()
	jobs := h.started()
	if len(jobs) != 1 || jobs[0].job.ID != "t" {
		t.Fatalf("jobs %+v", jobs)
	}
	jobs[0].complete(sv.StatusOK, "")
	h.waitIdle()
	got := h.state("t")
	if !slices.Equal(got.Files, []string{"T/B", "T/New", "top.txt"}) || !got.NeedsResync {
		t.Fatalf("adjusted item %+v", got)
	}
	filters, _ := os.ReadFile(paths.ForHome(h.dir).FiltersFile("t"))
	if !strings.Contains(string(filters), "+ /T/New/**\n") || strings.Contains(string(filters), "/T/C") {
		t.Fatalf("filters %q", filters)
	}
	if h.e.rt["t"].due != h.now {
		t.Fatal("the resync must run right away")
	}

	// Nothing new: the Selection stays; it never becomes empty.
	for _, p := range []string{"T", "top.txt"} {
		_ = os.RemoveAll(filepath.Join(it.StoragePath, p))
	}
	h.e.mu.Lock()
	fresh := h.state("t")
	changed := h.e.adjustSelectionLocked(&fresh, true)
	h.e.mu.Unlock()
	if changed || len(fresh.Files) != 3 {
		t.Fatalf("emptied Selection: changed=%v %+v", changed, fresh)
	}
}

func TestNoPruneAfterSelectionChangedDuringRun(t *testing.T) {
	h := newHarness(t)
	it := store.OfflineItem{ID: "t", ConnectionID: "c1", Kind: "files", Files: []string{"a.wav"},
		StoragePath: filepath.Join(h.dir, "store"), Excludes: store.DefaultSettings().DefaultExcludes, State: StateIdle}
	if err := h.st.InsertOfflineItem(it); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, it.StoragePath, "a.wav")
	h.e.mu.Lock()
	h.e.itemRT("t").due = h.now
	h.e.mu.Unlock()
	h.step()
	jobs := h.started()
	if len(jobs) != 1 {
		t.Fatalf("jobs %+v", jobs)
	}
	// b.wav is merged while the run is in progress; it is not local yet.
	h.e.mu.Lock()
	merged := h.state("t")
	merged.Files = []string{"a.wav", "b.wav"}
	h.e.requestResyncLocked(&merged)
	if err := h.st.UpdateOfflineItem(merged); err != nil {
		t.Fatal(err)
	}
	h.e.mu.Unlock()
	jobs[0].complete(sv.StatusOK, "")
	h.waitIdle()
	if got := h.state("t"); !slices.Equal(got.Files, []string{"a.wav", "b.wav"}) || !got.NeedsResync {
		t.Fatalf("merged entry pruned: %+v", got)
	}
}

func TestStatusForNestedStorage(t *testing.T) {
	h := newHarness(t)
	inner := h.addItem("in") // folder item "in" at store/in
	outer := store.OfflineItem{ID: "out", ConnectionID: "c1", Kind: "files", Files: []string{"other/x.wav"},
		StoragePath: filepath.Join(h.dir, "store"), State: StateIdle} // listed before the inner item
	if err := h.st.InsertOfflineItem(outer); err != nil {
		t.Fatal(err)
	}
	h.e.localChange("in", "Song.wav", 0, false, h.now)
	song, other, loose := filepath.Join(inner.StoragePath, "Song.wav"), filepath.Join(outer.StoragePath, "other/x.wav"),
		filepath.Join(outer.StoragePath, "loose.txt")
	got, err := h.e.StatusForPaths([]string{song, other, loose})
	if err != nil {
		t.Fatal(err)
	}
	if got[song] != "syncing" || got[other] != "synced" || got[loose] != "none" {
		t.Fatalf("the innermost item that syncs a path decides: %v", got)
	}
}

func TestErrorReason(t *testing.T) {
	const missing = `error reading source root directory: Directory Not Found`
	for _, c := range []struct {
		state, reason, code string
	}{
		{StateError, missing, "offline.cloudFolderMissing"},
		{StateError, "couldn't connect: 503 Service Unavailable", "offline.syncFailed"},
		{StateError, "", ""},
		{StatePaused, pauserules.RuleBattery, ""},
		{StatePaused, ReasonLocationMissing, ""},
		{StateNeedsConfirmation, "too many deletes", ""},
	} {
		got := errorReason(store.OfflineItem{State: c.state, LastError: c.reason})
		if got.Code != c.code {
			t.Errorf("%s %q: code %q, want %q", c.state, c.reason, got.Code, c.code)
		}
		// The raw reason is never translated: it travels as the detail.
		want := ""
		if c.code != "" {
			want = c.reason
		}
		if got.Detail() != want {
			t.Errorf("%s %q: detail %q, want %q", c.state, c.reason, got.Detail(), want)
		}
	}
}
