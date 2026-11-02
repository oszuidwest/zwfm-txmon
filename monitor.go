package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/oszuidwest/zwfm-txmon/internal/alarm"
	"github.com/oszuidwest/zwfm-txmon/internal/notify"
	"github.com/oszuidwest/zwfm-txmon/internal/tex"
)

// Poll every second, wait 3 s after an error.
const (
	pollEvery = time.Second
	retryWait = 3 * time.Second
)

// Status is the last known state of the transmitter.
type Status struct {
	Address   uint16         `json:"address"`
	Link      bool           `json:"link"`
	Error     string         `json:"error,omitempty"`
	Updated   time.Time      `json:"updated"`
	Telemetry *tex.Telemetry `json:"telemetry"`
	Alarms    []alarm.State  `json:"alarms"`
}

const maxEvents = 100

// Monitor polls one TEX and runs controls between polls. It owns the bus:
// polls and controls never overlap.
type Monitor struct {
	bus      sync.Mutex // held during every bus transaction
	dev      tex.Device
	settings *settings
	mu       sync.Mutex // guards status, alarms and events
	status   Status
	alarms   *alarm.Engine
	events   []notify.Event // newest last
}

// newMonitor polls the TEX at addr, or scans for one when addr is 0. Once
// found the address is kept.
func newMonitor(bus tex.Bus, addr uint16, s *settings) *Monitor {
	return &Monitor{dev: tex.Device{Bus: bus, Addr: addr}, settings: s, alarms: alarm.NewEngine()}
}

// Run polls until ctx is done.
func (m *Monitor) Run(ctx context.Context) {
	for {
		wait := pollEvery
		if err := m.poll(); err != nil {
			slog.Warn("poll failed", "error", err)
			wait = retryWait
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (m *Monitor) poll() error {
	m.bus.Lock()
	defer m.bus.Unlock()
	if m.dev.Addr == 0 {
		addr, err := findOne(m.dev.Bus)
		if err != nil {
			m.polled(nil, err)
			return err
		}
		m.dev.Addr = addr
		slog.Info("TEX found", "address", addr)
	}
	t, err := m.dev.Read()
	m.polled(&t, err)
	return err
}

// polled records a poll result, runs the alarms and sends their notifications.
func (m *Monitor) polled(t *tex.Telemetry, err error) {
	cfg := m.settings.get()
	if err != nil {
		t = nil
	}
	m.mu.Lock()
	m.record(t, err)
	events := m.alarms.Update(cfg.Alarms, time.Now(), t)
	for i := range events {
		events[i].Transmitter = cfg.Name
		slog.Info("alarm", "message", events[i].Message())
	}
	m.events = append(m.events, events...)
	m.events = m.events[max(0, len(m.events)-maxEvents):]
	m.mu.Unlock()
	for i := range events {
		notify.Dispatch(&cfg.Notifications, &events[i])
	}
}

// Control runs f against the transmitter while no poll is running.
func (m *Monitor) Control(f func(tex.Device) (tex.Telemetry, error)) (tex.Telemetry, error) {
	m.bus.Lock()
	defer m.bus.Unlock()
	if m.dev.Addr == 0 {
		return tex.Telemetry{}, errors.New("no transmitter found yet")
	}
	t, err := f(m.dev)
	if err == nil {
		m.mu.Lock()
		m.record(&t, nil)
		m.mu.Unlock()
	}
	return t, err
}

// Status returns a copy of the last known state, with the alarm states.
func (m *Monitor) Status() Status {
	cfg := m.settings.get()
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	st.Alarms = m.alarms.States(cfg.Alarms)
	return st
}

// Events returns the recent alarm events, newest first.
func (m *Monitor) Events() []notify.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]notify.Event{}, m.events...) // [] rather than null in JSON
	slices.Reverse(out)
	return out
}

// ResetAlarm clears the counter of an alarm and lets it qualify again.
func (m *Monitor) ResetAlarm(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.alarms.Reset(key)
}

// record stores a poll or control result. The caller holds m.mu.
func (m *Monitor) record(t *tex.Telemetry, err error) {
	m.status.Address = m.dev.Addr
	m.status.Link = err == nil
	m.status.Error = ""
	if err != nil {
		m.status.Error = err.Error()
		return
	}
	m.status.Telemetry = t
	m.status.Updated = time.Now()
}

func findOne(bus tex.Bus) (uint16, error) {
	found := tex.Scan(bus)
	switch len(found) {
	case 0:
		return 0, errors.New("no TEX found on addresses 1..31; check wiring or set the address")
	case 1:
		return found[0], nil
	}
	return 0, fmt.Errorf("several TEX found at %v; set the address", found)
}
