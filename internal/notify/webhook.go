package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// WebhookConfig posts each event as JSON to URL.
type WebhookConfig struct {
	URL    string `json:"url"`
	Events Events `json:"events"`
}

var httpClient = &http.Client{
	Timeout: 10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (c *WebhookConfig) send(ctx context.Context, e *Event) error {
	kind := "alarm"
	if !e.Active() {
		kind = "recovery"
	}
	body, err := json.Marshal(map[string]any{
		"event":       kind,
		"transmitter": e.Transmitter,
		"alarm":       e.Alarm,
		"label":       e.Label,
		"state":       e.State,
		"value":       e.Value,
		"message":     e.Message(),
		"timestamp":   e.Time.Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	// The URL is a secret, so errors leave it out: they reach the log and the browser.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("webhook: invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		if u, ok := errors.AsType[*url.Error](err); ok {
			err = u.Err
		}
		return fmt.Errorf("webhook: %w", err)
	}
	_ = resp.Body.Close()
	if !success(resp.StatusCode) {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// RunHeartbeat GETs the URL that pushURL returns every minute until ctx is
// done. An empty URL skips the ping.
func RunHeartbeat(ctx context.Context, pushURL func() string) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	failing := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		u := pushURL()
		if u == "" {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
		if err != nil {
			continue
		}
		resp, err := httpClient.Do(req)
		failed := err != nil
		if err == nil {
			failed = !success(resp.StatusCode)
			_ = resp.Body.Close()
		}
		if failed && !failing && ctx.Err() == nil {
			// Errors can contain the secret URL, so log none.
			slog.Warn("heartbeat ping failed")
		}
		failing = failed
	}
}

func success(code int) bool { return code >= 200 && code < 300 }
