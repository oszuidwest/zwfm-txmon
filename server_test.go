package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := defaultConfig()
	cfg.Password = "secret"
	st := &settings{cfg: cfg}
	ts := httptest.NewServer(newServer(st, newMonitor(newDemoBus(), demoAddr, st)).routes())
	t.Cleanup(ts.Close)
	return ts
}

func client(t *testing.T) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// do sends a request and returns the status code and body.
func do(t *testing.T, c *http.Client, req *http.Request) (code int, body string) {
	t.Helper()
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res.StatusCode, string(b)
}

func get(t *testing.T, c *http.Client, u string) (code int, body string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u, http.NoBody)
	return do(t, c, req)
}

// formToken loads a login page and returns the CSRF token of its form.
func formToken(t *testing.T, c *http.Client, u string) string {
	t.Helper()
	_, page := get(t, c, u)
	m := regexp.MustCompile(`name="csrf_token" value="([0-9a-f]{64})"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no form token on the login page")
	}
	return m[1]
}

func login(t *testing.T, ts *httptest.Server, c *http.Client, pass string) int {
	t.Helper()
	code, _ := postLogin(t, ts, c, url.Values{"username": {"admin"}, "password": {pass}, "csrf_token": {formToken(t, c, ts.URL+"/login")}}.Encode())
	return code
}

// postLogin posts a raw login form and returns the status and headers.
func postLogin(t *testing.T, ts *httptest.Server, c *http.Client, form string) (int, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/login", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode, res.Header
}

func post(t *testing.T, c *http.Client, u, csrf, body string) (code int, resp string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	return do(t, c, req)
}

func TestNeedsLoginAndCSRF(t *testing.T) {
	ts := newTestServer(t)
	c := client(t)
	if code, _ := get(t, c, ts.URL+"/"); code != http.StatusSeeOther {
		t.Errorf("GET / without session: %d, want redirect to login", code)
	}
	if code, _ := get(t, c, ts.URL+"/api/status"); code != http.StatusUnauthorized {
		t.Errorf("GET /api/status without session: %d, want 401", code)
	}
	if code := login(t, ts, c, "secret"); code != http.StatusSeeOther {
		t.Fatalf("login: %d", code)
	}
	if code, _ := post(t, c, ts.URL+"/api/rf", "", `{"on":false}`); code != http.StatusForbidden {
		t.Errorf("POST without CSRF token: %d, want 403", code)
	}
	if _, body := get(t, c, ts.URL+"/api/events"); body != "[]\n" {
		t.Errorf("events before any alarm: %q, want []", body)
	}
}

func TestControlThroughAPI(t *testing.T) {
	ts := newTestServer(t)
	c := client(t)
	login(t, ts, c, "secret")
	_, page := get(t, c, ts.URL+"/")
	m := regexp.MustCompile(`name="csrf-token" content="([0-9a-f]{64})"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no CSRF token on the page")
	}
	csrf := m[1]

	if code, body := post(t, c, ts.URL+"/api/power", csrf, `{"percent":50}`); code != http.StatusOK || !strings.Contains(body, `"target_power_pct":50`) {
		t.Errorf("set power: %d %s", code, body)
	}
	if code, body := post(t, c, ts.URL+"/api/power", csrf, `{"percent":130}`); code != http.StatusBadRequest {
		t.Errorf("power 130%%: %d %s, want 400", code, body)
	}
	if code, body := post(t, c, ts.URL+"/api/frequency", csrf, `{"mhz":"98.505"}`); code != http.StatusBadRequest {
		t.Errorf("three decimals: %d %s, want 400", code, body)
	}
}

func TestLoginRateLimit(t *testing.T) {
	ts := newTestServer(t)
	c := client(t)
	for range loginAttempts {
		if code := login(t, ts, c, "wrong"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password: %d, want 401", code)
		}
	}
	if code := login(t, ts, c, "secret"); code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: %d, want 429", loginAttempts, code)
	}
}

func TestLoginFormToken(t *testing.T) {
	ts := newTestServer(t)
	c := client(t)
	form := func(pass, token string) string {
		return url.Values{"username": {"admin"}, "password": {pass}, "csrf_token": {token}}.Encode()
	}
	if code, h := postLogin(t, ts, c, form("secret", "")); code != http.StatusSeeOther || h.Get("Location") != "/login?expired=1" {
		t.Fatalf("login without form token: %d to %q, want redirect to the expired form", code, h.Get("Location"))
	}
	if _, page := get(t, c, ts.URL+"/login?expired=1"); !strings.Contains(page, "Sign-in form expired") {
		t.Error("expired login form has no message")
	}
	tok := formToken(t, c, ts.URL+"/login")
	if code, h := postLogin(t, ts, client(t), form("secret", tok)); code != http.StatusSeeOther || h.Get("Location") != "/login?expired=1" {
		t.Fatalf("form token without its cookie: %d to %q, want the expired form", code, h.Get("Location"))
	}
	if code, _ := postLogin(t, ts, c, form("secret", tok)+"&broken=%"); code != http.StatusBadRequest {
		t.Errorf("malformed form: %d, want 400", code)
	}
	if code, h := postLogin(t, ts, c, form("wrong", tok)); code != http.StatusUnauthorized || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("wrong password: %d with Cache-Control %q, want 401 and no-store", code, h.Get("Cache-Control"))
	}
	postLogin(t, ts, c, form("secret", tok)) // the failed login above issued a new token
	if code, _ := get(t, c, ts.URL+"/api/status"); code != http.StatusUnauthorized {
		t.Errorf("logged in with a missing or spent form token: %d", code)
	}
	// The expired form comes with a fresh token that works.
	postLogin(t, ts, c, form("secret", formToken(t, c, ts.URL+"/login?expired=1")))
	if code, _ := get(t, c, ts.URL+"/api/status"); code != http.StatusOK {
		t.Errorf("login with the token of the expired form: %d, want 200", code)
	}
}

func TestFormTokenNotEmpty(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", http.NoBody)
	r.AddCookie(&http.Cookie{Name: formCookie, Value: "", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	if validFormToken(r, "") {
		t.Error("an empty form token matched an empty cookie")
	}
}
