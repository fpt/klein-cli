package connectrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sync"

	"connectrpc.com/connect"

	agentv1 "github.com/fpt/klein-cli/internal/gen/agentv1"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"

	"github.com/fpt/klein-cli/internal/app"
	"github.com/fpt/klein-cli/internal/config"
	"github.com/fpt/klein-cli/internal/infra"
	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/agent/events"
	client "github.com/fpt/klein-cli/pkg/client"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// AgentServer implements agentv1connect.AgentServiceHandler using Connect-gRPC.
type AgentServer struct {
	agentv1connect.UnimplementedAgentServiceHandler

	mu       sync.RWMutex
	sessions map[string]*sessionState
	nextID   int

	// Shared dependencies for creating agents
	settings        *config.Settings
	mcpToolManagers map[string]domain.ToolManager
	logger          *pkgLogger.Logger
	sessionsDir     string // Directory for per-session persistence files
}

type sessionState struct {
	agent *app.Agent
}

// NewAgentServer creates a Connect AgentService handler.
func NewAgentServer(settings *config.Settings, mcpToolManagers map[string]domain.ToolManager, logger *pkgLogger.Logger, sessionsDir string) *AgentServer {
	return &AgentServer{
		sessions:        make(map[string]*sessionState),
		settings:        settings,
		mcpToolManagers: mcpToolManagers,
		logger:          logger.WithComponent("connect-server"),
		sessionsDir:     sessionsDir,
	}
}

func (s *AgentServer) StartSession(ctx context.Context, req *connect.Request[agentv1.StartSessionRequest]) (*connect.Response[agentv1.StartSessionResponse], error) {
	msg := req.Msg

	// Merge request settings with server defaults
	settings := s.settings
	if msg.Settings != nil {
		if msg.Settings.Model != "" {
			settings.LLM.Model = msg.Settings.Model
		}
		if msg.Settings.WorkingDir != "" {
			// Respect the working dir from the request
		}
		if msg.Settings.MaxIterations > 0 {
			settings.Agent.MaxIterations = int(msg.Settings.MaxIterations)
		}
	}

	workingDir := "."
	if msg.Settings != nil && msg.Settings.WorkingDir != "" {
		workingDir = msg.Settings.WorkingDir
	}

	// Create LLM client for this session
	llmClient, err := client.NewLLMClient(settings.LLM)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create LLM client: %w", err))
	}

	// In Connect/gRPC mode: auto-approve all tool calls, each session gets isolated in-memory state.
	// Persistence is enabled per-session via X-Persistence-Key header.
	fsRepo := infra.NewOSFilesystemRepository()
	out := io.Discard
	agent := app.NewAgentWithOptions(llmClient, workingDir, s.mcpToolManagers, settings, s.logger, out, true, false, fsRepo)

	// Enable file-backed persistence if a persistence key is provided
	persistenceKey := req.Header().Get("X-Persistence-Key")
	if persistenceKey != "" && s.sessionsDir != "" {
		safeName := sanitizeFilename(persistenceKey)
		filePath := filepath.Join(s.sessionsDir, safeName+".json")
		if err := agent.EnablePersistence(filePath); err != nil {
			s.logger.Warn("Failed to enable persistence", "key", persistenceKey, "error", err)
		}
	}

	s.mu.Lock()
	s.nextID++
	sessionID := fmt.Sprintf("session-%d", s.nextID)
	s.sessions[sessionID] = &sessionState{agent: agent}
	s.mu.Unlock()

	s.logger.Info("Session started", "session_id", sessionID, "working_dir", workingDir, "persistence_key", persistenceKey)

	return connect.NewResponse(&agentv1.StartSessionResponse{
		SessionId: sessionID,
		Capabilities: &agentv1.Capabilities{
			ToolCalling: true,
			Thinking:    settings.LLM.Thinking,
		},
	}), nil
}

