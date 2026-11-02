package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/oszuidwest/zwfm-txmon/internal/tex"
)

type server struct {
	cfg      Config
	settings *settings
	mon      *Monitor
	sessions *sessions
	pages    *template.Template
}

type pageData struct {
	Name, Version, CSRF, Error string
	FormToken                  string // login form only
	Expired                    bool   // login form only
	Year                       int
}

func newServer(st *settings, mon *Monitor) *server {
	return &server{cfg: st.get(), settings: st, mon: mon, sessions: newSessions(), pages: template.Must(template.ParseFS(webFS, "web/*.html"))}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.auth(s.handleLogout))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	for _, f := range []string{"style.css", "app.js", "login.js", "icons.js", "alpine.min.js", "favicon.svg"} {
		mux.HandleFunc("GET /"+f, func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, webFS, "web/"+f) })
	}
	mux.HandleFunc("GET /api/status", s.auth(s.handleStatus))
	mux.HandleFunc("POST /api/power", s.auth(s.handlePower))
	mux.HandleFunc("POST /api/rf", s.auth(s.handleRF))
	mux.HandleFunc("POST /api/frequency", s.auth(s.handleFrequency))
	mux.HandleFunc("GET /api/settings", s.auth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/settings", s.auth(s.handlePutSettings))
	mux.HandleFunc("POST /api/notifications/test", s.auth(s.handleTestNotification))
	mux.HandleFunc("POST /api/alarms/{key}/reset", s.auth(s.handleResetAlarm))
	mux.HandleFunc("GET /api/events", s.auth(s.handleEvents))
	mux.HandleFunc("GET /{$}", s.auth(s.handleIndex))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		// Alpine.js evaluates its expressions, so scripts need 'unsafe-eval'.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// auth lets only logged-in browsers through. Changes also need the
// session's CSRF token in the X-CSRF-Token header.
func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ses, ok := s.sessions.get(r)
		switch {
		case !ok && r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/"):
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		case !ok:
			writeError(w, http.StatusUnauthorized, "not logged in")
		case r.Method != http.MethodGet && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(ses.csrf)) != 1:
			writeError(w, http.StatusForbidden, "missing or wrong CSRF token")
		default:
			next(w, r)
		}
	}
}

func (s *server) page(w http.ResponseWriter, status int, name string, d *pageData) {
	d.Name, d.Version, d.Year = s.cfg.Name, Version, time.Now().Year()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := s.pages.ExecuteTemplate(w, name, d); err != nil {
		slog.Error("render page", "page", name, "error", err)
	}
}

func (s *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessions.get(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.loginPage(w, r, http.StatusOK, &pageData{Expired: r.URL.Query().Has("expired")})
}

// loginPage renders the login form with a fresh form token.
func (s *server) loginPage(w http.ResponseWriter, r *http.Request, status int, d *pageData) {
	d.FormToken = newFormToken(w, r)
	s.page(w, status, "login.html", d)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Room for a long password URL-encoded (up to 3x) plus the other fields.
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !validFormToken(r, r.PostForm.Get("csrf_token")) {
		// A form left open too long or from before a restart: offer a fresh one.
		slog.Warn("login rejected: invalid form token", "remote", clientIP(r))
		http.Redirect(w, r, "/login?expired=1", http.StatusSeeOther)
		return
	}
	ok, limited := s.sessions.login(w, r, r.PostForm.Get("username"), r.PostForm.Get("password"), &s.cfg)
	if limited {
		slog.Warn("login rate limited", "remote", clientIP(r))
		s.loginPage(w, r, http.StatusTooManyRequests, &pageData{Error: "Too many attempts. Try again in a few minutes."})
		return
	}
	if !ok {
		slog.Warn("login failed", "remote", clientIP(r))
		s.loginPage(w, r, http.StatusUnauthorized, &pageData{Error: "Wrong username or password."})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.sessions.logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ses, _ := s.sessions.get(r)
	s.page(w, http.StatusOK, "index.html", &pageData{CSRF: ses.csrf})
}

func (s *server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mon.Status())
}

func (s *server) handlePower(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Percent *int `json:"percent"`
	}
	if !decode(w, r, &req) || req.Percent == nil {
		writeError(w, http.StatusBadRequest, "send {\"percent\": 0..120}")
		return
	}
	slog.Info("set target power", "percent", *req.Percent, "remote", clientIP(r))
	s.control(w, func(d tex.Device) (tex.Telemetry, error) { return d.SetTarget(*req.Percent) })
}

