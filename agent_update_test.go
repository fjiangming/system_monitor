package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testAgentArtifacts(t *testing.T) *agentArtifacts {
	t.Helper()
	data := []byte("isolated-update-fixture")
	path := filepath.Join(t.TempDir(), "agent")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	f := agentArtifactFile{AgentArtifact: AgentArtifact{Version: version, Revision: "fixture", Arch: "amd64", SHA256: digest(string(data)), Size: int64(len(data))}, path: path}
	return &agentArtifacts{files: map[string]agentArtifactFile{"amd64": f}}
}

func setUpdatableAgent(t *testing.T, s *Store, id string) {
	t.Helper()
	v, _ := s.Server(id)
	v.Snapshot.Agent = &AgentInfo{Protocol: 1, Arch: "amd64", Revision: "previous", SHA256: strings.Repeat("1", 64), Updatable: true}
	if e := s.SaveSample(id, &v.Snapshot); e != nil {
		t.Fatal(e)
	}
}

func agentUpdateRequest(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(encode(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAgentUpdateAuthorizationLifecycleAndPinnedArtifact(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	setUpdatableAgent(t, s, id)
	a := testAgentArtifacts(t)
	f, _ := a.artifact("amd64")
	h := newHubAPI(s, make(chan struct{}, 1)).(*hubAPI)
	h.artifacts = a
	if e := s.Setup(s.SetupCode(), "admin", "a-long-update-password"); e != nil {
		t.Fatal(e)
	}
	admin, csrf, _, _ := s.Login("admin", "a-long-update-password", "127.0.0.1")
	if e := s.SaveUser("", "viewer", "viewer", "a-long-viewer-password", true); e != nil {
		t.Fatal(e)
	}
	viewer, viewerCSRF, _, _ := s.Login("viewer", "a-long-viewer-password", "127.0.0.2")
	body := map[string]string{"id": id, "sha256": f.SHA256}
	for _, check := range []struct {
		cookie, csrf string
		status       int
	}{{"", "", 401}, {admin, "", 403}, {viewer, viewerCSRF, 403}} {
		if w := requestJSON(t, h, "/api/server/agent-update", body, check.cookie, check.csrf); w.Code != check.status {
			t.Fatalf("authorization: %d %s", w.Code, w.Body.String())
		}
	}
	if _, e := s.CreateAgentUpdate(id, strings.Repeat("0", 64), "test", a); e == nil {
		t.Fatal("accepted stale artifact confirmation")
	}
	w := requestJSON(t, h, "/api/server/agent-update", body, admin, csrf)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var update AgentUpdate
	if e := json.Unmarshal(w.Body.Bytes(), &update); e != nil {
		t.Fatal(e)
	}
	if _, e := s.CreateAgentUpdate(id, f.SHA256, "test", a); e == nil {
		t.Fatal("duplicate update accepted")
	}
	if _, e := s.CreateJob(id, testContainerID, "restart", "", "postgres", "test"); e == nil {
		t.Fatal("container action allowed during update")
	}
	code, _ := s.Enrollment(id)
	token, _ := s.Enroll(id, code)
	download := map[string]any{"id": id, "update": AgentUpdate{ID: update.ID}}
	if w = agentUpdateRequest(t, h, "/api/agent/update-download", token, download); w.Code != 403 {
		t.Fatal("unleased artifact downloadable")
	}
	w = agentUpdateRequest(t, h, "/api/agent/poll", token, map[string]string{"id": id})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var reply agentReply
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if reply.Update == nil || reply.Update.ID != update.ID || reply.Job != nil {
		t.Fatal("typed update dispatch failed", w.Body.String())
	}
	if u, e := s.LeaseAgentUpdate(id); e != nil || u != nil {
		t.Fatal("update replayed", e)
	}
	if w = agentUpdateRequest(t, h, "/api/agent/update-download", "wrong-token", download); w.Code != 401 {
		t.Fatal("download accepted wrong identity")
	}
	other := testServer(t, s)
	otherCode, _ := s.Enrollment(other)
	otherToken, _ := s.Enroll(other, otherCode)
	if w = agentUpdateRequest(t, h, "/api/agent/update-download", otherToken, map[string]any{"id": other, "update": AgentUpdate{ID: update.ID}}); w.Code != 403 {
		t.Fatal("another server downloaded update")
	}
	w = agentUpdateRequest(t, h, "/api/agent/update-download", token, download)
	if w.Code != 200 || w.Body.String() != "isolated-update-fixture" {
		t.Fatal("download", w.Code, w.Body.String())
	}
	if e := s.ReportAgentUpdate(id, AgentUpdate{ID: update.ID, State: "success"}); e == nil {
		t.Fatal("success accepted without fresh target sample")
	}
	if e := s.ReportAgentUpdate(id, AgentUpdate{ID: update.ID, State: "restarting"}); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Server(id)
	s.confirmAgentUpdate(v)
	u, _ := s.AgentUpdate(update.ID)
	if u.State != "restarting" {
		t.Fatal("old sample confirmed success")
	}
	v.Snapshot.Agent.SHA256 = f.SHA256
	if e := s.SaveSample(id, &v.Snapshot); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Server(id)
	s.confirmAgentUpdate(v)
	u, _ = s.AgentUpdate(update.ID)
	if u.State != "success" {
		t.Fatal("target sample not confirmed", u)
	}
	if view := s.agentUpdateView(v, a); view.Available {
		t.Fatal("same binary offered as update")
	}
	if len(s.Events(id, 20)) < 2 {
		t.Fatal("update audit missing")
	}
}

func TestAgentUpdateCompatibilityCancellationAndExpiry(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	a := testAgentArtifacts(t)
	f, _ := a.artifact("amd64")
	v, _ := s.Server(id)
	if view := s.agentUpdateView(v, a); view.Available || !strings.Contains(view.Reason, "手动") {
		t.Fatal("legacy agent accepted", view)
	}
	setUpdatableAgent(t, s, id)
	create := func() AgentUpdate {
		t.Helper()
		u, e := s.CreateAgentUpdate(id, f.SHA256, "test", a)
		if e != nil {
			t.Fatal(e)
		}
		return u
	}
	u := create()
	v, _ = s.Server(id)
	if _, e := s.SaveServer(id, v.Config, v.Revision); e != nil {
		t.Fatal(e)
	}
	u, _ = s.AgentUpdate(u.ID)
	if u.State != "cancelled" {
		t.Fatal("settings change retained queued update")
	}
	u = create()
	_, _ = s.DB.Exec(`UPDATE agent_updates SET deadline=? WHERE id=?`, unixNow()-1, u.ID)
	s.ExpireJobs()
	u, _ = s.AgentUpdate(u.ID)
	if u.State != "cancelled" {
		t.Fatal("expired queued update")
	}
	u = create()
	if _, e := s.LeaseAgentUpdate(id); e != nil {
		t.Fatal(e)
	}
	_, _ = s.DB.Exec(`UPDATE agent_updates SET deadline=? WHERE id=?`, unixNow()-1, u.ID)
	s.ExpireJobs()
	u, _ = s.AgentUpdate(u.ID)
	if u.State != "unknown" {
		t.Fatal("expired in-flight update claimed success")
	}
	if retry, e := s.LeaseAgentUpdate(id); e != nil || retry != nil {
		t.Fatal("uncertain update replayed")
	}
	if e := s.ReportAgentUpdate(id, AgentUpdate{ID: u.ID, State: "failed", Result: "restored"}); e != nil {
		t.Fatal(e)
	}
	u, _ = s.AgentUpdate(u.ID)
	if u.State != "failed" {
		t.Fatal("late rollback receipt lost")
	}
}

func TestAgentUpdateTransportAndArtifactValidation(t *testing.T) {
	c := defaultServer()
	for _, hub := range []string{"http://example.org", "http://127.0.0.1.example.org", "ftp://example.org"} {
		if secureAgentUpdateHub(hub, c) {
			t.Fatalf("insecure update hub accepted: %s", hub)
		}
	}
	if !secureAgentUpdateHub("https://monitor.example.org", c) || !secureAgentUpdateHub("http://127.0.0.1:18080", c) {
		t.Fatal("trusted transport rejected")
	}
	c.VerifyTLS = false
	if secureAgentUpdateHub("https://example.org", c) {
		t.Fatal("TLS verification bypass allowed")
	}
	a := testAgentArtifacts(t)
	f, _ := a.artifact("amd64")
	if !validAgentArtifact(f.AgentArtifact) {
		t.Fatal("valid artifact rejected")
	}
	for _, arch := range []string{"../amd64", "windows-amd64", "arm"} {
		if _, e := a.artifact(arch); e == nil {
			t.Fatal("unsupported artifact path accepted")
		}
	}
	f.SHA256 = "bad"
	if validAgentArtifact(f.AgentArtifact) {
		t.Fatal("invalid hash accepted")
	}
	if checkAgentELF(f.path, "amd64") == nil {
		t.Fatal("non-ELF artifact accepted")
	}
}
