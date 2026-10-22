package gemini

import (
	"bytes"
	"testing"

	"google.golang.org/genai"

	"github.com/fpt/klein-cli/pkg/message"
)

const toolRead = "Read"

func TestGetGeminiModel(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                 modelGemini4Argon,
		"argon":            modelGemini4Argon,
		"gemini-4-argon":   modelGemini4Argon,
		"gemini-9-unknown": "gemini-9-unknown", // passed through, never swapped
	}
	for in, want := range tests {
		if got := getGeminiModel(in); got != want {
			t.Errorf("getGeminiModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArgonCapabilities(t *testing.T) {
	t.Parallel()
	c := &GeminiClient{GeminiCore: &GeminiCore{model: modelGemini4Argon}}
	if !c.IsToolCapable() {
		t.Error("gemini-4-argon should be tool capable")
	}
	if got := getModelCapabilities(modelGemini4Argon).MaxTokens; got != 32768 {
		t.Errorf("argon MaxTokens = %d, want 32768", got)
	}
}

func TestToolCallsFromPartsKeepsSignature(t *testing.T) {
	t.Parallel()
	sig := []byte{0x12, 0x59, 0x0a}
	parts := []*genai.Part{
		{Text: "thinking", Thought: true},
		{FunctionCall: &genai.FunctionCall{Name: toolRead, Args: map[string]any{"path": "a.go"}}, ThoughtSignature: sig},
	}
	call, ok := toolCallsFromParts(parts).(*message.ToolCallMessage)
	if !ok {
		t.Fatalf("expected a single ToolCallMessage")
	}

	contents, _ := convertMessagesToGemini([]message.Message{
		message.NewChatMessage(message.MessageTypeUser, "read a.go"),
		call,
		message.NewToolResultMessage(call.ID(), "package a", ""),
	})
	if len(contents) != 3 {
		t.Fatalf("got %d contents, want 3", len(contents))
	}
	fcPart := contents[1].Parts[0]
	if fcPart.FunctionCall == nil || fcPart.FunctionCall.Name != toolRead {
		t.Fatalf("expected a native Read functionCall part, got %+v", fcPart)
	}
	if !bytes.Equal(fcPart.ThoughtSignature, sig) {
		t.Errorf("signature = %v, want %v", fcPart.ThoughtSignature, sig)
	}
	fr := contents[2].Parts[0].FunctionResponse
	if fr == nil || fr.Name != toolRead || fr.Response["output"] != "package a" {
		t.Errorf("unexpected functionResponse %+v", fr)
	}
}

func TestConvertMessagesGroupsBatchAndAddsPlaceholder(t *testing.T) {
	t.Parallel()
	// Calls without signatures, e.g. restored from an old session
	a := message.NewToolCallMessage("LS", message.ToolArgumentValues{})
	b := message.NewToolCallMessage("Glob", message.ToolArgumentValues{"pattern": "*.go"})
	contents, _ := convertMessagesToGemini([]message.Message{
		message.NewChatMessage(message.MessageTypeUser, "look around"),
		a, b,
		message.NewToolResultMessage(a.ID(), "x", ""),
		message.NewToolResultMessage(b.ID(), "", "boom"),
	})
	if len(contents) != 3 {
		t.Fatalf("got %d contents, want 3 (user, model calls, user results)", len(contents))
	}
	calls, results := contents[1], contents[2]
	if len(calls.Parts) != 2 || len(results.Parts) != 2 {
		t.Fatalf("batch not grouped: %d calls, %d results", len(calls.Parts), len(results.Parts))
	}
	if !bytes.Equal(calls.Parts[0].ThoughtSignature, skipThoughtSignature) {
		t.Error("unsigned turn should carry the placeholder signature on its first call")
	}
	if len(calls.Parts[1].ThoughtSignature) != 0 {
		t.Error("only the first call of a turn gets a signature")
	}
	if results.Parts[1].FunctionResponse.Response["error"] != "boom" {
		t.Errorf("tool error not forwarded: %+v", results.Parts[1].FunctionResponse.Response)
	}
}

func TestThinkingConfig(t *testing.T) {
	t.Parallel()

	cases := map[string]genai.ThinkingLevel{
		"":        "",
		"none":    genai.ThinkingLevelLow,
		"minimal": genai.ThinkingLevelLow,
		"medium":  genai.ThinkingLevelMedium,
		"max":     genai.ThinkingLevelHigh,
	}
	for effort, want := range cases {
		cfg := (&GeminiCore{effort: effort}).thinkingConfig(true)
		if cfg == nil || !cfg.IncludeThoughts || cfg.ThinkingLevel != want {
			t.Errorf("effort %q: got %+v, want level %q with thoughts", effort, cfg, want)
		}
	}
	if cfg := (&GeminiCore{}).thinkingConfig(false); cfg != nil {
		t.Errorf("no effort and no thoughts should set nothing, got %+v", cfg)
	}
}
