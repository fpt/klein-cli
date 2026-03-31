package state

import (
	"context"
	"fmt"
	"strings"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
	"github.com/fpt/klein-cli/pkg/message"
)

// Package-level logger for state management operations
var logger = pkgLogger.NewComponentLogger("state-manager")

// Standard compaction strategy - universal values for all models
const (
	CompactAtPercent   = 0.70 // Trigger compaction at 70% of context window
	TargetAfterPercent = 0.35 // Compact down to 35% of context window
	MinReductionTokens = 5000 // Must save at least 5K tokens to be worthwhile
)

// Lightweight cleanup configuration.
// keepRecentTurns defines how many complete user turns to leave fully intact;
// messages in older turns are eligible for content reduction.
// microCompactMinChars is the tool-result size above which the content is
// replaced with a compact stub during cleanup of old turns.
const (
	keepRecentTurns      = 5     // protect the last N user turns from content reduction
	microCompactMinChars = 2_000 // minimum chars to trigger tool-result stub replacement
)

// offloadStubPrefix is the prefix written by ToolResultStorage when a result
// has already been persisted to disk. Such stubs are never re-stubbed.
const offloadStubPrefix = "[Result offloaded to disk:"

// microCompactStub is the replacement text inserted when a tool result is
// cleared by lightweight cleanup.
const microCompactStub = "[Content cleared by micro-compaction — was %d chars]"

// CleanupMandatory performs lightweight, LLM-free cleanup on every invocation:
//
//  1. Removes stale summary and situation messages (source-based filtering).
//  2. Finds the start of the Nth most recent user turn (the "old/recent boundary").
//  3. Applies applyLightweightCleanup to every message before that boundary,
//     stripping vision content and micro-compacting large tool results in a
//     single unified pass.
//
// This unifies the previous image-truncation logic with the new micro-compaction
// so there is exactly one pass over old messages, and one consistent boundary
// definition across both concerns.
func (c *MessageState) CleanupMandatory() error {
	// 1. Remove stale metadata messages
	if n := c.RemoveMessagesBySource(message.MessageSourceSummary); n > 0 {
		logger.DebugWithIntention(pkgLogger.IntentionDebug, "Removed stale summary messages", "count", n)
	}
	if n := c.RemoveMessagesBySource(message.MessageSourceSituation); n > 0 {
		logger.DebugWithIntention(pkgLogger.IntentionDebug, "Removed stale situation messages", "count", n)
	}

	// 2. Locate the boundary: first message of the Nth most recent user turn.
	//    Everything before boundary is "old" and eligible for content reduction.
	boundary := findTurnBoundary(c.Messages, keepRecentTurns)
	if boundary == 0 {
		return nil // not enough history yet — nothing to clean
	}

	// 3. Single pass: strip vision content and micro-compact large tool results.
	reduced := 0
	for i := 0; i < boundary; i++ {
		if cleaned, ok := applyLightweightCleanup(c.Messages[i]); ok {
			c.Messages[i] = cleaned
			reduced++
		}
	}
	if reduced > 0 {
		logger.DebugWithIntention(pkgLogger.IntentionDebug,
			"Lightweight cleanup reduced content in old turns",
			"messages_cleaned", reduced, "boundary", boundary)
	}
	return nil
}

// findTurnBoundary returns the index of the start of the Nth most recent user
// turn. The caller should clean messages at indices [0, boundary).
// Returns 0 when there are fewer than n user messages (nothing is old enough).
func findTurnBoundary(messages []message.Message, n int) int {
	found := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type() == message.MessageTypeUser {
			found++
			if found >= n {
				return i
			}
		}
	}
	return 0
}

