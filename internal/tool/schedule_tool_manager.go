package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

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
	Cron        string `json:"cron"`
	Timezone    string `json:"timezone"`
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
	path  string // e.g. ~/.klein/schedules.json
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
		"Create or update a recurring scheduled task. Use for requests like 'notify me every weekday morning at 8am'. "+
			"Timing is a standard 5-field cron expression evaluated in 'timezone': \"0 8 * * 1-5\" = weekdays 08:00, "+
			"\"0 8 * * *\" = every day 08:00, \"0 */6 * * *\" = every 6 hours. "+
			"At fire time the gateway runs 'prompt' under 'skill' headlessly and posts the response to the given channel — "+
			"so 'prompt' MUST be the work itself, executable standalone (\"今朝の日本・米国市場の主要イベントをまとめて\"), "+
			"NEVER the scheduling request (\"毎朝8時に…送ってください\" is wrong: timing belongs in cron, not the prompt). "+
			"Use the channel_type/channel_id from the [SCHEDULING CONTEXT] block in the conversation.",
		[]message.ToolArgument{
			{Name: "name", Description: "Unique kebab-case id (e.g. 'morning-market'); reusing a name updates that schedule", Required: true, Type: "string"},
			{Name: "prompt", Description: "The task to EXECUTE at each fire, written as a standalone imperative for that moment (e.g. '今朝のマーケットイベントを簡潔にまとめて'). Must NOT mention the schedule or recurrence itself.", Required: true, Type: "string"},
			{Name: "cron", Description: "Standard 5-field cron expression (minute hour day-of-month month day-of-week), e.g. '0 8 * * 1-5' (weekdays 08:00), '0 22 * * *' (daily 22:00), '0 */6 * * *' (every 6h).", Required: true, Type: "string"},
			{Name: "timezone", Description: "IANA timezone the cron expression is evaluated in (e.g. 'Asia/Tokyo'). Defaults to Asia/Tokyo.", Required: false, Type: "string"},
			{Name: "skill", Description: "Skill the scheduled run executes under. Default 'report' (headless deliverable generator) — prefer it for briefings/summaries; only use a conversational skill if the task truly needs it.", Required: false, Type: "string"},
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

	cronExpr := strings.TrimSpace(stringArg(args, "cron"))
	if cronExpr == "" {
		return message.NewToolResultError("cron is required — a 5-field expression, e.g. '0 8 * * 1-5' (weekdays 08:00), '0 22 * * *' (daily 22:00), '0 */6 * * *' (every 6h)"), nil
	}
	if _, err := cron.ParseStandard(cronExpr); err != nil {
		return message.NewToolResultError(fmt.Sprintf("invalid cron expression %q: %v (5 fields: minute hour day-of-month month day-of-week, e.g. '0 8 * * 1-5')", cronExpr, err)), nil
	}

	// Guard against meta prompts: a prompt that phrases the schedule itself
	// ("毎朝8時に…", "every morning at 8 …") makes the fire-time agent try to
	// register a schedule instead of doing the work.
	if reason := promptLooksMeta(prompt); reason != "" {
		return message.NewToolResultError(fmt.Sprintf(
			"prompt looks like a scheduling request (%s). Write the prompt as the task to execute at fire time — e.g. '今朝の日本・米国市場の主要イベントを簡潔にまとめて' — and put the timing in 'cron' instead.", reason)), nil
	}

	tz := strings.TrimSpace(stringArg(args, "timezone"))
	if tz == "" {
		tz = "Asia/Tokyo"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return message.NewToolResultError(fmt.Sprintf("invalid timezone %q: %v (use an IANA name like 'Asia/Tokyo')", tz, err)), nil
	}
	skill := strings.TrimSpace(stringArg(args, "skill"))
	if skill == "" {
		// Default to the headless report skill: scheduled runs must produce a
		// deliverable, not converse.
		skill = "report"
	}
	channelType := strings.TrimSpace(stringArg(args, "channel_type"))
	channelID := strings.TrimSpace(stringArg(args, "channel_id"))
	if channelID == "" {
		return message.NewToolResultError("channel_id is required — use the channel_id from the [SCHEDULING CONTEXT] block"), nil
	}

	entry := scheduleEntry{
		Name: name, Enabled: true, Cron: cronExpr, Timezone: tz,
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

	when := fmt.Sprintf("cron %q %s", cronExpr, tz)
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
		when := fmt.Sprintf("cron %q %s", e.Cron, e.Timezone)
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
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: the gateway scheduler (a separate process) polls this file,
	// so a truncating write could be read mid-flight. temp-file + rename means it
	// only ever sees the old or the new complete file.
	return atomicWriteFile(m.path, append(data, '\n'), 0o644)
}

// metaPromptMarkers are phrases that indicate a schedule prompt describes the
// schedule itself rather than the task to execute. Kept deliberately narrow —
// only recurrence phrasing, in Japanese and English — to avoid rejecting
// legitimate task text.
var metaPromptMarkers = []string{
	// Japanese recurrence phrasing
	"毎朝", "毎晩", "毎日", "毎週", "毎時",
	// English recurrence phrasing
	"every morning", "every evening", "every day", "every week", "each morning",
	"daily at", "schedule a", "set up a schedule", "register a schedule",
}

// promptLooksMeta returns a short reason when the prompt appears to describe
// the schedule (recurrence phrasing) instead of the work, or "" when it looks
// fine. Fire-time agents receiving a meta prompt try to (re)register schedules
// instead of producing the deliverable.
func promptLooksMeta(prompt string) string {
	lower := strings.ToLower(prompt)
	for _, m := range metaPromptMarkers {
		if strings.Contains(lower, m) {
			return fmt.Sprintf("contains %q", m)
		}
	}
	return ""
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
