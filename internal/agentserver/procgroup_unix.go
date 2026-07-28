//go:build !windows

package agentserver

import (
	"os/exec"
	"syscall"
)

// detachFromTerminalSignals puts the app-server in its own process group.
//
// Without it the child shares klein's group, and a terminal Ctrl+C is delivered
// by the tty driver to every process in the foreground group — not just klein.
// The app-server installs no SIGINT handler (verified against codex-cli
// 0.144.1: it dies with signal 2), so the interrupt klein means as "stop this
// turn" killed the backend outright. Every prompt after that failed on a dead
// pipe: `codex turn/start EOF`.
//
// klein already handles Ctrl+C itself — signal.Notify in executeTurn cancels the
// turn context, and runTurn asks the backend to stop via turn/interrupt — so the
// child has no need to see the signal at all.
//
// Nothing is leaked by taking it out of the group. Close() shuts the child down
// on the normal path, and if klein dies without running it the stdin pipe closes,
// which is the stdio-server contract for "exit".
func detachFromTerminalSignals(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
