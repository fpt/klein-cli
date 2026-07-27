package tool

import (
	"context"

	"github.com/fpt/klein-cli/internal/sanitize"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// ControlTokenSanitizer wraps a ToolManager and neutralizes chat-template
// control tokens in every tool result before it reaches the model.
//
// Tool results are the main way arbitrary repository text enters a prompt: a
// diff can be clean while a Read of a neighboring file carries a token the
// provider rejects, failing the whole run on content the change never touched.
//
// Only use this around a read-only toolset. Sanitizing rewrites what the model
// sees, so a manager that also exposes Write/Edit could round-trip the
// substitution back to disk and corrupt the file.
type ControlTokenSanitizer struct {
	inner domain.ToolManager
}

// NewControlTokenSanitizer wraps inner so its tool results are sanitized.
func NewControlTokenSanitizer(inner domain.ToolManager) *ControlTokenSanitizer {
	return &ControlTokenSanitizer{inner: inner}
}

// GetTools delegates to the inner tool manager.
func (s *ControlTokenSanitizer) GetTools() map[message.ToolName]message.Tool {
	return s.inner.GetTools()
}

// RegisterTool delegates to the inner tool manager.
func (s *ControlTokenSanitizer) RegisterTool(
	name message.ToolName,
	description message.ToolDescription,
	arguments []message.ToolArgument,
	handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error),
) {
	s.inner.RegisterTool(name, description, arguments, handler)
}

// CallTool runs the tool and sanitizes the text of its result. Errors are
// sanitized too — they routinely quote the file content that caused them.
// Images are base64 and carry no control tokens.
func (s *ControlTokenSanitizer) CallTool(
	ctx context.Context, name message.ToolName, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	res, err := s.inner.CallTool(ctx, name, args)
	res.Text = sanitize.ControlTokens(res.Text)
	res.Error = sanitize.ControlTokens(res.Error)
	// Passed through unwrapped: this decorator adds no failure of its own, and
	// callers match on the inner manager's sentinels.
	return res, err //nolint:wrapcheck // transparent decorator
}
