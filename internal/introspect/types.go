package introspect

// Liveness is the result of an HTTP HEAD /health check.
type Liveness struct {
	Up        bool `json:"up"`
	LatencyMs int  `json:"latency_ms"`
}

// ChannelStatus is a simplified view of a channel's health.
type ChannelStatus struct {
	Configured bool  `json:"configured"`
	Linked     *bool `json:"linked,omitempty"`
}

// HealthDetail is the result of a WebSocket RPC health call.
type HealthDetail struct {
	OK             bool                     `json:"ok"`
	SessionCount   int                      `json:"session_count"`
	HeartbeatSecs  int                      `json:"heartbeat_seconds"`
	UptimeMs       int64                    `json:"uptime_ms"`
	Channels       map[string]ChannelStatus `json:"channels,omitempty"`
	DefaultAgentID string                   `json:"default_agent_id,omitempty"`
	FetchedAt      int64                    `json:"fetched_at"`
}

// ContainerHealth combines liveness and deep health for one container.
type ContainerHealth struct {
	Liveness     *Liveness     `json:"liveness,omitempty"`
	HealthDetail *HealthDetail `json:"health_detail,omitempty"`
}
