package codex

import (
	"context"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// BackendName is the settings.LLM.Backend value that selects the codex backend.
const BackendName = "codex"

// Approval policy values for RunnerOptions.ApprovalPolicy: ApprovalNever
// auto-accepts (headless surfaces), ApprovalOnRequest prompts (interactive repl).
const (
	ApprovalNever     = "never"
	ApprovalOnRequest = "on-request"
)

// noop is the cleanup returned when a backend owns no closable resource.
func noop() {}

// Start eagerly spawns the codex app-server when settings select the codex
// backend, returning (nil, nil) for every other backend. The returned Runner is
// shared across sessions and must be Closed by the caller; wrap it with
// NewSharedBackend to inject it into agent construction. opts supplies the
// mode's approval behavior (headless surfaces pass ApprovalNever).
func Start(
	ctx context.Context, settings *config.Settings, workingDir string, logger *pkgLogger.Logger, opts RunnerOptions,
) (*Runner, error) {
	if settings.LLM.Backend != BackendName {
		return nil, nil
	}
	logger.Info("Starting codex app-server backend", "model", settings.LLM.Model)
	// NewRunnerFromSettings spawns the app-server and validates it is
	// authenticated, so a login/config failure surfaces here at startup.
	runner, err := NewRunnerFromSettings(ctx, settings, workingDir, opts)
	if err != nil {
		return nil, err
	}
	logger.Info("Codex backend ready")
	return runner, nil
}

// lazyBackend spawns a fresh codex app-server on EnsureBackendProcess and hands
// back a cleanup that closes it. Used by the single-agent surfaces (CLI one-shot
// / interactive repl / claw repl) where the agent owns the codex process.
type lazyBackend struct {
	settings *config.Settings
	logger   *pkgLogger.Logger
	opts     RunnerOptions
}

// NewBackend returns an AgentBackend that starts a dedicated codex app-server
// when the agent is constructed. opts carries the mode's approval behavior.
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
// backend needs no external process (every backend other than codex). opts
// supplies the codex approval behavior for the lazy (agent-owned) case.
func Select(settings *config.Settings, logger *pkgLogger.Logger, opts RunnerOptions) domain.AgentBackend {
	if settings.LLM.Backend != BackendName {
		return nil
	}
	return NewBackend(settings, logger, opts)
}
