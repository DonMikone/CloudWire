package worker

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
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

func TestServeMountAnswersPingWhileVFSBlocks(t *testing.T) {
	release := make(chan struct{})
	forgot := make(chan string, 1)
	old := vfsCall
	vfsCall = func(method string, in map[string]any) (json.RawMessage, error) {
		switch method {
		case "vfs/stats":
			<-release
			return json.RawMessage(`{"ok":1}`), nil
		case "vfs/forget":
			forgot <- in["dir"].(string)
		}
		return nil, nil
	}
	t.Cleanup(func() { vfsCall = old })

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
	var unmounted atomic.Bool
	code := make(chan int, 1)
	go func() {
		code <- serveMount("/x", cmds, &emitter{w: w}, func() error { unmounted.Store(true); return nil }, func() bool { return true })
	}()

	cmds <- sv.Command{Cmd: "stats"}
	cmds <- sv.Command{Cmd: "forget", Dir: "a"}
	cmds <- sv.Command{Cmd: "ping"}
	next(t, msgs, "pong")
	if d := <-forgot; d != "a" {
		t.Fatalf("forget dir %q", d)
	}

	cmds <- sv.Command{Cmd: "stop"}
	select {
	case c := <-code:
		if c != 0 {
			t.Fatalf("exit code %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stop blocked behind a VFS call")
	}
	if !unmounted.Load() {
		t.Fatal("unmount not called")
	}
	if m := next(t, msgs, "result"); m.Status != sv.StatusStopped {
		t.Fatalf("result %+v", m)
	}
	close(release)
	if m := next(t, msgs, "stats"); string(m.VFS) != `{"ok":1}` {
		t.Fatalf("stats %+v", m)
	}
	_ = w.Close()
}
