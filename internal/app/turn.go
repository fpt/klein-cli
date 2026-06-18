package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/fpt/klein-cli/pkg/message"
)

// handleDrivingCommand dispatches the multi-turn driving slash commands
// (/goal and /loop), which need the execution context and active skill. It
// returns true if the input was one of these commands (whether or not it did
// useful work), so the REPL can skip the argument-less command dispatch.
func handleDrivingCommand(ctx context.Context, a *Agent, skillName, input string) bool {
	trimmed := strings.TrimSpace(input)
	name, args, _ := strings.Cut(strings.TrimPrefix(trimmed, "/"), " ")
	switch name {
	case "goal":
		runGoal(ctx, a, skillName, args)
		return true
	case "loop":
		runLoop(ctx, a, skillName, args)
		return true
	}
	return false
}

// executeTurn runs a single agent turn for userInput with Ctrl+C cancellation,
// printing the response (with model header) or an error to the agent's writer.
// It returns the agent's response message, whether the turn was cancelled by the
// user (Ctrl+C), and any non-cancellation error.
//
// This is the shared execution path for normal REPL turns as well as the
// auto-continuing /goal and repeating /loop drivers.
func executeTurn(ctx context.Context, a *Agent, userInput, skillName string) (message.Message, bool, error) {
	execCtx, cancel := context.WithCancel(ctx)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	go func() {
		select {
		case <-sigChan:
			fmt.Println() // move to a new line after ^C
			cancel()
		case <-execCtx.Done():
		}
	}()

	response, invokeErr := a.Invoke(execCtx, userInput, skillName)
	canceled := execCtx.Err() == context.Canceled

	signal.Stop(sigChan)
	close(sigChan)
	cancel()

	if invokeErr != nil {
		if canceled {
			return nil, true, nil
		}
		fmt.Printf("❌ Error: %v\n", invokeErr)
		return nil, false, invokeErr
	}

	w := a.OutWriter()
	WriteResponseHeader(w, a.GetLLMClient().ModelID(), true)
	fmt.Fprintln(w, response.Content())
	return response, canceled, nil
}

// interruptibleSleep blocks for d, returning true if it was interrupted early by
// Ctrl+C or context cancellation, false if the full duration elapsed.
func interruptibleSleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return false
	}
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)
	defer signal.Stop(sigChan)

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return false
	case <-sigChan:
		fmt.Println()
		return true
	case <-ctx.Done():
		return true
	}
}
