package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"github.com/fpt/klein-cli/internal/researcher/config"
	"github.com/fpt/klein-cli/internal/researcher/model"
	"github.com/fpt/klein-cli/internal/researcher/narrative"
	"github.com/fpt/klein-cli/internal/researcher/source"
	"github.com/fpt/klein-cli/internal/researcher/store"
)

// Pipeline runs the researcher workflow:
//
//	Fetch  → pull RSS/Atom feeds, normalise events, append-only-store as JSONL.
//	Analyze → cluster events into narratives, score, write JSON + markdown.
//
// Agent-based narrative refinement (LLM-driven post-processing of the
// deterministic cluster) is intentionally not included — the heuristic
// extractor is sufficient for klein's needs and avoids duplicating LLM
// client logic. If you want LLM refinement, run the resulting JSON through
// klein's existing client/skill machinery.
type Pipeline struct {
	Config         config.Config
	DataDir        string
	ReportsDir     string
	Logger         *slog.Logger
	NarrativeLimit int
	WindowDays     int
	Now            func() time.Time
}

func (p Pipeline) Run(ctx context.Context) ([]model.Narrative, error) {
	if err := p.Fetch(ctx); err != nil {
		return nil, err
	}
	return p.Analyze(ctx)
}

func (p Pipeline) Fetch(ctx context.Context) error {
	fetcher := source.Fetcher{Now: p.now}
	var all []model.Event
	for _, src := range p.Config.Sources {
		events, err := fetcher.Fetch(ctx, src)
		if err != nil {
			p.log().Warn("source fetch failed", "source", src.Name, "error", err)
			continue
		}
		p.log().Info("source fetched", "source", src.Name, "events", len(events))
		all = append(all, events...)
	}
	if len(all) == 0 {
		return fmt.Errorf("no events fetched")
	}

	existing, err := store.ReadEvents(p.eventsPath())
	if err != nil {
		return err
	}
	newEvents := onlyNew(existing, all)
	if err := store.AppendEvents(p.eventsPath(), newEvents); err != nil {
		return err
	}
	p.log().Info("events stored", "new_events", len(newEvents), "path", p.eventsPath())
	return nil
}

func (p Pipeline) Analyze(ctx context.Context) ([]model.Narrative, error) {
	events, err := store.ReadEvents(p.eventsPath())
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("no events to analyze")
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].PublishedAt.Before(events[j].PublishedAt)
	})

	extractor := narrative.Extractor{Now: p.now}
	narratives := extractor.Extract(events, events, narrative.Options{
		WindowDays: p.WindowDays,
		Limit:      p.NarrativeLimit,
	})
	if err := store.WriteNarratives(p.narrativesPath(), narratives); err != nil {
		return nil, err
	}
	if err := store.WriteNarrativeReport(p.reportPath(), narratives, p.now()); err != nil {
		return nil, err
	}
	p.log().Info("narratives written", "count", len(narratives), "json", p.narrativesPath(), "report", p.reportPath())
	return narratives, nil
}

func (p Pipeline) eventsPath() string {
	return filepath.Join(p.dataDir(), "events.jsonl")
}

func (p Pipeline) narrativesPath() string {
	return filepath.Join(p.dataDir(), "narratives.json")
}

func (p Pipeline) reportPath() string {
	return filepath.Join(p.reportsDir(), "narratives", p.now().Format("2006-01-02")+".md")
}

func (p Pipeline) dataDir() string {
	if p.DataDir != "" {
		return p.DataDir
	}
	return "data"
}

func (p Pipeline) reportsDir() string {
	if p.ReportsDir != "" {
		return p.ReportsDir
	}
	return "reports"
}

func (p Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now().UTC()
}

func (p Pipeline) log() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

func onlyNew(existing, incoming []model.Event) []model.Event {
	seen := map[string]bool{}
	for _, ev := range existing {
		seen[ev.ID] = true
	}
	var out []model.Event
	for _, ev := range incoming {
		if seen[ev.ID] {
			continue
		}
		seen[ev.ID] = true
		out = append(out, ev)
	}
	return out
}
