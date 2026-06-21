// Researcher tools: fetch RSS/Atom signal feeds, extract market
// narratives, and surface the resulting structured data through klein's tool
// interface. The pipeline lives under internal/researcher/. Default storage
// lives under ~/.klein/researcher/.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fpt/klein-cli/internal/researcher"
	ehconfig "github.com/fpt/klein-cli/internal/researcher/config"
	"github.com/fpt/klein-cli/internal/researcher/crawler"
	"github.com/fpt/klein-cli/internal/researcher/duckdb"
	"github.com/fpt/klein-cli/internal/researcher/model"
	"github.com/fpt/klein-cli/internal/researcher/pipeline"
	"github.com/fpt/klein-cli/internal/researcher/store"
	"github.com/fpt/klein-cli/pkg/message"
)

// ResearcherToolManager exposes the Researcher* family of tools.
type ResearcherToolManager struct {
	tools map[message.ToolName]message.Tool
}

// NewResearcherToolManager constructs the manager and registers all tools.
func NewResearcherToolManager() *ResearcherToolManager {
	m := &ResearcherToolManager{tools: make(map[message.ToolName]message.Tool)}
	m.tools["ResearcherFetch"] = &ehFetchTool{}
	m.tools["ResearcherAnalyze"] = &ehAnalyzeTool{}
	m.tools["ResearcherNarratives"] = &ehNarrativesTool{}
	m.tools["ResearcherEvents"] = &ehEventsTool{}
	m.tools["ResearcherIngestURL"] = &ehIngestURLTool{}
	m.tools["ResearcherCrawlListing"] = &ehCrawlListingTool{}
	m.tools["ResearcherQuery"] = &ehQueryTool{}
	return m
}

func (m *ResearcherToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *ResearcherToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *ResearcherToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool %q not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *ResearcherToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &genericTool{name: name, description: description, arguments: arguments, handler: handler}
}

// ---------- shared helpers ----------

// resolveDefaults computes the per-call paths, falling back to the klein-
// managed ~/.klein/researcher defaults. config_path/data_dir/reports_dir
// arguments override the defaults when set.
func resolveDefaults(args message.ToolArgumentValues) (researcher.Defaults, error) {
	d, err := researcher.LoadDefaults()
	if err != nil {
		return d, err
	}
	if v, ok := args["config_path"].(string); ok && v != "" {
		d.ConfigPath = v
	}
	if v, ok := args["data_dir"].(string); ok && v != "" {
		d.DataDir = v
	}
	if v, ok := args["reports_dir"].(string); ok && v != "" {
		d.ReportsDir = v
	}
	return d, nil
}

func intArg(args message.ToolArgumentValues, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if v == "" {
			return def
		}
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n == 0 {
			return def
		}
		return n
	}
	return def
}

func stringArg(args message.ToolArgumentValues, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// silentLogger discards INFO/WARN lines from pipeline operations so they
// don't interleave with the agent's stdout transcript. Failures still
// surface via the tool result text.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }

// ---------- ResearcherFetch ----------

type ehFetchTool struct{}

func (t *ehFetchTool) RawName() message.ToolName { return "ResearcherFetch" }
func (t *ehFetchTool) Name() message.ToolName    { return "ResearcherFetch" }
func (t *ehFetchTool) Description() message.ToolDescription {
	return "Pull RSS/Atom feeds defined in the Researcher config and append new events to the JSONL store. " +
		"On first run the embedded default config (US/UK/EU government feeds) is seeded to ~/.klein/researcher/config.yaml. " +
		"Returns the number of new events fetched and the total event count in the store. " +
		"Run this before ResearcherAnalyze to refresh the dataset."
}

func (t *ehFetchTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "config_path", Description: "Path to a YAML/JSON config listing RSS/Atom sources. Defaults to ~/.klein/researcher/config.yaml (auto-seeded with reasonable defaults on first run).", Required: false, Type: "string"},
		{Name: "data_dir", Description: "Directory to store events.jsonl. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
	}
}

