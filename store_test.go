package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testStore(t *testing.T) *Store {
	t.Helper()
	s, e := openStore(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func testServer(t *testing.T, s *Store) string {
	t.Helper()
	c := defaultServer()
	c.Name = "test-host"
	c.Containers["postgres"] = ContainerRule{Monitor: true, Restart: true, Database: true}
	id, e := s.SaveServer("", c, 0)
	if e != nil {
		t.Fatal(e)
	}
	snap := Snapshot{Containers: []Container{{ID: testContainerID, Name: "postgres", State: "running", Health: "healthy", Database: true}}, MemoryTotal: 1024, MemoryUsed: 256, AgentVersion: version}
	if e = s.SaveSample(id, &snap); e != nil {
		t.Fatal(e)
	}
	return id
}
func TestConfigurationValidation(t *testing.T) {
	s := testStore(t)
	c, _ := s.Config()
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	a := defaultServer()
	a.Name = "linux"
	a.Units = "docker.service\nbackup@daily.service"
	if e := a.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, unit := range []string{"--help", "x.service; touch /tmp/x", "../a.service", "aXservice"} {
		a.Units = unit
		if a.Validate() == nil {
			t.Fatalf("accepted unit %q", unit)
		}
	}
	a.Units = ""
	a.HeartbeatTimeout = 60
	if a.Validate() == nil {
		t.Fatal("accepted invalid timeout")
	}
	c.Service.PublicURL = "https://admin:password@example.org"
	if c.Validate() == nil {
		t.Fatal("accepted URL userinfo")
	}
	c, _ = s.Config()
	c.Access.AllowIPs = "not-an-ip"
	if c.Validate() == nil {
		t.Fatal("accepted invalid network")
	}
}
func TestMissingOrMismatchedIdentityKey(t *testing.T) {
	root := t.TempDir()
	s, e := openStore(root)
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	keyPath := filepath.Join(root, "identity.key")
	key, e := os.ReadFile(keyPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(keyPath); e != nil {
		t.Fatal(e)
	}
	if bad, e := openStore(root); e == nil {
		bad.Close()
		t.Fatal("silently replaced missing identity key")
	}
	key[0] ^= 255
	if e = os.WriteFile(keyPath, key, 0600); e != nil {
		t.Fatal(e)
	}
	if bad, e := openStore(root); e == nil {
		bad.Close()
		t.Fatal("accepted mismatched key")
	}
}
func TestLocalAccountRecovery(t *testing.T) {
	s := testStore(t)
	_ = s.Setup(s.SetupCode(), "admin", "a-long-test-password")
	old, _, _, _ := s.Login("admin", "a-long-test-password", "127.0.0.1")
	c, rev := s.Config()
	c.Access.AllowIPs = "192.0.2.1"
	if e := s.SaveConfig(c, rev, nil); e != nil {
		t.Fatal(e)
	}
	name, password, e := s.RecoverAccess()
	if e != nil {
		t.Fatal(e)
	}
	if _, _, _, e = s.Login(name, password, "127.0.0.1"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Session(old); e == nil {
		t.Fatal("old session survived recovery")
	}
	c, _ = s.Config()
	if c.Access.AllowIPs != "" {
		t.Fatal("IP restriction survived recovery")
	}
}
func TestSecretsPersistenceCASAndBackup(t *testing.T) {
	s := testStore(t)
	c, rev := s.Config()
	secret := "https://example.org/private?token=DO_NOT_EXPOSE"
	if e := s.SaveConfig(c, rev, map[string]string{"webhookEndpoint": secret}); e != nil {
		t.Fatal(e)
	}
	if s.SaveConfig(c, rev, nil) == nil {
		t.Fatal("stale config revision accepted")
	}
	var raw string
	_ = s.DB.QueryRow(`SELECT v FROM secrets WHERE k='webhookEndpoint'`).Scan(&raw)
	if strings.Contains(raw, "DO_NOT_EXPOSE") || raw == secret {
		t.Fatal("credential not encrypted")
	}
	if v, e := s.decrypt("other", raw); e == nil || v != "" {
		t.Fatal("encryption scope not bound")
	}
	if s.Secret("webhookEndpoint") != secret {
		t.Fatal("credential roundtrip failed")
	}
	if e := s.ManualBackup(); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Join(s.Root, "backups"))
	if e != nil || len(entries) != 1 {
		t.Fatal("missing backup", e)
	}
	bundle := filepath.Join(s.Root, "backups", entries[0].Name())
	restored, e := openStore(bundle)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if restored.Secret("webhookEndpoint") != secret {
		t.Fatal("backup did not preserve matching key")
	}
	_, rev = s.Config()
	if e := s.SaveConfig(c, rev, nil, "webhookEndpoint"); e != nil {
		t.Fatal(e)
	}
	if s.SecretStatus()["webhookEndpoint"] {
		t.Fatal("credential not cleared")
	}
}
func TestSetupRaceAndLastAdministrator(t *testing.T) {
	s := testStore(t)
	code := s.SetupCode()
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Setup(code, "administrator", "a-long-test-password") == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 || len(s.Users()) != 1 {
		t.Fatal("setup was not atomic")
	}
	if s.Secret("bootstrap:setup") != "" {
		t.Fatal("bootstrap credential retained")
	}
	u := s.Users()[0]
	if s.SaveUser(u.ID, u.Username, "viewer", "", true) == nil {
		t.Fatal("removed last administrator")
	}
	token, csrf, _, e := s.Login(u.Username, "a-long-test-password", "127.0.0.1")
	if e != nil || csrf == "" {
		t.Fatal(e)
	}
	if _, _, e = s.Session(token); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveUser(u.ID, u.Username, "admin", "a-new-long-password", true); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Session(token); e == nil {
		t.Fatal("password change retained session")
	}
}
func TestEnrollmentOneUseAndRevoke(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	code, e := s.Enrollment(id)
	if e != nil {
		t.Fatal(e)
	}
	token, e := s.Enroll(id, code)
	if e != nil || !s.AgentAuthorized(id, token) {
		t.Fatal("enrollment failed", e)
	}
	if _, e = s.Enroll(id, code); e == nil {
		t.Fatal("reused one-time code")
	}
	code, _ = s.Enrollment(id)
	_, _ = s.DB.Exec(`UPDATE servers SET enroll_expires=? WHERE id=?`, unixNow()-1, id)
	if _, e = s.Enroll(id, code); e == nil {
		t.Fatal("accepted expired code")
	}
	_, _ = s.DB.Exec(`UPDATE servers SET token_hash='' WHERE id=?`, id)
	if s.AgentAuthorized(id, token) {
		t.Fatal("revocation ineffective")
	}
}
func TestRestartAuthorizationDedupeAndCompletion(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	if _, e := s.CreateJob(id, testContainerID, "restart", "", "", "admin"); e == nil {
		t.Fatal("database confirmation bypassed")
	}
	var wg sync.WaitGroup
	var accepted atomic.Int32
	var job Job
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := s.CreateJob(id, testContainerID, "restart", "planned", "postgres", "admin")
			if e == nil {
				accepted.Add(1)
				mu.Lock()
				job = j
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d duplicate operations", accepted.Load())
	}
	leased, e := s.Lease(id)
	if e != nil || leased == nil || leased.ID != job.ID {
		t.Fatal("missing lease", e)
	}
	if second, _ := s.Lease(id); second != nil {
		t.Fatal("leased twice")
	}
	job.State = "success"
	job.Result = "healthy"
	if e = s.Complete("other-server", job); e != nil {
		t.Fatal(e)
	}
	still, _ := s.Job(job.ID)
	if still.State != "leased" {
		t.Fatal("cross-server completion")
	}
	if e = s.Complete(id, job); e != nil {
		t.Fatal(e)
	}
	if e = s.Complete(id, job); e != nil {
		t.Fatal(e)
	}
	if len(s.Events(id, 100)) != 1 {
		t.Fatal("completion duplicated audit")
	}
	_, _ = s.DB.Exec(`UPDATE servers SET sample_at=1 WHERE id=?`, id)
	if _, e = s.CreateJob(id, testContainerID, "logs", "", "", "admin"); e == nil {
		t.Fatal("accepted stale sample")
	}
}
func TestPermissionChangesCancelQueueAndTimeoutIsUnknown(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	j, e := s.CreateJob(id, testContainerID, "restart", "", "postgres", "admin")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := s.Server(id)
	v.Config.Containers["postgres"] = ContainerRule{Monitor: true}
	if _, e = s.SaveServer(id, v.Config, v.Revision); e != nil {
		t.Fatal(e)
	}
	if leased, _ := s.Lease(id); leased != nil {
		t.Fatal("leased revoked operation")
	}
	got, _ := s.Job(j.ID)
	if got.State != "cancelled" {
		t.Fatal(got.State)
	}
	j, e = s.CreateJob(id, testContainerID, "logs", "", "", "admin")
	if e != nil {
		t.Fatal(e)
	}
	_, _ = s.DB.Exec(`UPDATE jobs SET deadline=1 WHERE id=?`, j.ID)
	s.ExpireJobs()
	got, _ = s.Job(j.ID)
	if got.State != "unknown" {
		t.Fatal(got.State)
	}
}
func TestMonitorCountsNewSamplesAndRecovery(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	c, _ := s.Config()
	c.Alerts.Failures = 2
	c.Notifications.Notices = true
	c.Notifications.GroupWindow = 0
	at := unixNow()
	for i := 0; i < 4; i++ {
		s.observe(v, "test", "critical", "failure", "", true, at, 2, 0, c)
	}
	if len(s.Alerts(id)) != 0 {
		t.Fatal("same observation counted multiple times")
	}
	s.observe(v, "test", "critical", "failure", "", true, at+1, 2, 0, c)
	if len(s.Alerts(id)) != 1 {
		t.Fatal("second observation did not alert")
	}
	_, _ = s.DB.Exec(`UPDATE alerts SET acknowledged=1 WHERE server_id=?`, id)
	s.observe(v, "test", "critical", "failure", "", false, at+2, 2, 0, c)
	if len(s.Alerts(id)) != 0 {
		t.Fatal("recovery did not clear alert")
	}
	events := s.Events(id, 100)
	if len(events) != 2 || events[0].Kind != "recovery" {
		t.Fatal("missing state transitions", events)
	}
	v.MaintenanceUntil = unixNow() + 600
	before := 0
	_ = s.DB.QueryRow(`SELECT count(*) FROM outbox`).Scan(&before)
	s.observe(v, "maint", "critical", "maintenance", "", true, at+3, 1, 0, c)
	after := 0
	_ = s.DB.QueryRow(`SELECT count(*) FROM outbox`).Scan(&after)
	if before != after {
		t.Fatal("maintenance leaked notification")
	}
}
func TestMigrationPreservesIdentityAndRejectsExistingTarget(t *testing.T) {
	s := testStore(t)
	_ = s.SetSecret("webhookSecret", "migration-secret")
	id := testServer(t, s)
	target := filepath.Join(t.TempDir(), "new-data")
	next, unlock, e := migrateStore(s, target)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	defer next.Close()
	if next.Secret("webhookSecret") != "migration-secret" {
		t.Fatal("lost encryption key")
	}
	if _, e = next.Server(id); e != nil {
		t.Fatal(e)
	}
	if _, _, e = migrateStore(s, target); e == nil {
		t.Fatal("overwrote existing data directory")
	}
}
func TestRetentionAndBackupIdempotency(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	_, _ = s.DB.Exec(`INSERT INTO history(server_id,at,cpu,memory,restarts) VALUES(?,1,0,0,'{}')`, id)
	s.Prune()
	var n int
	_ = s.DB.QueryRow(`SELECT count(*) FROM history WHERE at=1`).Scan(&n)
	if n != 0 {
		t.Fatal("expired history retained")
	}
	now := time.Date(2026, 11, 21, 23, 0, 0, 0, time.UTC)
	if e := s.BackupData(now); e != nil {
		t.Fatal(e)
	}
	if e := s.BackupData(now); e != nil {
		t.Fatal(e)
	}
}
func TestAgentRecoveryDoesNotReplay(t *testing.T) {
	s := testStore(t)
	j := Job{ID: "old-job", State: "leased"}
	_, e := s.DB.Exec(`INSERT INTO agent_jobs(id,state,result) VALUES(?,'started',?)`, j.ID, encode(j))
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(`UPDATE agent_jobs SET state='done',result=json_set(result,'$.state','unknown','$.result','interrupted') WHERE state='started'`)
	if e != nil {
		t.Fatal(e)
	}
	var raw, state string
	_ = s.DB.QueryRow(`SELECT state,result FROM agent_jobs WHERE id=?`, j.ID).Scan(&state, &raw)
	_ = json.Unmarshal([]byte(raw), &j)
	if state != "done" || j.State != "unknown" {
		t.Fatal("would replay interrupted operation")
	}
}
func TestInspectorReportsMissingMonitoredContainer(t *testing.T) {
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	c, _ := s.Config()
	c.Alerts.Failures = 1
	v.Snapshot.Containers = nil
	s.inspectServer(context.Background(), v, c)
	alerts := s.Alerts(id)
	if len(alerts) != 1 || !strings.Contains(alerts[0].Title, "postgres") {
		t.Fatal("missing monitored container not reported", alerts)
	}
}
