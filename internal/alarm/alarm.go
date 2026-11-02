// Package alarm watches TEX telemetry: ten alarms with trigger and release
// delays, a 15 s hold-off after the link comes up and a counter per alarm.
package alarm

import (
	"fmt"
	"math"
	"time"

	"github.com/oszuidwest/zwfm-txmon/internal/notify"
	"github.com/oszuidwest/zwfm-txmon/internal/tex"
)

// Def describes one alarm.
type Def struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Unit     string `json:"unit,omitempty"`
	Analog   bool   `json:"analog"` // low and/or high limit; the others are on or off
	HighOnly bool   `json:"high_only,omitempty"`
	event    bool   // on/off alarm that notifies only when it goes on (panel access)
	value    func(*tex.Telemetry) float64
	on       func(*tex.Telemetry) bool
}

// Defs are the alarms for a TEX.
var Defs = []Def{
	{Key: "forward_power", Label: "Forward Power", Unit: "W", Analog: true, value: func(t *tex.Telemetry) float64 { return t.ForwardW }},
	{Key: "reflected_power", Label: "Reflected Power", Unit: "W", HighOnly: true, Analog: true, value: func(t *tex.Telemetry) float64 { return t.ReflectedW }},
	{Key: "temperature", Label: "Temperature", Unit: "°C", Analog: true, value: func(t *tex.Telemetry) float64 { return float64(t.TemperatureC) }},
	{Key: "modulation", Label: "Modulation", Unit: "kHz", Analog: true, value: func(t *tex.Telemetry) float64 { return float64(t.ModulationKHz) }},
	{Key: "pll_lock_loss", Label: "PLL Lock Loss", on: func(t *tex.Telemetry) bool { return !t.PLLLocked }},
	{Key: "audio_loss", Label: "Audio Loss", on: func(t *tex.Telemetry) bool { return t.AudioLoss }},
	{Key: "swr", Label: "SWR Alarm", on: func(t *tex.Telemetry) bool { return t.SWRAlarm }},
	{Key: "foldback", Label: "FoldBack", on: func(t *tex.Telemetry) bool { return t.Foldback }},
	{Key: "panel_access", Label: "Panel Access", event: true, on: func(t *tex.Telemetry) bool { return t.PanelAccess }},
	{Key: LinkLoss, Label: "Link Loss"},
}

// LinkLoss is the alarm for losing contact with the TEX.
const LinkLoss = "link_loss"

// Setting configures one alarm. Analog alarms use Low and High; the others
// use Enabled. Trigger and release delays (3..60 s) only apply to analog
// alarms; the others use a fixed 3 s.
type Setting struct {
	Enabled   bool    `json:"enabled,omitempty"`
	Low       bool    `json:"low,omitempty"`
	High      bool    `json:"high,omitempty"`
	LowLimit  float64 `json:"low_limit,omitempty"`
	HighLimit float64 `json:"high_limit,omitempty"`
	Trigger   int     `json:"trigger,omitempty"`
	Release   int     `json:"release,omitempty"`
}

// Config holds the setting of each alarm by key.
type Config map[string]Setting

// Defaults: all alarms off, analog delays 10/10 s,
// temperature 20/70 °C and modulation 40/90 kHz. Power limits have no default
// because txmon does not know the rated power of the transmitter.
func Defaults() Config {
	return Config{
		"forward_power":   {Trigger: 10, Release: 10},
		"reflected_power": {Trigger: 10, Release: 10},
		"temperature":     {LowLimit: 20, HighLimit: 70, Trigger: 10, Release: 10},
		"modulation":      {LowLimit: 40, HighLimit: 90, Trigger: 10, Release: 10},
	}
}

// Validate checks the settings against the allowed ranges.
func (c Config) Validate() error {
	for _, d := range Defs {
		s := c[d.Key]
		if !d.Analog || !s.Low && !s.High {
			continue
		}
		switch {
		case d.HighOnly && s.Low:
			return fmt.Errorf("%s has no low limit", d.Label)
		case s.Trigger < 3 || s.Trigger > 60 || s.Release < 3 || s.Release > 60:
			return fmt.Errorf("%s: trigger and release delays must be 3..60 s", d.Label)
		case s.LowLimit < 0 || s.HighLimit < 0 || (s.High && s.HighLimit == 0):
			return fmt.Errorf("%s: set a positive limit", d.Label)
		case s.Low && s.High && s.LowLimit >= s.HighLimit:
			return fmt.Errorf("%s: the low limit must be below the high limit", d.Label)
		}
	}
	return nil
}

const (
	holdOff      = 15 * time.Second // after the link comes up
	digitalDelay = 3 * time.Second
	maxCount     = 999
)

// State of one alarm.
type State struct {
	Key   string `json:"key"`
	State string `json:"state"` // disabled, idle, low, high or alarm
	Count int    `json:"count"`
}

type monitor struct {
	state      string    // idle, low, high or alarm
	lowSince   time.Time // start of the condition that may change the state
	highSince  time.Time
	count      int
	lastConfig Setting
}

