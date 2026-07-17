package tool

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// Argument names shared by the review tools.
const (
	reviewArgPath      = "path"
	reviewArgLine      = "line"
	reviewArgEndLine   = "end_line"
	reviewArgSeverity  = "severity"
	reviewArgComment   = "comment"
	reviewArgRationale = "rationale"
	reviewArgSummary   = "summary"
	reviewArgVerdict   = "verdict"
	reviewArgID        = "id"
	reviewArgNote      = "note"
)

// reviewVerdictDefault is used when the model never sets a verdict.
const reviewVerdictDefault = "comment"

// ReviewComment is one accumulated inline review comment. Line/EndLine are
// new-side (RIGHT) line numbers; EndLine equals Line for single-line comments.
//
//nolint:tagliatelle // snake_case keys are the harness contract (mapped to the GitHub reviews API by jq)
type ReviewComment struct {
	Path     string `json:"path"`
	Severity string `json:"severity,omitempty"`
	Body     string `json:"body"`
	// Rationale is why the finding is a problem and what was verified in the
	// code to confirm it — kept separate from Body (problem + fix) so the
	// harness can render it collapsed and it can be audited from the result.
	Rationale string `json:"rationale,omitempty"`
	Line      int    `json:"line"`
	EndLine   int    `json:"end_line,omitempty"`
}

// ResolvedComment marks a previous-round comment the model verified as fixed.
// ID round-trips from the harness's previous_comments input (an opaque
// review-thread id); the harness resolves the thread.
type ResolvedComment struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty"`
}

// ReviewResult is the outcome of a review session, serialized by the
// `klein review` subcommand for the harness to post.
//
//nolint:tagliatelle // snake_case keys are the harness contract (mapped by jq)
type ReviewResult struct {
	Summary  string            `json:"summary"`
	Verdict  string            `json:"verdict"`
	Comments []ReviewComment   `json:"comments"`
	Resolved []ResolvedComment `json:"resolved,omitempty"`
	// Trimmed is the number of lower-severity comments dropped by the
	// max-comments cap (they are not in Comments and were never posted).
	Trimmed   int  `json:"trimmed_comments,omitempty"`
	Finalized bool `json:"finalized"`
	// ForceFullNext asks the harness to run the *next* review as a full
	// review: more must-level findings existed than the cap could post, so an
	// incremental round would miss the ones that were trimmed.
	ForceFullNext bool `json:"force_full_next,omitempty"`
}

// LineValidator checks that an inline comment target [line, endLine] on path
// falls within the diff's commentable ranges (endLine == line for single-line).
// Injected by the subcommand so this package needs no diff knowledge.
type LineValidator func(path string, line, endLine int) error

// reviewSeverities is the classification every inline comment must carry:
// must (fix before merge), major (real bug/regression), minor (edge case,
// robustness), nits (small but worth fixing).
var reviewSeverities = map[string]bool{"must": true, "major": true, "minor": true, "nits": true}

var reviewVerdicts = map[string]bool{"approve": true, reviewVerdictDefault: true, "request_changes": true}

// ReviewToolManager provides AddInlineReview/AddSummaryReview/FinalizeReview
// tools that accumulate a code review in memory. It performs no I/O — the
// `klein review` subcommand reads Result() after the agent run.
type ReviewToolManager struct {
	tools       map[message.ToolName]message.Tool
	validate    LineValidator
	previousIDs map[string]bool
	summary     string
	verdict     string
	comments    []ReviewComment
	resolved    []ResolvedComment

	mu        sync.Mutex
	finalized bool
}

// NewReviewToolManager creates a review tool manager. validate must not be
// nil. previousIDs are the ids of unresolved previous-round comments — the
// ResolveReviewComment tool is only registered when there are any.
func NewReviewToolManager(validate LineValidator, previousIDs []string) *ReviewToolManager {
	m := &ReviewToolManager{
		tools:       make(map[message.ToolName]message.Tool),
		validate:    validate,
		previousIDs: make(map[string]bool, len(previousIDs)),
		verdict:     reviewVerdictDefault,
	}
	for _, id := range previousIDs {
		m.previousIDs[id] = true
	}
	m.register()
	return m
}

