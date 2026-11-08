//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly || solaris || aix

package workspace

import (
	"os"
	"syscall"
)

// Platforms are listed explicitly instead of relying on the `unix` build tag,
// which is not guaranteed to be recognized as a built-in tag by every
// toolchain in use.

// procGroupAttr puts the child in its own process group so a timeout or
// cancel can kill the whole tree (children and grandchildren included)
// instead of orphaning them.
func procGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree kills the child's whole process group. The child was
// started with Setpgid, so its pid is the group id and -pid reaches every
// descendant, not just the direct child — no orphan keeps the log pipes open.
func killProcessTree(p *os.Process) error {
	if p == nil {
		return os.ErrProcessDone
	}
	return syscall.Kill(-p.Pid, syscall.SIGKILL)
}

// pidAlive reports whether a pid is still running.
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// canInspectProcesses reports whether pidAlive can actually inspect other
// processes on this platform.
const canInspectProcesses = true