func (t *ehFetchTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
		cfg, err := ehconfig.Load(d.ConfigPath)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("loading config %s: %v", d.ConfigPath, err)), nil
		}

		p := pipeline.Pipeline{
			Config:     cfg,
			DataDir:    d.DataDir,
			ReportsDir: d.ReportsDir,
			Logger:     silentLogger(),
			Now:        func() time.Time { return time.Now().UTC() },
		}

		// Snapshot pre-state so we can report what changed.
		eventsPath := filepath.Join(d.DataDir, "events.jsonl")
		before, _ := store.ReadEvents(eventsPath)
		beforeN := len(before)

		if err := p.Fetch(ctx); err != nil {
			return message.NewToolResultError(fmt.Sprintf("fetch failed: %v", err)), nil
		}

		after, _ := store.ReadEvents(eventsPath)
		afterN := len(after)
		added := afterN - beforeN

		out := fmt.Sprintf("Fetched from %d sources.\nNew events added: %d\nTotal events in store: %d\nStore: %s",
			len(cfg.Sources), added, afterN, eventsPath)
		return message.ToolResult{Text: out}, nil
	}
}

// ---------- ResearcherAnalyze ----------

type ehAnalyzeTool struct{}

func (t *ehAnalyzeTool) RawName() message.ToolName { return "ResearcherAnalyze" }
func (t *ehAnalyzeTool) Name() message.ToolName    { return "ResearcherAnalyze" }
func (t *ehAnalyzeTool) Description() message.ToolDescription {
	return "Cluster stored events into market narratives, score them by source diversity / trust tier / outcome confirmation / recency, " +
		"and write narratives.json plus a daily markdown report. " +
		"Run ResearcherFetch first to refresh the dataset. " +
		"Returns the narrative count and the path to the markdown report."
}

func (t *ehAnalyzeTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "data_dir", Description: "Directory containing events.jsonl. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
		{Name: "reports_dir", Description: "Directory for daily markdown reports (reports/narratives/YYYY-MM-DD.md). Defaults to ~/.klein/researcher/reports.", Required: false, Type: "string"},
		{Name: "window_days", Description: "Days of recent events to consider when clustering. Default 7.", Required: false, Type: "number"},
		{Name: "limit", Description: "Maximum narratives to retain. Default 20.", Required: false, Type: "number"},
	}
}

func (t *ehAnalyzeTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
		windowDays := intArg(args, "window_days", 7)
		limit := intArg(args, "limit", 20)

		// Pipeline.Analyze doesn't read sources from config, so an empty
		// Config is fine here. We pass one anyway in case future versions
		// need it.
		cfg, _ := ehconfig.Load(d.ConfigPath)
		now := func() time.Time { return time.Now().UTC() }
		p := pipeline.Pipeline{
			Config:         cfg,
			DataDir:        d.DataDir,
			ReportsDir:     d.ReportsDir,
			Logger:         silentLogger(),
			NarrativeLimit: limit,
			WindowDays:     windowDays,
			Now:            now,
		}

		narratives, err := p.Analyze(ctx)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("analyze failed: %v", err)), nil
		}

		reportPath := filepath.Join(d.ReportsDir, "narratives", now().Format("2006-01-02")+".md")
		out := fmt.Sprintf("Extracted %d narratives over a %d-day window.\nReport: %s\nNarratives JSON: %s",
			len(narratives), windowDays, reportPath,
			filepath.Join(d.DataDir, "narratives.json"))
		return message.ToolResult{Text: out}, nil
	}
}

// ---------- ResearcherNarratives ----------

type ehNarrativesTool struct{}

func (t *ehNarrativesTool) RawName() message.ToolName { return "ResearcherNarratives" }
func (t *ehNarrativesTool) Name() message.ToolName    { return "ResearcherNarratives" }
func (t *ehNarrativesTool) Description() message.ToolDescription {
	return "List the most recent extracted narratives with score, themes, event count, source mix, trend, and evidence excerpts. " +
		"Reads narratives.json written by ResearcherAnalyze. Use this to inspect the current top narratives without running the full pipeline."
}

func (t *ehNarrativesTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "data_dir", Description: "Directory containing narratives.json. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
		{Name: "limit", Description: "Maximum narratives to return (top-scored first). Default 10.", Required: false, Type: "number"},
	}
}

