// Package domain defines the core agent interfaces and types shared across the
// app, client, and backend layers, free of any concrete infrastructure deps.
package domain

import "context"

// BackendRunner runs a whole conversation turn via an external agent process
// (e.g. the codex app-server). When an Agent has one set, Invoke routes the
// entire turn here instead of the ReAct loop: the external agent runs its own
// reasoning + tool loop and klein takes back the final text.
//
// threadID is empty for a session's first turn; RunTurn returns the (created)
// thread id to persist for continuation.
type BackendRunner interface {
	RunTurn(ctx context.Context, threadID, prompt, developerInstructions string) (newThreadID, response string, err error)
}

// AgentBackend lazily provisions the external process backing a whole-agent
// backend. The app layer depends only on this interface; the concrete
// implementation (codex) is injected from the composition root, keeping the
// app layer free of any concrete backend dependency.
type AgentBackend interface {
	// EnsureBackendProcess starts (or reuses) the backend process for workingDir
	// and returns the turn runner plus a cleanup func. Both are non-nil on
	// success; the caller must invoke cleanup when the agent is done.
	EnsureBackendProcess(ctx context.Context, workingDir string) (BackendRunner, func(), error)
}