// applyLightweightCleanup applies non-destructive content reduction to a single
// message. It handles two concerns in one function so old messages are visited
// exactly once:
//
//   - ChatMessage with images: image data is removed (vision truncation).
//   - ToolResultMessage with images: image data is removed.
//   - ToolResultMessage with a large result text: content is replaced with a
//     stub that records the original size. Already-offloaded stubs are skipped.
//
// Returns (cleaned, true) when a reduction was made, (original, false) otherwise.
func applyLightweightCleanup(msg message.Message) (message.Message, bool) {
	switch m := msg.(type) {
	case *message.ToolResultMessage:
		stripImages := len(m.Images()) > 0
		stubResult := m.Error == "" &&
			len(m.Result) > microCompactMinChars &&
			!strings.HasPrefix(m.Result, offloadStubPrefix) &&
			!strings.HasPrefix(m.Result, "[Content cleared")

		if !stripImages && !stubResult {
			return msg, false
		}

		newResult := m.Result
		if stubResult {
			newResult = fmt.Sprintf(microCompactStub, len(m.Result))
		}
		cleaned := message.NewToolResultMessage(m.ID(), newResult, m.Error)
		cleaned.SetTokenUsage(m.InputTokens(), m.OutputTokens(), m.TotalTokens())
		return cleaned, true

	case *message.ChatMessage:
		if len(m.Images()) == 0 {
			return msg, false
		}
		cleaned := message.NewChatMessage(m.Type(), m.Content())
		cleaned.SetTokenUsage(m.InputTokens(), m.OutputTokens(), m.TotalTokens())
		return cleaned, true
	}
	return msg, false
}

// getAccurateTokenCount returns the most accurate token count available
func (c *MessageState) getAccurateTokenCount(llm domain.LLM) int {
	// Try to get actual token usage from the LLM client first
	if usageProvider, ok := llm.(domain.TokenUsageProvider); ok {
		if usage, ok2 := usageProvider.LastTokenUsage(); ok2 && usage.InputTokens > 0 {
			// Use actual input token count from last request as baseline
			// This is the most accurate measure of current context size
			return usage.InputTokens
		}
	}

	// Fallback: estimate from message content (improved heuristic)
	return c.estimateTokensFromMessages()
}

// estimateTokensFromMessages provides improved token estimation from message content
func (c *MessageState) estimateTokensFromMessages() int {
	var totalTokens int

	for _, msg := range c.Messages {
		// Use stored token usage if available (most accurate)
		if stored := msg.TotalTokens(); stored > 0 {
			totalTokens += stored
			continue
		}

		// Improved estimation heuristic
		content := msg.Content()
		thinking := msg.Thinking()

		// Combine content and thinking for total character count
		totalChars := len(content) + len(thinking)

		// Conservative token estimation (to avoid overestimating)
		// Use the original simple approach to maintain compatibility
		var estimatedTokens int
		if totalChars > 0 {
			// Simple heuristic: ~4 chars/token + small overhead
			estimatedTokens = int(float64(totalChars)/4.0) + 8
		} else {
			estimatedTokens = 8 // Minimum overhead for empty messages
		}

		totalTokens += estimatedTokens
	}

	return totalTokens
}

// CompactIfNeeded performs efficient token-based compaction
func (c *MessageState) CompactIfNeeded(ctx context.Context, llm domain.LLM, maxTokens int, thresholdPercent float64) error {
	if maxTokens <= 0 {
		return nil // No token limit specified
	}

	// Get accurate current token count
	currentTokens := c.getAccurateTokenCount(llm)

	// Calculate thresholds using standard compaction strategy
	compactThreshold := int(float64(maxTokens) * CompactAtPercent)
	targetAfterCompaction := int(float64(maxTokens) * TargetAfterPercent)

	usagePercent := (float64(currentTokens) / float64(maxTokens)) * 100

	logger.DebugWithIntention(pkgLogger.IntentionStatistics, "Token-based compaction check",
		"current_tokens", currentTokens,
		"max_tokens", maxTokens,
		"usage_percent", fmt.Sprintf("%.1f%%", usagePercent),
		"compact_threshold", compactThreshold,
		"target_after", targetAfterCompaction)

	// Check if we need to compact
	if currentTokens < compactThreshold {
		logger.DebugWithIntention(pkgLogger.IntentionStatistics, "Usage below compaction threshold, skipping",
			"usage_percent", fmt.Sprintf("%.1f%%", usagePercent),
			"threshold", fmt.Sprintf("%.1f%%", CompactAtPercent*100))
		return nil
	}

	// Check if compaction will save meaningful tokens
	tokensToSave := currentTokens - targetAfterCompaction
	if tokensToSave < MinReductionTokens {
		logger.InfoWithIntention(pkgLogger.IntentionStatus, "Compaction would save too few tokens, skipping",
			"tokens_to_save", tokensToSave, "min_reduction", MinReductionTokens)
		return nil
	}

	logger.InfoWithIntention(pkgLogger.IntentionStatus, "Performing token-based compaction",
		"current_tokens", currentTokens,
		"usage_percent", fmt.Sprintf("%.1f%%", usagePercent),
		"target_tokens", targetAfterCompaction,
		"tokens_to_save", tokensToSave)

	return c.performCompaction(ctx, llm)
}

