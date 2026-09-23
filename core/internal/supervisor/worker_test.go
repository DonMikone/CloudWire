package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeWorker installs a shell script as the worker executable.
func fakeWorker(t *testing.T, script string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-core")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Executable
	Executable = func() (string, error) { return p, nil }
	t.Cleanup(func() { Executable = old })
}

const cooperative = `
[ "$1" = worker ] || exit 9
read job
case "$job" in *'"configPass":"s3cret"'*) ;; *) echo '{"type":"result","status":"error","error":"no secret"}'; exit 1;; esac
echo '{"type":"ready"}'
echo '{"level":"info","msg":"Copied (new)","object":"a.wav"}' >&2
while read l; do
  case "$l" in
    *stop*) echo '{"type":"result","status":"stopped"}'; exit 0;;
    *stats*) echo '{"type":"stats","vfs":{"diskCache":1}}';;
  esac
done
`

func TestWorkerProtocolAndGracefulStop(t *testing.T) {
	fakeWorker(t, cooperative)
	logs := make(chan LogLine, 4)
	w, err := Spawn(Job{Type: JobMount, ID: "m"}, Secrets{ConfigPass: "s3cret"}, Handlers{OnLog: func(l LogLine) { logs <- l }})
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.Stats(10 * time.Second)
	if err != nil || string(m.VFS) != `{"diskCache":1}` {
		t.Fatalf("stats: %+v %v", m, err)
	}
	select {
	case l := <-logs:
		if l.Msg != "Copied (new)" || l.Object != "a.wav" {
			t.Fatalf("log line %+v", l)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("stderr JSON log not delivered")
	}
	w.Stop(10 * time.Second)
	if out := w.Outcome(); out.Status != StatusStopped || out.Error != "" {
		t.Fatalf("outcome %+v", out)
	}
}

func TestWorkerKilledAfterGrace(t *testing.T) {
	fakeWorker(t, "read job\ntrap '' INT TERM\nwhile true; do sleep 1; done\n")
	w, err := Spawn(Job{Type: JobBisync}, Secrets{}, Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	w.Stop(200 * time.Millisecond)
	if time.Since(start) > 8*time.Second {
		t.Fatal("stop did not kill the unresponsive worker")
	}
	if out := w.Outcome(); out.Status != StatusStopped {
		t.Fatalf("a worker stopped by us must report stopped, got %+v", out)
	}
}

func TestWorkerCrashReportsStderr(t *testing.T) {
	fakeWorker(t, "read job\necho 'panic: boom' >&2\nexit 3\n")
	w, err := Spawn(Job{Type: JobBisync}, Secrets{}, Handlers{})
	if err != nil {
		t.Fatal(err)
	}
	out := w.Outcome()
	if out.Status != StatusError || !strings.Contains(out.Error, "boom") || !strings.Contains(out.Error, "exit status 3") {
		t.Fatalf("crash outcome %+v", out)
	}
}
