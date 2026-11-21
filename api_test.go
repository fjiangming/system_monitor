package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestPrepareBrowserData(t *testing.T) {
	root := os.Getenv("SYSTEM_MONITOR_PREPARE_DATA")
	if root == "" {
		t.Skip("browser fixture preparation")
	}
	s, e := openStore(root)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	c, rev := s.Config()
	port, _ := strconv.Atoi(os.Getenv("SYSTEM_MONITOR_PREPARE_PORT"))
	c.Service.ListenAddress = "127.0.0.1"
	c.Service.ListenPort = port
	c.Alerts.Interval = 10
	c.Storage.DataBackup = false
	if e = s.SaveConfig(c, rev, nil); e != nil {
		t.Fatal(e)
	}
	_ = s.setMeta("activeService", encode(c.Service))
	t.Log("BROWSER_SETUP=" + s.SetupCode())
}

func requestJSON(t *testing.T, h http.Handler, path string, body any, cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	method := "GET"
	var data io.Reader
	if body != nil {
		method = "POST"
		data = strings.NewReader(encode(body))
	}
	r := httptest.NewRequest(method, path, data)
	r.RemoteAddr = "127.0.0.1:1234"
	if cookie != "" {
		r.Header.Set("Cookie", "monitor_session="+cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestHTTPAuthenticationCSRFViewerAndSecrets(t *testing.T) {
	s := testStore(t)
	h := newHubAPI(s, make(chan struct{}, 1))
	if w := requestJSON(t, h, "/api/state", nil, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if e := s.Setup(s.SetupCode(), "admin", "a-long-test-password"); e != nil {
		t.Fatal(e)
	}
	token, csrf, _, _ := s.Login("admin", "a-long-test-password", "127.0.0.1")
	c, rev := s.Config()
	body := map[string]any{"config": c, "revision": rev, "secrets": map[string]string{"webhookEndpoint": "https://example.org/VERY_SECRET"}, "clearSecrets": []string{}}
	if w := requestJSON(t, h, "/api/config/save", body, token, ""); w.Code != 403 {
		t.Fatal("CSRF accepted", w.Code)
	}
	if w := requestJSON(t, h, "/api/config/save", body, token, csrf); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w := requestJSON(t, h, "/api/config", nil, token, "")
	if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte("VERY_SECRET")) {
		t.Fatal("secret leaked", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"webhookEndpoint":true`) {
		t.Fatal("missing presence flag")
	}
	if e := s.SaveUser("", "reader", "viewer", "a-long-viewer-password", true); e != nil {
		t.Fatal(e)
	}
	vt, vc, _, _ := s.Login("reader", "a-long-viewer-password", "127.0.0.2")
	if w = requestJSON(t, h, "/api/state", nil, vt, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = requestJSON(t, h, "/api/config", nil, vt, ""); w.Code != 403 {
		t.Fatal("viewer read settings")
	}
	if w = requestJSON(t, h, "/api/job/create", map[string]string{}, vt, vc); w.Code != 403 {
		t.Fatal("viewer wrote command")
	}
	r := httptest.NewRequest("POST", "/api/logout", strings.NewReader(`{}`))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Cookie", "monitor_session="+token)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin write accepted")
	}
	r = httptest.NewRequest("GET", "/", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "app.js") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("embedded UI or policy missing")
	}
}
func TestProxyTrustAndLoginLockout(t *testing.T) {
	c := defaultConfig(t.TempDir())
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.12:80"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if remoteIP(r, c) != "203.0.113.12" {
		t.Fatal("untrusted forwarded IP accepted")
	}
	r.RemoteAddr = "127.0.0.1:80"
	if remoteIP(r, c) != "1.2.3.4" {
		t.Fatal("trusted proxy ignored")
	}
	s := testStore(t)
	_ = s.Setup(s.SetupCode(), "admin", "a-long-test-password")
	for i := 0; i < 5; i++ {
		_, _, _, _ = s.Login("admin", "wrong", "10.0.0.1")
	}
	if _, _, _, e := s.Login("admin", "a-long-test-password", "10.0.0.1"); e == nil {
		t.Fatal("lockout bypassed")
	}
	if _, _, _, e := s.Login("admin", "a-long-test-password", "10.0.0.2"); e != nil {
		t.Fatal(e)
	}
}
func TestAgentHTTPIsolationAndOneTimeEnrollment(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	code, _ := s.Enrollment(id)
	h := newHubAPI(s, make(chan struct{}, 1))
	w := requestJSON(t, h, "/api/agent/enroll", map[string]string{"id": id, "code": code}, "", "")
	var b map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	token := b["token"]
	if w.Code != 200 || token == "" {
		t.Fatal(w.Code)
	}
	if w = requestJSON(t, h, "/api/agent/enroll", map[string]string{"id": id, "code": code}, "", ""); w.Code != 401 {
		t.Fatal("reused code")
	}
	r := httptest.NewRequest("POST", "/api/agent/poll", strings.NewReader(encode(map[string]any{"id": "other"})))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("cross-server token accepted")
	}
	r = httptest.NewRequest("POST", "/api/agent/poll", strings.NewReader(encode(map[string]any{"id": id})))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), token) {
		t.Fatal("agent token reflected")
	}
}
