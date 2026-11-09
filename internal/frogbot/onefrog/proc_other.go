//go:build !unix

package onefrog

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}
