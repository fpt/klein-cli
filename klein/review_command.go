package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/internal/repository"
	"github.com/fpt/klein-cli/internal/review"
	"github.com/fpt/klein-cli/internal/tool"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/react"
	client "github.com/fpt/klein-cli/pkg/client"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// reviewAllowedTools is the hard tool sandbox for the review agent: read-only
// exploration plus the review accumulation tools. Enforced via
// SetAllowedToolsOverride (the skill's allowed-tools alone would leave other
// tools reachable through deferred tool loading).
// Task is safe here because the override also bounds every dispatched
// subagent by intersection (see Agent.runSubagent): a read-only agent like
// explore keeps its Read/Grep/Glob/LS, while an uncapped one (general-purpose)
// collapses to this same read-only set instead of inheriting Bash/Write.
// Grep is included so delegated exploration keeps its primary search tool.
var reviewAllowedTools = []string{
	"Read", "Glob", "Grep", "LS", "Task",
	"AddInlineReview", "AddSummaryReview", "FinalizeReview", "ResolveReviewComment",
}

const reviewUsage = `Usage:
  klein review --input <request.json> [--output <result.json>] [flags]

Runs an AI code review over a unified diff. The harness (e.g. a GitHub Action)
supplies PR metadata and the diff, and posts the resulting comments — klein
never runs git or gh.

Input JSON:  {"title": "...", "body": "...", "diff": "<unified diff>"}
             --input - reads it from stdin.
Output JSON: {"summary", "verdict", "finalized", "comments": [{"path", "line",
             "end_line", "severity", "body"}]}
             Written to --output, or stdout when omitted. Inline comment lines
             are validated to fall within the diff's new-side hunk ranges.
`

const debugLogLevel = "debug"

// reviewOptions holds the parsed `klein review` flags.
type reviewOptions struct {
	input            string
	output           string
	workdir          string
	backend          string
	model            string
	effort           string
	settingsPath     string
	language         string
	contextLines     int
	maxTurns         int
	maxBudget        int
	maxComments      int
	maxDiffBytes     int
	includeGenerated bool
	verbose          bool
}

// parseReviewFlags parses the review flag set; ok=false means exit with code.
func parseReviewFlags(args []string) (opts reviewOptions, code int, ok bool) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	input := fs.String("input", "", "Path to the review request JSON ('-' = stdin)")
	output := fs.String("output", "", "Path for the review result JSON (default: stdout)")
	workdir := fs.String("workdir", ".", "PR-head checkout the diff applies to")
	backend := fs.String("b", "", "LLM backend (openai, anthropic, gemini)")
	backendLong := fs.String("backend", "", "LLM backend (openai, anthropic, gemini)")
	model := fs.String("m", "", "Model name to use")
	modelLong := fs.String("model", "", "Model name to use")
	effort := fs.String("effort", "", "Reasoning effort (none|minimal|low|medium|high|xhigh)")
	settingsPath := fs.String("settings", "", "Path to settings file")
	language := fs.String("language", "en", "Language for the review output (code or name, e.g. en, ja, Japanese)")
	contextLines := fs.Int("context", 10, "Extra context lines around each hunk in the annotated diff")
	maxTurns := fs.Int("max-turns", 0, "Maximum agent iterations for the review run (0 = settings default)")
	maxBudget := fs.Int("max-budget-tokens", 0, "Cumulative token budget for the run; exceeding it stops the review, keeping comments gathered so far (0 = unlimited)")
	includeGenerated := fs.Bool("include-generated", false,
		"Review generated files too (default: skip '// Code generated ... DO NOT EDIT.' files)")
	maxComments := fs.Int("max-comments", 15,
		"Cap on inline comments; excess are trimmed lowest-severity-first (0 = unlimited)")
	maxDiffBytes := fs.Int("max-diff-bytes", 500_000,
		"Budget for the enriched diff; it is truncated at a line boundary past this size (0 = unbounded)")
	verbose := fs.Bool("v", false, "Enable verbose (debug) logging")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, reviewUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return opts, 2, false
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "Error: --input is required")
		fs.Usage()
		return opts, 2, false
	}
	return reviewOptions{
		input:            *input,
		output:           *output,
		workdir:          *workdir,
		backend:          resolveStringFlag(*backend, *backendLong),
		model:            resolveStringFlag(*model, *modelLong),
		effort:           *effort,
		settingsPath:     *settingsPath,
		language:         *language,
		contextLines:     *contextLines,
		maxTurns:         *maxTurns,
		maxBudget:        *maxBudget,
		maxComments:      *maxComments,
		maxDiffBytes:     *maxDiffBytes,
		includeGenerated: *includeGenerated,
		verbose:          *verbose,
	}, 0, true
}

