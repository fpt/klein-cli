package app

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
	"golang.org/x/term"
)

// ContextDisplay handles the status line shown above the REPL prompt.
type ContextDisplay struct{}

// NewContextDisplay creates a new context display instance
func NewContextDisplay() *ContextDisplay {
	return &ContextDisplay{}
}

// CalculateUsageDetails calculates context window usage details from message state and LLM client.
func (cd *ContextDisplay) CalculateUsageDetails(messageState domain.State, llmClient domain.LLM) (currentTokens, maxTokens, percentage int) {
	messages := messageState.GetMessages()
	if len(messages) == 0 {
		return 0, 0, 0
	}

	estimateTokensFor := func(msg message.Message) int {
		content := msg.Content()
		if t := msg.Thinking(); t != "" {
			content += "\n" + t
		}
		// ~4 chars per token + small per-message overhead
		approx := int(math.Ceil(float64(len(content))/4.0)) + 8
		if approx < 0 {
			approx = 0
		}
		return approx
	}

	for _, msg := range messages {
		currentTokens += estimateTokensFor(msg)
	}

	// Prefer the interface-based value; fall back to hardcoded estimates.
	if cwp, ok := llmClient.(domain.ContextWindowProvider); ok {
		maxTokens = cwp.MaxContextTokens()
	}
	if maxTokens <= 0 {
		return 0, 0, 0
	}

	percentage = int(math.Round(float64(currentTokens) * 100.0 / float64(maxTokens)))
	if percentage > 100 {
		percentage = 100
	}
	return
}

// ShowStatusLine renders the combined task + context status line printed above the prompt.
// taskSummary may be empty; contextState is always shown when available.
func (cd *ContextDisplay) ShowStatusLine(messageState domain.State, llmClient domain.LLM, taskSummary string) string {
	terminalWidth := 80
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		terminalWidth = width
	}

	currentTokens, maxTokens, percentage := cd.CalculateUsageDetails(messageState, llmClient)
	if maxTokens <= 0 && taskSummary == "" {
		return ""
	}

	// Right side: context usage with color.
	var contextStr string
	if maxTokens > 0 {
		var colorCode string
		switch {
		case percentage < 50:
			colorCode = "\033[32m" // green
		case percentage < 80:
			colorCode = "\033[33m" // yellow
		default:
			colorCode = "\033[31m" // red
		}
		contextVisible := fmt.Sprintf("Context: %dk/%dk (%d%%)", currentTokens/1000, maxTokens/1000, percentage)
		contextStr = colorCode + contextVisible + "\033[0m"

		// Left side: task summary (dim/grey).
		if taskSummary != "" {
			taskStr := "\033[2m" + taskSummary + "\033[0m"
			taskVisible := taskSummary
			contextVisibleLen := len(contextVisible)
			taskVisibleLen := len(taskVisible)
			gap := terminalWidth - taskVisibleLen - contextVisibleLen
			if gap < 1 {
				gap = 1
			}
			return taskStr + strings.Repeat(" ", gap) + contextStr
		}

		// No tasks — right-align context only.
		visLen := len(fmt.Sprintf("Context: %dk/%dk (%d%%)", currentTokens/1000, maxTokens/1000, percentage))
		pad := terminalWidth - visLen
		if pad < 0 {
			pad = 0
		}
		return strings.Repeat(" ", pad) + contextStr
	}

	// No context info — show task summary left-aligned.
	return "\033[2m" + taskSummary + "\033[0m"
}

// ShowContextUsage is kept for backward compatibility; delegates to ShowStatusLine with no task summary.
func (cd *ContextDisplay) ShowContextUsage(messageState domain.State, llmClient domain.LLM) string {
	return cd.ShowStatusLine(messageState, llmClient, "")
}
