//go:build !unix

package radio

import "os/exec"

// The client runs the radio on Linux (R13); elsewhere a program is only
// killed.
func ownGroup(*exec.Cmd) {}

func signal(c *exec.Cmd, _ bool) {
	if c.Process != nil {
		_ = c.Process.Kill()
	}
}
