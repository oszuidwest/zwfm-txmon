// Package notify sends alarm notifications by webhook, Microsoft 365 email and
// Zabbix, and pings a heartbeat URL. The channels follow zwfm-encoder.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// Event is one alarm going on or off.
type Event struct {
	Transmitter string    `json:"transmitter"`
	Alarm       string    `json:"alarm"`
	Label       string    `json:"label"`
	State       string    `json:"state"` // "low", "high" or "alarm"; "ok" when it clears
	Value       string    `json:"value,omitempty"`
	Time        time.Time `json:"timestamp"`
}

// Active reports whether the alarm went on.
func (e *Event) Active() bool { return e.State != "ok" }

// Message is a one-line description, used as mail subject and Zabbix value.
func (e *Event) Message() string {
	state := map[string]string{"low": "Alert Low", "high": "Alert High", "alarm": "Alert", "ok": "Idle"}[e.State]
	msg := fmt.Sprintf("%s: Alarm '%s' is %s!", e.Transmitter, e.Label, state)
	if e.Value != "" {
		msg += " (" + e.Value + ")"
	}
	return msg
}

// Events selects which alarms a channel sends.
type Events struct {
	Alarms     []string `json:"alarms"`
	Recoveries bool     `json:"recoveries"`
}

func (s *Events) wants(e *Event) bool {
	return slices.Contains(s.Alarms, e.Alarm) && (e.Active() || s.Recoveries)
}

// Config holds the notification settings.
type Config struct {
	Heartbeat struct {
		URL string `json:"url"`
	} `json:"heartbeat"`
	Webhook WebhookConfig `json:"webhook"`
	Email   EmailConfig   `json:"email"`
	Zabbix  ZabbixConfig  `json:"zabbix"`
}

// channel is one configured way to deliver an event.
type channel struct {
	name   string
	events Events
	send   func(context.Context, *Event) error
}

func (c *Config) channels() []channel {
	var chs []channel
	if c.Webhook.URL != "" {
		chs = append(chs, channel{"webhook", c.Webhook.Events, c.Webhook.send})
	}
	if c.Email.configured() {
		chs = append(chs, channel{"email", c.Email.Events, c.Email.send})
	}
	if c.Zabbix.configured() {
		chs = append(chs, channel{"zabbix", c.Zabbix.Events, c.Zabbix.send})
	}
	return chs
}

const sendTimeout = 2 * time.Minute

// Dispatch sends e to every channel that wants it, in the background.
func Dispatch(cfg *Config, e *Event) {
	for _, ch := range cfg.channels() {
		if !ch.events.wants(e) {
			continue
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
			defer cancel()
			if err := ch.send(ctx, e); err != nil {
				slog.Warn("notification failed", "channel", ch.name, "alarm", e.Alarm, "error", err)
			}
		}()
	}
}

// Test sends a test event through one channel and waits for the result.
func Test(ctx context.Context, cfg *Config, name, transmitter string) error {
	e := &Event{Transmitter: transmitter, Alarm: "test", Label: "Test notification", State: "alarm", Time: time.Now()}
	for _, ch := range cfg.channels() {
		if ch.name == name {
			return ch.send(ctx, e)
		}
	}
	return fmt.Errorf("%s is not configured", name)
}
