// Package worker is the job runner executed as "cloudwire-core worker". It
// reads its job from stdin, reports on stdout (JSON lines) and logs rclone
// JSON lines on stderr.
package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	fslog "github.com/rclone/rclone/fs/log"

	"github.com/DonMikone/CloudWire/core/internal/mounts"
	"github.com/DonMikone/CloudWire/core/internal/paths"
	"github.com/DonMikone/CloudWire/core/internal/platform"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

type emitter struct {
	mu sync.Mutex
	w  io.Writer
}

func (e *emitter) emit(m sv.Msg) {
	b, _ := json.Marshal(m)
	e.mu.Lock()
	defer e.mu.Unlock()
	_, _ = e.w.Write(append(b, '\n'))
}

// Run executes one job and returns the process exit code.
func Run(stdin io.Reader, stdout, stderr io.Writer, p paths.Paths) int {
	in := bufio.NewReaderSize(stdin, 64<<10)
	line, err := in.ReadBytes('\n')
	if err != nil {
		fmt.Fprintln(stderr, "worker: no job on stdin:", err)
		return 2
	}
	var start sv.Start
	if err := json.Unmarshal(line, &start); err != nil {
		fmt.Fprintln(stderr, "worker: bad job:", err)
		return 2
	}
	job := start.Job
	out := &emitter{w: stdout}
	fail := func(err error) int {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	if job.Background {
		if err := platform.SetBackgroundPriority(); err != nil {
			return fail(err)
		}
	}
	if err := rcl.Init(p.RcloneConf, start.Secrets.ConfigPass, rcl.Options{JSONLog: true, CacheDir: p.CacheDir}); err != nil {
		return fail(err)
	}
	setupLogging(stderr, job.LogLevel)

	cmds := make(chan sv.Command, 8)
	go func() {
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			var c sv.Command
			if json.Unmarshal(sc.Bytes(), &c) == nil {
				cmds <- c
			}
		}
		// stdin EOF: the supervisor is gone or wants us to stop.
		cmds <- sv.Command{Cmd: "stop"}
		close(cmds)
	}()
	out.emit(sv.Msg{Type: "ready"})

	switch job.Type {
	case sv.JobMount:
		return runMount(job, cmds, out)
	case sv.JobBisync:
		return runBisync(job, cmds, out)
	case sv.JobVaultMigrate:
		return runMigrate(job, cmds, out)
	default:
		return fail(fmt.Errorf("unknown job type %q", job.Type))
	}
}

// setupLogging routes every rclone log record as a JSON line to stderr,
// including records bisync captures for its own report.
func setupLogging(stderr io.Writer, level string) {
	var mu sync.Mutex
	fslog.Handler.SetOutput(func(slog.Level, string) {})
	fslog.Handler.AddOutput(true, func(_ slog.Level, text string) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = io.WriteString(stderr, text)
	})
	if level == "" {
		level = "INFO"
	}
	_, _ = rcl.Call("options/set", map[string]any{"main": map[string]any{"LogLevel": level}})
}

func applyBwLimit(rate string) {
	if rate == "" {
		return
	}
	if _, err := rcl.Call("core/bwlimit", map[string]any{"rate": rate}); err != nil {
		slog.Error("bwlimit", "rate", rate, "err", err)
	}
}

// ---- Mount ----

func runMount(job sv.Job, cmds <-chan sv.Command, out *emitter) int {
	if job.Mount == nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: "missing mount job"})
		return 1
	}
	if job.Mount.Serve {
		return runNFSMount(job.Mount, cmds, out)
	}
	params := job.Mount.Params
	mountPoint, _ := params["mountPoint"].(string)
	if _, err := rcl.Call("mount/mount", params); err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	out.emit(sv.Msg{Type: "mounted", MountPoint: mountPoint})
	return serveMount(mountPoint, cmds, out, func() error {
		_, err := rcl.Call("mount/unmount", map[string]any{"mountPoint": mountPoint})
		return err
	}, func() bool { return mountListed(mountPoint) })
}

