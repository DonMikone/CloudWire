// Package pauserules decides whether Offline Item syncing must pause:
// Studio Mode, battery, metered network and CPU load.
package pauserules

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"

	"github.com/DonMikone/CloudWire/core/internal/msg"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/store"
)

// Rule ids.
const (
	RuleStudioMode     = "studioMode"
	RuleBattery        = "battery"
	RuleMeteredNetwork = "meteredNetwork"
	RuleCPU            = "cpu"
	RuleManual         = "manual"
)

// Probes observe the system (fakes in tests).
type Probes interface {
	RunningExecutables() ([]string, error)
	OnBattery() bool
	LowPowerMode() bool
	// Network returns expensive/constrained and whether the state is known.
	Network() (expensive, constrained, known bool)
	CPUTicks() (busy, total uint64, err error)
	// BundleID returns the CFBundleIdentifier of the app bundle at path ("" if none).
	BundleID(bundle string) string
}

// ActiveRule is a rule that currently pauses syncing. Detail is the running
// app, "battery"/"lowPowerMode", "expensive"/"constrained" or the CPU load;
// the embedded text says the same in words (package msg).
type ActiveRule struct {
	ID     string `json:"id"`
	Detail string `json:"detail"`
	msg.Text
}

// Evaluator evaluates the Pause Rules.
type Evaluator struct {
	Probes   Probes
	Settings func() store.Settings

	mu        sync.Mutex
	samples   []float64
	lastBusy  uint64
	lastTotal uint64
	bundleIDs map[string]string // bundle path -> CFBundleIdentifier ("" = none)
}

// SampleCPU records the CPU utilisation since the previous sample. Call it
// every 10 s while a sync job is pending or running.
func (e *Evaluator) SampleCPU() {
	busy, total, err := e.Probes.CPUTicks()
	if err != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastTotal != 0 && total > e.lastTotal {
		pct := float64(busy-e.lastBusy) / float64(total-e.lastTotal) * 100
		e.samples = append(e.samples, pct)
		keep := e.window()
		if len(e.samples) > keep {
			e.samples = e.samples[len(e.samples)-keep:]
		}
	}
	e.lastBusy, e.lastTotal = busy, total
}

// ResetCPU forgets samples (sampling stopped because no job is pending).
func (e *Evaluator) ResetCPU() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.samples, e.lastBusy, e.lastTotal = nil, 0, 0
}

func (e *Evaluator) window() int {
	n := 3
	if e.Settings != nil {
		if w := e.Settings().PauseRules.CPU.WindowSeconds; w >= 10 {
			n = w / 10
		}
	}
	return n
}

// CPUAverage returns the average of the collected samples and whether the
// window is full.
func (e *Evaluator) CPUAverage() (float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.samples) == 0 {
		return 0, false
	}
	var sum float64
	for _, s := range e.samples {
		sum += s
	}
	return sum / float64(len(e.samples)), len(e.samples) >= e.window()
}

// Evaluate returns the active rules.
func (e *Evaluator) Evaluate() []ActiveRule {
	st := e.Settings()
	var out []ActiveRule
	pr := st.PauseRules
	if pr.StudioMode.Enabled {
		if app := e.runningStudioApp(pr.StudioMode.Apps); app != "" {
			out = append(out, ActiveRule{ID: RuleStudioMode, Detail: app, Text: msg.New("pause.studioMode", "app", app)})
		}
	}
	if pr.Battery.Enabled {
		if e.Probes.OnBattery() {
			out = append(out, ActiveRule{ID: RuleBattery, Detail: "battery", Text: msg.New("pause.battery")})
		} else if e.Probes.LowPowerMode() {
			out = append(out, ActiveRule{ID: RuleBattery, Detail: "lowPowerMode", Text: msg.New("pause.lowPowerMode")})
		}
	}
	if pr.MeteredNetwork.Enabled {
		if exp, con, known := e.Probes.Network(); known && (exp || con) {
			r := ActiveRule{ID: RuleMeteredNetwork, Detail: "expensive", Text: msg.New("pause.expensiveNetwork")}
			if con {
				r.Detail, r.Text = "constrained", msg.New("pause.constrainedNetwork")
			}
			out = append(out, r)
		}
	}
	if pr.CPU.Enabled {
		if avg, full := e.CPUAverage(); full && avg > float64(pr.CPU.ThresholdPercent) {
			percent := fmt.Sprintf("%.0f", avg)
			out = append(out, ActiveRule{ID: RuleCPU, Detail: percent + "%", Text: msg.New("pause.cpu", "percent", percent)})
		}
	}
	return out
}

// StudioAppRunning returns the name of a running Studio Mode app, also while
// Studio Mode is off ("" if none runs). The Core does not restart for an
// update while one runs.
func (e *Evaluator) StudioAppRunning() string {
	return e.runningStudioApp(e.Settings().PauseRules.StudioMode.Apps)
}