func (s *server) handleRF(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On *bool `json:"on"`
	}
	if !decode(w, r, &req) || req.On == nil {
		writeError(w, http.StatusBadRequest, "send {\"on\": true|false}")
		return
	}
	slog.Info("switch RF", "on", *req.On, "remote", clientIP(r))
	s.control(w, func(d tex.Device) (tex.Telemetry, error) { return d.SetRF(*req.On) })
}

func (s *server) handleFrequency(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MHz string `json:"mhz"`
	}
	kHz, ok := 0, decode(w, r, &req)
	if ok {
		kHz, ok = parseMHz(req.MHz)
	}
	if !ok {
		writeError(w, http.StatusBadRequest, "send {\"mhz\": \"98.50\"}, at most two decimals")
		return
	}
	slog.Info("set frequency", "kHz", kHz, "remote", clientIP(r))
	s.control(w, func(d tex.Device) (tex.Telemetry, error) { return d.SetFrequency(kHz) })
}

// control runs a command and answers with the new telemetry.
func (s *server) control(w http.ResponseWriter, f func(tex.Device) (tex.Telemetry, error)) {
	t, err := s.mon.Control(f)
	if err != nil {
		var inputErr *tex.InputError
		status := http.StatusBadGateway
		if errors.As(err, &inputErr) {
			status = http.StatusBadRequest
		}
		slog.Warn("control failed", "error", err)
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, 1024)
}

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return json.NewDecoder(r.Body).Decode(v) == nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	st := s.mon.Status()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	fmt.Fprintf(&b, "tex_link %d\n", b2i(st.Link))
	defer func() { _, _ = io.WriteString(w, b.String()) }()
	t := st.Telemetry
	if t == nil {
		return
	}
	for _, m := range []struct {
		name string
		v    float64
	}{
		{"tex_firmware", float64(t.Firmware)},
		{"tex_frequency_mhz", t.FrequencyMHz},
		{"tex_target_power_pct", float64(t.TargetPct)},
		{"tex_forward_power_watts", t.ForwardW},
		{"tex_reflected_power_watts", t.ReflectedW},
		{"tex_temperature_celsius", float64(t.TemperatureC)},
		{"tex_modulation_khz", float64(t.ModulationKHz)},
		{"tex_deviation_left_khz", float64(t.DeviationLKHz)},
		{"tex_deviation_right_khz", float64(t.DeviationRKHz)},
		{"tex_pa_voltage_volts", t.PAVoltage},
		{"tex_pa_current_amperes", t.PACurrent},
		{"tex_efficiency_pct", float64(t.EfficiencyPct)},
		{"tex_on_air", float64(b2i(t.OnAir))},
		{"tex_power_set_ok", float64(b2i(t.PowerSetOK))},
		{"tex_pll_locked", float64(b2i(t.PLLLocked))},
		{"tex_foldback", float64(b2i(t.Foldback))},
		{"tex_interlock", float64(b2i(t.Interlock))},
		{"tex_audio_loss", float64(b2i(t.AudioLoss))},
		{"tex_swr_alarm", float64(b2i(t.SWRAlarm))},
		{"tex_panel_access", float64(b2i(t.PanelAccess))},
		{"tex_last_update_seconds", float64(st.Updated.Unix())},
	} {
		fmt.Fprintf(&b, "%s %g\n", m.name, m.v)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
