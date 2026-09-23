//go:build darwin

package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("CW_PLATFORM_CHILD") == "1" {
		if IsBackgroundPriority() {
			os.Stdout.WriteString("bg-before ")
		}
		if err := SetBackgroundPriority(); err != nil {
			os.Stdout.WriteString("error " + err.Error())
			os.Exit(1)
		}
		if IsBackgroundPriority() {
			os.Stdout.WriteString("bg-after")
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestBackgroundPriorityInChild(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "CW_PLATFORM_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "bg-after" {
		t.Fatalf("child reported %q, want background only after SetBackgroundPriority", got)
	}
}

func TestRunningExecutablesContainsSelf(t *testing.T) {
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	paths, err := RunningExecutables()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if p == exe {
			return
		}
	}
	t.Fatalf("own executable %s not among %d processes", exe, len(paths))
}

func TestCPUTicksMonotonic(t *testing.T) {
	b1, t1, err := CPUTicks()
	if err != nil {
		t.Fatal(err)
	}
	x := 0
	for i := range 20_000_000 {
		x += i
	}
	_ = x
	b2, t2, _ := CPUTicks()
	if t2 <= t1 || b2 < b1 || b1 > t1 {
		t.Fatalf("ticks not monotonic/consistent: busy %d→%d total %d→%d", b1, b2, t1, t2)
	}
}

func TestFreeBytes(t *testing.T) {
	n, err := FreeBytes(t.TempDir())
	if err != nil || n == 0 {
		t.Fatalf("FreeBytes = %d, %v", n, err)
	}
	if _, err := FreeBytes("/definitely/missing/path"); err == nil {
		t.Fatal("missing path must fail")
	}
}

func TestRunDisclaimed(t *testing.T) {
	out, err := RunDisclaimed(5*time.Second, "/bin/sh", "-c", "echo out; echo err >&2")
	if err != nil || string(out) != "out\nerr\n" {
		t.Fatalf("output %q, %v", out, err)
	}
	if _, err := RunDisclaimed(5*time.Second, "/usr/bin/false"); err == nil || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("exit status not reported: %v", err)
	}
	start := time.Now()
	if _, err := RunDisclaimed(200*time.Millisecond, "/bin/sleep", "10"); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout not enforced: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout took too long")
	}
}
