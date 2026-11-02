package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"
)

const (
	sessionCookie   = "txmon_session"
	formCookie      = "txmon_login"
	sessionDuration = 24 * time.Hour
	loginWindow     = 15 * time.Minute
	loginAttempts   = 5 // failed logins per client in loginWindow
	formTokenTTL    = 10 * time.Minute
)

type session struct {
	csrf    string
	expires time.Time
}

// sessions keeps logged-in browsers in memory; a restart logs everyone out.
type sessions struct {
	mu       sync.Mutex
	byToken  map[string]session
	failures map[string][]time.Time
}

func newSessions() *sessions {
	return &sessions{byToken: map[string]session{}, failures: map[string][]time.Time{}}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails on supported platforms
	return hex.EncodeToString(b)
}

// login checks the credentials and starts a session. The rate limit check,
// the comparison and the recording of a failure happen under one lock, so
// parallel attempts cannot get past the limit.
func (s *sessions) login(w http.ResponseWriter, r *http.Request, user, pass string, cfg *Config) (ok, limited bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ip := clientIP(r)
	recent := slices.DeleteFunc(s.failures[ip], func(t time.Time) bool { return time.Since(t) >= loginWindow })
	s.failures[ip] = recent
	if len(recent) >= loginAttempts {
		return false, true
	}
	ok = subtle.ConstantTimeCompare([]byte(user), []byte(cfg.Username)) == 1
	ok = subtle.ConstantTimeCompare([]byte(pass), []byte(cfg.Password)) == 1 && ok
	if !ok {
		s.failures[ip] = append(recent, time.Now())
		return false, false
	}
	token := randomToken()
	s.byToken[token] = session{csrf: randomToken(), expires: time.Now().Add(sessionDuration)}
	setCookie(w, r, sessionCookie, token, int(sessionDuration.Seconds()))
	return true, false
}

// newFormToken returns a CSRF token for the login form and puts it in a
// cookie as well; the form must send it back (double submit). Nothing is
// stored, so anonymous page loads cannot use up memory or push out tokens.
func newFormToken(w http.ResponseWriter, r *http.Request) string {
	t := randomToken()
	setCookie(w, r, formCookie, t, int(formTokenTTL.Seconds()))
	return t
}

// validFormToken reports whether token matches the form cookie of the request.
func validFormToken(r *http.Request, token string) bool {
	c, err := r.Cookie(formCookie)
	return err == nil && token != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1
}

// get returns the session of the request, if it is valid.
func (s *sessions) get(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.byToken[c.Value]
	if ok && time.Now().After(ses.expires) {
		delete(s.byToken, c.Value)
		return session{}, false
	}
	return ses, ok
}

func (s *sessions) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.byToken, c.Value)
		s.mu.Unlock()
	}
	setCookie(w, r, sessionCookie, "", -1)
}

func setCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	//nolint:gosec // Secure follows the request, so plain HTTP on the LAN keeps working.
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