// GetTotalTokenUsage returns the total token usage across all messages
func (c *MessageState) GetTotalTokenUsage() (inputTokens, outputTokens, totalTokens int) {
	for _, msg := range c.Messages {
		inputTokens += msg.InputTokens()
		outputTokens += msg.OutputTokens()
		totalTokens += msg.TotalTokens()
	}
	return inputTokens, outputTokens, totalTokens
}

// performCompaction contains the original compaction logic
func (c *MessageState) performCompaction(ctx context.Context, llm domain.LLM) error {
	messages := c.Messages

	// Reset counters before compaction to avoid double counting across histories
	c.ResetTokenCounters()

	// Block-based compaction strategy: keep recent complete conversation blocks
	const preserveRecentBlocks = 5 // Keep the last 5 complete conversation blocks

	// Always perform compaction if we reach this point (either from token or message threshold)

	// Try block-based compaction first
	blocksToPreserve := findConversationBlocksToPreserve(messages, preserveRecentBlocks)

	var olderMessages, recentMessages []message.Message

	// Try to use block-based compaction, but ensure we preserve at least 10 messages for compatibility
	const minMessagesToPreserve = 10

	if len(blocksToPreserve) > 0 && len(blocksToPreserve) >= minMessagesToPreserve && len(blocksToPreserve) < len(messages)-5 {
		// Block-based compaction with good number of messages
		splitIndex := len(messages) - len(blocksToPreserve)
		olderMessages = messages[:splitIndex]
		recentMessages = blocksToPreserve
		logger.InfoWithIntention(pkgLogger.IntentionStatus, "Using block-based compaction",
			"total_messages", len(messages), "blocks_preserved", len(blocksToPreserve))
	} else {
		// Fallback to message-count based compaction
		const preserveRecent = 10
		splitPoint := findSafeSplitPoint(messages, preserveRecent)
		if splitPoint <= 0 {
			logger.DebugWithIntention(pkgLogger.IntentionDebug, "No safe split point found, skipping compaction")
			return nil
		}
		olderMessages = messages[:splitPoint]
		recentMessages = messages[splitPoint:]
		logger.InfoWithIntention(pkgLogger.IntentionStatus, "Using fallback message-based compaction",
			"total_messages", len(messages), "messages_preserved", len(recentMessages))
	}

	// Create an LLM-generated summary of older messages (with vision truncation applied)
	summary, err := c.createLLMSummary(ctx, llm, olderMessages)
	if err != nil {
		logger.Warn("Failed to create LLM summary, using fallback",
			"error", err, "message_count", len(olderMessages))
		summary = createBasicMessageSummary(olderMessages)
	}

	// Create new message state with summary + recent messages
	c.Clear()

	// Add new summary as system message
	summaryMsg := message.NewSummarySystemMessage(
		fmt.Sprintf("# Previous Conversation Summary\n%s\n\n# Current Conversation Continues", summary))
	c.AddMessage(summaryMsg)

	// Add back recent messages, filtering out situation messages
	skippedAlignment := 0
	for _, msg := range recentMessages {
		// Skip situation messages during compaction
		if isSituationMessage(msg) {
			skippedAlignment++
			continue
		}
		c.AddMessage(msg)
	}

	if skippedAlignment > 0 {
		logger.DebugWithIntention(pkgLogger.IntentionDebug, "Skipped alignment messages during compaction",
			"skipped_count", skippedAlignment)
	}
	logger.InfoWithIntention(pkgLogger.IntentionStatus, "Message compaction completed",
		"before_count", len(messages),
		"after_count", len(c.Messages),
		"compression_ratio", fmt.Sprintf("%.1f%%", float64(len(c.Messages))/float64(len(messages))*100))

	// Update token counters based on current messages (sum of input+output)
	c.RecalculateTokenCountersFromMessages()
	in, out, total := c.TokenCountersSnapshot()
	logger.InfoWithIntention(pkgLogger.IntentionStatistics, "Token counters updated after compaction",
		"input_tokens", in, "output_tokens", out, "total_tokens", total)

	return nil
}

