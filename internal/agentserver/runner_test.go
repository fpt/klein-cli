package agentserver

import (
	"context"
	"testing"
)

// A turn klein never learned the id of cannot be interrupted, and finding that
// out must not reach for the client: this runs on the error path, where the
// connection may be exactly what failed. The nil client makes the check
// load-bearing — if interruptTurn called through, this panics.
func TestInterruptTurn_WithoutTurnID_DoesNotCallTheBackend(t *testing.T) {
	t.Parallel()

	r := &Runner{}
	r.interruptTurn(context.Background(), "thread_1", "")
}
