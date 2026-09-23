//go:build darwin

// Package platform wraps the macOS system APIs the Core needs: process
// priority, running apps, power, CPU load, network path and sleep/wake
// notifications, Trash, and free space.
package platform

/*
#cgo CFLAGS: -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Foundation -framework IOKit -framework Network -framework CoreFoundation
#include <errno.h>
#include <stdlib.h>
#include <sys/resource.h>
#include <libproc.h>
#include <mach/mach.h>
#include "platform.h"

static int cwSetBackground(void) {
	return setpriority(PRIO_DARWIN_PROCESS, 0, PRIO_DARWIN_BG);
}

static int cwGetBackground(void) {
	errno = 0;
	return getpriority(PRIO_DARWIN_PROCESS, 0);
}

static int cwCPUTicks(unsigned long long *user, unsigned long long *sys, unsigned long long *idle, unsigned long long *nice) {
	host_cpu_load_info_data_t info;
	mach_msg_type_number_t count = HOST_CPU_LOAD_INFO_COUNT;
	kern_return_t kr = host_statistics(mach_host_self(), HOST_CPU_LOAD_INFO, (host_info_t)&info, &count);
	if (kr != KERN_SUCCESS) return (int)kr;
	*user = info.cpu_ticks[CPU_STATE_USER];
	*sys = info.cpu_ticks[CPU_STATE_SYSTEM];
	*idle = info.cpu_ticks[CPU_STATE_IDLE];
	*nice = info.cpu_ticks[CPU_STATE_NICE];
	return 0;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SetBackgroundPriority puts the calling process into the Darwin background
// band: lowest CPU priority, throttled disk and network I/O.
func SetBackgroundPriority() error {
	if rc, err := C.cwSetBackground(); rc != 0 {
		return fmt.Errorf("setpriority(PRIO_DARWIN_BG): %v", err)
	}
	return nil
}

// IsBackgroundPriority reports whether the process runs in the background band.
func IsBackgroundPriority() bool {
	return C.cwGetBackground() > 0
}

// RunningExecutables returns the executable paths of all visible processes.
func RunningExecutables() ([]string, error) {
	n := C.proc_listallpids(nil, 0)
	if n <= 0 {
		return nil, errors.New("proc_listallpids failed")
	}
	pids := make([]C.int, int(n)+64)
	n = C.proc_listallpids(unsafe.Pointer(&pids[0]), C.int(len(pids))*C.int(unsafe.Sizeof(pids[0])))
	if n <= 0 {
		return nil, errors.New("proc_listallpids failed")
	}
	buf := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
	out := make([]string, 0, n)
	for _, pid := range pids[:n] {
		if pid <= 0 {
			continue
		}
		l := C.proc_pidpath(pid, unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
		if l > 0 {
			out = append(out, string(buf[:l]))
		}
	}
	return out, nil
}

// OnBattery reports whether the Mac currently draws from its battery.
func OnBattery() bool { return C.cwOnBattery() == 1 }

// LowPowerMode reports whether Low Power Mode is enabled.
func LowPowerMode() bool { return C.cwLowPowerMode() == 1 }

// CPUTicks returns cumulative busy and total CPU ticks over all cores.
func CPUTicks() (busy, total uint64, err error) {
	var user, sys, idle, nice C.ulonglong
	if rc := C.cwCPUTicks(&user, &sys, &idle, &nice); rc != 0 {
		return 0, 0, fmt.Errorf("host_statistics: %d", int(rc))
	}
	busy = uint64(user) + uint64(sys) + uint64(nice)
	return busy, busy + uint64(idle), nil
}

// FreeBytes returns the bytes available to the user on path's volume.
func FreeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

// MoveToTrash moves path into the user's Trash.
func MoveToTrash(path string) error {
	cp := C.CString(path)
	defer C.free(unsafe.Pointer(cp))
	var cerr *C.char
	if C.cwMoveToTrash(cp, &cerr) != 0 {
		msg := "unknown error"
		if cerr != nil {
			msg = C.GoString(cerr)
			C.free(unsafe.Pointer(cerr))
		}
		return fmt.Errorf("move %s to Trash: %s", path, msg)
	}
	return nil
}

// ---- Network path monitor ----

// NetworkState is the current primary network path.
type NetworkState struct {
	Satisfied   bool
	Expensive   bool
	Constrained bool
}

var (
	netMu       sync.Mutex
	netState    NetworkState
	netHandlers []func(NetworkState)
	netOnce     sync.Once
	netKnown    atomic.Bool
)

// OnNetworkChange registers fn for path updates and starts the monitor.
func OnNetworkChange(fn func(NetworkState)) {
	netMu.Lock()
	netHandlers = append(netHandlers, fn)
	netMu.Unlock()
	netOnce.Do(func() { C.cwStartNetworkMonitor() })
}

// Network returns the last observed network path (start the monitor first).
func Network() (NetworkState, bool) {
	netMu.Lock()
	defer netMu.Unlock()
	return netState, netKnown.Load()
}

//export goNetworkChanged
func goNetworkChanged(satisfied, expensive, constrained C.int) {
	st := NetworkState{Satisfied: satisfied != 0, Expensive: expensive != 0, Constrained: constrained != 0}
	netMu.Lock()
	netState = st
	netKnown.Store(true)
	hs := append([]func(NetworkState){}, netHandlers...)
	netMu.Unlock()
	for _, h := range hs {
		go h(st)
	}
}

// ---- Sleep / wake ----

var (
	powerMu     sync.Mutex
	willSleepFn func()
	didWakeFn   func()
	powerOnce   sync.Once
)

// OnPowerEvents registers sleep/wake callbacks. willSleep gets at most 5 s
// before the system is allowed to sleep.
func OnPowerEvents(willSleep, didWake func()) error {
	powerMu.Lock()
	willSleepFn, didWakeFn = willSleep, didWake
	powerMu.Unlock()
	var err error
	powerOnce.Do(func() {
		if C.cwStartPowerNotifications() != 0 {
			err = errors.New("IORegisterForSystemPower failed")
		}
	})
	return err
}

//export goSystemWillSleep
func goSystemWillSleep() {
	powerMu.Lock()
	fn := willSleepFn
	powerMu.Unlock()
	if fn == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

//export goSystemDidWake
func goSystemDidWake() {
	powerMu.Lock()
	fn := didWakeFn
	powerMu.Unlock()
	if fn != nil {
		go fn()
	}
}