// findSafeSplitPoint finds a split point that doesn't break tool call chains
// This is critical for Anthropic API compatibility which requires tool calls and results to be paired
func findSafeSplitPoint(messages []message.Message, preserveRecent int) int {
	desiredSplitPoint := len(messages) - preserveRecent

	// Work backwards from the desired split point to find a safe boundary
	// A safe boundary is one where we don't split tool call/result pairs
	for i := desiredSplitPoint; i >= 0; i-- {
		if isSafeSplitPoint(messages, i) {
			return i
		}
	}

	// If no safe split point found, don't compact
	return 0
}

// isSafeSplitPoint checks if splitting at this point would break tool call chains
func isSafeSplitPoint(messages []message.Message, splitPoint int) bool {
	if splitPoint <= 0 || splitPoint >= len(messages) {
		return false
	}

	// Check if we're splitting in the middle of a tool call chain
	// Rule: Don't split if there's an unpaired tool call before the split point
	// or an unpaired tool result after the split point

	// Count unpaired tool calls before split point (looking backwards)
	unpairedToolCalls := 0
	for i := splitPoint - 1; i >= 0; i-- {
		switch messages[i].Type() {
		case message.MessageTypeToolCall:
			unpairedToolCalls++
		case message.MessageTypeToolResult:
			if unpairedToolCalls > 0 {
				unpairedToolCalls--
			}
		}
	}

	// If there are unpaired tool calls before the split, it's not safe
	if unpairedToolCalls > 0 {
		return false
	}

	// Check for orphaned tool results after split point
	unpairedToolResults := 0
	for i := splitPoint; i < len(messages); i++ {
		switch messages[i].Type() {
		case message.MessageTypeToolResult:
			unpairedToolResults++
		case message.MessageTypeToolCall:
			if unpairedToolResults > 0 {
				unpairedToolResults--
			}
		}
	}

	// If there are unpaired tool results after the split, it's not safe
	if unpairedToolResults > 0 {
		return false
	}

	return true
}

// createLLMSummary creates an intelligent summary using LLM
func (c *MessageState) createLLMSummary(ctx context.Context, llm domain.LLM, messages []message.Message) (string, error) {
	if len(messages) == 0 {
		return "No previous conversation.", nil
	}

	// Build conversation text for summarization
	var conversationBuilder strings.Builder
	conversationBuilder.WriteString("Previous conversation to summarize:\n\n")

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			conversationBuilder.WriteString(fmt.Sprintf("User: %s\n", msg.Content()))
		case message.MessageTypeAssistant:
			// Only include actual responses, not tool calls
			if len(msg.Content()) > 0 && !strings.HasPrefix(msg.Content(), "Tool call:") {
				conversationBuilder.WriteString(fmt.Sprintf("Assistant: %s\n", msg.Content()))
			}
		case message.MessageTypeToolCall:
			if toolMsg, ok := msg.(*message.ToolCallMessage); ok {
				conversationBuilder.WriteString(fmt.Sprintf("Tool used: %s\n", toolMsg.ToolName()))
			}
		case message.MessageTypeToolResult:
			if toolResult, ok := msg.(*message.ToolResultMessage); ok {
				result := toolResult.Result
				if len(result) > 200 {
					result = result[:200] + "..."
				}

				// Drop all images from older messages to save tokens - recent messages keep the latest images
				if len(msg.Images()) > 0 {
					conversationBuilder.WriteString(fmt.Sprintf("Tool result: %s [Image data truncated for token efficiency]\n", result))
				} else {
					conversationBuilder.WriteString(fmt.Sprintf("Tool result: %s\n", result))
				}
			}
		}
	}

	// Create summarization prompt
	summaryPrompt := fmt.Sprintf(`Please create a concise summary of the following conversation. Focus on:
1. Main topics discussed
2. Key findings or results
3. Important context that should be preserved
4. Any ongoing tasks or decisions

Keep the summary under 200 words and preserve essential context for continuing the conversation.

%s

Summary:`, conversationBuilder.String())

	// Use LLM to create summary
	summaryMessage := message.NewChatMessage(message.MessageTypeUser, summaryPrompt)
	response, err := llm.Chat(ctx, []message.Message{summaryMessage}, false, nil) // Summary doesn't need thinking
	if err != nil {
		return "", fmt.Errorf("failed to generate LLM summary: %w", err)
	}

	return response.Content(), nil
}

