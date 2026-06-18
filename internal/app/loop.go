package app

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// loopDefaultInterval matches Claude Code's bundled /loop default cadence.
const loopDefaultInterval = 10 * time.Minute

// loopMaintenancePrompt is used when /loop is given an interval but no prompt
// (or a bare /loop). It mirrors the built-in maintenance behavior: continue
// unfinished work and tidy up, without starting new initiatives.
const loopMaintenancePrompt = "Continue any unfinished work from this conversation. " +
	"If there is nothing pending, do a small cleanup pass (review for bugs or simplifications) " +
	"and report findings in one or two lines. Do not start new initiatives or take irreversible " +
	"actions (pushing, deleting) unless they continue something already authorized in this conversation."

var (
	reLeadingInterval = regexp.MustCompile(`^(\d+)([smhd])$`)
	reTrailingEvery   = regexp.MustCompile(`(?i)\s+every\s+(\d+)\s*([smhd]|sec(?:ond)?s?|min(?:ute)?s?|hours?|days?)$`)
)

// runLoop drives the /loop command: it repeatedly runs a prompt, sleeping for
// the parsed interval between iterations, until the user presses Ctrl+C.
//
// klein has no background scheduler, so the loop runs in the foreground and the
// inter-iteration wait is an interruptible sleep (Esc/Ctrl+C stops it).
func runLoop(ctx context.Context, a *Agent, skillName, args string) {
	w := a.OutWriter()

	interval, prompt := parseLoopArgs(strings.TrimSpace(args))
	if prompt == "" {
		prompt = loopMaintenancePrompt
	}

	fmt.Fprintf(w, "\n↻ Loop every %s (Ctrl+C to stop): %s\n", interval, truncateForDisplay(prompt, 80))

	for iter := 1; ; iter++ {
		fmt.Fprintf(w, "\n↻ Loop iteration %d\n", iter)

		_, canceled, err := executeTurn(ctx, a, prompt, skillName)
		if canceled {
			fmt.Fprintf(w, "\n↻ Loop stopped by user.\n")
			return
		}
		if err != nil {
			fmt.Fprintf(w, "\n↻ Loop stopped: iteration failed: %v\n", err)
			return
		}

		fmt.Fprintf(w, "\n↻ Waiting %s until next iteration (Ctrl+C to stop)...\n", interval)
		if interruptibleSleep(ctx, interval) {
			fmt.Fprintf(w, "↻ Loop stopped.\n")
			return
		}
	}
}

// parseLoopArgs splits the /loop argument into (interval, prompt). It accepts a
// leading bare token (e.g. "5m check x"), a trailing "every <n><unit>" clause
// (e.g. "check x every 20m"), or neither (defaulting the interval).
func parseLoopArgs(args string) (time.Duration, string) {
	if args == "" {
		return loopDefaultInterval, ""
	}

	// Rule 1: leading interval token.
	fields := strings.Fields(args)
	if len(fields) > 0 {
		if m := reLeadingInterval.FindStringSubmatch(fields[0]); m != nil {
			d := intervalToDuration(m[1], m[2])
			rest := strings.TrimSpace(strings.TrimPrefix(args, fields[0]))
			return d, rest
		}
	}

	// Rule 2: trailing "every <n><unit>" clause (only when it's a time expr).
	if m := reTrailingEvery.FindStringSubmatch(args); m != nil {
		d := intervalToDuration(m[1], normalizeUnit(m[2]))
		rest := strings.TrimSpace(reTrailingEvery.ReplaceAllString(args, ""))
		return d, rest
	}

	// Rule 3: no interval — whole input is the prompt.
	return loopDefaultInterval, args
}

// normalizeUnit maps long unit words (minutes, hours, ...) to single-letter form.
func normalizeUnit(u string) string {
	u = strings.ToLower(u)
	switch {
	case strings.HasPrefix(u, "sec"), u == "s":
		return "s"
	case strings.HasPrefix(u, "min"), u == "m":
		return "m"
	case strings.HasPrefix(u, "hour"), u == "h":
		return "h"
	case strings.HasPrefix(u, "day"), u == "d":
		return "d"
	}
	return "m"
}

// intervalToDuration converts a numeric value and single-letter unit to a
// Duration, with a 1-second floor.
func intervalToDuration(value, unit string) time.Duration {
	n, err := strconv.Atoi(value)
	if err != nil || n <= 0 {
		return loopDefaultInterval
	}
	switch unit {
	case "s":
		return time.Duration(n) * time.Second
	case "m":
		return time.Duration(n) * time.Minute
	case "h":
		return time.Duration(n) * time.Hour
	case "d":
		return time.Duration(n) * 24 * time.Hour
	}
	return loopDefaultInterval
}

// truncateForDisplay shortens s to n runes with an ellipsis for one-line output.
func truncateForDisplay(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
