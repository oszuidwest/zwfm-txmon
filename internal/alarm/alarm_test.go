package alarm

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/oszuidwest/zwfm-txmon/internal/tex"
)

// run feeds one poll per second, from second 0, and returns the events as
// "second key state" lines. A nil reading is a failed poll.
func run(e *Engine, cfg Config, readings []*tex.Telemetry) []string {
	var got []string
	start := time.Unix(0, 0)
	for i, t := range readings {
		for _, ev := range e.Update(cfg, start.Add(time.Duration(i)*time.Second), t) {
			got = append(got, fmt.Sprintf("%d %s %s", i, ev.Alarm, ev.State))
		}
	}
	return got
}

func repeat(t *tex.Telemetry, n int) []*tex.Telemetry {
	out := make([]*tex.Telemetry, n)
	for i := range out {
		out[i] = t
	}
	return out
}

func TestAnalog(t *testing.T) {
	cfg := Defaults()
	cfg["temperature"] = Setting{Low: true, High: true, LowLimit: 20, HighLimit: 70, Trigger: 10, Release: 10}
	hot, cold := &tex.Telemetry{TemperatureC: 71}, &tex.Telemetry{TemperatureC: 19}
	atHigh, atLow := &tex.Telemetry{TemperatureC: 70}, &tex.Telemetry{TemperatureC: 20} // on a limit is fine

	// Hot from the start: the first 15 s after link-up are ignored, then the
	// alarm fires once more than 10 s have passed (the comparison is
	// strict). Values on a limit are fine, both when idle and to release.
	in := append(repeat(hot, 27), repeat(atHigh, 24)...)
	in = append(in, repeat(atLow, 12)...)
	in = append(in, repeat(cold, 12)...)
	in = append(in, repeat(atLow, 12)...)
	e := NewEngine()
	got := strings.Join(run(e, cfg, in), ", ")
	if want := "26 temperature high, 38 temperature ok, 74 temperature low, 86 temperature ok"; got != want {
		t.Errorf("events: %s\nwant:   %s", got, want)
	}
	if st := e.States(cfg)[2]; st.State != "idle" || st.Count != 2 {
		t.Errorf("state %+v, want idle with count 2", st)
	}
}

func TestDigital(t *testing.T) {
	cfg := Config{"panel_access": {Enabled: true}, "swr": {Enabled: true}, LinkLoss: {Enabled: true}}
	quiet := &tex.Telemetry{PLLLocked: true}
	busy := &tex.Telemetry{PLLLocked: true, PanelAccess: true, SWRAlarm: true}

	in := append(repeat(quiet, 15), repeat(busy, 5)...)
	in = append(in, repeat(quiet, 5)...)
	in = append(in, repeat(busy, 5)...)
	in = append(in, repeat(nil, 5)...)
	in = append(in, repeat(quiet, 25)...)
	got := strings.Join(run(NewEngine(), cfg, in), ", ")
	// Panel access has no recovery; SWR does. Link loss fires after 3 s down
	// and resets SWR without a recovery message, also after the hold-off.
	want := "19 swr alarm, 19 panel_access alarm, 24 swr ok, 29 swr alarm, 29 panel_access alarm, 34 link_loss alarm, 39 link_loss ok"
	if got != want {
		t.Errorf("events: %s\nwant:   %s", got, want)
	}
}

func TestValidate(t *testing.T) {
	for name, s := range map[string]Setting{
		"short delay":    {High: true, HighLimit: 70, Trigger: 2, Release: 10},
		"low above high": {Low: true, High: true, LowLimit: 70, HighLimit: 20, Trigger: 10, Release: 10},
		"no limit":       {High: true, Trigger: 10, Release: 10},
	} {
		if (Config{"temperature": s}).Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if (Config{"reflected_power": {Low: true, LowLimit: 1, Trigger: 10, Release: 10}}).Validate() == nil {
		t.Error("reflected power accepted a low limit")
	}
	if err := Defaults().Validate(); err != nil {
		t.Errorf("defaults: %v", err)
	}
}