// runNFSMount serves NFS on the fixed port and attaches the mount point
// unless the kernel mount from a previous worker is still there.
func runNFSMount(m *sv.MountJob, cmds <-chan sv.Command, out *emitter) int {
	params := map[string]any{}
	for k, v := range m.Params {
		params[k] = v
	}
	params["type"] = "nfs"
	params["addr"] = fmt.Sprintf("localhost:%d", m.Port)
	var started struct {
		ID string `json:"id"`
	}
	if err := rcl.CallInto("serve/start", params, &started); err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	stopServer := func() { _, _ = rcl.Call("serve/stop", map[string]any{"id": started.ID}) }
	// Finder names the volume's server after the host (ADR 0009). The record
	// lives as long as this worker, also when it only takes over an existing
	// kernel mount, in case the NFS client resolves the host again.
	host := mounts.NFSHost(m.MountPoint)
	release, regErr := platform.RegisterLoopbackHost(host)
	if regErr == nil {
		defer release()
	}
	var warning string
	if !mounts.IsMounted(m.MountPoint) {
		if regErr != nil {
			warning = regErr.Error()
			host = "localhost"
		}
		args := []string{"-t", "nfs", "-o", fmt.Sprintf("port=%d", m.Port), "-o", fmt.Sprintf("mountport=%d", m.Port), "-o", "tcp"}
		for _, o := range m.MountOptions {
			args = append(args, "-o", o)
		}
		args = append(args, host+":/", m.MountPoint)
		// Disclaimed: mount must not inherit our (missing) Network Volumes consent.
		if b, err := platform.RunDisclaimed(60*time.Second, "/sbin/mount", args...); err != nil {
			stopServer()
			out.emit(sv.Msg{Type: "result", Status: sv.StatusError,
				Error: fmt.Sprintf("mount failed: %v: %s", err, strings.TrimSpace(string(b)))})
			return 1
		}
	}
	out.emit(sv.Msg{Type: "mounted", MountPoint: m.MountPoint, Error: warning})
	return serveMount(m.MountPoint, cmds, out, func() error {
		// The server is still running, so the system NFS client detaches quickly.
		err := forceUnmount(m.MountPoint)
		stopServer()
		return err
	}, func() bool { return mounts.IsMounted(m.MountPoint) })
}

func forceUnmount(mountPoint string) error {
	if !mounts.IsMounted(mountPoint) {
		return nil
	}
	return mounts.ForceUnmount(mountPoint)
}

// serveMount answers commands until stop/EOF or until the volume is ejected.
func serveMount(mountPoint string, cmds <-chan sv.Command, out *emitter, unmount func() error, attached func() bool) int {
	check := time.NewTicker(5 * time.Second)
	defer check.Stop()
	for {
		select {
		case c, ok := <-cmds:
			if !ok || c.Cmd == "stop" {
				if err := unmount(); err != nil {
					out.emit(sv.Msg{Type: "result", Status: sv.StatusStopped, Error: err.Error()})
					return 1
				}
				out.emit(sv.Msg{Type: "result", Status: sv.StatusStopped})
				return 0
			}
			switch c.Cmd {
			case "stats":
				var st json.RawMessage
				if err := rcl.CallInto("vfs/stats", nil, &st); err != nil {
					st, _ = json.Marshal(map[string]string{"error": err.Error()})
				}
				out.emit(sv.Msg{Type: "stats", VFS: st})
			case "forget":
				in := map[string]any{}
				if c.Dir != "" {
					in["dir"] = c.Dir
				}
				_, _ = rcl.Call("vfs/forget", in)
			}
		case <-check.C:
			// Detect an unmount done outside CloudWire (Finder "Eject").
			if !attached() {
				out.emit(sv.Msg{Type: "result", Status: sv.StatusStopped, Error: "ejected"})
				return 0
			}
		}
	}
}