func (m *ReviewToolManager) register() {
	m.RegisterTool("AddInlineReview",
		"Add one inline review comment on a specific line of the diff. "+
			"The line MUST be a bracketed new-side line number shown in the annotated diff (a commentable line); "+
			"lines marked 'ctx' are not valid targets. Call once per finding.",
		[]message.ToolArgument{
			{Name: reviewArgPath, Required: true, Type: "string",
				Description: "File path exactly as shown in the diff (e.g. internal/foo.go)"},
			{Name: reviewArgLine, Required: true, Type: "number",
				Description: "New-side line number the comment targets; for a multi-line comment this is the FIRST " +
					"line of the range (end_line is the last)"},
			{Name: reviewArgEndLine, Required: false, Type: "number",
				Description: "For a multi-line comment: the last line of the range; 'line' is then the first. Omit for single-line."},
			{Name: reviewArgSeverity, Required: true, Type: "string",
				Description: "Classification of the finding: must (fix before merge), major (real bug/regression), minor (edge case, robustness), nits (small but worth fixing)"},
			{Name: reviewArgComment, Required: true, Type: "string",
				Description: "The review comment (markdown): the problem and a concrete fix. Keep the reasoning out — put it in rationale."},
			{Name: reviewArgRationale, Required: true, Type: "string",
				Description: "Why this is a problem and what you verified in the code to confirm it — e.g. " +
					"'verified: server.go:42 passes nil when config is absent, so the removed check reintroduces a panic'. " +
					"Recorded separately and shown collapsed, so hand-wavy reasoning is visible."},
		},
		m.handleAddInline)

	m.RegisterTool("AddSummaryReview",
		"Set the overall review summary (markdown) posted as the review body. "+
			"Call once, after all inline comments; calling again replaces the previous summary.",
		[]message.ToolArgument{
			{Name: reviewArgSummary, Required: true, Type: "string",
				Description: "Overall assessment of the change: what it does, strengths, key risks, and a wrap-up of the findings."},
			{Name: reviewArgVerdict, Required: false, Type: "string",
				Description: "One of: approve, comment (default), request_changes"},
		},
		m.handleAddSummary)

	m.RegisterTool("FinalizeReview",
		"Mark the review complete. Call exactly once, after AddSummaryReview. Returns the final comment count.",
		nil,
		m.handleFinalize)

	if len(m.previousIDs) > 0 {
		m.RegisterTool("ResolveReviewComment",
			"Mark a previous-round review comment as fixed, after verifying the fix in the current code. "+
				"Use the id shown in the 'Previous Review Comments' section. The harness resolves the thread.",
			[]message.ToolArgument{
				{Name: reviewArgID, Required: true, Type: "string",
					Description: "The id of the previous comment (exactly as listed in the prompt)"},
				{Name: reviewArgNote, Required: false, Type: "string",
					Description: "Short note on how it was fixed (e.g. 'guarded in commit …', 'divisor restored')"},
			},
			m.handleResolve)
	}
}

