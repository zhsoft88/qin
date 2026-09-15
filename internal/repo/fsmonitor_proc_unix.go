//go:build !windows
// +build !windows

package repo

import (
	"syscall"
	"time"
)

// terminateGrace is how long a daemon is given to notice SIGTERM before it is
// killed outright. It is short on purpose: a daemon holds no state that a clean
// exit would flush — everything it writes is written as it goes — so the
// graceful signal is a courtesy, not a requirement. A stale heartbeat and a
// dead generation are as safe as a clean shutdown, and the next client tells
// them apart from a live daemon by the same rule either way.
const terminateGrace = 2 * time.Second

// detachSysProcAttr is what makes a spawned daemon outlive the process that
// started it. setsid puts the child in a session of its own, so it is not in
// the terminal's process group and neither a hangup nor a Ctrl-C sent to the
// group reaches it.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// daemonPidAlive reports whether a process id is in use.
//
// This is only a hint. A pid is reused freely, so a yes is not proof that the
// daemon is the process that holds it, and the client does not rely on it:
// the daemon's heartbeat and its answer to a sync request are what establish
// that. A no, on the other hand, is conclusive enough to skip an attempt.
func daemonPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	// EPERM means the process exists and belongs to someone else.
	return err == nil || err == syscall.EPERM
}

// terminateDaemon stops the process holding pid.
//
// The caller must have established that this pid is still the daemon it
// intends to stop — read it from the daemon's own record and check that the
// record has not since changed — because there is nothing here that would
// notice the number having been handed to an unrelated process in the
// meantime.
func terminateDaemon(pid int) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	deadline := time.Now().Add(terminateGrace)
	for time.Now().Before(deadline) {
		if !daemonPidAlive(pid) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
