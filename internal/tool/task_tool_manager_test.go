package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/fpt/klein-cli/pkg/message"
)

func newTestTaskManager() *TaskToolManager {
	return NewInMemoryTaskToolManager()
}

func callTask(t *testing.T, m *TaskToolManager, tool message.ToolName, args message.ToolArgumentValues) string {
	t.Helper()
	res, err := m.CallTool(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("CallTool(%s) unexpected error: %v", tool, err)
	}
	if res.Error != "" {
		return res.Error
	}
	return res.Text
}

func TestTaskCreate_Basic(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject": "Implement login",
	})
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS, got: %s", out)
	}
	if !strings.Contains(out, "Implement login") {
		t.Errorf("subject missing from output: %s", out)
	}
}

func TestTaskCreate_MissingSubject(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{})
	if !strings.Contains(out, "subject is required") {
		t.Errorf("expected validation error, got: %s", out)
	}
}

func TestTaskCreate_WithDescription(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject":     "Add tests",
		"description": "Write unit tests for the auth module",
	})
	if !strings.Contains(out, "PASS") {
		t.Errorf("expected PASS, got: %s", out)
	}
	// Verify stored
	listOut := callTask(t, m, "TaskList", message.ToolArgumentValues{})
	if !strings.Contains(listOut, "Add tests") {
		t.Errorf("task not listed: %s", listOut)
	}
}

func TestTaskCreate_BlockedBy(t *testing.T) {
	m := newTestTaskManager()

	// Create blocker
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Blocker"})
	// Extract ID from "PASS Created task #<id>: Blocker"
	parts := strings.Split(out, "#")
	if len(parts) < 2 {
		t.Fatalf("could not parse id from: %s", out)
	}
	blockerID := strings.SplitN(parts[1], ":", 2)[0]

	// Create task blocked by it
	out2 := callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject":    "Dependent",
		"blocked_by": []interface{}{blockerID},
	})
	if !strings.Contains(out2, "PASS") {
		t.Errorf("expected PASS, got: %s", out2)
	}

	// Blocker should have Blocks populated via reverse link
	listOut := callTask(t, m, "TaskList", message.ToolArgumentValues{})
	if !strings.Contains(listOut, "blocked by") {
		t.Errorf("blocked_by not reflected in list: %s", listOut)
	}
}

func TestTaskCreate_BlockedBy_InvalidID(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject":    "Task",
		"blocked_by": []interface{}{"nonexistent"},
	})
	if !strings.Contains(out, "not found") {
		t.Errorf("expected not found error, got: %s", out)
	}
}

func TestTaskUpdate_Status(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Task A"})
	id := extractID(t, out)

	upd := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{
		"id":     id,
		"status": "in_progress",
	})
	if !strings.Contains(upd, "PASS") {
		t.Errorf("expected PASS, got: %s", upd)
	}

	get := callTask(t, m, "TaskGet", message.ToolArgumentValues{"id": id})
	if !strings.Contains(get, "in_progress") {
		t.Errorf("status not updated: %s", get)
	}
}

func TestTaskUpdate_InvalidStatus(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Task"})
	id := extractID(t, out)

	upd := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{
		"id":     id,
		"status": "invalid",
	})
	if !strings.Contains(upd, "invalid status") {
		t.Errorf("expected validation error, got: %s", upd)
	}
}

func TestTaskUpdate_InvalidTransition_CompletedIsTerminal(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Task"})
	id := extractID(t, out)

	// pending → in_progress → completed
	callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": id, "status": "in_progress"})
	callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": id, "status": "completed"})

	// completed → pending must fail
	upd := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": id, "status": "pending"})
	if !strings.Contains(upd, "invalid transition") {
		t.Errorf("expected transition error, got: %s", upd)
	}
}

func TestTaskUpdate_InvalidTransition_PendingToCompleted(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Task"})
	id := extractID(t, out)

	// pending → completed must fail (must go through in_progress)
	upd := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": id, "status": "completed"})
	if !strings.Contains(upd, "invalid transition") {
		t.Errorf("expected transition error, got: %s", upd)
	}
}

func TestTaskUpdate_BlockedByNotCompleted(t *testing.T) {
	m := newTestTaskManager()
	outA := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Blocker"})
	idA := extractID(t, outA)
	outB := callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject":    "Dependent",
		"blocked_by": []interface{}{idA},
	})
	idB := extractID(t, outB)

	// Try to start B while A is still pending — must fail
	upd := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": idB, "status": "in_progress"})
	if !strings.Contains(upd, "not completed") {
		t.Errorf("expected blocker error, got: %s", upd)
	}

	// Complete A, then B can start
	callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": idA, "status": "in_progress"})
	callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": idA, "status": "completed"})
	upd2 := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": idB, "status": "in_progress"})
	if !strings.Contains(upd2, "PASS") {
		t.Errorf("expected PASS after blocker completed, got: %s", upd2)
	}
}