// reviewSettings loads settings and applies the CLI overrides. The whole-agent
// backends (codex/appserver) run their own toolset out-of-process and can't
// expose the review tools, so klein review requires a direct LLM backend. Not
// supported for now, by decision: codex could run a review only if its tools
// were wired as dynamicTools, but its sandbox/security model makes that
// complicated; a local app-server agent typically targets local GGUF models,
// which don't fit a GHA runner.
func reviewSettings(opts reviewOptions) (*config.Settings, error) {
	settings, err := config.LoadSettings(opts.settingsPath)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	if opts.backend != "" {
		settings.LLM = config.GetDefaultLLMSettingsForBackend(opts.backend)
	}
	if opts.model != "" {
		settings.LLM.Model = opts.model
	}
	if opts.effort != "" {
		settings.LLM.Effort = strings.ToLower(opts.effort)
	}
	if err := config.ValidateSettings(settings); err != nil {
		return nil, fmt.Errorf("settings validation failed: %w", err)
	}
	if settings.LLM.Backend == config.BackendCodex || settings.LLM.Backend == config.BackendAppServer {
		return nil, fmt.Errorf(
			"backend %q is not supported by klein review (it can't expose the review tools); use openai, anthropic, or gemini",
			settings.LLM.Backend)
	}
	if opts.maxTurns > 0 {
		settings.Agent.MaxIterations = opts.maxTurns
	}
	return settings, nil
}

