//go:build unix

package onefrog

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让命令在独立进程组里运行，取消时整组杀掉（包括它启动的子进程）。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