func (t *ehNarrativesTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
		limit := intArg(args, "limit", 10)

		path := filepath.Join(d.DataDir, "narratives.json")
		raw, err := readNarratives(path)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("reading %s: %v", path, err)), nil
		}
		if len(raw) == 0 {
			return message.ToolResult{Text: "No narratives stored. Run ResearcherAnalyze first."}, nil
		}
		sort.Slice(raw, func(i, j int) bool { return raw[i].Score > raw[j].Score })
		if limit > 0 && limit < len(raw) {
			raw = raw[:limit]
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Top %d narratives (from %s):\n\n", len(raw), path)
		for i, n := range raw {
			fmt.Fprintf(&b, "[%d] %s\n", i+1, n.Label)
			fmt.Fprintf(&b, "    score=%.3f  events=%d (signal=%d outcome=%d)  sources=%d  trend=%s\n",
				n.Score, n.EventCount, n.SignalCount, n.OutcomeCount, n.SourceCount, n.Trend)
			if len(n.Themes) > 0 {
				fmt.Fprintf(&b, "    themes: %s\n", strings.Join(n.Themes, ", "))
			}
			if len(n.Entities) > 0 {
				ents := n.Entities
				if len(ents) > 6 {
					ents = append(append([]string{}, ents[:6]...), "…")
				}
				fmt.Fprintf(&b, "    entities: %s\n", strings.Join(ents, ", "))
			}
			if len(n.SourceMix) > 0 {
				fmt.Fprintf(&b, "    source mix: %s\n", joinIntakes(n.IntakeMix))
			}
			if len(n.Evidence) > 0 {
				ev := n.Evidence
				if len(ev) > 3 {
					ev = ev[:3]
				}
				for _, e := range ev {
					fmt.Fprintf(&b, "      - %s\n", e)
				}
			}
			b.WriteString("\n")
		}
		return message.ToolResult{Text: b.String()}, nil
	}
}

func joinIntakes(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// ---------- ResearcherEvents ----------

type ehEventsTool struct{}

func (t *ehEventsTool) RawName() message.ToolName { return "ResearcherEvents" }
func (t *ehEventsTool) Name() message.ToolName    { return "ResearcherEvents" }
func (t *ehEventsTool) Description() message.ToolDescription {
	return "List recent stored events from events.jsonl with optional filters (window_days, role, source, keyword). " +
		"Use this for raw evidence — e.g. to ground a narrative analysis or look up the source articles behind a particular theme."
}

func (t *ehEventsTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "data_dir", Description: "Directory containing events.jsonl. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
		{Name: "window_days", Description: "Restrict to events published in the last N days. 0 = no limit. Default 7.", Required: false, Type: "number"},
		{Name: "role", Description: "Filter by role: 'signal' (causes/policies/events) or 'outcome' (price moves). Empty = both.", Required: false, Type: "string"},
		{Name: "source", Description: "Filter by source name (substring match against the configured source 'name').", Required: false, Type: "string"},
		{Name: "keyword", Description: "Case-insensitive substring filter over event title + summary.", Required: false, Type: "string"},
		{Name: "limit", Description: "Maximum events to return (newest first). Default 20.", Required: false, Type: "number"},
	}
}

func (t *ehEventsTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(_ context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
		windowDays := intArg(args, "window_days", 7)
		limit := intArg(args, "limit", 20)
		role := strings.ToLower(stringArg(args, "role"))
		source := strings.ToLower(stringArg(args, "source"))
		keyword := strings.ToLower(stringArg(args, "keyword"))

		path := filepath.Join(d.DataDir, "events.jsonl")
		events, err := store.ReadEvents(path)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("reading %s: %v", path, err)), nil
		}
		if len(events) == 0 {
			return message.ToolResult{Text: "No events stored. Run ResearcherFetch first."}, nil
		}

		cutoff := time.Time{}
		if windowDays > 0 {
			cutoff = time.Now().UTC().AddDate(0, 0, -windowDays)
		}

		filtered := make([]model.Event, 0, len(events))
		for _, e := range events {
			if !cutoff.IsZero() && e.PublishedAt.Before(cutoff) {
				continue
			}
			if role != "" && strings.ToLower(e.Role) != role {
				continue
			}
			if source != "" && !strings.Contains(strings.ToLower(e.Source), source) {
				continue
			}
			if keyword != "" {
				hay := strings.ToLower(e.Title + " " + e.Summary)
				if !strings.Contains(hay, keyword) {
					continue
				}
			}
			filtered = append(filtered, e)
		}

		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].PublishedAt.After(filtered[j].PublishedAt)
		})
		if limit > 0 && limit < len(filtered) {
			filtered = filtered[:limit]
		}

		var b strings.Builder
		fmt.Fprintf(&b, "%d event(s) match (from %d total).\n\n", len(filtered), len(events))
		for _, e := range filtered {
			fmt.Fprintf(&b, "• [%s] %s\n", e.PublishedAt.Format(time.RFC3339), e.Title)
			fmt.Fprintf(&b, "  source=%s intake=%s role=%s trust=%s\n", e.Source, e.Intake, e.Role, e.TrustTier)
			if e.URL != "" {
				fmt.Fprintf(&b, "  url: %s\n", e.URL)
			}
			if e.Summary != "" {
				s := e.Summary
				if len(s) > 200 {
					s = s[:200] + "…"
				}
				fmt.Fprintf(&b, "  %s\n", s)
			}
			b.WriteString("\n")
		}
		return message.ToolResult{Text: b.String()}, nil
	}
}

