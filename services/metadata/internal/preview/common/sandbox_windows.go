//go:build windows

package common

import (
	"os/exec"
	"syscall"
)

// windowsCREATE_NEW_PROCESS_GROUP detaches the child from the parent's
// console group so group termination does not take the worker down with it.
const windowsCREATE_NEW_PROCESS_GROUP = 0x00000200

// sandboxSysProcAttr on Windows: see constant above. Process-group kill
// degrades to killing the direct child (os.Process.Kill); ffmpeg/poppler on
// Windows rarely spawn detached helper trees, and containerized deployments
// (Linux) get the full group-kill behavior from sandbox_unix.go.
func sandboxSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windowsCREATE_NEW_PROCESS_GROUP,
	}
}

// applySandboxAttrs attaches the platform SysProcAttr to the command.
func applySandboxAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = sandboxSysProcAttr()
}

// killTree on Windows kills the direct child only.
func killTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
