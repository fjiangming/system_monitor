//go:build linux

package main

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func buildUpdateFixture(t *testing.T, name string) string {
	t.Helper()
	if dir := os.Getenv("SYSTEM_MONITOR_UPDATE_FIXTURES"); dir != "" {
		path := filepath.Join(dir, name)
		if _, e := os.Stat(path); e != nil {
			t.Fatal(e)
		}
		return path
	}
	if _, e := exec.LookPath("go"); e != nil {
		t.Skip("Go toolchain or prebuilt update fixtures required")
	}
	path := filepath.Join(t.TempDir(), name)
	args := []string{"build", "-buildvcs=false", "-trimpath", "-o", path}
	if name == "previous" {
		args = append(args, "-ldflags=-s -w -X main.buildRevision=previous", ".")
	} else if name == "hang" {
		args = append(args, "-ldflags=-X main.mode=hang", "./testdata/agent-update-crash")
	} else if name == "crash" {
		args = append(args, "./testdata/agent-update-crash")
	} else {
		args = append(args, ".")
	}
	cmd := exec.Command("go", args...)
	if output, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("build %s: %v %s", name, e, output)
	}
	return path
}

func waitAgentUpdate(t *testing.T, s *Store, id, state string) AgentUpdate {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		u, e := s.AgentUpdate(id)
		if e == nil && u.State == state {
			return u
		}
		time.Sleep(100 * time.Millisecond)
	}
	u, _ := s.AgentUpdate(id)
	t.Fatalf("update did not reach %s: %+v", state, u)
	return u
}

