package supervisor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Handlers receive a worker's output. Callbacks run on the worker's reader
// goroutines and must not block for long.
type Handlers struct {
	OnMsg func(Msg)
	OnLog func(LogLine)
}

// Worker is a running worker process.
type Worker struct {
	Job Job

	cmd      *exec.Cmd
	stdinMu  sync.Mutex
	stdin    io.WriteCloser
	done     chan struct{}
	exitErr  error
	stopping bool
	mu       sync.Mutex
	result   *Msg
	stats    chan Msg
	statsMu  sync.Mutex // one stats request at a time
	stderr   *tailBuffer
}

// Executable is the binary spawned as worker (os.Executable by default).
var Executable = func() (string, error) { return os.Executable() }

// Spawn starts "<exe> worker" and sends it the job.
func Spawn(job Job, secrets Secrets, h Handlers) (*Worker, error) {
	exe, err := Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(exe, "worker")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = os.Environ()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	w := &Worker{Job: job, cmd: cmd, stdin: stdin, done: make(chan struct{}), stats: make(chan Msg, 1), stderr: newTail(8 << 10)}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	start, _ := json.Marshal(Start{Job: job, Secrets: secrets})
	if _, err := stdin.Write(append(start, '\n')); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("send job: %w", err)
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		w.readStdout(stdout, h)
	}()
	go func() {
		defer readers.Done()
		w.readStderr(stderr, h)
	}()
	go func() {
		readers.Wait()
		err := cmd.Wait()
		w.mu.Lock()
		w.exitErr = err
		w.mu.Unlock()
		close(w.done)
	}()
	return w, nil
}

func (w *Worker) readStdout(r io.Reader, h Handlers) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var m Msg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch m.Type {
		case "stats":
			select {
			case w.stats <- m:
			default:
			}
		case "result":
			w.mu.Lock()
			mm := m
			w.result = &mm
			w.mu.Unlock()
		}
		if h.OnMsg != nil {
			h.OnMsg(m)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

func (w *Worker) readStderr(r io.Reader, h Handlers) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		var l LogLine
		if json.Unmarshal([]byte(line), &l) != nil || l.Msg == "" && l.Level == "" {
			// Non-JSON output (Go panics, cgo prints): keep for diagnostics.
			w.stderr.Write(line)
			if h.OnLog != nil {
				h.OnLog(LogLine{Level: "error", Msg: line, Raw: line})
			}
			continue
		}
		// bisync re-logs its whole captured report as one record; skip it.
		if strings.HasPrefix(l.Msg, "{") {
			continue
		}
		l.Msg = strings.TrimSpace(ansi.ReplaceAllString(l.Msg, ""))
		l.Raw = line
		// Ordinary rclone errors reach the Activity Log via OnLog; only fatal
		// messages explain an unexpected exit.
		if l.Level == "critical" || l.Level == "emergency" || l.Level == "alert" {
			w.stderr.Write(l.Msg)
		}
		if h.OnLog != nil {
			h.OnLog(l)
		}
	}
	_, _ = io.Copy(io.Discard, r)
}

// Send writes a command to the worker's stdin.
func (w *Worker) Send(c Command) error {
	b, _ := json.Marshal(c)
	w.stdinMu.Lock()
	defer w.stdinMu.Unlock()
	if w.stdin == nil {
		return errors.New("worker stdin closed")
	}
	_, err := w.stdin.Write(append(b, '\n'))
	return err
}

// Done is closed when the process has exited.
func (w *Worker) Done() <-chan struct{} { return w.done }

// PID returns the worker's process id.
func (w *Worker) PID() int { return w.cmd.Process.Pid }

// Stats asks the worker for stats and waits up to timeout for the reply.
func (w *Worker) Stats(timeout time.Duration) (Msg, error) {
	w.statsMu.Lock()
	defer w.statsMu.Unlock()
	select {
	case <-w.stats: // drop a stale reply
	default:
	}
	if err := w.Send(Command{Cmd: "stats"}); err != nil {
		return Msg{}, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case m := <-w.stats:
		return m, nil
	case <-w.done:
		return Msg{}, errors.New("worker exited")
	case <-t.C:
		return Msg{}, errors.New("worker did not answer in time")
	}
}

// Stop asks the worker to stop, then kills its process group after grace.
func (w *Worker) Stop(grace time.Duration) {
	w.mu.Lock()
	w.stopping = true
	w.mu.Unlock()
	_ = w.Send(Command{Cmd: "stop"})
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-w.done:
	case <-t.C:
		w.Kill()
		<-w.done
	}
}

// Kill sends SIGKILL to the worker's process group.
func (w *Worker) Kill() {
	w.mu.Lock()
	w.stopping = true
	w.mu.Unlock()
	if w.cmd.Process != nil {
		_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// Outcome returns the final result after the worker exited. A worker that
// died without a result maps to "stopped" (if stopped by us) or "error".
func (w *Worker) Outcome() Msg {
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.result != nil {
		return *w.result
	}
	if w.stopping {
		return Msg{Type: "result", Status: StatusStopped}
	}
	msg := "worker exited unexpectedly"
	if w.exitErr != nil {
		msg += ": " + w.exitErr.Error()
	}
	if tail := strings.TrimSpace(w.stderr.String()); tail != "" {
		msg += ": " + lastLine(tail)
	}
	return Msg{Type: "result", Status: StatusError, Error: msg}
}

// ExitErr returns the process exit error (nil while running).
func (w *Worker) ExitErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exitErr
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// tailBuffer keeps the last n bytes of stderr text.
type tailBuffer struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func newTail(n int) *tailBuffer { return &tailBuffer{n: n} }

func (t *tailBuffer) Write(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, line...)
	t.buf = append(t.buf, '\n')
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)
