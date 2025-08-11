package app

import (
	"strings"
	"testing"
	"time"

	"github.com/fpt/klein-cli/internal/infra"
)

func TestPromptBuilder_Empty(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	if got := pb.VisiblePrompt(); got != "" {
		t.Fatalf("VisiblePrompt: want empty, got %q", got)
	}
	if got := pb.RawPrompt(); got != "" {
		t.Fatalf("RawPrompt: want empty, got %q", got)
	}
}

func TestPromptBuilder_InputASCII(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	for _, r := range []rune("Hello, world!") {
		pb.Input(r)
		time.Sleep(pasteInterval + time.Millisecond) // simulate human typing
	}
	want := "Hello, world!"
	if got := pb.VisiblePrompt(); got != want {
		t.Fatalf("VisiblePrompt: want %q, got %q", want, got)
	}
	if got := pb.RawPrompt(); got != want {
		t.Fatalf("RawPrompt: want %q, got %q", want, got)
	}
}

func TestPromptBuilder_InputUnicode(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	input := "こんにちは 世界🌟"
	for _, r := range []rune(input) {
		pb.Input(r)
		time.Sleep(pasteInterval + time.Millisecond)
	}
	if got := pb.VisiblePrompt(); got != input {
		t.Fatalf("VisiblePrompt: want %q, got %q", input, got)
	}
	if got := pb.RawPrompt(); got != input {
		t.Fatalf("RawPrompt: want %q, got %q", input, got)
	}
}

func TestPromptBuilder_AppendAfterRead(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	for _, r := range []rune("ABC") {
		pb.Input(r)
		time.Sleep(pasteInterval + time.Millisecond)
	}
	if pb.VisiblePrompt() != "ABC" || pb.RawPrompt() != "ABC" {
		t.Fatalf("initial content mismatch")
	}
	for _, r := range []rune("123") {
		pb.Input(r)
		time.Sleep(pasteInterval + time.Millisecond)
	}
	want := "ABC123"
	if pb.VisiblePrompt() != want || pb.RawPrompt() != want {
		t.Fatalf("after append: want %q, got Visible=%q Raw=%q", want, pb.VisiblePrompt(), pb.RawPrompt())
	}
}

func TestPromptBuilder_PasteDetectionVisibleDiff(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	// First a slow rune
	pb.Input('H')
	time.Sleep(pasteInterval + time.Millisecond)
	// Then a fast burst simulating paste (short: below threshold)
	for _, r := range []rune("ello, world!") {
		pb.Input(r)
		// no sleep to emulate paste
	}
	raw := pb.RawPrompt()
	vis := pb.VisiblePrompt()
	if raw != "Hello, world!" {
		t.Fatalf("raw mismatch: %q", raw)
	}
	// Below threshold, visible should match raw (no compression)
	if vis != raw {
		t.Fatalf("expected no compression for short paste, vis=%q raw=%q", vis, raw)
	}
}

func TestPromptBuilder_BackspaceDeletesWholePasteBlock(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	// Enable paste handling for this test scenario
	pb.SetUsePaste(true)
	// Slow first char
	pb.Input('H')
	// Simulate a clear pause before paste so first char is not part of paste
	time.Sleep(pasteInterval*5 + time.Millisecond)
	// Fast paste burst (long: above threshold)
	long := strings.Repeat("A", minPasteBlockLen)
	for _, r := range []rune(long) {
		pb.Input(r)
	}
	if pb.RawPrompt() != "H"+long {
		t.Fatalf("precondition raw mismatch: %q", pb.RawPrompt())
	}
	if !strings.Contains(pb.VisiblePrompt(), "[pasted: '") {
		t.Fatalf("expected placeholder before deletion: %q", pb.VisiblePrompt())
	}
	// Backspace at end should delete entire paste block
	pb.Backspace()
	if got := pb.RawPrompt(); got != "H" {
		t.Fatalf("expected raw to be 'H' after block deletion, got %q", got)
	}
	if got := pb.VisiblePrompt(); got != "H" {
		t.Fatalf("expected visible to be 'H' after block deletion, got %q", got)
	}
}

func TestPromptBuilder_VisiblePromptSanitizesNewlines(t *testing.T) {
	fsRepo := infra.NewOSFilesystemRepository()
	pb := NewPromptBuilder(fsRepo, "")
	pb.Input('A')
	time.Sleep(pasteInterval + time.Millisecond)
	pb.Input('\n')
	time.Sleep(pasteInterval + time.Millisecond)
	pb.Input('B')
	if got := pb.RawPrompt(); got != "A\nB" {
		t.Fatalf("raw mismatch: %q", got)
	}
	if got := pb.VisiblePrompt(); got != "A B" {
		t.Fatalf("visible newline sanitization failed: got %q", got)
	}
}