func TestTaskUpdate_AddBlockedBy(t *testing.T) {
	m := newTestTaskManager()
	outA := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "A"})
	idA := extractID(t, outA)
	outB := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "B"})
	idB := extractID(t, outB)

	callTask(t, m, "TaskUpdate", message.ToolArgumentValues{
		"id":            idB,
		"add_blocked_by": []interface{}{idA},
	})

	get := callTask(t, m, "TaskGet", message.ToolArgumentValues{"id": idB})
	if !strings.Contains(get, idA) {
		t.Errorf("blocked_by not set: %s", get)
	}
}

func TestTaskUpdate_NotFound(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskUpdate", message.ToolArgumentValues{
		"id":     "deadbeef",
		"status": "completed",
	})
	if !strings.Contains(out, "not found") {
		t.Errorf("expected not found, got: %s", out)
	}
}

func TestTaskList_Empty(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskList", message.ToolArgumentValues{})
	if !strings.Contains(out, "No tasks") {
		t.Errorf("expected empty list, got: %s", out)
	}
}

func TestTaskList_HidesDeleted(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "Visible"})
	id := extractID(t, out)
	callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "ToDelete"})
	outD := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "ToDelete2"})
	idD := extractID(t, outD)

	callTask(t, m, "TaskUpdate", message.ToolArgumentValues{"id": idD, "status": "deleted"})

	list := callTask(t, m, "TaskList", message.ToolArgumentValues{})
	if strings.Contains(list, "ToDelete2") {
		t.Errorf("deleted task should be hidden: %s", list)
	}
	if !strings.Contains(list, "Visible") {
		t.Errorf("visible task missing: %s", list)
	}
	_ = id
}

func TestTaskList_ShowsBlocksDeps(t *testing.T) {
	m := newTestTaskManager()
	outA := callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "A"})
	idA := extractID(t, outA)
	callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject":    "B",
		"blocked_by": []interface{}{idA},
	})

	list := callTask(t, m, "TaskList", message.ToolArgumentValues{})
	if !strings.Contains(list, "blocks:") {
		t.Errorf("expected 'blocks:' in list for A, got: %s", list)
	}
	if !strings.Contains(list, "blocked by:") {
		t.Errorf("expected 'blocked by:' in list for B, got: %s", list)
	}
}

func TestTaskGet_FullDetail(t *testing.T) {
	m := newTestTaskManager()
	callTask(t, m, "TaskCreate", message.ToolArgumentValues{
		"subject":     "Full detail",
		"description": "Some details here",
	})
	// List to get the ID
	list := callTask(t, m, "TaskList", message.ToolArgumentValues{})
	parts := strings.Split(list, "#")
	if len(parts) < 2 {
		t.Fatalf("could not parse list: %s", list)
	}
	id := strings.SplitN(parts[1], ":", 2)[0]

	get := callTask(t, m, "TaskGet", message.ToolArgumentValues{"id": id})
	if !strings.Contains(get, "Full detail") {
		t.Errorf("subject missing: %s", get)
	}
	if !strings.Contains(get, "Some details here") {
		t.Errorf("description missing: %s", get)
	}
}

func TestTaskGet_NotFound(t *testing.T) {
	m := newTestTaskManager()
	out := callTask(t, m, "TaskGet", message.ToolArgumentValues{"id": "missing"})
	if !strings.Contains(out, "not found") {
		t.Errorf("expected not found, got: %s", out)
	}
}

func TestGetToolState_Empty(t *testing.T) {
	m := newTestTaskManager()
	if s := m.GetToolState(); s != "" {
		t.Errorf("expected empty state for no tasks, got: %q", s)
	}
}

func TestGetToolState_Summary(t *testing.T) {
	m := newTestTaskManager()
	callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "A"})
	callTask(t, m, "TaskCreate", message.ToolArgumentValues{"subject": "B"})

	s := m.GetToolState()
	if !strings.Contains(s, "2 tasks") {
		t.Errorf("expected 2 tasks in state, got: %q", s)
	}
	if !strings.Contains(s, "pending") {
		t.Errorf("expected pending in state, got: %q", s)
	}
}

func TestTaskToolMetadata(t *testing.T) {
	m := newTestTaskManager()
	tools := m.GetTools()
	for _, name := range []message.ToolName{"TaskCreate", "TaskUpdate", "TaskList", "TaskGet"} {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("tool %q not registered", name)
			continue
		}
		if tool.RawName() != name {
			t.Errorf("RawName mismatch for %q", name)
		}
		if tool.Description() == "" {
			t.Errorf("empty description for %q", name)
		}
	}
}

// extractID parses "#<id>:" from a task_create PASS response.
func extractID(t *testing.T, out string) string {
	t.Helper()
	parts := strings.Split(out, "#")
	if len(parts) < 2 {
		t.Fatalf("could not parse id from: %s", out)
	}
	return strings.SplitN(parts[1], ":", 2)[0]
}
