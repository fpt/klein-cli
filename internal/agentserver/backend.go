package agentserver

import (
	"context"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// settings.LLM.Backend values that select a whole-agent app-server backend.
//
// Both speak the same JSON-RPC app-server protocol, so one Runner drives either;
// they differ only in the binary spawned and how it is configured.
// BackendAppServer is the generic case — any local agent implementing the subset
// of the protocol used here (initialize/thread/turn plus `dynamicTools`); codex
// is kept separate only because it carries codex-specific behavior (sandbox
// modes, the login probe).
//
// "app-server" here means the codex-app-server compatible protocol, and nothing
// else. This backend was briefly called `acp`, which was wrong in a way worth
// recording: "ACP" also names the agentclientprotocol.com standard
// (session/new, session/prompt, session/update), which klein does not speak and
// which rs-gallium — the reference implementation of this backend — has ruled
// out (fpt/rs-gallium#15, reaffirmed in #13). Naming the backend after the
// protocol it actually speaks keeps the two senses distinct.
const (
	BackendCodex     = "codex"
	BackendAppServer = "appserver"
)

// IsAgentBackend reports whether a backend name selects a whole-agent backend —
// one that runs its own reasoning + tool loop, bypassing klein's ReAct loop —
// as opposed to a chat model plugged in as a domain.LLM.
func IsAgentBackend(backend string) bool {
	return backend == BackendCodex || backend == BackendAppServer
}

// Approval policy values for RunnerOptions.ApprovalPolicy: ApprovalNever
// auto-accepts (headless surfaces), ApprovalOnRequest prompts (interactive repl).
const (
	ApprovalNever     = "never"
	ApprovalOnRequest = "on-request"
)

// noop is the cleanup returned when a backend owns no closable resource.
func noop() {}

// Start eagerly spawns the app-server when settings select a whole-agent
// backend, returning (nil, nil) for every other backend. The returned Runner is
// shared across sessions and must be Closed by the caller; wrap it with
// NewSharedBackend to inject it into agent construction. opts supplies the
// mode's approval behavior (headless surfaces pass ApprovalNever).
func Start(
	ctx context.Context, settings *config.Settings, workingDir string, logger *pkgLogger.Logger, opts RunnerOptions,
) (*Runner, error) {
	if !IsAgentBackend(settings.LLM.Backend) {
		return nil, nil
	}
	logger.Info("Starting app-server backend", "backend", settings.LLM.Backend, "model", settings.LLM.Model)
	// The runner has no logger of its own; this is the one place upstream that
	// has one, and it is what lets an unrendered item type get reported rather
	// than dropped.
	opts.Logger = logger
	// NewRunnerFromSettings spawns the app-server and validates it is
	// authenticated, so a login/config failure surfaces here at startup.
	runner, err := NewRunnerFromSettings(ctx, settings, workingDir, opts)
	if err != nil {
		return nil, err
	}
	logger.Info("Agent backend ready", "backend", settings.LLM.Backend)
	return runner, nil
}

// lazyBackend spawns a fresh app-server on EnsureBackendProcess and hands back a
// cleanup that closes it. Used by the single-agent surfaces (CLI one-shot /
// interactive repl / claw repl) where the agent owns the backend process.
type lazyBackend struct {
	settings *config.Settings
	logger   *pkgLogger.Logger
	opts     RunnerOptions
}

// NewBackend returns an AgentBackend that starts a dedicated app-server when the
// agent is constructed. opts carries the mode's approval behavior.
func NewBackend(settings *config.Settings, logger *pkgLogger.Logger, opts RunnerOptions) domain.AgentBackend {
	return &lazyBackend{settings: settings, logger: logger, opts: opts}
}

func (b *lazyBackend) EnsureBackendProcess(
	ctx context.Context, workingDir string,
) (domain.BackendRunner, func(), error) {
	runner, err := Start(ctx, b.settings, workingDir, b.logger, b.opts)
	if err != nil {
		return nil, noop, err
	}
	return runner, func() { _ = runner.Close() }, nil
}

// sharedBackend wraps an already-started Runner. EnsureBackendProcess returns it
// with a noop cleanup — the owner (e.g. the server that started it once) is
// responsible for Close. Used when many per-session agents share one process.
type sharedBackend struct{ runner *Runner }

// NewSharedBackend adapts an already-started Runner to the AgentBackend
// interface so it can be injected into agent construction. The caller retains
// ownership of the Runner's lifetime.
func NewSharedBackend(runner *Runner) domain.AgentBackend {
	return &sharedBackend{runner: runner}
}

func (b *sharedBackend) EnsureBackendProcess(_ context.Context, _ string) (domain.BackendRunner, func(), error) {
	return b.runner, noop, nil
}

// Select returns an AgentBackend for the configured backend, or nil when the
// backend needs no external process (every backend other than codex/appserver).
// opts supplies the approval behavior for the lazy (agent-owned) case.
func Select(settings *config.Settings, logger *pkgLogger.Logger, opts RunnerOptions) domain.AgentBackend {
	if !IsAgentBackend(settings.LLM.Backend) {
		return nil
	}
	return NewBackend(settings, logger, opts)
}
