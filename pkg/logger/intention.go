package logger

// Intention represents the semantic intent of a log line, orthogonal to level.
// It lets us keep emojis out of source while still emitting meaningful icons
// at the console and structured attributes in logs.
type Intention string

const (
	IntentionThinking   Intention = "thinking"
	IntentionReasoning  Intention = "reasoning"
	IntentionTool       Intention = "tool"
	IntentionStatistics Intention = "statistics"
	IntentionStatus     Intention = "status"
	IntentionOutput     Intention = "output"
	IntentionWarning    Intention = "warning" // no icon mapping; level handles emphasis
	IntentionError      Intention = "error"   // no icon mapping; level handles emphasis
	IntentionSuccess    Intention = "success"
	IntentionDebug      Intention = "debug"
	IntentionCancel     Intention = "cancel"
	IntentionConfig     Intention = "config"
)

// iconFor returns a short emoji string for console output for the intention.
func iconFor(i Intention) string {
	switch i {
	case IntentionThinking, IntentionReasoning:
		return "🧠"
	case IntentionTool:
		return "🔧"
	case IntentionStatistics:
		return "📊"
	case IntentionStatus:
		return "ℹ️"
	case IntentionOutput:
		return "↳"
	case IntentionSuccess:
		return "✅"
	case IntentionDebug:
		return "🛠️"
	case IntentionCancel:
		return "🛑"
	case IntentionConfig:
		return "⚙️"
	default:
		return "➤"
	}
}
