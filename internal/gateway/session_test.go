package gateway

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/fpt/klein-cli/internal/gen/agentv1"
	"github.com/fpt/klein-cli/internal/gen/agentv1/agentv1connect"
	pkgLogger "github.com/fpt/klein-cli/pkg/logger"
)

// fakeAgentClient captures the StartSession request. It embeds the interface
// (nil) so only StartSession needs an implementation for this test.
type fakeAgentClient struct {
	agentv1connect.AgentServiceClient
	lastStart *agentv1.StartSessionRequest
}

func (f *fakeAgentClient) StartSession(ctx context.Context, req *connect.Request[agentv1.StartSessionRequest]) (*connect.Response[agentv1.StartSessionResponse], error) {
	f.lastStart = req.Msg
	return connect.NewResponse(&agentv1.StartSessionResponse{SessionId: "s1"}), nil
}

// TestGatewayDoesNotDictateModelOrMaxIterations verifies the gateway leaves the
// model and max_iterations unset in StartSession, so the agent's own
// settings.json governs them (no gateway-side clobbering).
func TestGatewayDoesNotDictateModelOrMaxIterations(t *testing.T) {
	fake := &fakeAgentClient{}
	cfg := &GatewayConfig{WorkingDir: "/tmp/work", SessionTimeout: "30m"}
	sm := NewSessionManager(fake, cfg, pkgLogger.NewComponentLogger("test"))

	_, err := sm.GetOrCreateSession(context.Background(), SessionKey{
		ChannelType: "discord", ChannelID: "c1", PeerID: "p1",
	})
	if err != nil {
		t.Fatalf("GetOrCreateSession: %v", err)
	}
	if fake.lastStart == nil || fake.lastStart.Settings == nil {
		t.Fatal("StartSession was not called with settings")
	}
	s := fake.lastStart.Settings
	if s.Model != "" {
		t.Errorf("gateway sent Model=%q, want empty (agent settings.json owns it)", s.Model)
	}
	if s.MaxIterations != 0 {
		t.Errorf("gateway sent MaxIterations=%d, want 0 (agent settings.json owns it)", s.MaxIterations)
	}
	if s.WorkingDir != "/tmp/work" {
		t.Errorf("gateway WorkingDir=%q, want /tmp/work", s.WorkingDir)
	}
}
