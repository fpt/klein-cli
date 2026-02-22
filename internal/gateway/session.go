package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/fpt/klein-cli/internal/gen/agentv1"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"
)

// SessionKey uniquely identifies a conversation context.
type SessionKey struct {
	ChannelType string
	ChannelID   string
	PeerID      string
}

// Session holds per-peer state.
type Session struct {
	Key            SessionKey
	AgentSessionID string // Connect RPC session ID
	Skill          string
	LastActivity   time.Time
	mu             sync.Mutex
}

// SessionManager manages per-peer sessions.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[SessionKey]*Session
	client   agentv1connect.AgentServiceClient
	config   *GatewayConfig
}

// NewSessionManager creates a session manager.
func NewSessionManager(client agentv1connect.AgentServiceClient, cfg *GatewayConfig) *SessionManager {
	return &SessionManager{
		sessions: make(map[SessionKey]*Session),
		client:   client,
		config:   cfg,
	}
}

// GetOrCreateSession returns an existing session or creates a new one via Connect RPC.
func (sm *SessionManager) GetOrCreateSession(ctx context.Context, key SessionKey) (*Session, error) {
	sm.mu.RLock()
	if s, ok := sm.sessions[key]; ok {
		sm.mu.RUnlock()
		s.mu.Lock()
		s.LastActivity = time.Now()
		s.mu.Unlock()
		return s, nil
	}
	sm.mu.RUnlock()

	// Create new agent session via Connect RPC
	resp, err := sm.client.StartSession(ctx, connect.NewRequest(&agentv1.StartSessionRequest{
		Settings: &agentv1.Settings{
			Model:         sm.config.DefaultModel,
			WorkingDir:    sm.config.WorkingDir,
			MaxIterations: int32(sm.config.MaxIterations),
		},
		Interactive: true,
	}))
	if err != nil {
		return nil, fmt.Errorf("failed to start agent session: %w", err)
	}

	session := &Session{
		Key:            key,
		AgentSessionID: resp.Msg.SessionId,
		Skill:          sm.config.DefaultSkill,
		LastActivity:   time.Now(),
	}

	sm.mu.Lock()
	sm.sessions[key] = session
	sm.mu.Unlock()

	return session, nil
}

// ClearSession removes a session from the manager.
func (sm *SessionManager) ClearSession(ctx context.Context, key SessionKey) error {
	sm.mu.Lock()
	session, ok := sm.sessions[key]
	if ok {
		delete(sm.sessions, key)
	}
	sm.mu.Unlock()

	if ok {
		_, err := sm.client.ClearSession(ctx, connect.NewRequest(&agentv1.ClearSessionRequest{
			SessionId: session.AgentSessionID,
		}))
		return err
	}
	return nil
}
