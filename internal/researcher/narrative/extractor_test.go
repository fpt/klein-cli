package narrative

import (
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/researcher/model"
)

func TestExtractMultiThemeNarratives(t *testing.T) {
	now := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	events := []model.Event{
		{
			ID:          "1",
			Source:      "a",
			Intake:      "government-us",
			Role:        "signal",
			TrustTier:   "primary",
			Weight:      1.0,
			Title:       "Iran tensions lift oil and naphtha prices",
			Summary:     "Energy markets react to Middle East attack risk.",
			PublishedAt: now.AddDate(0, 0, -1),
		},
		{
			ID:          "2",
			Source:      "b",
			Intake:      "market-outcome",
			Role:        "outcome",
			TrustTier:   "outcome",
			Weight:      0.65,
			Title:       "Oil futures jump as Iran tensions hit energy markets",
			Summary:     "Naphtha and crude prices move higher.",
			PublishedAt: now.AddDate(0, 0, -1),
		},
		{
			ID:          "3",
			Source:      "c",
			Intake:      "news",
			Role:        "signal",
			TrustTier:   "news",
			Weight:      0.45,
			Title:       "AI data center boom drives semiconductor stocks and copper",
			Summary:     "Nvidia and chip suppliers rise as metals demand grows.",
			PublishedAt: now.AddDate(0, 0, -1),
		},
		{
			ID:          "4",
			Source:      "d",
			Title:       "Local sports result",
			PublishedAt: now.AddDate(0, 0, -1),
		},
	}

	extractor := Extractor{Now: func() time.Time { return now }}
	narratives := extractor.Extract(events, events, Options{WindowDays: 7, Limit: 10})
	if len(narratives) != 2 {
		t.Fatalf("narratives = %d, want 2", len(narratives))
	}
	if narratives[0].EventCount == 0 || narratives[0].Score == 0 {
		t.Fatalf("invalid narrative: %+v", narratives[0])
	}
	if narratives[0].SignalCount == 0 {
		t.Fatalf("narrative should have a signal: %+v", narratives[0])
	}
}
