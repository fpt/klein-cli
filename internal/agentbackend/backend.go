// Package agentbackend is klein's side of the app-server client in
// pkg/agentserver: it turns klein settings into a client Config, adapts klein's
// tool managers, event stream, logger, and approval policy into the interfaces
// that client asks for, and exposes the result as a domain.AgentBackend the app
// layer can inject.
//
// The division is protocol knowledge versus policy. Parsing the wire, rendering
// a JSON Schema, and knowing codex has an account to probe belong to the client.
// Which commands may run unattended, how long a tool argument may be before it
// is truncated for display, and how an approval prompt is phrased belong here —
// they are decisions about klein and its user, and a different program driving
// the same server would decide them differently.
package agentbackend

import (
	"context"

	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agentserver"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// The backend ids live in internal/config, which owns settings vocabulary; the
// client is told a Dialect instead and never sees a settings value. Both ids
// speak the same JSON-RPC app-server protocol, so one Runner drives either — they
// differ in the binary spawned, how it is configured, and whether there is an
// account to probe.
//
// "app-server" here means the codex-app-server compatible protocol, and nothing
// else. This backend was briefly called `acp`, which was wrong in a way worth
// recording: "ACP" also names the agentclientprotocol.com standard
// (session/new, session/prompt, session/update), which klein does not speak and
// which rs-gallium — the reference implementation of this backend — has ruled
// out (fpt/rs-gallium#15, reaffirmed in #13). Naming the backend after the
// protocol it actually speaks keeps the two senses distinct.

// dialectFor maps a settings backend id onto the client's dialect. Anything that
// is not codex is driven as a plain conforming server, which is what an
// unrecognized id should mean: the protocol subset is the contract.
func dialectFor(backend string) agentserver.Dialect {
	if backend == config.BackendCodex {
		return agentserver.DialectCodex
	}
	return agentserver.DialectGeneric
}

// noop is the cleanup returned when a backend owns no closable resource.
func noop() {}

// Start eagerly spawns the app-server when settings select a whole-agent
// backend, returning (nil, nil) for every other backend. The returned Runner is
// shared across sessions and must be Closed by the caller; wrap it with
// NewSharedBackend to inject it into agent construction. opts supplies the
// mode's approval behavior (headless surfaces pass ApprovalNever).
func Start(
	ctx context.Context, settings *config.Settings, workingDir string, logger *pkgLogger.Logger, opts RunnerOptions,
) (*agentserver.Runner, error) {
	if !config.IsAgentServerBackend(settings.LLM.Backend) {
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
	return turnRunner{runner: runner}, func() { _ = runner.Close() }, nil
}

// sharedBackend wraps an already-started Runner. EnsureBackendProcess returns it
// with a noop cleanup — the owner (e.g. the server that started it once) is
// responsible for Close. Used when many per-session agents share one process.
type sharedBackend struct{ runner *agentserver.Runner }

// NewSharedBackend adapts an already-started Runner to the AgentBackend
// interface so it can be injected into agent construction. The caller retains
// ownership of the Runner's lifetime.
func NewSharedBackend(runner *agentserver.Runner) domain.AgentBackend {
	return &sharedBackend{runner: runner}
}

func (b *sharedBackend) EnsureBackendProcess(_ context.Context, _ string) (domain.BackendRunner, func(), error) {
	return turnRunner{runner: b.runner}, noop, nil
}

// Select returns an AgentBackend for the configured backend, or nil when the
// backend needs no external process (every backend other than codex/appserver).
// opts supplies the approval behavior for the lazy (agent-owned) case.
func Select(settings *config.Settings, logger *pkgLogger.Logger, opts RunnerOptions) domain.AgentBackend {
	if !config.IsAgentServerBackend(settings.LLM.Backend) {
		return nil
	}
	return NewBackend(settings, logger, opts)
}
