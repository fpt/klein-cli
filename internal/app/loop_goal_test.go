package app

import (
	"testing"
	"time"
)

func TestParseLoopArgs(t *testing.T) {
	cases := []struct {
		name         string
		args         string
		wantInterval time.Duration
		wantPrompt   string
	}{
		{"leading interval", "5m check the deploy", 5 * time.Minute, "check the deploy"},
		{"leading seconds", "30s ping", 30 * time.Second, "ping"},
		{"leading hours", "2h /review", 2 * time.Hour, "/review"},
		{"leading days", "1d nightly", 24 * time.Hour, "nightly"},
		{"trailing every short", "check the deploy every 20m", 20 * time.Minute, "check the deploy"},
		{"trailing every words", "run tests every 5 minutes", 5 * time.Minute, "run tests"},
		{"trailing every hours", "poll every 2 hours", 2 * time.Hour, "poll"},
		{"every not a time expr", "check every PR", loopDefaultInterval, "check every PR"},
		{"no interval", "check the deploy", loopDefaultInterval, "check the deploy"},
		{"empty", "", loopDefaultInterval, ""},
		{"interval only", "5m", 5 * time.Minute, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iv, prompt := parseLoopArgs(tc.args)
			if iv != tc.wantInterval {
				t.Errorf("interval = %v, want %v", iv, tc.wantInterval)
			}
			if prompt != tc.wantPrompt {
				t.Errorf("prompt = %q, want %q", prompt, tc.wantPrompt)
			}
		})
	}
}

func TestParseGoalEvaluation(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		wantMet    bool
		wantReason string
	}{
		{"met yes", "MET: yes\nREASON: all tests pass", true, "all tests pass"},
		{"met no", "MET: no\nREASON: lint still failing", false, "lint still failing"},
		{"case insensitive", "met: YES\nreason: done", true, "done"},
		{"extra prose ignored", "Here is my verdict.\nMET: no\nREASON: build broke", false, "build broke"},
		{"unparseable", "I think it is probably fine", false, goalEvalReason},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			met, reason := parseGoalEvaluation(tc.content)
			if met != tc.wantMet {
				t.Errorf("met = %v, want %v", met, tc.wantMet)
			}
			if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestGoalStopAfterTurns(t *testing.T) {
	cases := []struct {
		condition string
		want      int
	}{
		{"all tests pass or stop after 5 turns", 5},
		{"finish the migration or stop after 1 turn", 1},
		{"no explicit budget", goalMaxTurns},
		{"stop after 999 turns (over cap)", goalMaxTurns},
	}
	for _, tc := range cases {
		t.Run(tc.condition, func(t *testing.T) {
			max := goalMaxTurns
			if m := reStopAfterTurns.FindStringSubmatch(tc.condition); m != nil {
				if n := atoiSafe(m[1]); n > 0 && n < goalMaxTurns {
					max = n
				}
			}
			if max != tc.want {
				t.Errorf("maxTurns = %d, want %d", max, tc.want)
			}
		})
	}
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}
