package pauserules

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rclone/rclone/fs"

	"github.com/DonMikone/CloudWire/core/internal/store"
)

type fakeProbes struct {
	exes                   []string
	battery, lowPower      bool
	expensive, constrained bool
	known                  bool
	busy, total            uint64
}

func (f *fakeProbes) RunningExecutables() ([]string, error) { return f.exes, nil }
func (f *fakeProbes) OnBattery() bool                       { return f.battery }
func (f *fakeProbes) LowPowerMode() bool                    { return f.lowPower }
func (f *fakeProbes) Network() (bool, bool, bool)           { return f.expensive, f.constrained, f.known }
func (f *fakeProbes) CPUTicks() (uint64, uint64, error)     { return f.busy, f.total, nil }

func newEval(p *fakeProbes, mutate func(*store.Settings)) *Evaluator {
	st := store.DefaultSettings()
	st.PauseRules.StudioMode.Apps = []string{"/Applications/REAPER.app"}
	if mutate != nil {
		mutate(&st)
	}
	return &Evaluator{Probes: p, Settings: func() store.Settings { return st }}
}

func ids(rules []ActiveRule) map[string]string {
	m := map[string]string{}
	for _, r := range rules {
		m[r.ID] = r.Detail
	}
	return m
}

func TestStudioMode(t *testing.T) {
	p := &fakeProbes{exes: []string{"/Applications/REAPER.app/Contents/MacOS/REAPER"}}
	if got := ids(newEval(p, nil).Evaluate()); got[RuleStudioMode] != "REAPER" {
		t.Fatalf("studio mode not active: %v", got)
	}
	// A helper outside Contents/MacOS or a similarly named bundle must not match.
	p.exes = []string{"/Applications/REAPER.app/Contents/Plugins/x", "/Applications/REAPER.app.bak/Contents/MacOS/REAPER"}
	if got := ids(newEval(p, nil).Evaluate()); got[RuleStudioMode] != "" {
		t.Fatalf("false positive: %v", got)
	}
	p.exes = []string{"/Applications/REAPER.app/Contents/MacOS/REAPER"}
	off := newEval(p, func(s *store.Settings) { s.PauseRules.StudioMode.Enabled = false })
	if len(off.Evaluate()) != 0 {
		t.Fatal("disabled rule active")
	}
}

func TestBatteryAndLowPower(t *testing.T) {
	p := &fakeProbes{battery: true}
	if ids(newEval(p, nil).Evaluate())[RuleBattery] != "battery" {
		t.Fatal("battery rule inactive")
	}
	p = &fakeProbes{lowPower: true}
	if ids(newEval(p, nil).Evaluate())[RuleBattery] != "lowPowerMode" {
		t.Fatal("low power mode rule inactive")
	}
	if len(newEval(p, func(s *store.Settings) { s.PauseRules.Battery.Enabled = false }).Evaluate()) != 0 {
		t.Fatal("disabled battery rule active")
	}
}

func TestMeteredNetwork(t *testing.T) {
	p := &fakeProbes{expensive: true, known: true}
	if ids(newEval(p, nil).Evaluate())[RuleMeteredNetwork] != "expensive" {
		t.Fatal("expensive network not detected")
	}
	p = &fakeProbes{constrained: true, known: true}
	if ids(newEval(p, nil).Evaluate())[RuleMeteredNetwork] != "constrained" {
		t.Fatal("constrained network not detected")
	}
	p = &fakeProbes{expensive: true, known: false}
	if len(newEval(p, nil).Evaluate()) != 0 {
		t.Fatal("unknown network state must not pause")
	}
	p = &fakeProbes{expensive: true, known: true}
	if len(newEval(p, func(s *store.Settings) { s.PauseRules.MeteredNetwork.Enabled = false }).Evaluate()) != 0 {
		t.Fatal("disabled rule active")
	}
}

func TestCPUAveraging(t *testing.T) {
	p := &fakeProbes{}
	e := newEval(p, nil)
	sample := func(busyDelta, totalDelta uint64) {
		p.busy += busyDelta
		p.total += totalDelta
		e.SampleCPU()
	}
	sample(0, 1000) // baseline, no sample yet
	sample(900, 1000)
	sample(900, 1000)
	if ids(e.Evaluate())[RuleCPU] != "" {
		t.Fatal("rule active before the 30 s window is full")
	}
	sample(300, 1000) // average of 90, 90, 30 = 70: not above 70
	if ids(e.Evaluate())[RuleCPU] != "" {
		t.Fatal("average equal to threshold must not pause")
	}
	sample(900, 1000) // 90, 30, 90 = 70 → still not; oldest sample dropped
	sample(900, 1000) // 30, 90, 90 = 70
	sample(950, 1000) // 90, 90, 95 = 91.7
	if got := ids(e.Evaluate())[RuleCPU]; got != "92%" {
		t.Fatalf("cpu rule detail %q, want 92%%", got)
	}
	e.ResetCPU()
	if ids(e.Evaluate())[RuleCPU] != "" {
		t.Fatal("reset must clear samples")
	}
	hi := newEval(p, func(s *store.Settings) { s.PauseRules.CPU.ThresholdPercent = 95 })
	hi.samples = []float64{96, 96, 96}
	if ids(hi.Evaluate())[RuleCPU] == "" {
		t.Fatal("configurable threshold ignored")
	}
}

func TestBwLimitStrings(t *testing.T) {
	cases := []struct {
		b    store.BandwidthSettings
		want string
	}{
		{store.BandwidthSettings{Enabled: true, UploadMiBps: 5, DownloadMiBps: 20}, "5M:20M"},
		{store.BandwidthSettings{Enabled: true, UploadMiBps: 0, DownloadMiBps: 20}, "off:20M"},
		{store.BandwidthSettings{Enabled: true, UploadMiBps: 5, DownloadMiBps: 0}, "5M:off"},
		{store.BandwidthSettings{Enabled: true}, "off"},
		{store.BandwidthSettings{Enabled: false, UploadMiBps: 5, DownloadMiBps: 20}, "off"},
	}
	for _, c := range cases {
		got := BwLimit(c.b)
		if got != c.want {
			t.Errorf("BwLimit(%+v) = %q, want %q", c.b, got, c.want)
		}
		// rclone must accept the string, with "off" meaning unlimited on that side.
		var tt fs.BwTimetable
		if err := tt.Set(got); err != nil {
			t.Errorf("rclone rejects %q: %v", got, err)
			continue
		}
		if c.want == "off:20M" {
			if len(tt) != 1 || tt[0].Bandwidth.Tx > 0 || tt[0].Bandwidth.Rx != 20*fs.Mebi {
				t.Errorf("off:20M parsed as %+v", tt)
			}
		}
		if c.want == "5M:off" {
			if len(tt) != 1 || tt[0].Bandwidth.Tx != 5*fs.Mebi || tt[0].Bandwidth.Rx > 0 {
				t.Errorf("5M:off parsed as %+v", tt)
			}
		}
	}
}

func TestDetectDAWs(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"REAPER.app", "Audio/Ableton Live 12 Suite.app", "Safari.app", "Deep/Deeper/Cubase 14.app"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := DetectDAWs(root)
	want := []string{filepath.Join(root, "Audio/Ableton Live 12 Suite.app"), filepath.Join(root, "REAPER.app")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("DetectDAWs = %v, want %v", got, want)
	}
}
