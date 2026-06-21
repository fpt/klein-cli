package narrative

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/fpt/klein-cli/internal/researcher/model"
)

type Extractor struct {
	Now func() time.Time
}

type Options struct {
	WindowDays int
	Limit      int
}

type theme struct {
	Name  string
	Terms []string
}

var themes = []theme{
	{Name: "geopolitics", Terms: []string{"iran", "israel", "trump", "white house", "pentagon", "war", "attack", "strike", "sanction", "tariff", "china", "russia", "taiwan", "nato", "middle east", "red sea", "ukraine", "イラン", "イスラエル", "トランプ", "戦争", "攻撃", "制裁", "関税", "中国", "ロシア", "台湾", "中東", "ウクライナ"}},
	{Name: "energy", Terms: []string{"oil", "crude", "brent", "wti", "gas", "lng", "opec", "naphtha", "refinery", "energy", "石油", "原油", "ナフサ", "ガス", "エネルギー", "製油"}},
	{Name: "ai", Terms: []string{"ai", "artificial intelligence", "openai", "anthropic", "llm", "gpu", "data center", "datacenter", "生成ai", "人工知能", "データセンター"}},
	{Name: "semiconductors", Terms: []string{"chip", "chips", "semiconductor", "nvidia", "tsmc", "asml", "amd", "intel", "memory", "hbm", "半導体", "エヌビディア", "tsmc", "メモリ"}},
	{Name: "metals", Terms: []string{"gold", "silver", "copper", "aluminum", "nickel", "lithium", "rare earth", "commodity", "commodities", "金", "銀", "銅", "アルミ", "ニッケル", "リチウム", "レアアース", "資源"}},
	{Name: "markets", Terms: []string{"stock", "stocks", "nasdaq", "s&p", "dow", "yield", "bond", "dollar", "yen", "equity", "futures", "etf", "株", "株式", "債券", "利回り", "ドル", "円", "先物"}},
	{Name: "inflation", Terms: []string{"inflation", "cpi", "ppi", "prices", "rate cut", "rate hike", "fed", "boj", "ecb", "central bank", "インフレ", "物価", "利下げ", "利上げ", "frb", "日銀", "中央銀行"}},
	{Name: "supply-chain", Terms: []string{"supply chain", "shipping", "port", "container", "factory", "export", "import", "inventory", "サプライチェーン", "物流", "輸出", "輸入", "在庫", "港"}},
}

var entityRE = regexp.MustCompile(`\b[A-Z][A-Za-z0-9&.-]*(?:\s+[A-Z][A-Za-z0-9&.-]*){0,3}\b`)

