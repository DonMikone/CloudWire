package worker

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonMikone/CloudWire/core/internal/identity"
	"github.com/DonMikone/CloudWire/core/internal/rcl"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cw-worker")
	if err != nil {
		panic(err)
	}
	if err := rcl.Init(filepath.Join(dir, "rclone.conf"), "test-pass", rcl.Options{CacheDir: filepath.Join(dir, "cache")}); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// bisyncWorker runs a bisync job like the worker process and returns its
// command channel and messages.
func bisyncWorker(t *testing.T, job sv.Job) (chan<- sv.Command, <-chan sv.Msg) {
	t.Helper()
	cmds := make(chan sv.Command, 8)
	r, w := io.Pipe()
	msgs := make(chan sv.Msg, 64)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			var m sv.Msg
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				msgs <- m
			}
		}
		close(msgs)
	}()
	go func() {
		runBisync(job, cmds, &emitter{w: w})
		_ = w.Close()
	}()
	return cmds, msgs
}

func next(t *testing.T, msgs <-chan sv.Msg, typ string) sv.Msg {
	t.Helper()
	timeout := time.After(time.Minute)
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatalf("worker ended before a %q message", typ)
			}
			if m.Type == typ {
				return m
			}
		case <-timeout:
			t.Fatalf("no %q message", typ)
		}
	}
}

func TestBisyncWaitsForRenameBookkeeping(t *testing.T) {
	local, cloud, work := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(cloud, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	params := func(path1 string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"path1": path1, "path2": cloud, "workdir": work, "resync": true})
		return b
	}
	job := sv.Job{Type: sv.JobBisync, ID: "x", Bisync: params(filepath.Join(local, "missing")), Identity: &sv.IdentityJob{
		StoragePath: local, RemoteFs: cloud, ConnFs: "/", Files: []string{""}, Snapshot: filepath.Join(work, "identity.json")}}

	// Stopped while the engine records the renames: no sync.
	cmds, msgs := bisyncWorker(t, job)
	if m := next(t, msgs, "renames"); len(m.Renames) != 0 {
		t.Fatalf("renames without a snapshot: %+v", m.Renames)
	}
	cmds <- sv.Command{Cmd: "stop"}
	if res := next(t, msgs, "result"); res.Status != sv.StatusStopped {
		t.Fatalf("result %+v", res)
	}

	// Continued with the params the renames changed; the identities are recorded after.
	cmds, msgs = bisyncWorker(t, job)
	next(t, msgs, "renames")
	cmds <- sv.Command{Cmd: "continue", Bisync: params(local)}
	if res := next(t, msgs, "result"); res.Status != sv.StatusOK {
		t.Fatalf("result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(local, "a.txt")); err != nil {
		t.Fatalf("not synced: %v", err)
	}
	s, ok := identity.Load(job.Identity.Snapshot)
	if !ok || s.Local["a.txt"].Ino == 0 || s.Provider != identity.ProviderNone {
		t.Fatalf("snapshot %+v", s)
	}
}