func TestManagedAgentRealUpdateCorruptionAndStartupRollback(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("native agent supervisor requires root; run in isolated Linux test environment")
	}
	previous := buildUpdateFixture(t, "previous")
	next := buildUpdateFixture(t, "next")
	crash := buildUpdateFixture(t, "crash")
	hang := buildUpdateFixture(t, "hang")
	s := testStore(t)
	id := testServer(t, s)
	v, _ := s.Server(id)
	v.Config.Heartbeat = 10
	v.Config.HeartbeatTimeout = 21
	v.Config.CommandPoll = 1
	v.Config.DockerSocket = filepath.Join(t.TempDir(), "missing.sock")
	if _, e := s.SaveServer(id, v.Config, v.Revision); e != nil {
		t.Fatal(e)
	}
	h := newHubAPI(s, make(chan struct{}, 1)).(*hubAPI)
	artifactRoot := t.TempDir()
	artifactPath := filepath.Join(artifactRoot, "system-monitor-linux-"+runtime.GOARCH)
	if e := copyExclusive(next, artifactPath, 0755); e != nil {
		t.Fatal(e)
	}
	h.artifacts = &agentArtifacts{root: artifactRoot}
	hub := httptest.NewServer(h)
	defer hub.Close()
	root := filepath.Join(t.TempDir(), "agent-data")
	agentStore, e := openStore(root)
	if e != nil {
		t.Fatal(e)
	}
	code, _ := s.Enrollment(id)
	if e = enrollAgent(context.Background(), agentStore, hub.URL, id, code, defaultServer()); e != nil {
		t.Fatal(e)
	}
	agentStore.Close()
	if e = os.MkdirAll(filepath.Dir(managedAgentPath(root)), 0700); e != nil {
		t.Fatal(e)
	}
	if e = copyExclusive(previous, managedAgentPath(root), 0755); e != nil {
		t.Fatal(e)
	}
	logFile, e := os.Create(filepath.Join(t.TempDir(), "agent.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer logFile.Close()
	cmd := exec.Command(previous, "agent", "supervise", "--data", root)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		stopManagedChild(cmd, done)
		if t.Failed() {
			data, _ := os.ReadFile(logFile.Name())
			t.Log(string(data))
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		v, _ = s.Server(id)
		if v.Snapshot.Agent != nil && v.Snapshot.Agent.Updatable {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if v.Snapshot.Agent == nil || !v.Snapshot.Agent.Updatable {
		t.Fatal("managed agent did not connect", v.Snapshot.Agent)
	}
	oldSHA := v.Snapshot.Agent.SHA256
	f, e := h.artifacts.artifact(runtime.GOARCH)
	if e != nil {
		t.Fatal(e)
	}
	u, e := s.CreateAgentUpdate(id, f.SHA256, "integration", h.artifacts)
	if e != nil {
		t.Fatal(e)
	}
	waitAgentUpdate(t, s, u.ID, "success")
	v, _ = s.Server(id)
	if v.Snapshot.Agent.SHA256 != f.SHA256 || oldSHA == f.SHA256 {
		t.Fatal("new binary did not report fresh sample")
	}
	if saved, e := fileSHA256(previousAgentPath(root, u.ID)); e != nil || saved != oldSHA {
		t.Fatal("previous binary not retained", e)
	}
	// A corrupt transfer must leave the working executable and identity untouched.
	if e = os.Remove(artifactPath); e != nil {
		t.Fatal(e)
	}
	if e = copyExclusive(crash, artifactPath, 0755); e != nil {
		t.Fatal(e)
	}
	h.artifacts.mu.Lock()
	h.artifacts.files = nil
	h.artifacts.mu.Unlock()
	target, e := h.artifacts.artifact(runtime.GOARCH)
	if e != nil {
		t.Fatal(e)
	}
	file, e := os.OpenFile(artifactPath, os.O_WRONLY, 0)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = file.WriteAt([]byte{0}, 0)
	file.Close()
	u, e = s.CreateAgentUpdate(id, target.SHA256, "integration", h.artifacts)
	if e != nil {
		t.Fatal(e)
	}
	waitAgentUpdate(t, s, u.ID, "failed")
	if hash, e := fileSHA256(managedAgentPath(root)); e != nil || hash != f.SHA256 {
		t.Fatal("corrupt download replaced running binary", e)
	}
	// A valid executable that crashes on startup is rolled back by the stable supervisor.
	if e = os.Remove(artifactPath); e != nil {
		t.Fatal(e)
	}
	if e = copyExclusive(crash, artifactPath, 0755); e != nil {
		t.Fatal(e)
	}
	u, e = s.CreateAgentUpdate(id, target.SHA256, "integration", h.artifacts)
	if e != nil {
		t.Fatal(e)
	}
	waitAgentUpdate(t, s, u.ID, "failed")
	if hash, e := fileSHA256(managedAgentPath(root)); e != nil || hash != f.SHA256 {
		t.Fatal("startup failure did not restore working binary", e)
	}
	if e := cmd.Process.Signal(syscall.Signal(0)); e != nil {
		t.Fatal("stable supervisor exited", e)
	}
	// Advance the durable deadline to exercise timeout recovery without a two-minute test delay.
	if e = os.Remove(artifactPath); e != nil {
		t.Fatal(e)
	}
	if e = copyExclusive(hang, artifactPath, 0755); e != nil {
		t.Fatal(e)
	}
	h.artifacts.mu.Lock()
	h.artifacts.files = nil
	h.artifacts.mu.Unlock()
	target, e = h.artifacts.artifact(runtime.GOARCH)
	if e != nil {
		t.Fatal(e)
	}
	u, e = s.CreateAgentUpdate(id, target.SHA256, "integration", h.artifacts)
	if e != nil {
		t.Fatal(e)
	}
	waitAgentUpdate(t, s, u.ID, "restarting")
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hash, _ := fileSHA256(managedAgentPath(root))
		state, _ := readAgentRecovery(root)
		if hash == target.SHA256 && state.Launched {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	state, e := readAgentRecovery(root)
	if e != nil {
		t.Fatal(e)
	}
	state.Deadline = unixNow() - 1
	if e = writeAgentRecovery(root, state); e != nil {
		t.Fatal(e)
	}
	waitAgentUpdate(t, s, u.ID, "failed")
	if hash, e := fileSHA256(managedAgentPath(root)); e != nil || hash != f.SHA256 {
		t.Fatal("confirmation timeout did not restore working binary", e)
	}
	if _, e := os.Stat(filepath.Join(root, "identity.key")); e != nil {
		t.Fatal("agent identity lost", e)
	}
}

func TestAgentRecoveryRefusesUnexpectedBinary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "agent-data")
	if e := os.MkdirAll(filepath.Dir(managedAgentPath(root)), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(managedAgentPath(root), []byte("unrelated-program"), 0700); e != nil {
		t.Fatal(e)
	}
	state := agentRecovery{ID: "abcdefghijklmnopqrstuvwx", Target: digest("new"), Previous: digest("previous"), State: "pending", Deadline: unixNow() - 1}
	if e := os.WriteFile(previousAgentPath(root, state.ID), []byte("previous"), 0700); e != nil {
		t.Fatal(e)
	}
	if rollbackManagedAgent(root, state) == nil {
		t.Fatal("unrelated binary overwritten")
	}
	data, _ := os.ReadFile(managedAgentPath(root))
	if string(data) != "unrelated-program" {
		t.Fatal("unexpected binary changed")
	}
}