// ---------- ResearcherIngestURL ----------

type ehIngestURLTool struct{}

func (t *ehIngestURLTool) RawName() message.ToolName { return "ResearcherIngestURL" }
func (t *ehIngestURLTool) Name() message.ToolName    { return "ResearcherIngestURL" }
func (t *ehIngestURLTool) Description() message.ToolDescription {
	return "Fetch a primary-source URL (HTML page OR PDF) and store it as a single event in the Researcher JSONL store. " +
		"For HTML pages the title and a short summary are extracted automatically (og:title / meta description / main text); " +
		"for PDFs only a pointer (URL + filename) is recorded — use PDFRead to extract the body on demand. " +
		"Pass intake/role/trust_tier matching the source policy (e.g. corporate IR PDF → intake='corporate', role='signal', trust_tier='corporate'). " +
		"Idempotent: re-ingesting the same URL is a no-op."
}

func (t *ehIngestURLTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "url", Description: "The primary-source URL to ingest (HTML or PDF).", Required: true, Type: "string"},
		{Name: "intake", Description: "Provenance group: 'government-jp', 'government-us', 'government-eu', 'government-uk', 'corporate', 'analyst-corporate', 'exchange', 'news', 'market-outcome', etc.", Required: true, Type: "string"},
		{Name: "role", Description: "'signal' for causes/policies/announcements; 'outcome' for price action.", Required: false, Type: "string"},
		{Name: "trust_tier", Description: "'primary' (official institutional release), 'corporate' (IR/earnings), 'analyst', 'news', 'outcome'.", Required: false, Type: "string"},
		{Name: "title", Description: "Override the auto-extracted title. Recommended for PDFs where the auto-derived filename isn't descriptive.", Required: false, Type: "string"},
		{Name: "published_at", Description: "ISO-8601 publication date (e.g. 2026-06-19 or 2026-06-19T09:00:00Z). Overrides any auto-detected date.", Required: false, Type: "string"},
		{Name: "data_dir", Description: "Directory containing events.jsonl. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
	}
}

func (t *ehIngestURLTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		urlStr := stringArg(args, "url")
		if urlStr == "" {
			return message.NewToolResultError("ResearcherIngestURL: 'url' is required"), nil
		}
		intake := stringArg(args, "intake")
		if intake == "" {
			return message.NewToolResultError("ResearcherIngestURL: 'intake' is required"), nil
		}
		role := stringArg(args, "role")
		if role == "" {
			role = "signal"
		}
		trustTier := stringArg(args, "trust_tier")
		if trustTier == "" {
			// Default trust tier follows the project's source policy:
			// corporate intake → corporate, exchange/government → primary,
			// otherwise news.
			switch {
			case strings.HasPrefix(intake, "government-"), intake == "exchange":
				trustTier = "primary"
			case intake == "corporate", strings.HasPrefix(intake, "analyst-"):
				trustTier = "corporate"
			default:
				trustTier = "news"
			}
		}

		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}

		result, err := crawler.FetchSingle(ctx, urlStr)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("fetch failed: %v", err)), nil
		}
		if titleOverride := stringArg(args, "title"); titleOverride != "" {
			result.Title = titleOverride
		}
		if pubStr := stringArg(args, "published_at"); pubStr != "" {
			if pub, perr := parseFlexibleDate(pubStr); perr == nil {
				result.PublishedAt = pub
			}
		}

		event := crawler.ResultToEvent(result, intake, role, trustTier)

		eventsPath := filepath.Join(d.DataDir, "events.jsonl")
		existing, _ := store.ReadEvents(eventsPath)
		for _, e := range existing {
			if e.ID == event.ID {
				return message.ToolResult{Text: fmt.Sprintf(
					"Already ingested (event ID %s).\nTitle: %s\nURL: %s\nSkipped — re-ingest is a no-op.",
					event.ID, e.Title, e.URL)}, nil
			}
		}
		if err := store.AppendEvents(eventsPath, []model.Event{event}); err != nil {
			return message.NewToolResultError(fmt.Sprintf("appending to %s: %v", eventsPath, err)), nil
		}

		return message.ToolResult{Text: fmt.Sprintf(
			"Ingested 1 event into %s.\nID: %s\nTitle: %s\nURL: %s\nContent-Type: %s\nIntake: %s  Role: %s  Trust: %s  Published: %s",
			eventsPath, event.ID, event.Title, event.URL, result.ContentType,
			event.Intake, event.Role, event.TrustTier, event.PublishedAt.Format(time.RFC3339))}, nil
	}
}

