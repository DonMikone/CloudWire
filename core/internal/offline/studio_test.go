package offline

import (
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/pauserules"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

const logicExe = "/Applications/Logic Pro.app/Contents/MacOS/Logic Pro"

func (h *harness) studioMode(enabled bool) {
	h.t.Helper()
	if _, err := h.st.UpdateSettings(map[string]any{"pauseRules": map[string]any{"studioMode": map[string]any{
		"enabled": enabled, "apps": []string{"/Applications/GarageBand.app", "/Applications/Logic Pro.app"}}}}); err != nil {
		h.t.Fatal(err)
	}
}

func TestStudioModeStopsRunCleanlyAndRequeues(t *testing.T) {
	h := newHarness(t)
	h.studioMode(true)
	h.addItem("a")
	job := h.startRun("a")
	// bisync may end a stopped run with its interruption as the error.
	job.mu.Lock()
	job.stopOut = sv.Msg{Type: "result", Status: sv.StatusError, Error: "context canceled"}
	job.mu.Unlock()

	h.probes.setExes("/sbin/launchd", logicExe)
	h.now = h.now.Add(pauseCheckInterval)
	h.step()
	h.waitIdle()
	job.mu.Lock()
	stopped, grace := job.stopped, job.grace
	job.mu.Unlock()
	if !stopped || grace != stopGrace {
		t.Fatalf("Logic Pro must stop the run cleanly: stopped=%v grace=%v", stopped, grace)
	}
	if it := h.state("a"); it.State != StatePaused || it.LastError != pauserules.RuleStudioMode {
		t.Fatalf("item %s/%s, want paused by Studio Mode", it.State, it.LastError)
	}
	if rt := h.e.rt["a"]; rt.retryCount != 0 || !rt.retryAt.IsZero() || len(h.notify.kinds) != 0 {
		t.Fatalf("a clean stop is no failure: retries %d, notifications %v", rt.retryCount, h.notify.kinds)
	}
	// No run while Logic Pro is open.
	for range 3 {
		h.now = h.now.Add(30 * time.Second)
		h.step()
	}
	if n := len(h.started()); n != 1 {
		t.Fatalf("%d runs while Logic Pro runs", n)
	}
	// Logic Pro quit: the stopped run comes first.
	h.probes.setExes("/sbin/launchd")
	h.now = h.now.Add(30 * time.Second)
	h.step()
	if jobs := h.started(); len(jobs) != 2 || jobs[1].job.ID != "a" {
		t.Fatalf("stopped run must resume when Logic Pro quits, got %d runs", len(jobs))
	}
}

func TestSleepStopsRunCleanlyUntilWake(t *testing.T) {
	if sleepStopGrace <= 0 || sleepStopGrace >= platform.MaxSleepDelay {
		t.Fatalf("sleep stop grace %v must end before the sleep acknowledgement (%v)", sleepStopGrace, platform.MaxSleepDelay)
	}
	h := newHarness(t)
	h.addItem("a")
	job := h.startRun("a")
	h.e.Sleep()
	select {
	case <-job.Done():
	default:
		t.Fatal("Sleep returned before the run ended")
	}
	h.waitIdle()
	job.mu.Lock()
	grace := job.grace
	job.mu.Unlock()
	if grace != sleepStopGrace {
		t.Fatalf("sleep stop grace %v, want %v", grace, sleepStopGrace)
	}
	if it := h.state("a"); it.State != StatePending {
		t.Fatalf("item %s, want pending", it.State)
	}
	h.step()
	if n := len(h.started()); n != 1 {
		t.Fatalf("a run started while the Mac goes to sleep (%d runs)", n)
	}
	h.e.Wake()
	h.step()
	if jobs := h.started(); len(jobs) != 2 || jobs[1].job.ID != "a" {
		t.Fatalf("stopped run must resume after wake, got %d runs", len(jobs))
	}
}

func TestSleepWithoutWakeReleasesLater(t *testing.T) {
	h := newHarness(t)
	h.addItem("a")
	h.startRun("a")
	h.e.Sleep()
	h.waitIdle()
	h.now = h.now.Add(sleepHold)
	h.step()
	if n := len(h.started()); n != 2 {
		t.Fatalf("runs must resume when no wake is reported, got %d", n)
	}
}

func TestHoldForRestart(t *testing.T) {
	h := newHarness(t)
	h.studioMode(false)
	h.addItem("a")
	job := h.startRun("a")
	if reason, held := h.e.HoldForRestart(); !held || reason.Code != "update.waitingForSync" {
		t.Fatalf("restart during a run: held=%v %+v", held, reason)
	}
	job.complete(sv.StatusOK, "")
	h.waitIdle()
	// Logic Pro runs: no restart, even with Studio Mode off.
	h.probes.setExes(logicExe)
	if reason, held := h.e.HoldForRestart(); !held || reason.Code != "update.waitingForStudioApp" || reason.Params["app"] != "Logic Pro" {
		t.Fatalf("restart while Logic Pro runs: held=%v %+v", held, reason)
	}
	h.probes.setExes()
	if _, held := h.e.HoldForRestart(); held {
		t.Fatal("restart held although nothing runs")
	}
	// The Core stops now: no run starts meanwhile.
	h.e.rt["a"].due = h.now
	h.step()
	if n := len(h.started()); n != 1 {
		t.Fatalf("a run started after the restart was granted (%d runs)", n)
	}
}
