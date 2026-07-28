//go:build windows

package agentserver

import (
	"os/exec"
	"syscall"
)

// detachFromTerminalSignals is the Windows counterpart of the unix version: a
// new process group means the console's Ctrl+C event stops at klein instead of
// also reaching the app-server. See the unix file for why that matters.
func detachFromTerminalSignals(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