func mountListed(mountPoint string) bool {
	var res struct {
		MountPoints []struct {
			MountPoint string `json:"MountPoint"`
		} `json:"mountPoints"`
	}
	if err := rcl.CallInto("mount/listmounts", nil, &res); err != nil {
		return true
	}
	for _, m := range res.MountPoints {
		if m.MountPoint == mountPoint {
			return true
		}
	}
	return false
}

// ---- Bisync ----

func runBisync(job sv.Job, cmds <-chan sv.Command, out *emitter) int {
	var params map[string]any
	if err := json.Unmarshal(job.Bisync, &params); err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: "bad bisync params: " + err.Error()})
		return 1
	}
	applyBwLimit(job.BwLimit)
	params["_async"] = true
	jobID, err := startAsync("sync/bisync", params)
	if err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	st, err := waitJob(jobID, cmds, out, gracefulStop)
	if err != nil {
		out.emit(sv.Msg{Type: "result", Status: sv.StatusError, Error: err.Error()})
		return 1
	}
	res := sv.Msg{Type: "result", Stats: st.stats}
	switch {
	case st.Success:
		res.Status = sv.StatusOK
	case IsMassDelete(st.Error):
		res.Status, res.Error = sv.StatusMassDelete, st.Error
	default:
		res.Status, res.Error = sv.StatusError, st.Error
	}
	out.emit(res)
	return 0
}

// IsMassDelete reports whether a bisync error is one of its safety aborts that
// need the user's decision (rclone cmd/bisync/operations.go).
func IsMassDelete(errText string) bool {
	return strings.Contains(errText, "too many deletes") || strings.Contains(errText, "all files were changed")
}

// gracefulStop lets rclone's bisync shut down gracefully: it handles SIGINT
// through lib/atexit (finish the current transfer, keep listings consistent).
func gracefulStop() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
}

func startAsync(method string, params map[string]any) (int64, error) {
	var res struct {
		JobID int64 `json:"jobid"`
	}
	if err := rcl.CallInto(method, params, &res); err != nil {
		return 0, err
	}
	return res.JobID, nil
}

type jobState struct {
	Finished bool   `json:"finished"`
	Success  bool   `json:"success"`
	Error    string `json:"error"`
	stats    json.RawMessage
}

type coreStats struct {
	Bytes      int64    `json:"bytes"`
	TotalBytes int64    `json:"totalBytes"`
	Transfers  int64    `json:"transfers"`
	ETA        *float64 `json:"eta"`
}

// waitJob polls an async rc job every 2 s, reports progress and handles
// stdin commands until the job finishes.
func waitJob(jobID int64, cmds <-chan sv.Command, out *emitter, stop func()) (jobState, error) {
	group := fmt.Sprintf("job/%d", jobID)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	stopping := false
	for {
		select {
		case c, ok := <-cmds:
			if !ok {
				cmds = nil
				continue
			}
			switch c.Cmd {
			case "stop":
				if !stopping {
					stopping = true
					stop()
				}
			case "bwlimit":
				applyBwLimit(c.Rate)
			case "stats":
				var st json.RawMessage
				_ = rcl.CallInto("core/stats", map[string]any{"group": group}, &st)
				out.emit(sv.Msg{Type: "stats", Stats: st})
			}
		case <-tick.C:
			var js jobState
			if err := rcl.CallInto("job/status", map[string]any{"jobid": jobID}, &js); err != nil {
				return js, err
			}
			var raw json.RawMessage
			_ = rcl.CallInto("core/stats", map[string]any{"group": group}, &raw)
			var cs coreStats
			_ = json.Unmarshal(raw, &cs)
			if js.Finished {
				js.stats = raw
				return js, nil
			}
			out.emit(sv.Msg{Type: "progress", Bytes: cs.Bytes, TotalBytes: cs.TotalBytes, Transfers: cs.Transfers, ETA: cs.ETA})
		}
	}
}

// ---- helpers shared with vault migration ----

func jobContext() context.Context { return context.Background() }