// ---------- ResearcherCrawlListing ----------

type ehCrawlListingTool struct{}

func (t *ehCrawlListingTool) RawName() message.ToolName { return "ResearcherCrawlListing" }
func (t *ehCrawlListingTool) Name() message.ToolName    { return "ResearcherCrawlListing" }
func (t *ehCrawlListingTool) Description() message.ToolDescription {
	return "Crawl an HTML index/listing page (e.g. a corporate IR landing page or a regulator news index), " +
		"extract candidate dated entries by scanning anchor links and surrounding text, and ingest each as an event. " +
		"Use this for sources that don't expose RSS — typical case: Japanese corporate IR pages. " +
		"Best-effort: date detection covers Japanese (YYYY年MM月DD日) and Western (YYYY-MM-DD, YYYY/MM/DD, YYYY.MM.DD) formats. " +
		"Entries without a parseable date are stored with today's date — review and adjust if precision matters."
}

func (t *ehCrawlListingTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "url", Description: "The index/listing page URL to crawl.", Required: true, Type: "string"},
		{Name: "source_name", Description: "Friendly source name for clustering (e.g. 'kioxia-ir', 'jpx-news'). All extracted items inherit this.", Required: true, Type: "string"},
		{Name: "intake", Description: "Provenance group, same vocabulary as ResearcherIngestURL.", Required: true, Type: "string"},
		{Name: "role", Description: "'signal' (default) or 'outcome'.", Required: false, Type: "string"},
		{Name: "trust_tier", Description: "'primary' / 'corporate' / 'analyst' / 'news' / 'outcome'. Defaults by intake (corporate→corporate, government-*/exchange→primary, else news).", Required: false, Type: "string"},
		{Name: "max_items", Description: "Maximum entries to extract. Default 30.", Required: false, Type: "number"},
		{Name: "data_dir", Description: "Directory containing events.jsonl. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
		{Name: "require_date", Description: "If true, skip entries where no date could be parsed. Default false (entries default to today).", Required: false, Type: "string"},
	}
}