// runReviewCommand implements `klein review`. Exit codes: 0 = review produced
// (even with zero comments), 1 = error, 2 = flag error.
func runReviewCommand(args []string) int {
	opts, code, ok := parseReviewFlags(args)
	if !ok {
		return code
	}

	// Agent/status output goes to stderr so stdout stays clean for the result
	// JSON when --output is omitted.
	out := os.Stderr

	settings, err := reviewSettings(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	logLevel := settings.Agent.LogLevel
	if opts.verbose {
		logLevel = debugLogLevel
	}
	pkgLogger.SetGlobalLoggerWithConsoleWriter(pkgLogger.LogLevel(logLevel), out)
	logger := pkgLogger.NewLoggerWithConsoleWriter(pkgLogger.LogLevel(logLevel), out)

	result, err := executeReview(context.Background(), opts, settings, logger, out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if err := writeReviewResult(result, opts.output); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	logger.Info("Review complete",
		"comments", len(result.Comments), "verdict", result.Verdict, "finalized", result.Finalized)
	return 0
}

// preparedReview is the model-facing material derived from the request.
type preparedReview struct {
	prompt      string
	ranges      review.Ranges // commentable ranges, always from the full PR diff
	previousIDs []string
	skipped     []string // generated files excluded from review
	numFiles    int
}

// prepareReviewPrompt reads the request, parses the diff(s), and builds the
// annotated review prompt. Commentable ranges come from full_diff when the
// harness supplies one (incremental round) — GitHub validates comments
// against the complete PR diff, not the increment being reviewed.
func prepareReviewPrompt(
	ctx context.Context, opts reviewOptions, fsRepo repository.FilesystemRepository,
) (preparedReview, error) {
	var p preparedReview
	req, err := readReviewRequest(opts.input)
	if err != nil {
		return p, err
	}
	files, err := review.ParseUnifiedDiff(req.Diff)
	if err != nil {
		return p, fmt.Errorf("parse diff: %w", err)
	}
	enricher := review.NewEnricher(fsRepo, opts.workdir, opts.contextLines).WithMaxBytes(opts.maxDiffBytes)

	// Skip machine-generated files (protoc, sqlc, …) unless asked to include
	// them: they are noise to review and their marker says DO NOT EDIT.
	fullFiles := files
	if req.FullDiff = strings.TrimSpace(req.FullDiff); req.FullDiff != "" {
		if fullFiles, err = review.ParseUnifiedDiff(req.FullDiff); err != nil {
			return p, fmt.Errorf("parse full_diff: %w", err)
		}
	}
	if !opts.includeGenerated {
		files, p.skipped = enricher.DropGenerated(ctx, files)
		fullFiles, _ = enricher.DropGenerated(ctx, fullFiles)
	}

	p.ranges = review.CommentableRanges(fullFiles)
	for _, c := range req.PreviousComments {
		p.previousIDs = append(p.previousIDs, c.ID)
	}
	p.numFiles = len(files)
	if p.numFiles == 0 {
		// Nothing to review — the caller short-circuits without a model call.
		return p, nil
	}
	p.prompt = review.BuildPrompt(req, enricher.Render(ctx, files, p.ranges), opts.language)
	return p, nil
}

// noReviewableFilesResult builds the approve result used when nothing is left
// to review after filtering generated files.
func noReviewableFilesResult(skipped []string) tool.ReviewResult {
	summary := "No reviewable changes: all changed files are machine-generated."
	if len(skipped) > 0 {
		summary = fmt.Sprintf("No reviewable changes — skipped %d generated file(s): %s.",
			len(skipped), strings.Join(skipped, ", "))
	}
	return tool.ReviewResult{Summary: summary, Verdict: "approve", Finalized: true}
}

// newReviewAgent builds the review agent and applies the review sandbox: the
// hard read-only tool override and control-token sanitizing. Returns the
// agent's cleanup func, which the caller must defer.
func newReviewAgent(
	ctx context.Context, opts reviewOptions, settings *config.Settings,
	reviewMgr domain.ToolManager, fsRepo repository.FilesystemRepository,
	logger *pkgLogger.Logger, out io.Writer,
) (*app.Agent, func(), error) {
	llmClient, err := client.NewLLMClient(settings.LLM)
	if err != nil {
		return nil, nil, fmt.Errorf("create LLM client: %w", err)
	}

	a, cleanup, err := app.NewAgentWithOptions(ctx, app.AgentOptions{
		Settings:   settings,
		WorkingDir: opts.workdir,
		MCPToolManagers: map[string]domain.ToolManager{
			"review": reviewMgr,
		},
		Logger:            logger,
		Out:               out,
		FsRepo:            fsRepo,
		IsInteractiveMode: false,
		LLMClient:         llmClient,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("create agent: %w", err)
	}
	a.SetAllowedToolsOverride(reviewAllowedTools)
	// Safe here because reviewAllowedTools is read-only: no Write/Edit can carry
	// the substitution back to disk. See Agent.SetSanitizeToolResults.
	a.SetSanitizeToolResults(true)
	if opts.maxBudget > 0 {
		a.SetTokenBudget(opts.maxBudget)
	}
	return a, cleanup, nil
}

// executeReview parses the request, runs the review agent with the sandboxed
// toolset, and returns the accumulated review.
func executeReview(
	ctx context.Context, opts reviewOptions, settings *config.Settings,
	logger *pkgLogger.Logger, out io.Writer,
) (tool.ReviewResult, error) {
	var zero tool.ReviewResult

	if _, err := os.Stat(opts.workdir); err != nil {
		return zero, fmt.Errorf("working directory %q: %w", opts.workdir, err)
	}

	fsRepo := infra.NewOSFilesystemRepository()
	prepared, err := prepareReviewPrompt(ctx, opts, fsRepo)
	if err != nil {
		return zero, err
	}
	if len(prepared.skipped) > 0 {
		logger.Info("Skipping generated files", "count", len(prepared.skipped), "files", prepared.skipped)
	}
	// Every reviewable file was generated (or filtered) — approve without a
	// model call and say why. The harness still gets a postable result.
	if prepared.numFiles == 0 {
		return noReviewableFilesResult(prepared.skipped), nil
	}
	reviewMgr := tool.NewReviewToolManager(prepared.ranges.Validate, prepared.previousIDs)

	a, cleanup, err := newReviewAgent(ctx, opts, settings, reviewMgr, fsRepo, logger, out)
	if err != nil {
		return zero, err
	}
	defer cleanup()

	logger.Info("Starting review",
		"files", prepared.numFiles, "previous_comments", len(prepared.previousIDs),
		"backend", settings.LLM.Backend, "model", settings.LLM.Model)
	response, err := a.Invoke(ctx, prepared.prompt, "review")
	budgetExceeded := errors.Is(err, react.ErrTokenBudgetExceeded)
	if err != nil && !budgetExceeded {
		return zero, fmt.Errorf("review run failed: %w", err)
	}
	printTokenUsage(a.GetLLMClient())

	result := finalizeReviewResult(reviewMgr.Result(), response, opts, budgetExceeded, logger)
	return capComments(result, opts.maxComments, logger), nil
}

// severityRank orders comments for trimming: must first, then major, minor,
// nits; anything unclassified sorts last.
func severityRank(s string) int {
	switch s {
	case "must":
		return 0
	case "major":
		return 1
	case "minor":
		return 2
	case "nits":
		return 3
	default:
		return 4
	}
}

// capComments sorts comments by severity and trims to maxComments (0 =
// unlimited), dropping the lowest-severity excess so it is never posted. When
// must-level findings alone exceed the cap, ForceFullNext is set so the next
// round re-scans the whole PR instead of only the increment.
func capComments(result tool.ReviewResult, maxComments int, logger *pkgLogger.Logger) tool.ReviewResult {
	if maxComments <= 0 || len(result.Comments) <= maxComments {
		return result
	}
	mustCount := 0
	for _, c := range result.Comments {
		if c.Severity == "must" {
			mustCount++
		}
	}
	sort.SliceStable(result.Comments, func(i, j int) bool {
		return severityRank(result.Comments[i].Severity) < severityRank(result.Comments[j].Severity)
	})
	result.Trimmed = len(result.Comments) - maxComments
	result.Comments = result.Comments[:maxComments]
	result.ForceFullNext = mustCount > maxComments
	logger.Warn("Trimmed comments to cap",
		"cap", maxComments, "trimmed", result.Trimmed, "must", mustCount, "force_full_next", result.ForceFullNext)

	note := fmt.Sprintf("\n\n_%d lower-severity comment(s) were trimmed to respect the comment cap (%d)._",
		result.Trimmed, maxComments)
	if result.ForceFullNext {
		note += " _More must-level findings exist than the cap allows; the next review will re-scan the full PR._"
	}
	result.Summary = strings.TrimRight(result.Summary, "\n") + note
	return result
}

// finalizeReviewResult applies the salvage/fallback rules to the accumulated
// review: a budget cutoff yields a partial result, and a missing summary falls
// back to the model's final message. Both set finalized:false.
func finalizeReviewResult(
	result tool.ReviewResult, response message.Message, opts reviewOptions,
	budgetExceeded bool, logger *pkgLogger.Logger,
) tool.ReviewResult {
	if budgetExceeded {
		logger.Warn("Token budget exceeded — emitting partial review",
			"budget", opts.maxBudget, "comments", len(result.Comments))
		if result.Summary == "" {
			result.Summary = fmt.Sprintf(
				"⚠️ Review stopped early: the token budget (%d) was exhausted. "+
					"Inline comments gathered before the cutoff are included; the review is incomplete.",
				opts.maxBudget)
		}
		result.Finalized = false
		return result
	}
	if result.Summary == "" {
		// The model never called AddSummaryReview — fall back to its final
		// response so the harness still has something to post.
		result.Summary = strings.TrimSpace(response.Content())
		result.Finalized = false
	}
	if !result.Finalized {
		logger.Warn("Review was not finalized by the model", "comments", len(result.Comments))
	}
	return result
}

// writeReviewResult writes the result JSON to path, or stdout when path is "".
func writeReviewResult(result tool.ReviewResult, path string) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	data = append(data, '\n')
	if path == "" {
		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write result to %s: %w", path, err)
	}
	return nil
}

// readReviewRequest loads the request JSON from a file or stdin ("-").
func readReviewRequest(path string) (review.Request, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return review.Request{}, fmt.Errorf("read input: %w", err)
	}
	var req review.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return review.Request{}, fmt.Errorf("parse input JSON: %w", err)
	}
	if strings.TrimSpace(req.Diff) == "" {
		return review.Request{}, errors.New(`input JSON has an empty "diff" field`)
	}
	return req, nil
}
