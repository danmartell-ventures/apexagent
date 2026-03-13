package introspect

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

var httpClient = &http.Client{
	Timeout: 3 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// CheckLiveness performs an HTTP HEAD /health on the container's gateway.
func CheckLiveness(ctx context.Context, host string, port int) Liveness {
	start := time.Now()
	url := fmt.Sprintf("http://%s:%d/health", host, port)

	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return Liveness{Up: false, LatencyMs: int(time.Since(start).Milliseconds())}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return Liveness{Up: false, LatencyMs: int(time.Since(start).Milliseconds())}
	}
	resp.Body.Close()

	return Liveness{
		Up:        resp.StatusCode == 200,
		LatencyMs: int(time.Since(start).Milliseconds()),
	}
}