// Engine runs the alarm state machines. It is not safe for concurrent use.
type Engine struct {
	mons    map[string]*monitor
	running time.Time // when the link came up; zero while down
}

func NewEngine() *Engine {
	e := &Engine{mons: map[string]*monitor{}}
	for _, d := range Defs {
		e.mons[d.Key] = &monitor{state: "idle"}
	}
	return e
}

// Update feeds one poll result and returns the alarms that went on or off.
// t is nil when the poll failed.
func (e *Engine) Update(cfg Config, now time.Time, t *tex.Telemetry) []notify.Event {
	var events []notify.Event
	link := t != nil
	if link && e.running.IsZero() {
		e.running = now
	}
	if !link && !e.running.IsZero() {
		// Forget the other alarms without recovery messages.
		e.running = time.Time{}
		for key, m := range e.mons {
			if key != LinkLoss {
				m.idle()
			}
		}
	}
	for i := range Defs {
		d := &Defs[i]
		m, s := e.mons[d.Key], cfg[d.Key]
		if s != m.lastConfig { // a changed setting starts the alarm over
			m.lastConfig = s
			m.idle()
		}
		if d.Key == LinkLoss {
			if s.Enabled {
				events = append(events, m.digital(d, now, !link)...)
			}
			continue
		}
		if !link || now.Sub(e.running) < holdOff {
			continue
		}
		switch {
		case d.Analog && (s.Low || s.High):
			events = append(events, m.analog(d, s, now, d.value(t))...)
		case !d.Analog && s.Enabled:
			events = append(events, m.digital(d, now, d.on(t))...)
		}
	}
	return events
}

// elapsed reports whether a condition that started at since has lasted
// longer than d, starting the clock on its first sample.
func elapsed(since *time.Time, now time.Time, d time.Duration) bool {
	if since.IsZero() {
		*since = now
	}
	return now.Sub(*since) > d
}

func (m *monitor) analog(d *Def, s Setting, now time.Time, v float64) []notify.Event {
	trigger := time.Duration(s.Trigger) * time.Second
	release := time.Duration(s.Release) * time.Second
	value := func(limit float64) string {
		return fmt.Sprintf("%s %s, limit %s %s", num(v), d.Unit, num(limit), d.Unit)
	}
	switch m.state {
	case "idle":
		if !s.Low || v >= s.LowLimit {
			m.lowSince = time.Time{}
		} else if elapsed(&m.lowSince, now, trigger) {
			return m.fire(d, now, "low", value(s.LowLimit))
		}
		if !s.High || v <= s.HighLimit {
			m.highSince = time.Time{}
		} else if elapsed(&m.highSince, now, trigger) {
			return m.fire(d, now, "high", value(s.HighLimit))
		}
	case "low":
		if v < s.LowLimit {
			m.lowSince = time.Time{}
		} else if elapsed(&m.lowSince, now, release) {
			return m.clear(d, now, value(s.LowLimit))
		}
	case "high":
		if v > s.HighLimit {
			m.highSince = time.Time{}
		} else if elapsed(&m.highSince, now, release) {
			return m.clear(d, now, value(s.HighLimit))
		}
	}
	return nil
}

func (m *monitor) digital(d *Def, now time.Time, on bool) []notify.Event {
	switch {
	case m.state == "idle" && !on, m.state == "alarm" && on:
		m.lowSince = time.Time{}
	case !elapsed(&m.lowSince, now, digitalDelay):
	case m.state == "idle":
		return m.fire(d, now, "alarm", "")
	case d.event:
		m.idle() // re-arm silently
	default:
		return m.clear(d, now, "")
	}
	return nil
}

func (m *monitor) fire(d *Def, now time.Time, state, value string) []notify.Event {
	*m = monitor{state: state, count: min(m.count+1, maxCount), lastConfig: m.lastConfig}
	return []notify.Event{{Alarm: d.Key, Label: d.Label, State: state, Value: value, Time: now}}
}

// idle resets the monitor, keeping its count and settings.
func (m *monitor) idle() {
	*m = monitor{state: "idle", count: m.count, lastConfig: m.lastConfig}
}

func (m *monitor) clear(d *Def, now time.Time, value string) []notify.Event {
	m.idle()
	return []notify.Event{{Alarm: d.Key, Label: d.Label, State: "ok", Value: value, Time: now}}
}

// States returns the state of every alarm, in the order of Defs.
func (e *Engine) States(cfg Config) []State {
	out := make([]State, 0, len(Defs))
	for _, d := range Defs {
		s, m := cfg[d.Key], e.mons[d.Key]
		st := m.state
		if !s.Enabled && !s.Low && !s.High {
			st = "disabled"
		}
		out = append(out, State{Key: d.Key, State: st, Count: m.count})
	}
	return out
}

// Reset clears the counter of an alarm and lets it qualify again. It does
// not touch the TEX.
func (e *Engine) Reset(key string) bool {
	m, ok := e.mons[key]
	if ok {
		*m = monitor{state: "idle", lastConfig: m.lastConfig}
	}
	return ok
}

func num(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}
