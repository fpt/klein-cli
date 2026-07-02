package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fpt/klein-cli/pkg/agent/domain"
	"github.com/fpt/klein-cli/pkg/message"
)

// scheduleEntry mirrors internal/gateway.ScheduleConfig by its JSON tags. The
// JSON file is the contract between the agent (which writes it via these tools)
// and the gateway scheduler (which reads and reconciles it). Keep the tags in
// sync with gateway.ScheduleConfig.
type scheduleEntry struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval,omitempty"`
	At          string `json:"at,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	Prompt      string `json:"prompt"`
	Skill       string `json:"skill"`
	Silent      bool   `json:"silent"`
	ChannelType string `json:"channel_type"`
	ChannelID   string `json:"channel_id"`
}

// ScheduleToolManager provides ScheduleCreate/List/Delete tools that maintain
// the gateway's dynamic schedule store (a JSON array). The gateway polls this
// file and starts/stops jobs live.
type ScheduleToolManager struct {
	tools map[message.ToolName]message.Tool
	path  string // e.g. ~/.klein/claw/schedules.json
	mu    sync.Mutex
}

// NewScheduleToolManager creates a schedule tool manager backed by storePath.
func NewScheduleToolManager(storePath string) *ScheduleToolManager {
	m := &ScheduleToolManager{
		tools: make(map[message.ToolName]message.Tool),
		path:  storePath,
	}
	m.register()
	return m
}

func (m *ScheduleToolManager) register() {
	m.RegisterTool("ScheduleCreate",
		"Create or update a recurring scheduled message. Use for requests like 'notify me every morning at 8am'. "+
			"Provide either 'at' (daily HH:MM, 24h) with a 'timezone', or 'interval' (e.g. '6h'). The gateway will run "+
			"'prompt' under 'skill' at that time and post the reply to the given channel. Use the channel_type/channel_id "+
			"from the [SCHEDULING CONTEXT] block in the conversation.",
		[]message.ToolArgument{
			{Name: "name", Description: "Unique kebab-case id (e.g. 'morning-market'); reusing a name updates that schedule", Required: true, Type: "string"},
			{Name: "prompt", Description: "The message the scheduled run will act on (e.g. '今朝のマーケットイベントをまとめて')", Required: true, Type: "string"},
			{Name: "at", Description: "Daily time HH:MM (24h), e.g. '08:00'. Provide this OR interval.", Required: false, Type: "string"},
			{Name: "timezone", Description: "IANA timezone for 'at' (e.g. 'Asia/Tokyo'). Defaults to Asia/Tokyo.", Required: false, Type: "string"},
			{Name: "interval", Description: "Fixed period instead of a daily time, e.g. '6h', '30m' (min 5m).", Required: false, Type: "string"},
			{Name: "skill", Description: "Skill to run under (default 'claw')", Required: false, Type: "string"},
			{Name: "channel_type", Description: "Output channel type from [SCHEDULING CONTEXT], e.g. 'discord'", Required: false, Type: "string"},
			{Name: "channel_id", Description: "Output channel id from [SCHEDULING CONTEXT]", Required: false, Type: "string"},
		},
		m.handleCreate)

	m.RegisterTool("ScheduleList",
		"List the currently configured recurring schedules.",
		nil,
		m.handleList)

	m.RegisterTool("ScheduleDelete",
		"Delete a recurring schedule by its name.",
		[]message.ToolArgument{
			{Name: "name", Description: "The schedule name to delete", Required: true, Type: "string"},
		},
		m.handleDelete)
}

func (m *ScheduleToolManager) handleCreate(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	prompt := strings.TrimSpace(stringArg(args, "prompt"))
	if name == "" || prompt == "" {
		return message.NewToolResultError("name and prompt are required"), nil
	}

	at := strings.TrimSpace(stringArg(args, "at"))
	interval := strings.TrimSpace(stringArg(args, "interval"))
	if at == "" && interval == "" {
		return message.NewToolResultError("provide either 'at' (HH:MM daily) or 'interval' (e.g. '6h')"), nil
	}
	if at != "" && interval != "" {
		return message.NewToolResultError("provide only one of 'at' or 'interval', not both"), nil
	}
	if at != "" {
		if err := validateHHMM(at); err != nil {
			return message.NewToolResultError(err.Error()), nil
		}
	}

	tz := strings.TrimSpace(stringArg(args, "timezone"))
	if at != "" && tz == "" {
		tz = "Asia/Tokyo"
	}
	skill := strings.TrimSpace(stringArg(args, "skill"))
	if skill == "" {
		skill = "claw"
	}
	channelType := strings.TrimSpace(stringArg(args, "channel_type"))
	channelID := strings.TrimSpace(stringArg(args, "channel_id"))
	if channelID == "" {
		return message.NewToolResultError("channel_id is required — use the channel_id from the [SCHEDULING CONTEXT] block"), nil
	}

	entry := scheduleEntry{
		Name: name, Enabled: true, At: at, Timezone: tz, Interval: interval,
		Prompt: prompt, Skill: skill, ChannelType: channelType, ChannelID: channelID,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.load()
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read schedules: %v", err)), nil
	}
	// Upsert by name.
	replaced := false
	for i := range entries {
		if entries[i].Name == name {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	if err := m.save(entries); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to save schedules: %v", err)), nil
	}

	when := "every " + interval
	if at != "" {
		when = fmt.Sprintf("daily at %s %s", at, tz)
	}
	verb := "Created"
	if replaced {
		verb = "Updated"
	}
	return message.NewToolResultText(fmt.Sprintf("%s schedule %q: %s → runs %q (skill %s). It becomes active within ~20s.",
		verb, name, when, truncate(prompt, 60), skill)), nil
}

func (m *ScheduleToolManager) handleList(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.load()
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read schedules: %v", err)), nil
	}
	if len(entries) == 0 {
		return message.NewToolResultText("No schedules configured."), nil
	}
	var b strings.Builder
	b.WriteString("Schedules:\n")
	for _, e := range entries {
		when := "every " + e.Interval
		if e.At != "" {
			when = fmt.Sprintf("daily %s %s", e.At, e.Timezone)
		}
		status := ""
		if !e.Enabled {
			status = " (disabled)"
		}
		fmt.Fprintf(&b, "- %s: %s%s → %q\n", e.Name, when, status, truncate(e.Prompt, 50))
	}
	return message.NewToolResultText(strings.TrimRight(b.String(), "\n")), nil
}

func (m *ScheduleToolManager) handleDelete(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return message.NewToolResultError("name is required"), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := m.load()
	if err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to read schedules: %v", err)), nil
	}
	out := entries[:0]
	removed := false
	for _, e := range entries {
		if e.Name == name {
			removed = true
			continue
		}
		out = append(out, e)
	}
	if !removed {
		return message.NewToolResultText(fmt.Sprintf("No schedule named %q.", name)), nil
	}
	if err := m.save(out); err != nil {
		return message.NewToolResultError(fmt.Sprintf("failed to save schedules: %v", err)), nil
	}
	return message.NewToolResultText(fmt.Sprintf("Deleted schedule %q. Stops within ~20s.", name)), nil
}

func (m *ScheduleToolManager) load() ([]scheduleEntry, error) {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var entries []scheduleEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("existing schedule store is not valid JSON: %w", err)
	}
	return entries, nil
}

func (m *ScheduleToolManager) save(entries []scheduleEntry) error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, append(data, '\n'), 0o644)
}

// validateHHMM checks a "HH:MM" 24-hour time string.
func validateHHMM(s string) error {
	var hh, mm int
	if n, err := fmt.Sscanf(s, "%d:%d", &hh, &mm); err != nil || n != 2 {
		return fmt.Errorf("bad time %q (want HH:MM, 24-hour)", s)
	}
	if hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return fmt.Errorf("time out of range: %q", s)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// --- domain.ToolManager ---

func (m *ScheduleToolManager) GetTool(name message.ToolName) (message.Tool, bool) {
	t, ok := m.tools[name]
	return t, ok
}

func (m *ScheduleToolManager) GetTools() map[message.ToolName]message.Tool { return m.tools }

func (m *ScheduleToolManager) CallTool(ctx context.Context, name message.ToolName, args message.ToolArgumentValues) (message.ToolResult, error) {
	t, ok := m.tools[name]
	if !ok {
		return message.NewToolResultError(fmt.Sprintf("tool '%s' not found", name)), nil
	}
	return t.Handler()(ctx, args)
}

func (m *ScheduleToolManager) RegisterTool(name message.ToolName, description message.ToolDescription, arguments []message.ToolArgument, handler func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)) {
	m.tools[name] = &scheduleTool{name: name, description: description, arguments: arguments, handler: handler}
}

type scheduleTool struct {
	name        message.ToolName
	description message.ToolDescription
	arguments   []message.ToolArgument
	handler     func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error)
}

func (t *scheduleTool) RawName() message.ToolName            { return t.name }
func (t *scheduleTool) Name() message.ToolName               { return t.name }
func (t *scheduleTool) Description() message.ToolDescription { return t.description }
func (t *scheduleTool) Arguments() []message.ToolArgument    { return t.arguments }
func (t *scheduleTool) Handler() func(ctx context.Context, args message.ToolArgumentValues) (message.ToolResult, error) {
	return t.handler
}

var _ domain.ToolManager = (*ScheduleToolManager)(nil)
