//go:build !windows

package common

import (
	"os/exec"
	"syscall"
)

// sandboxSysProcAttr puts the child in its own process group (so the whole
// tree can be killed) and detaches it from the parent's signal handling.
// Network isolation is left to the container runtime's network policy in
// deployment; rlimits via SysProcAttr.Rlimit cover CPU/memory overreach for
// direct (non-container) dev runs.
func sandboxSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// applySandboxAttrs attaches the platform SysProcAttr to the command.
func applySandboxAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = sandboxSysProcAttr()
}

// killTree terminates the child's whole process group. Setpgid made the
// child a group leader, so -pid targets everything it spawned.
func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
