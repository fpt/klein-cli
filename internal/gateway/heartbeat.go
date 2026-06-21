package gateway

// HeartbeatConfig is the legacy single-job scheduler configuration. New
// deployments should prefer the multi-job Schedules field on
// GatewayConfig — this struct is kept so existing config.json files keep
// working unchanged. At gateway startup it is converted to a single
// ScheduleConfig via HeartbeatToSchedule and folded into the Scheduler.
type HeartbeatConfig struct {
	Enabled     bool   `json:"enabled"`
	Interval    string `json:"interval"` // Go duration string, e.g. "24h", "1h"
	Prompt      string `json:"prompt"`
	Skill       string `json:"skill"`
	ChannelType string `json:"channel_type"`
	ChannelID   string `json:"channel_id"`
}