func (m *ReviewToolManager) handleResolve(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	id := stringArg(args, reviewArgID)
	if id == "" {
		return message.NewToolResultError("id is required"), nil
	}
	if !m.previousIDs[id] {
		return message.NewToolResultError(fmt.Sprintf(
			"unknown comment id %q — use an id exactly as listed in the 'Previous Review Comments' section", id)), nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finalized {
		return message.NewToolResultError("review already finalized; no further comments can be resolved"), nil
	}
	for _, r := range m.resolved {
		if r.ID == id {
			return message.NewToolResultError(fmt.Sprintf("comment %s is already marked resolved", id)), nil
		}
	}
	m.resolved = append(m.resolved, ResolvedComment{ID: id, Note: strings.TrimSpace(stringArg(args, reviewArgNote))})
	return message.NewToolResultText(fmt.Sprintf("Marked previous comment %s as resolved.", id)), nil
}

// parseInlineArgs validates AddInlineReview arguments and returns the comment
// to record, or a non-empty error message for the model.
func parseInlineArgs(args message.ToolArgumentValues) (ReviewComment, string) {
	c := ReviewComment{
		Path:      stringArg(args, reviewArgPath),
		Body:      strings.TrimSpace(stringArg(args, reviewArgComment)),
		Rationale: strings.TrimSpace(stringArg(args, reviewArgRationale)),
		Line:      intArg(args, reviewArgLine, 0),
		EndLine:   intArg(args, reviewArgEndLine, 0),
	}
	if c.Path == "" || c.Body == "" {
		return c, "path and comment are required"
	}
	if c.Rationale == "" {
		return c, "rationale is required: state why it's a problem and what you verified in the code"
	}
	if c.Line <= 0 {
		return c, "line must be a positive new-side line number from the annotated diff"
	}
	if c.EndLine == 0 {
		c.EndLine = c.Line
	}
	if c.EndLine < c.Line {
		// Tolerate swapped bounds rather than bouncing the model.
		c.Line, c.EndLine = c.EndLine, c.Line
	}
	c.Severity = strings.ToLower(stringArg(args, reviewArgSeverity))
	if c.Severity == "" {
		return c, "severity is required: must, major, minor, or nits"
	}
	if !reviewSeverities[c.Severity] {
		return c, fmt.Sprintf("invalid severity %q: use must, major, minor, or nits", c.Severity)
	}
	return c, ""
}

func (m *ReviewToolManager) handleAddInline(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	c, errMsg := parseInlineArgs(args)
	if errMsg != "" {
		return message.NewToolResultError(errMsg), nil
	}
	if err := m.validate(c.Path, c.Line, c.EndLine); err != nil {
		return message.NewToolResultError(err.Error()), nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finalized {
		return message.NewToolResultError("review already finalized; no further comments can be added"), nil
	}
	for _, prev := range m.comments {
		if prev.Path == c.Path && prev.Line == c.Line && prev.EndLine == c.EndLine {
			return message.NewToolResultError(fmt.Sprintf(
				"a comment on %s:%d already exists; do not repeat findings", c.Path, c.Line)), nil
		}
	}
	m.comments = append(m.comments, c)
	return message.NewToolResultText(fmt.Sprintf(
		"Recorded inline comment #%d on %s:%d.", len(m.comments), c.Path, c.Line)), nil
}

func (m *ReviewToolManager) handleAddSummary(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	summary := strings.TrimSpace(stringArg(args, reviewArgSummary))
	if summary == "" {
		return message.NewToolResultError("summary is required"), nil
	}
	verdict := strings.ToLower(stringArg(args, reviewArgVerdict))
	if verdict != "" && !reviewVerdicts[verdict] {
		return message.NewToolResultError(fmt.Sprintf(
			"invalid verdict %q: use approve, comment, or request_changes", verdict)), nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finalized {
		return message.NewToolResultError("review already finalized; the summary can no longer be changed"), nil
	}
	replaced := m.summary != ""
	m.summary = summary
	if verdict != "" {
		m.verdict = verdict
	}
	if replaced {
		return message.NewToolResultText("Summary replaced. Call FinalizeReview to complete the review."), nil
	}
	return message.NewToolResultText("Summary recorded. Call FinalizeReview to complete the review."), nil
}

func (m *ReviewToolManager) handleFinalize(_ context.Context, _ message.ToolArgumentValues) (message.ToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.summary == "" {
		return message.NewToolResultError("no summary set — call AddSummaryReview before FinalizeReview"), nil
	}
	if m.finalized {
		return message.NewToolResultError("review already finalized"), nil
	}
	m.finalized = true
	return message.NewToolResultText(fmt.Sprintf(
		"Review finalized: %d inline comment(s), verdict %s. "+
			"Reply with a one-line confirmation; the harness posts the review.",
		len(m.comments), m.verdict)), nil
}

// Result returns the accumulated review.
func (m *ReviewToolManager) Result() ReviewResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	comments := make([]ReviewComment, len(m.comments))
	copy(comments, m.comments)
	resolved := make([]ResolvedComment, len(m.resolved))
	copy(resolved, m.resolved)
	return ReviewResult{
		Summary: m.summary, Verdict: m.verdict, Finalized: m.finalized,
		Comments: comments, Resolved: resolved,
	}
}

// --- domain.ToolManager ---

// GetTools returns all registered review tools.
func (m *ReviewToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

// CallTool executes the named review tool.
func (m *ReviewToolManager) CallTool(
	ctx context.Context, name message.ToolName, args message.ToolArgumentValues,
) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

// RegisterTool registers a tool with the manager.
func (m *ReviewToolManager) RegisterTool(
	name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument,
	handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error),
) {
	m.tools[name] = &reviewTool{name: name, description: description, arguments: arguments, handler: handler}
}

type reviewTool struct {
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
}

func (t *reviewTool) RawName() message.ToolName            { return t.name }
func (t *reviewTool) Name() message.ToolName               { return t.name }
func (t *reviewTool) Description() message.ToolDescription { return t.description }
func (t *reviewTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *reviewTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

var _ domain.ToolManager = (*ReviewToolManager)(nil)