func (t *ehCrawlListingTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		urlStr := stringArg(args, "url")
		if urlStr == "" {
			return message.NewToolResultError("ResearcherCrawlListing: 'url' is required"), nil
		}
		sourceName := stringArg(args, "source_name")
		if sourceName == "" {
			return message.NewToolResultError("ResearcherCrawlListing: 'source_name' is required"), nil
		}
		intake := stringArg(args, "intake")
		if intake == "" {
			return message.NewToolResultError("ResearcherCrawlListing: 'intake' is required"), nil
		}
		role := stringArg(args, "role")
		if role == "" {
			role = "signal"
		}
		trustTier := stringArg(args, "trust_tier")
		if trustTier == "" {
			switch {
			case strings.HasPrefix(intake, "government-"), intake == "exchange":
				trustTier = "primary"
			case intake == "corporate", strings.HasPrefix(intake, "analyst-"):
				trustTier = "corporate"
			default:
				trustTier = "news"
			}
		}
		maxItems := intArg(args, "max_items", 30)
		requireDate := stringArg(args, "require_date") == "true"

		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}

		items, err := crawler.FetchListing(ctx, urlStr, maxItems)
		if err != nil {
			return message.NewToolResultError(fmt.Sprintf("listing fetch failed: %v", err)), nil
		}
		if len(items) == 0 {
			return message.ToolResult{Text: fmt.Sprintf("No candidate items found at %s. The page may render content via JavaScript or have no anchor links matching the heuristic.", urlStr)}, nil
		}

		eventsPath := filepath.Join(d.DataDir, "events.jsonl")
		existing, _ := store.ReadEvents(eventsPath)
		seen := make(map[string]bool, len(existing))
		for _, e := range existing {
			seen[e.ID] = true
		}

		var newEvents []model.Event
		var skippedDate, skippedDup int
		for _, item := range items {
			if requireDate && item.PublishedAt.IsZero() {
				skippedDate++
				continue
			}
			ev := crawler.ListingItemToEvent(item, sourceName, intake, role, trustTier)
			if seen[ev.ID] {
				skippedDup++
				continue
			}
			seen[ev.ID] = true
			newEvents = append(newEvents, ev)
		}

		if len(newEvents) > 0 {
			if err := store.AppendEvents(eventsPath, newEvents); err != nil {
				return message.NewToolResultError(fmt.Sprintf("appending to %s: %v", eventsPath, err)), nil
			}
		}

		var b strings.Builder
		fmt.Fprintf(&b, "Crawled %s.\n", urlStr)
		fmt.Fprintf(&b, "Found %d candidate items, ingested %d new events (skipped %d duplicate, %d no-date).\n",
			len(items), len(newEvents), skippedDup, skippedDate)
		fmt.Fprintf(&b, "Source: %s  Intake: %s  Role: %s  Trust: %s\n", sourceName, intake, role, trustTier)
		fmt.Fprintf(&b, "Store: %s\n\nSample of ingested items:\n", eventsPath)
		sample := newEvents
		if len(sample) > 5 {
			sample = sample[:5]
		}
		for _, e := range sample {
			fmt.Fprintf(&b, "• [%s] %s\n  %s\n", e.PublishedAt.Format("2006-01-02"), e.Title, e.URL)
		}
		return message.ToolResult{Text: b.String()}, nil
	}
}

// ---------- ResearcherQuery ----------

type ehQueryTool struct{}

func (t *ehQueryTool) RawName() message.ToolName { return "ResearcherQuery" }
func (t *ehQueryTool) Name() message.ToolName    { return "ResearcherQuery" }
func (t *ehQueryTool) Description() message.ToolDescription {
	return "Run a DuckDB SQL query against the Researcher event store and return the result as a Markdown table. " +
		"Two views are pre-defined: `events` (one row per stored event with columns id, source, intake, role, trust_tier, " +
		"weight, title, url, summary, published_at::TIMESTAMP, fetched_at::TIMESTAMP) and `narratives` (one row per " +
		"narrative cluster with themes/entities arrays and score/trend/timestamps). " +
		"Use this for window-based time-series analysis: bucket by hour/day with DATE_TRUNC, compute baseline + z-score " +
		"to detect anomalies, temporal-join signal vs outcome events with INTERVAL clauses, filter by trust_tier to " +
		"weight evidence. Session timezone is JST (override with `SET TimeZone = '...'` at the top of your query). " +
		"Requires the `duckdb` CLI on PATH (brew install duckdb)."
}

func (t *ehQueryTool) Arguments() []message.ToolArgument {
	return []message.ToolArgument{
		{Name: "sql", Description: "The SQL query to execute. Reference the `events` and `narratives` views. Multi-statement queries (CTEs, multiple SELECTs) are supported.", Required: true, Type: "string"},
		{Name: "data_dir", Description: "Directory containing events.jsonl + narratives.json. Defaults to ~/.klein/researcher/data.", Required: false, Type: "string"},
	}
}

func (t *ehQueryTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
		sql := stringArg(args, "sql")
		if sql == "" {
			return message.NewToolResultError("ResearcherQuery: 'sql' is required"), nil
		}
		d, err := resolveDefaults(args)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}

		out, err := duckdb.Query(ctx, d.DataDir, sql)
		if err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
		if strings.TrimSpace(out) == "" {
			return message.ToolResult{Text: "(query returned no rows)"}, nil
		}
		return message.ToolResult{Text: out}, nil
	}
}

// parseFlexibleDate accepts ISO-8601 dates with or without time. Returns UTC.
func parseFlexibleDate(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
		"2006/01/02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date: %q", s)
}

// readNarratives loads narratives.json. The file is a JSON array.
// A missing file is treated as "no narratives yet" rather than an error.
func readNarratives(path string) ([]model.Narrative, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var out []model.Narrative
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
