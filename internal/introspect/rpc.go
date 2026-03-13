package introspect

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const rpcTimeout = 3 * time.Second

// wsMessage is a JSON-RPC v3 protocol message for OpenClaw gateway.
type wsMessage struct {
	Type    string      `json:"type"`
	ID      string      `json:"id,omitempty"`
	Event   string      `json:"event,omitempty"`
	Method  string      `json:"method,omitempty"`
	Params  interface{} `json:"params,omitempty"`
	OK      bool        `json:"ok,omitempty"`
	Error   string      `json:"error,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// FetchHealth connects via WebSocket RPC and calls the health method with probe=false.
func FetchHealth(ctx context.Context, host string, port int, token string) (*HealthDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, rpcTimeout)
	defer cancel()

	url := fmt.Sprintf("ws://%s:%d/", host, port)
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	// Step 1: Read connect.challenge
	var challenge wsMessage
	if err := wsjson.Read(ctx, conn, &challenge); err != nil {
		return nil, fmt.Errorf("reading challenge: %w", err)
	}
	if challenge.Type != "event" || challenge.Event != "connect.challenge" {
		return nil, fmt.Errorf("expected connect.challenge, got %s/%s", challenge.Type, challenge.Event)
	}

	// Step 2: Send connect request
	connectID := fmt.Sprintf("agent-%d", time.Now().UnixNano())
	if err := wsjson.Write(ctx, conn, wsMessage{
		Type:   "req",
		ID:     connectID,
		Method: "connect",
		Params: map[string]interface{}{
			"minProtocol": 3,
			"maxProtocol": 3,
			"client": map[string]string{
				"id":       "apex-agent",
				"version":  "1.0.0",
				"platform": "server",
				"mode":     "backend",
			},
			"role":   "operator",
			"scopes": []string{"operator.admin"},
			"auth":   map[string]string{"token": token},
		},
	}); err != nil {
		return nil, fmt.Errorf("sending connect: %w", err)
	}

	// Step 3: Read connect response
	var connectResp wsMessage
	if err := wsjson.Read(ctx, conn, &connectResp); err != nil {
		return nil, fmt.Errorf("reading connect response: %w", err)
	}
	if connectResp.ID != connectID || !connectResp.OK {
		errMsg := connectResp.Error
		if errMsg == "" {
			errMsg = "auth failed"
		}
		return nil, fmt.Errorf("connect rejected: %s", errMsg)
	}

	// Step 4: Send health RPC (probe: false = read cached state only, no network I/O)
	healthID := fmt.Sprintf("health-%d", time.Now().UnixNano())
	if err := wsjson.Write(ctx, conn, wsMessage{
		Type:   "req",
		ID:     healthID,
		Method: "health",
		Params: map[string]interface{}{"probe": false},
	}); err != nil {
		return nil, fmt.Errorf("sending health: %w", err)
	}

	// Step 5: Read health response (may need to skip intervening events)
	for i := 0; i < 10; i++ {
		var resp wsMessage
		if err := wsjson.Read(ctx, conn, &resp); err != nil {
			return nil, fmt.Errorf("reading health response: %w", err)
		}
		if resp.Type == "res" && resp.ID == healthID {
			if !resp.OK {
				return nil, fmt.Errorf("health RPC failed: %s", resp.Error)
			}
			detail := parseHealthPayload(resp.Payload)
			conn.Close(websocket.StatusNormalClosure, "")
			return detail, nil
		}
		// Skip events (e.g. presence updates) that may arrive between connect and response
	}

	return nil, fmt.Errorf("no matching health response after 10 messages")
}

func parseHealthPayload(raw json.RawMessage) *HealthDetail {
	if raw == nil {
		return &HealthDetail{OK: true, FetchedAt: time.Now().UnixMilli()}
	}

	// The health RPC returns a HealthSummary with nested structure.
	// We extract the fields we care about.
	var payload struct {
		OK       bool `json:"ok"`
		Sessions struct {
			Count int `json:"count"`
		} `json:"sessions"`
		Heartbeat struct {
			IntervalMs int `json:"intervalMs"`
		} `json:"heartbeat"`
		Channels map[string]struct {
			Configured bool  `json:"configured"`
			Linked     *bool `json:"linked"`
		} `json:"channels"`
		Snapshot struct {
			UptimeMs int64  `json:"uptimeMs"`
			AgentID  string `json:"agentId"`
		} `json:"snapshot"`
	}

	if err := json.Unmarshal(raw, &payload); err != nil {
		return &HealthDetail{OK: false, FetchedAt: time.Now().UnixMilli()}
	}

	detail := &HealthDetail{
		OK:             payload.OK,
		SessionCount:   payload.Sessions.Count,
		UptimeMs:       payload.Snapshot.UptimeMs,
		DefaultAgentID: payload.Snapshot.AgentID,
		FetchedAt:      time.Now().UnixMilli(),
	}

	if payload.Heartbeat.IntervalMs > 0 {
		detail.HeartbeatSecs = payload.Heartbeat.IntervalMs / 1000
	}

	if len(payload.Channels) > 0 {
		detail.Channels = make(map[string]ChannelStatus, len(payload.Channels))
		for name, ch := range payload.Channels {
			detail.Channels[name] = ChannelStatus{
				Configured: ch.Configured,
				Linked:     ch.Linked,
			}
		}
	}

	return detail
}
