package domain

// Situation injects situational context messages during ReAct iterations.
// Examples: iteration limits, tool result guidance, validation success hints.
// Messages are ephemeral — removed before each new iteration.
type Situation interface {
	InjectMessage(state State, currentStep, maxStep int)
}
