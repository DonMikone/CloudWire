package offline

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/activity"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/pauserules"
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
	done    chan struct{}
	mu      sync.Mutex
	out     sv.Msg
	stopped bool
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
func (j *fakeJob) Send(sv.Command) error { return nil }
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
	e.Run = func(job sv.Job, _ sv.Handlers) (Job, error) {
		j := &fakeJob{job: job, done: make(chan struct{})}
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
	h.e.localChange("a", "Song.wav", t0)
	h.step()
	h.now = t0.Add(30 * time.Second)
	h.e.localChange("a", "Song.wav", h.now)
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
	h.e.localChange("a", ".DS_Store", h.now)
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
	h.started()[0].complete(sv.StatusMassDelete, "too many deletes")
	h.waitIdle()
	if it := h.state("a"); it.State != StateNeedsConfirmation {
		t.Fatalf("state %s", it.State)
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
	h.e.localChange("a", "Sub/Changed.wav", h.now)
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
