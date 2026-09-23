//go:build darwin

package platform

/*
#include <errno.h>
#include <fcntl.h>
#include <signal.h>
#include <spawn.h>
#include <stdlib.h>
#include <sys/wait.h>
#include <unistd.h>

// Private libSystem API (used by Chromium and terminal apps): the child becomes
// responsible for itself in TCC instead of inheriting our responsibility.
extern int responsibility_spawnattrs_setdisclaim(posix_spawnattr_t *attrs, int disclaim);
extern char **environ;

static int cwSpawnDisclaimed(const char *path, char *const argv[], int outfd, pid_t *pid) {
	posix_spawnattr_t attr;
	posix_spawn_file_actions_t fa;
	int rc = posix_spawnattr_init(&attr);
	if (rc != 0) return rc;
	responsibility_spawnattrs_setdisclaim(&attr, 1);
	posix_spawnattr_setflags(&attr, POSIX_SPAWN_CLOEXEC_DEFAULT);
	posix_spawn_file_actions_init(&fa);
	posix_spawn_file_actions_addopen(&fa, 0, "/dev/null", O_RDONLY, 0);
	posix_spawn_file_actions_adddup2(&fa, outfd, 1);
	posix_spawn_file_actions_adddup2(&fa, outfd, 2);
	rc = posix_spawn(pid, path, &fa, &attr, argv, environ);
	posix_spawn_file_actions_destroy(&fa);
	posix_spawnattr_destroy(&attr);
	return rc;
}

static int cwWaitNoHang(pid_t pid, int *status) {
	return waitpid(pid, status, WNOHANG);
}
*/
import "C"

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// RunDisclaimed runs a program that is responsible for itself in TCC (like a
// program started from the Dock), waits up to timeout and returns its combined
// output and exit status. The Core uses it for diskutil: as the Core's child, diskutil would
// need the Core's "Network Volumes" consent to unmount an NFS Mount and would
// block on a privacy prompt the background Core cannot show.
func RunDisclaimed(timeout time.Duration, path string, args ...string) ([]byte, error) {
	all := append([]string{path}, args...)
	cargs := make([]*C.char, len(all)+1)
	for i, a := range all {
		cargs[i] = C.CString(a)
	}
	defer func() {
		for _, p := range cargs[:len(all)] {
			C.free(unsafe.Pointer(p))
		}
	}()
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var pid C.pid_t
	rc := C.cwSpawnDisclaimed(cpath, (**C.char)(unsafe.Pointer(&cargs[0])), C.int(pw.Fd()), &pid)
	_ = pw.Close()
	if rc != 0 {
		_ = pr.Close()
		return nil, fmt.Errorf("spawn %s: %w", path, syscall.Errno(rc))
	}
	outCh := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(pr, 1<<20))
		_ = pr.Close()
		outCh <- b
	}()
	deadline := time.Now().Add(timeout)
	for {
		var status C.int
		r := C.cwWaitNoHang(pid, &status)
		if r == pid {
			out := <-outCh
			ws := syscall.WaitStatus(status)
			switch {
			case ws.Exited() && ws.ExitStatus() == 0:
				return out, nil
			case ws.Signaled():
				return out, fmt.Errorf("%s: killed by %v", path, ws.Signal())
			}
			return out, fmt.Errorf("%s: exit status %d", path, ws.ExitStatus())
		}
		if r < 0 {
			return nil, fmt.Errorf("wait for %s failed", path)
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(int(pid), syscall.SIGKILL)
			var st C.int
			C.waitpid(pid, &st, 0)
			return nil, fmt.Errorf("%s: timed out after %s", path, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
