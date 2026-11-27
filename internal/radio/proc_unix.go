//go:build unix

package radio

import (
	"os/exec"
	"syscall"
)

// ownGroup starts c in a process group of its own, so that signalling it
// reaches anything it starts.
func ownGroup(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signal asks c's process group to end (SIGTERM), or kills it (SIGKILL).
func signal(c *exec.Cmd, kill bool) {
	if c.Process == nil {
		return
	}
	sig := syscall.SIGTERM
	if kill {
		sig = syscall.SIGKILL
	}
	_ = syscall.Kill(-c.Process.Pid, sig)
}