func (e Extractor) Extract(events []model.Event, previous []model.Event, opts Options) []model.Narrative {
	if opts.WindowDays <= 0 {
		opts.WindowDays = 7
	}
	if opts.Limit <= 0 {
		opts.Limit = 20
	}

	now := e.now()
	cutoff := now.AddDate(0, 0, -opts.WindowDays)
	recent := filterSince(events, cutoff)

	type bucket struct {
		key          string
		themes       map[string]int
		entities     map[string]int
		events       []model.Event
		signalEvents []model.Event
		outcomes     []model.Event
		sources      map[string]bool
		sourceMix    map[string]int
		trustMix     map[string]int
		intakeMix    map[string]int
		weighted     float64
	}

	buckets := map[string]*bucket{}
	for _, ev := range recent {
		text := eventText(ev)
		matchedThemes := detectThemes(text)
		if len(matchedThemes) < 2 {
			continue
		}
		sort.Strings(matchedThemes)
		key := strings.Join(matchedThemes, "+")
		b := buckets[key]
		if b == nil {
			b = &bucket{
				key:       key,
				themes:    map[string]int{},
				entities:  map[string]int{},
				sources:   map[string]bool{},
				sourceMix: map[string]int{},
				trustMix:  map[string]int{},
				intakeMix: map[string]int{},
			}
			buckets[key] = b
		}
		for _, theme := range matchedThemes {
			b.themes[theme]++
		}
		for _, entity := range detectEntities(text) {
			b.entities[entity]++
		}
		b.events = append(b.events, ev)
		if model.IsOutcomeRole(ev.Role) {
			b.outcomes = append(b.outcomes, ev)
		} else {
			b.signalEvents = append(b.signalEvents, ev)
		}
		b.sources[ev.Source] = true
		b.sourceMix[defaultValue(ev.Source, "unknown")]++
		b.trustMix[defaultValue(ev.TrustTier, model.TrustNews)]++
		b.intakeMix[defaultValue(ev.Intake, "general")]++
		b.weighted += eventWeight(ev)
	}

	previousCounts := previousThemeCounts(filterBefore(previous, cutoff))
	narratives := make([]model.Narrative, 0, len(buckets))
	for _, b := range buckets {
		if len(b.signalEvents) == 0 {
			continue
		}
		sort.Slice(b.events, func(i, j int) bool {
			return b.events[i].PublishedAt.Before(b.events[j].PublishedAt)
		})
		sort.Slice(b.signalEvents, func(i, j int) bool {
			return evidenceRank(b.signalEvents[i]) > evidenceRank(b.signalEvents[j])
		})
		sort.Slice(b.outcomes, func(i, j int) bool {
			return b.outcomes[i].PublishedAt.After(b.outcomes[j].PublishedAt)
		})
		themeNames := sortedKeys(b.themes)
		evIDs := make([]string, 0, len(b.events))
		signalIDs := make([]string, 0, len(b.signalEvents))
		outcomeIDs := make([]string, 0, len(b.outcomes))
		evidence := make([]string, 0, min(4, len(b.signalEvents)))
		outcomeEvidence := make([]string, 0, min(3, len(b.outcomes)))
		for _, ev := range b.events {
			evIDs = append(evIDs, ev.ID)
		}
		for i, ev := range b.signalEvents {
			signalIDs = append(signalIDs, ev.ID)
			if i < 4 {
				evidence = append(evidence, evidenceLine(ev))
			}
		}
		for i, ev := range b.outcomes {
			outcomeIDs = append(outcomeIDs, ev.ID)
			if i < 3 {
				outcomeEvidence = append(outcomeEvidence, evidenceLine(ev))
			}
		}
		prev := previousCounts[b.key]
		score := scoreNarrative(len(b.signalEvents), len(b.outcomes), len(b.sources), len(themeNames), b.weighted, prev)
		n := model.Narrative{
			ID:                    narrativeID(b.key),
			Label:                 label(themeNames, topKeys(b.entities, 3)),
			Themes:                themeNames,
			Entities:              topKeys(b.entities, 8),
			EventIDs:              evIDs,
			SignalEventIDs:        signalIDs,
			OutcomeEventIDs:       outcomeIDs,
			SourceCount:           len(b.sources),
			EventCount:            len(b.events),
			SignalCount:           len(b.signalEvents),
			OutcomeCount:          len(b.outcomes),
			SourceMix:             b.sourceMix,
			TrustMix:              b.trustMix,
			IntakeMix:             b.intakeMix,
			WeightedEvidenceScore: math.Round(b.weighted*100) / 100,
			FirstSeen:             b.events[0].PublishedAt,
			LastSeen:              b.events[len(b.events)-1].PublishedAt,
			Score:                 score,
			Evidence:              evidence,
			OutcomeEvidence:       outcomeEvidence,
			Trend:                 trend(len(b.signalEvents)+len(b.outcomes), prev),
			PreviousEvents:        prev,
		}
		narratives = append(narratives, n)
	}

	sort.Slice(narratives, func(i, j int) bool {
		if narratives[i].Score == narratives[j].Score {
			return narratives[i].LastSeen.After(narratives[j].LastSeen)
		}
		return narratives[i].Score > narratives[j].Score
	})
	if len(narratives) > opts.Limit {
		narratives = narratives[:opts.Limit]
	}
	return narratives
}

func (e Extractor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now().UTC()
}

