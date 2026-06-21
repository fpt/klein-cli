package model

import (
	"strings"
	"time"
)

type Source struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	URL       string  `json:"url"`
	Intake    string  `json:"intake"`
	Role      string  `json:"role"`
	TrustTier string  `json:"trust_tier"`
	Weight    float64 `json:"weight"`
}

type Event struct {
	ID          string    `json:"id"`
	Source      string    `json:"source"`
	Intake      string    `json:"intake"`
	Role        string    `json:"role"`
	TrustTier   string    `json:"trust_tier"`
	Weight      float64   `json:"weight"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Summary     string    `json:"summary,omitempty"`
	FetchedAt   time.Time `json:"fetched_at"`
}

type Narrative struct {
	ID                    string         `json:"id"`
	Label                 string         `json:"label"`
	Themes                []string       `json:"themes"`
	Entities              []string       `json:"entities"`
	EventIDs              []string       `json:"event_ids"`
	SignalEventIDs        []string       `json:"signal_event_ids"`
	OutcomeEventIDs       []string       `json:"outcome_event_ids"`
	SourceCount           int            `json:"source_count"`
	EventCount            int            `json:"event_count"`
	SignalCount           int            `json:"signal_count"`
	OutcomeCount          int            `json:"outcome_count"`
	SourceMix             map[string]int `json:"source_mix"`
	TrustMix              map[string]int `json:"trust_mix"`
	IntakeMix             map[string]int `json:"intake_mix"`
	WeightedEvidenceScore float64        `json:"weighted_evidence_score"`
	FirstSeen             time.Time      `json:"first_seen"`
	LastSeen              time.Time      `json:"last_seen"`
	Score                 float64        `json:"score"`
	Evidence              []string       `json:"evidence"`
	OutcomeEvidence       []string       `json:"outcome_evidence"`
	Trend                 string         `json:"trend"`
	PreviousEvents        int            `json:"previous_events"`
}

const (
	RoleSignal  = "signal"
	RoleOutcome = "outcome"

	TrustPrimary   = "primary"
	TrustAnalyst   = "analyst"
	TrustCorporate = "corporate"
	TrustNews      = "news"
	TrustOutcome   = "outcome"
)

func NormalizeSource(src Source) Source {
	src.Type = defaultString(src.Type, "rss")
	src.Intake = defaultString(src.Intake, "general")
	src.Role = defaultString(src.Role, RoleSignal)
	src.TrustTier = defaultString(src.TrustTier, TrustNews)
	if IsOutcomeRole(src.Role) && src.TrustTier == TrustNews {
		src.TrustTier = TrustOutcome
	}
	if src.Weight <= 0 {
		src.Weight = TrustWeight(src.TrustTier)
	}
	return src
}

func IsOutcomeRole(role string) bool {
	return strings.EqualFold(role, RoleOutcome)
}

func TrustWeight(tier string) float64 {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case TrustPrimary, "government", "official", "gov":
		return 1.0
	case TrustAnalyst:
		return 0.75
	case TrustCorporate, "company":
		return 0.7
	case TrustOutcome, "market":
		return 0.65
	case TrustNews:
		return 0.45
	default:
		return 0.4
	}
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return strings.ToLower(value)
}
