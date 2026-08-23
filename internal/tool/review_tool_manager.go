package tool

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/fpt/klein-cli/internal/review"
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

// RangeLister describes the commentable line ranges of path ("14-21, 30"), or
// "" when there are none to name. Optional: it turns a rejection raised before
// the validator runs into one the model can act on without re-reading the diff.
type RangeLister func(path string) string

// reviewSeverities is the classification every inline comment must carry:
// must (fix before merge), major (real bug/regression), minor (edge case,
// robustness), nits (small but worth fixing).
var reviewSeverities = map[string]bool{"must": true, "major": true, "minor": true, "nits": true}

var reviewVerdicts = map[string]bool{"approve": true, reviewVerdictDefault: true, "request_changes": true}

// ReviewToolManager provides AddInlineReview/AddSummaryReview/FinalizeReview
// tools that accumulate a code review in memory. It performs no I/O — the
// `klein review` subcommand reads Result() after the agent run.
type ReviewToolManager struct {
	tools      map[message.ToolName]message.Tool
	validate   LineValidator
	listRanges RangeLister
	// previousIDs maps every name a previous comment answers to — its short
	// alias ("p1") and its real id, both lowercased — to the real id the
	// harness needs back. Lowercased because the alternative is losing a
	// verified resolution to a capital letter (fpt/klein-cli#120).
	previousIDs map[string]string
	// previousAliases lists the aliases in prompt order, for the rejection
	// message. A model that got the id wrong needs to be told the right ones,
	// not told again that it was wrong.
	previousAliases []string
	// aliasByID names a resolved comment back in the vocabulary the model was
	// given, so the finalize bounce can say which threads are still open
	// rather than which opaque ids are.
	aliasByID map[string]string
	summary   string
	verdict   string
	comments  []ReviewComment
	resolved  []ResolvedComment

	mu sync.Mutex
	// rejected counts inline comments that were attempted but not recorded
	// because the target was unusable (unparseable/out-of-range line, bad
	// severity, missing field) — i.e. findings the model has lost track of.
	// Duplicates don't count: that finding is already recorded.
	rejected int
	// rejectedResolves counts ResolveReviewComment calls that named nothing:
	// a resolution the model meant to make and may have walked away from.
	rejectedResolves int
	bounced          bool
	bouncedResolve   bool
	finalized        bool
}

// NewReviewToolManager creates a review tool manager. validate must not be
// nil. previousIDs are the ids of unresolved previous-round comments — the
// ResolveReviewComment tool is only registered when there are any.
func NewReviewToolManager(validate LineValidator, previousIDs []string) *ReviewToolManager {
	m := &ReviewToolManager{
		tools:       make(map[message.ToolName]message.Tool),
		validate:    validate,
		previousIDs: make(map[string]string, 2*len(previousIDs)),
		aliasByID:   make(map[string]string, len(previousIDs)),
		verdict:     reviewVerdictDefault,
	}
	// previousIDs arrives in prompt order, which is what makes the i-th alias
	// name the i-th comment. Both spellings are accepted: the alias the model
	// is shown, and the real id — a review round that predates aliases, or a
	// model quoting the harness input, still resolves. Real ids go in first so
	// that an id spelled like an alias cannot shadow the alias the prompt
	// actually shows.
	for _, id := range previousIDs {
		m.previousIDs[strings.ToLower(id)] = id
	}
	for i, id := range previousIDs {
		alias := review.PreviousCommentAlias(i)
		m.previousAliases = append(m.previousAliases, alias)
		m.previousIDs[strings.ToLower(alias)] = id
		m.aliasByID[id] = alias
	}
	m.register()
	return m
}

// WithRangeLister installs the describer used to name a file's commentable
// lines in a rejection message. Optional; returns the receiver for chaining.
func (m *ReviewToolManager) WithRangeLister(l RangeLister) *ReviewToolManager {
	m.listRanges = l
	return m
}

// withRangeHint appends the commentable lines of path to a rejection message,
// so the model can re-target without re-reading the whole annotated diff.
func (m *ReviewToolManager) withRangeHint(path, msg string) string {
	if path == "" || m.listRanges == nil {
		return msg
	}
	ranges := m.listRanges(path)
	if ranges == "" {
		return msg
	}
	return fmt.Sprintf("%s. Commentable lines in %s: %s", msg, path, ranges)
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
				"Use the short id shown in the 'Previous Review Comments' section (P1, P2, …). "+
				"The harness resolves the thread.",
			[]message.ToolArgument{
				{Name: reviewArgID, Required: true, Type: "string",
					Description: "The short id of the previous comment as listed in the prompt: P1, P2, …"},
				{Name: reviewArgNote, Required: false, Type: "string",
					Description: "Short note on how it was fixed (e.g. 'guarded in commit …', 'divisor restored')"},
			},
			m.handleResolve)
	}
}