func detectThemes(text string) []string {
	lower := strings.ToLower(text)
	var out []string
	for _, theme := range themes {
		for _, term := range theme.Terms {
			if strings.Contains(lower, strings.ToLower(term)) {
				out = append(out, theme.Name)
				break
			}
		}
	}
	return out
}

func detectEntities(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range entityRE.FindAllString(text, -1) {
		match = strings.TrimSpace(match)
		if len(match) < 2 || isStopEntity(match) || seen[match] {
			continue
		}
		seen[match] = true
		out = append(out, match)
	}
	for _, term := range []string{"トランプ", "イラン", "イスラエル", "中国", "ロシア", "台湾", "日銀", "半導体", "ナフサ"} {
		if strings.Contains(text, term) && !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	return out
}

func isStopEntity(v string) bool {
	stops := map[string]bool{
		"The": true, "This": true, "That": true, "For": true, "With": true, "From": true,
		"Reuters": true, "Bloomberg": true, "Updated": true, "News": true,
	}
	if stops[v] {
		return true
	}
	for _, r := range v {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func previousThemeCounts(events []model.Event) map[string]int {
	counts := map[string]int{}
	for _, ev := range events {
		matched := detectThemes(eventText(ev))
		if len(matched) < 2 {
			continue
		}
		sort.Strings(matched)
		counts[strings.Join(matched, "+")]++
	}
	return counts
}

func scoreNarrative(signals, outcomes, sources, themeCount int, weightedEvidence float64, prev int) float64 {
	freshness := 1.0
	if prev > 0 {
		freshness = float64(signals+outcomes) / float64(prev+1)
	}
	outcomeBoost := math.Min(float64(outcomes)*1.1, 4.0)
	return math.Round((weightedEvidence*2.5+float64(sources)*1.4+float64(themeCount)*0.8+outcomeBoost+freshness)*100) / 100
}

func trend(current, previous int) string {
	switch {
	case previous == 0 && current > 0:
		return "emerging"
	case current >= previous*2 && current >= 3:
		return "accelerating"
	case current < previous:
		return "cooling"
	default:
		return "steady"
	}
}

func label(themes, entities []string) string {
	left := strings.Join(themes, " x ")
	if len(entities) == 0 {
		return left
	}
	return left + " around " + strings.Join(entities, ", ")
}

func narrativeID(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}

func filterSince(events []model.Event, cutoff time.Time) []model.Event {
	var out []model.Event
	for _, ev := range events {
		if ev.PublishedAt.IsZero() || !ev.PublishedAt.Before(cutoff) {
			out = append(out, ev)
		}
	}
	return out
}

func filterBefore(events []model.Event, cutoff time.Time) []model.Event {
	var out []model.Event
	for _, ev := range events {
		if !ev.PublishedAt.IsZero() && ev.PublishedAt.Before(cutoff) {
			out = append(out, ev)
		}
	}
	return out
}

func eventText(ev model.Event) string {
	return ev.Title + " " + ev.Summary
}

func eventWeight(ev model.Event) float64 {
	if ev.Weight > 0 {
		return ev.Weight
	}
	return model.TrustWeight(ev.TrustTier)
}

func evidenceRank(ev model.Event) float64 {
	rank := eventWeight(ev)
	if strings.EqualFold(ev.TrustTier, model.TrustPrimary) {
		rank += 1.0
	}
	if model.IsOutcomeRole(ev.Role) {
		rank -= 0.25
	}
	return rank
}

func evidenceLine(ev model.Event) string {
	return fmt.Sprintf("[%s/%s/%s] %s: %s", defaultValue(ev.Role, model.RoleSignal), defaultValue(ev.TrustTier, model.TrustNews), defaultValue(ev.Intake, "general"), ev.Source, ev.Title)
}

func defaultValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func topKeys(m map[string]int, limit int) []string {
	type item struct {
		key   string
		count int
	}
	items := make([]item, 0, len(m))
	for k, v := range m {
		items = append(items, item{key: k, count: v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count == items[j].count {
			return items[i].key < items[j].key
		}
		return items[i].count > items[j].count
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.key)
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