func (s *AgentServer) ClearSession(ctx context.Context, req *connect.Request[agentv1.ClearSessionRequest]) (*connect.Response[agentv1.ClearSessionResponse], error) {
	session, err := s.getSession(req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	session.agent.ClearHistory()
	s.logger.Info("Session cleared", "session_id", req.Msg.SessionId)
	return connect.NewResponse(&agentv1.ClearSessionResponse{}), nil
}

func (s *AgentServer) Invoke(ctx context.Context, req *connect.Request[agentv1.InvokeRequest], stream *connect.ServerStream[agentv1.InvokeEvent]) error {
	session, err := s.getSession(req.Msg.SessionId)
	if err != nil {
		return err
	}

	skillName := req.Msg.Scenario
	if skillName == "" {
		skillName = "code"
	}

	// Send STARTED status
	_ = stream.Send(&agentv1.InvokeEvent{
		Event: &agentv1.InvokeEvent_Status{
			Status: &agentv1.StatusEvent{State: agentv1.InvokeState_STARTED},
		},
	})

	// Set up event handler to translate agent events → Connect stream events
	session.agent.SetEventHandler(func(event events.AgentEvent) {
		protoEvent := translateEvent(event)
		if protoEvent != nil {
			_ = stream.Send(protoEvent)
		}
	})
	defer session.agent.SetEventHandler(nil)

	// Invoke the agent
	result, invokeErr := session.agent.Invoke(ctx, req.Msg.UserInput, skillName)

	// Send final message or error
	if invokeErr != nil {
		_ = stream.Send(&agentv1.InvokeEvent{
			Event: &agentv1.InvokeEvent_Error{Error: invokeErr.Error()},
		})
		return nil // Don't return error — we sent it via stream
	}

	if result != nil {
		final := &agentv1.FinalMessage{
			Text:     result.Content(),
			Thinking: result.Thinking(),
		}
		if usage := result.TotalTokens(); usage > 0 {
			final.Usage = &agentv1.TokenUsage{
				InputTokens:  int32(result.InputTokens()),
				OutputTokens: int32(result.OutputTokens()),
				TotalTokens:  int32(result.TotalTokens()),
			}
		}
		_ = stream.Send(&agentv1.InvokeEvent{
			Event: &agentv1.InvokeEvent_Final{Final: final},
		})
	}

	_ = stream.Send(&agentv1.InvokeEvent{
		Event: &agentv1.InvokeEvent_Status{
			Status: &agentv1.StatusEvent{State: agentv1.InvokeState_COMPLETED},
		},
	})

	return nil
}

func (s *AgentServer) GetConversationPreview(ctx context.Context, req *connect.Request[agentv1.GetConversationPreviewRequest]) (*connect.Response[agentv1.GetConversationPreviewResponse], error) {
	session, err := s.getSession(req.Msg.SessionId)
	if err != nil {
		return nil, err
	}
	maxMessages := int(req.Msg.MaxMessages)
	if maxMessages <= 0 {
		maxMessages = 10
	}
	preview := session.agent.GetConversationPreview(maxMessages)
	return connect.NewResponse(&agentv1.GetConversationPreviewResponse{Preview: preview}), nil
}

func (s *AgentServer) ListScenarios(ctx context.Context, req *connect.Request[agentv1.ListScenariosRequest]) (*connect.Response[agentv1.ListScenariosResponse], error) {
	return connect.NewResponse(&agentv1.ListScenariosResponse{
		Scenarios: []*agentv1.Scenario{
			{Name: "code", Description: "Comprehensive coding assistant"},
			{Name: "respond", Description: "Direct knowledge-based responses"},
			{Name: "claw", Description: "Personal AI assistant for messaging"},
		},
	}), nil
}

// SubmitClientEvent, GetTodos, WriteTodos, SetSettings use the unimplemented defaults for now.

func (s *AgentServer) getSession(sessionID string) (*sessionState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %q not found", sessionID))
	}
	return session, nil
}

var nonAlphanumericRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// sanitizeFilename converts a persistence key to a safe filename component.
func sanitizeFilename(key string) string {
	safe := nonAlphanumericRe.ReplaceAllString(key, "_")
	if len(safe) > 128 {
		safe = safe[:128]
	}
	return safe
}

// translateEvent converts an events.AgentEvent to a proto InvokeEvent.
func translateEvent(event events.AgentEvent) *agentv1.InvokeEvent {
	switch event.Type {
	case events.EventTypeThinkingChunk:
		if data, ok := event.Data.(events.ThinkingChunkData); ok && data.Content != "" {
			return &agentv1.InvokeEvent{
				Event: &agentv1.InvokeEvent_ThinkingDelta{
					ThinkingDelta: &agentv1.ThinkingDelta{Text: data.Content},
				},
			}
		}

	case events.EventTypeToolCallStart:
		if data, ok := event.Data.(events.ToolCallStartData); ok {
			argsJSON, _ := json.Marshal(data.Arguments)
			// Send status + tool call
			return &agentv1.InvokeEvent{
				Event: &agentv1.InvokeEvent_ToolCall{
					ToolCall: &agentv1.ToolCall{
						Id:            data.CallID,
						Name:          data.ToolName,
						ArgumentsJson: string(argsJSON),
					},
				},
			}
		}

	case events.EventTypeToolResult:
		if data, ok := event.Data.(events.ToolResultData); ok {
			errStr := ""
			if data.IsError {
				errStr = data.Content
			}
			return &agentv1.InvokeEvent{
				Event: &agentv1.InvokeEvent_ToolResult{
					ToolResult: &agentv1.ToolResult{
						Id:     data.CallID,
						Output: data.Content,
						Error:  errStr,
					},
				},
			}
		}

	case events.EventTypeError:
		if data, ok := event.Data.(events.ErrorData); ok {
			return &agentv1.InvokeEvent{
				Event: &agentv1.InvokeEvent_Error{Error: data.Error.Error()},
			}
		}
	}
	return nil
}