func (m *ReviewToolManager) handleResolve(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	named := strings.TrimSpace(stringArg(args, reviewArgID))
	if named == "" {
		return m.rejectResolve("id is required"), nil
	}
	id, ok := m.previousIDs[strings.ToLower(named)]
	if !ok {
		return m.rejectResolve(fmt.Sprintf("unknown comment id %q", named)), nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.finalized {
		return message.NewToolResultError("review already finalized; no further comments can be resolved"), nil
	}
	for _, r := range m.resolved {
		if r.ID == id {
			return message.NewToolResultError(fmt.Sprintf("comment %s is already marked resolved", named)), nil
		}
	}
	m.resolved = append(m.resolved, ResolvedComment{ID: id, Note: strings.TrimSpace(stringArg(args, reviewArgNote))})
	return message.NewToolResultText(fmt.Sprintf("Marked previous comment %s as resolved.", named)), nil
}

// rejectResolve records a resolve that named nothing and answers with the ids
// that would have worked. Naming them is the whole point: the model reaching
// this message has already verified a fix and only needs the right handle, and
// the previous wording ("use an id exactly as listed") sent it back to a prompt
// section it had evidently already misread.
func (m *ReviewToolManager) rejectResolve(msg string) message.ToolResult {
	m.mu.Lock()
	m.rejectedResolves++
	aliases := strings.Join(m.previousAliases, ", ")
	m.mu.Unlock()

	return message.NewToolResultError(fmt.Sprintf(
		"%s. Previous comments are named %s, in the order the 'Previous Review Comments' "+
			"section lists them — retry with one of those.", msg, aliases))
}

// describeArg renders an argument value for an error message: quoted and
// elided when it's a string, so a whole line of source pasted into a numeric
// field is recognizable without flooding the tool result.
func describeArg(v any) string {
	if v == nil {
		return "nothing"
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	if s == "" {
		return "an empty string"
	}
	const maxLen = 60
	if r := []rune(s); len(r) > maxLen {
		s = string(r[:maxLen]) + "…"
	}
	return strconv.Quote(s)
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
		// Echo what arrived: the usual failure is passing the line's *text*
		// (or its diff-row prefix) instead of the bracketed number, and a
		// message that doesn't name the offending value reads as unfixable.
		return c, fmt.Sprintf(
			"line must be a positive new-side line number from the annotated diff "+
				"(the integer inside the brackets, not the line's text); got %s",
			describeArg(args[reviewArgLine]))
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

// rejectInline records that a finding was attempted but not kept, and returns
// the error result for the model. A non-empty path adds the file's commentable
// lines to the message; pass "" when the message already names them (the
// validator's own errors do).
func (m *ReviewToolManager) rejectInline(path, msg string) message.ToolResult {
	m.mu.Lock()
	m.rejected++
	m.mu.Unlock()
	return message.NewToolResultError(m.withRangeHint(path, msg))
}

func (m *ReviewToolManager) handleAddInline(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	c, errMsg := parseInlineArgs(args)
	if errMsg != "" {
		return m.rejectInline(c.Path, errMsg), nil
	}
	if err := m.validate(c.Path, c.Line, c.EndLine); err != nil {
		return m.rejectInline("", err.Error()), nil
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

// unresolvedAliases names the previous comments no resolve has landed on, in
// prompt order. Callers hold m.mu.
//
// This is what makes the rejected-resolve bounce precise without guessing. A
// rejected call named nothing, so which comment it *meant* is unknowable — but
// whatever it meant was one of the comments still open, so an empty result
// proves the intent was carried out and anything else leaves it in doubt. A
// single "did any resolve land afterwards" flag got this wrong across
// comments: mistyping P1 and then resolving P2 cleared it, and P1 stayed
// dropped — the very failure this bounce exists to catch.
func (m *ReviewToolManager) unresolvedAliases() []string {
	done := make(map[string]bool, len(m.resolved))
	for _, r := range m.resolved {
		done[m.aliasByID[r.ID]] = true
	}
	var open []string
	for _, alias := range m.previousAliases {
		if !done[alias] {
			open = append(open, alias)
		}
	}
	return open
}

// plural picks the verb form for n, so a bounce message about one open thread
// does not read as though it were written for a list.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// bounceMessage returns why FinalizeReview should be refused once, or "" to
// let it through. Callers hold m.mu; it sets the bounce flags it reports on, so
// asking twice is what makes the second FinalizeReview succeed.
//
// Two independent losses, each with its own flag: a finding whose target was
// rejected and never re-landed, and a resolution whose id named nothing and was
// never retried. The second is fpt/klein-cli#120 — a verified fix silently not
// resolved, which no error and no counter had previously reported.
func (m *ReviewToolManager) bounceMessage() string {
	var msgs []string
	if len(m.comments) == 0 && m.rejected > 0 && !m.bounced {
		m.bounced = true
		msgs = append(msgs, fmt.Sprintf(
			"%d inline comment(s) were rejected and none were recorded, so this review would post a "+
				"verdict with no findings attached. Re-target each one with AddInlineReview (path plus a "+
				"bracketed new-side line number from the annotated diff), or — if a finding has no "+
				"commentable line — fold it into AddSummaryReview.",
			m.rejected))
	}
	if open := m.unresolvedAliases(); m.rejectedResolves > 0 && len(open) > 0 && !m.bouncedResolve {
		m.bouncedResolve = true
		msgs = append(msgs, fmt.Sprintf(
			"%d ResolveReviewComment call(s) named an unknown id, and %s %s still open — so a fix you "+
				"verified may be about to stay flagged as an open finding. Retry any of those you confirmed "+
				"fixed; if they are genuinely still unfixed, leave them open and call FinalizeReview again.",
			m.rejectedResolves, strings.Join(open, ", "), plural(len(open), "is", "are")))
	}
	if len(msgs) == 0 {
		return ""
	}
	return strings.Join(msgs, " ") + " Then call FinalizeReview again."
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
	// Work the model did and then lost to a rejection: finishing here would
	// post the loss silently. Each kind bounces at most once — the second call
	// always completes, so a stubborn rejection can't hang the run.
	if msg := m.bounceMessage(); msg != "" {
		return message.NewToolResultError(msg), nil
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
