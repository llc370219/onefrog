//go:build !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !solaris && !aix

package workspace

import (
	"os"
	"syscall"
)

// procGroupAttr returns an empty attr on platforms without process groups;
// the field still needs a *syscall.SysProcAttr to compile against exec.Cmd.
func procGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// killProcessTree falls back to killing only the direct child; there is no
// process-group equivalent on these platforms.
func killProcessTree(p *os.Process) error {
	if p == nil {
		return os.ErrProcessDone
	}
	return p.Kill()
}

// pidAlive cannot inspect other processes here.
func pidAlive(pid int) bool { return false }

// canInspectProcesses reports whether pidAlive can actually inspect other
// processes on this platform.
const canInspectProcesses = false