// appExeDir marks a bundle's main executables: <bundle>.app/Contents/MacOS/.
const appExeDir = ".app/Contents/MacOS/"

// runningStudioApp returns the name of the first listed app bundle with a
// running executable inside <bundle>/Contents/MacOS/. Paths compare without
// case and Unicode normalisation (APFS ignores both); a running bundle at
// another path, e.g. a moved or translocated copy, matches by its
// CFBundleIdentifier.
func (e *Evaluator) runningStudioApp(apps []string) string {
	if len(apps) == 0 {
		return ""
	}
	exes, err := e.Probes.RunningExecutables()
	if err != nil {
		return ""
	}
	seen := map[string]bool{}
	var running []string
	for _, exe := range exes {
		i := strings.Index(exe, appExeDir)
		if i < 0 {
			continue
		}
		b := exe[:i+len(".app")]
		if k := pathKey(b); !seen[k] {
			seen[k] = true
			running = append(running, b)
		}
	}
	for _, app := range apps {
		if seen[pathKey(app)] {
			return appName(app)
		}
	}
	byID := map[string]string{}
	for _, app := range apps {
		if id := e.bundleID(app); id != "" && byID[id] == "" {
			byID[id] = app
		}
	}
	if len(byID) == 0 {
		return ""
	}
	for _, b := range running {
		if id := e.bundleID(b); id != "" && byID[id] != "" {
			return appName(byID[id])
		}
	}
	return ""
}

// bundleID returns the cached bundle identifier of the bundle at path.
func (e *Evaluator) bundleID(path string) string {
	e.mu.Lock()
	id, ok := e.bundleIDs[path]
	e.mu.Unlock()
	if ok {
		return id
	}
	id = e.Probes.BundleID(path)
	e.mu.Lock()
	if e.bundleIDs == nil {
		e.bundleIDs = map[string]string{}
	}
	e.bundleIDs[path] = id
	e.mu.Unlock()
	return id
}

func pathKey(p string) string {
	return strings.ToLower(norm.NFC.String(filepath.Clean(p)))
}

func appName(app string) string {
	return strings.TrimSuffix(filepath.Base(app), ".app")
}

// BwLimit returns rclone's core/bwlimit rate "UP:DOWN" in MiB/s, with "off"
// for an unlimited side, or "off" when the limit is disabled.
func BwLimit(b store.BandwidthSettings) string {
	if !b.Enabled || (b.UploadMiBps <= 0 && b.DownloadMiBps <= 0) {
		return "off"
	}
	side := func(n int) string {
		if n <= 0 {
			return "off"
		}
		return fmt.Sprintf("%dM", n)
	}
	return side(b.UploadMiBps) + ":" + side(b.DownloadMiBps)
}

// DAWPatterns are the app bundles Studio Mode pre-selects.
var DAWPatterns = []string{
	"Logic Pro*.app", "REAPER*.app", "Ableton Live*.app", "Cubase*.app", "Nuendo*.app", "Studio One*.app",
	"Pro Tools*.app", "Bitwig Studio*.app", "FL Studio*.app", "GarageBand*.app", "Reason*.app",
	"Final Cut Pro*.app", "DaVinci Resolve*.app", "Adobe Premiere Pro*.app",
}

// DetectDAWs finds installed DAWs in /Applications and ~/Applications (one
// folder level deep).
func DetectDAWs(roots ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, root := range roots {
		for _, dir := range []string{root, filepath.Join(root, "*")} {
			for _, pat := range DAWPatterns {
				matches, _ := filepath.Glob(filepath.Join(dir, pat))
				for _, m := range matches {
					if !seen[m] {
						seen[m] = true
						out = append(out, m)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// StudioModeDefaults returns the Studio Mode settings of a new installation:
// the installed DAWs, and on when there is at least one.
func StudioModeDefaults(daws []string) store.StudioModeSettings {
	if daws == nil {
		daws = []string{}
	}
	return store.StudioModeSettings{Enabled: len(daws) > 0, Apps: daws}
}

// System implements Probes with the real platform APIs.
type System struct{}

// RunningExecutables implements Probes.
func (System) RunningExecutables() ([]string, error) { return platform.RunningExecutables() }

// OnBattery implements Probes.
func (System) OnBattery() bool { return platform.OnBattery() }

// LowPowerMode implements Probes.
func (System) LowPowerMode() bool { return platform.LowPowerMode() }

// Network implements Probes.
func (System) Network() (bool, bool, bool) {
	st, known := platform.Network()
	return st.Expensive, st.Constrained, known
}

// CPUTicks implements Probes.
func (System) CPUTicks() (uint64, uint64, error) { return platform.CPUTicks() }

// BundleID implements Probes.
func (System) BundleID(bundle string) string { return platform.BundleIdentifier(bundle) }