// createBasicMessageSummary creates a simple fallback summary of messages
func createBasicMessageSummary(messages []message.Message) string {
	if len(messages) == 0 {
		return "No previous conversation."
	}

	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("Summary of %d previous messages:\n\n", len(messages)))

	userQuestions := 0
	toolCalls := 0
	topics := make(map[string]int)
	hasVisionContent := false

	for _, msg := range messages {
		switch msg.Type() {
		case message.MessageTypeUser:
			userQuestions++
			content := strings.ToLower(msg.Content())
			if strings.Contains(content, "analyze") || strings.Contains(content, "analysis") {
				topics["code_analysis"]++
			}
			if strings.Contains(content, "function") || strings.Contains(content, "declaration") {
				topics["function_analysis"]++
			}
			if strings.Contains(content, "dependency") || strings.Contains(content, "import") {
				topics["dependency_analysis"]++
			}
		case message.MessageTypeToolCall:
			toolCalls++
		case message.MessageTypeToolResult:
			// Track if we had vision content (now truncated)
			if len(msg.Images()) > 0 {
				hasVisionContent = true
			}
		}
	}

	summary.WriteString(fmt.Sprintf("- User questions/requests: %d\n", userQuestions))
	summary.WriteString(fmt.Sprintf("- Tool calls executed: %d\n", toolCalls))

	if hasVisionContent {
		summary.WriteString("- Visual content: preserved most recent images for context\n")
	}

	if len(topics) > 0 {
		summary.WriteString("\nMain topics discussed:\n")
		for topic, count := range topics {
			summary.WriteString(fmt.Sprintf("- %s: %d occurrences\n",
				strings.ReplaceAll(topic, "_", " "), count))
		}
	}

	summary.WriteString("\n*This is a simplified summary. Full conversation history was compressed to manage context length.*")
	return summary.String()
}

// isSituationMessage checks if a message is a situation message and should be removed during compaction
func isSituationMessage(msg message.Message) bool {
	return msg.Source() == message.MessageSourceSituation
}

// findConversationBlocksToPreserve finds the last N complete conversation blocks to preserve
// A conversation block consists of: User prompt -> [Tool calls + Tool results]* -> Assistant response
func findConversationBlocksToPreserve(messages []message.Message, blocksToKeep int) []message.Message {
	if len(messages) == 0 || blocksToKeep <= 0 {
		return []message.Message{}
	}

	// Find conversation blocks by working backwards
	blocks := make([][]message.Message, 0)
	currentBlock := make([]message.Message, 0)

	// Work backwards to identify complete blocks
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		currentBlock = append([]message.Message{msg}, currentBlock...) // Prepend to maintain order

		// A block starts with a user message
		if msg.Type() == message.MessageTypeUser {
			// We found the start of a block
			blocks = append([][]message.Message{currentBlock}, blocks...) // Prepend to maintain order
			currentBlock = make([]message.Message, 0)

			// Stop if we have enough blocks
			if len(blocks) >= blocksToKeep {
				break
			}
		}
	}

	// Flatten the blocks we want to keep
	var result []message.Message
	for _, block := range blocks {
		result = append(result, block...)
	}

	// Also preserve any incomplete block at the beginning (might be ongoing)
	if len(currentBlock) > 0 && len(blocks) < blocksToKeep {
		result = append(currentBlock, result...)
	}

	return result
}
