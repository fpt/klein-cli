package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/fpt/klein-cli/internal/gen/agentv1"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// SessionKey uniquely identifies a conversation context.
type SessionKey struct {
	ChannelType string
	ChannelID   string
	PeerID      string
}

// PersistenceKeyString returns a string suitable for deriving a persistence file name.
func (k SessionKey) PersistenceKeyString() string {
	return fmt.Sprintf("%s_%s_%s", k.ChannelType, k.ChannelID, k.PeerID)
}

// Session holds per-peer state.
type Session struct {
	Key            SessionKey
	AgentSessionID string // Connect RPC session ID
	Skill          string
	LastActivity   time.Time
	mu             sync.Mutex
}

// SessionManager manages per-peer sessions with inactivity timeout.
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[SessionKey]*Session
	client   agentv1connect.AgentServiceClient
	config   *GatewayConfig
	timeout  time.Duration
	logger   *pkgLogger.Logger
}

// NewSessionManager creates a session manager.
func NewSessionManager(client agentv1connect.AgentServiceClient, cfg *GatewayConfig, logger *pkgLogger.Logger) *SessionManager {
	timeout, err := time.ParseDuration(cfg.SessionTimeout)
	if err != nil || timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &SessionManager{
		sessions: make(map[SessionKey]*Session),
		client:   client,
		config:   cfg,
		timeout:  timeout,
		logger:   logger.WithComponent("sessions"),
	}
}

// GetOrCreateSession returns an existing session or creates a new one via Connect RPC.
// If the existing session has been inactive beyond the configured timeout, it is
// cleared and a fresh session is created.
func (sm *SessionManager) GetOrCreateSession(ctx context.Context, key SessionKey) (*Session, error) {
	sm.mu.RLock()
	existing, ok := sm.sessions[key]
	sm.mu.RUnlock()

	if ok {
		existing.mu.Lock()
		inactive := time.Since(existing.LastActivity)
		existing.mu.Unlock()

		if inactive > sm.timeout {
			sm.logger.Info("Session expired, dropping from memory (file persisted)",
				"channel", key.ChannelID, "peer", key.PeerID, "inactive", inactive)
			// Drop from local map only — do NOT call ClearSession so the
			// persistence file is preserved. The next StartSession will
			// reload history from the file.
			sm.mu.Lock()
			delete(sm.sessions, key)
			sm.mu.Unlock()
		} else {
			existing.mu.Lock()
			existing.LastActivity = time.Now()
			existing.mu.Unlock()
			return existing, nil
		}
	}

	// Create new agent session via Connect RPC
	req := connect.NewRequest(&agentv1.StartSessionRequest{
		Settings: &agentv1.Settings{
			Model:         sm.config.DefaultModel,
			WorkingDir:    sm.config.WorkingDir,
			MaxIterations: int32(sm.config.MaxIterations),
		},
		Interactive: true,
	})
	req.Header().Set("X-Persistence-Key", key.PersistenceKeyString())
	resp, err := sm.client.StartSession(ctx, req)
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
