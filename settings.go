package main

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/oszuidwest/zwfm-txmon/internal/alarm"
	"github.com/oszuidwest/zwfm-txmon/internal/notify"
)

// settings holds the running config and saves changes to its file.
type settings struct {
	mu   sync.RWMutex
	path string // empty: changes are not saved
	cfg  Config
}

// get returns the config. Callers must not change its maps or slices: a save
// replaces them instead.
func (s *settings) get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// set stores alarms and notifications, keeping secrets that were sent empty.
func (s *settings) set(a alarm.Config, n *notify.Config, drop []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.cfg
	cfg.Alarms = a
	cfg.Notifications = withSecrets(n, &s.cfg.Notifications, drop)
	if s.path != "" {
		if err := saveConfig(s.path, &cfg); err != nil {
			return err
		}
	}
	s.cfg = cfg
	return nil
}

// secrets lists the fields the browser never gets back.
func secrets(n *notify.Config) map[string]*string {
	return map[string]*string{
		"heartbeat_url": &n.Heartbeat.URL,
		"webhook_url":   &n.Webhook.URL,
		"client_secret": &n.Email.ClientSecret,
	}
}

// withSecrets fills secrets left empty in n from old, unless listed in drop.
func withSecrets(n, old *notify.Config, drop []string) notify.Config {
	out := *n
	have := secrets(old)
	for name, p := range secrets(&out) {
		if *p == "" && !slices.Contains(drop, name) {
			*p = *have[name]
		}
	}
	return out
}

type settingsBody struct {
	Alarms        alarm.Config      `json:"alarms"`
	Notifications notify.Config     `json:"notifications"`
	Clear         []string          `json:"clear,omitempty"` // secrets to remove
	Secrets       map[string]bool   `json:"secrets,omitempty"`
	Defs          []alarm.Def       `json:"defs,omitempty"`
	Version       map[string]string `json:"version,omitempty"`
}

func (s *server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	cfg := s.settings.get()
	body := settingsBody{
		Alarms: cfg.Alarms, Notifications: cfg.Notifications, Secrets: map[string]bool{}, Defs: alarm.Defs,
		Version: map[string]string{"version": Version, "commit": Commit, "build_time": BuildTime},
	}
	for name, p := range secrets(&body.Notifications) {
		body.Secrets[name] = *p != ""
		*p = ""
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body settingsBody
	if !decodeLimit(w, r, &body, 64<<10) {
		writeError(w, http.StatusBadRequest, "invalid settings")
		return
	}
	if err := body.Alarms.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.settings.set(body.Alarms, &body.Notifications, body.Clear); err != nil {
		slog.Error("save settings", "error", err)
		writeError(w, http.StatusInternalServerError, "could not save the settings: "+err.Error())
		return
	}
	slog.Info("settings saved", "remote", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// handleTestNotification sends a test through one channel, with the settings
// from the form so they can be tried before saving.
func (s *server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel       string        `json:"channel"`
		Notifications notify.Config `json:"notifications"`
	}
	if !decodeLimit(w, r, &body, 64<<10) {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	cfg := s.settings.get()
	n := withSecrets(&body.Notifications, &cfg.Notifications, nil)
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := notify.Test(ctx, &n, body.Channel, cfg.Name); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleResetAlarm(w http.ResponseWriter, r *http.Request) {
	if !s.mon.ResetAlarm(r.PathValue("key")) {
		writeError(w, http.StatusNotFound, "no such alarm")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleEvents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.Events())
}
