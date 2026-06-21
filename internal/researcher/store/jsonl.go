package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fpt/klein-cli/internal/researcher/model"
)

func AppendEvents(path string, events []model.Event) error {
	if len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
	return nil
}

func ReadEvents(path string) ([]model.Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []model.Event
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)
	for scanner.Scan() {
		var ev model.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func WriteNarratives(path string, narratives []model.Narrative) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(narratives)
}

func WriteNarrativeReport(path string, narratives []model.Narrative, generatedAt time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "# Narrative Report: %s\n\n", generatedAt.Format("2006-01-02"))
	fmt.Fprintf(&b, "- Generated at: %s\n", generatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Narratives: %d\n\n", len(narratives))

	if len(narratives) == 0 {
		b.WriteString("No hot narratives detected in the current window.\n")
		return os.WriteFile(path, b.Bytes(), 0o644)
	}

	for i, n := range narratives {
		fmt.Fprintf(&b, "## %d. %s\n\n", i+1, n.Label)
		fmt.Fprintf(&b, "- Score: %.2f\n", n.Score)
		fmt.Fprintf(&b, "- Trend: %s\n", n.Trend)
		fmt.Fprintf(&b, "- Themes: %s\n", join(n.Themes))
		fmt.Fprintf(&b, "- Entities: %s\n", join(n.Entities))
		fmt.Fprintf(&b, "- Events: %d signals + %d outcomes across %d sources\n", n.SignalCount, n.OutcomeCount, n.SourceCount)
		fmt.Fprintf(&b, "- Weighted evidence: %.2f\n", n.WeightedEvidenceScore)
		fmt.Fprintf(&b, "- Trust mix: %s\n", joinCounts(n.TrustMix))
		fmt.Fprintf(&b, "- Intake mix: %s\n", joinCounts(n.IntakeMix))
		fmt.Fprintf(&b, "- Window: %s to %s\n\n", n.FirstSeen.Format("2006-01-02"), n.LastSeen.Format("2006-01-02"))
		b.WriteString("Signals:\n")
		for _, ev := range n.Evidence {
			fmt.Fprintf(&b, "- %s\n", ev)
		}
		if len(n.OutcomeEvidence) > 0 {
			b.WriteString("\nOutcomes:\n")
			for _, ev := range n.OutcomeEvidence {
				fmt.Fprintf(&b, "- %s\n", ev)
			}
		}
		b.WriteString("\n")
	}

	return os.WriteFile(path, b.Bytes(), 0o644)
}

func join(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	var b bytes.Buffer
	for i, v := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(v)
	}
	return b.String()
}

func joinCounts(values map[string]int) string {
	if len(values) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b bytes.Buffer
	for i, key := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%d", key, values[key])
	}
	return b.String()
}
