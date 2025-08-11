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

// ContextDisplay handles context window usage visualization
type ContextDisplay struct{}

// NewContextDisplay creates a new context display instance
func NewContextDisplay() *ContextDisplay {
	return &ContextDisplay{}
}

// CalculateUsageDetails calculates context window usage details from message state and LLM client
func (cd *ContextDisplay) CalculateUsageDetails(messageState domain.State, llmClient domain.LLM) (currentTokens, maxTokens, percentage int) {
	messages := messageState.GetMessages()
	if len(messages) == 0 {
		return 0, 0, 0
	}

	// Estimate tokens for all messages
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

	totalTokens := 0
	for _, msg := range messages {
		totalTokens += estimateTokensFor(msg)
	}

	// Get estimated context window size based on client type
	maxTokens = cd.estimateContextWindow(llmClient)
	if maxTokens <= 0 {
		return 0, 0, 0
	}

	percentage = int(math.Round(float64(totalTokens) * 100.0 / float64(maxTokens)))

	// Cap at 100%
	if percentage > 100 {
		percentage = 100
	}

	return totalTokens, maxTokens, percentage
}

// estimateContextWindow estimates the context window size based on LLM client type
func (cd *ContextDisplay) estimateContextWindow(llmClient domain.LLM) int {
	clientType := fmt.Sprintf("%T", llmClient)

	switch {
	case strings.Contains(clientType, "anthropic"):
		return 200000 // Claude models typically have 200k+ context windows
	case strings.Contains(clientType, "openai"):
		return 128000 // GPT-4o models have 128k context windows
	case strings.Contains(clientType, "gemini"):
		return 1000000 // Gemini models have 1M+ context windows
	case strings.Contains(clientType, "ollama"):
		return 32000 // Ollama models typically have 32k context windows (varies by model)
	default:
		return 32000 // Conservative fallback
	}
}

// FormatContextUsage creates a right-aligned context usage display with color coding
func (cd *ContextDisplay) FormatContextUsage(currentTokens, maxTokens, percentage int, terminalWidth int) string {
	var colorCode string
	var resetCode string = "\033[0m"

	// Color code based on usage level
	switch {
	case percentage < 50:
		colorCode = "\033[32m" // Green - low usage
	case percentage < 80:
		colorCode = "\033[33m" // Yellow - moderate usage
	default:
		colorCode = "\033[31m" // Red - high usage
	}

	contextStr := fmt.Sprintf("%sContext: %d/%d (%.1f%%)%s", colorCode, currentTokens, maxTokens, float64(percentage), resetCode)

	// Calculate padding to right-align (accounting for color codes)
	visibleLength := fmt.Sprintf("Context: %d/%d (%.1f%%)", currentTokens, maxTokens, float64(percentage))
	padding := terminalWidth - len(visibleLength)
	if padding < 0 {
		padding = 0
	}

	return strings.Repeat(" ", padding) + contextStr
}

// ShowContextUsage displays the context usage below the current output
func (cd *ContextDisplay) ShowContextUsage(messageState domain.State, llmClient domain.LLM) string {
	// Get terminal width
	terminalWidth := 80 // fallback
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		terminalWidth = width
	}

	// Calculate current context usage details
	currentTokens, maxTokens, percentage := cd.CalculateUsageDetails(messageState, llmClient)

	// Create and display the status line
	statusLine := cd.FormatContextUsage(currentTokens, maxTokens, percentage, terminalWidth)
	return statusLine
}
