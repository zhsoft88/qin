//go:build windows
// +build windows

package repo

import (
	"os"
	"syscall"
)

// Constants the syscall package does not export, with the values from the
// Windows headers. It exports CREATE_NEW_PROCESS_GROUP and nothing else in this
// family, so these two have to be declared here.
const (
	detachedProcess                = 0x00000008
	processQueryLimitedInformation = 0x00001000
	stillActive                    = 259
)

// detachSysProcAttr is what makes a spawned daemon outlive the process that
// started it.
//
// DETACHED_PROCESS is what does the work: the child is created with no console
// at all, so closing the console that ran the client cannot take the daemon
// down with it. CREATE_NO_WINDOW is deliberately not set — it asks for a
// console that is merely hidden, which is a superset of nothing that is wanted
// here and is ignored once DETACHED_PROCESS is given.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: detachedProcess,
		HideWindow:    true, // sets SW_HIDE; no console still means no flash
	}
}

// daemonPidAlive reports whether a process id is in use and has not exited.
//
// A yes is only a hint: Windows reuses process ids, and nothing here ties the
// number to the process that was spawned. The client does not rely on it — the
// daemon's heartbeat and its answer to a sync request are what establish that
// it is alive, and they cannot be forged by an unrelated process that happens
// to have inherited the number. A no, on the other hand, is worth acting on.
func daemonPidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// The least-privileged access right that still permits asking about exit
	// status, so this does not fail merely because the daemon runs elevated.
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// terminateDaemon stops the process holding pid.
//
// This is a hard kill, and it has to be: a detached process has no console, so
// there is no console control handler to deliver an interrupt to and
// os.Interrupt has nowhere to go. Nothing is lost by that — a daemon holds no
// state that a clean exit would flush, everything it writes is written as it
// goes, and a cursor left by a dead daemon cannot be mistaken for a live one,
// because liveness is proved by a sync answer and never by the fact that a
// cursor exists.
//
// The caller must have established that this pid is still the daemon it intends
// to stop; a yes from daemonPidAlive says only that some process holds the
// number.
func terminateDaemon(pid int) error {
	if pid <= 0 {
		return nil
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
