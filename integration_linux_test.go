package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentLoopDurableResultAndHeartbeat(t *testing.T) {
	hubStore := testStore(t)
	agentStore := testStore(t)
	id := testServer(t, hubStore)
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	var restarted atomic.Int64
	var restartCount atomic.Int32
	dockerServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/containers/json":
			respond(w, 200, []map[string]any{{"Id": testContainerID, "Names": []string{"/postgres"}}})
		case strings.HasSuffix(r.URL.Path, "/restart"):
			restarted.Store(time.Now().UnixNano())
			restartCount.Add(1)
			w.WriteHeader(204)
		default:
			health := "healthy"
			if stamp := restarted.Load(); stamp > 0 && time.Since(time.Unix(0, stamp)) < 3*time.Second {
				health = "starting"
			}
			respond(w, 200, map[string]any{"Id": testContainerID, "Name": "/postgres", "Config": map[string]string{"Image": "postgres:17"}, "State": map[string]any{"Status": "running", "Running": true, "Health": map[string]string{"Status": health}}})
		}
	})}
	go dockerServer.Serve(listener)
	defer dockerServer.Close()
	cfg, rev := hubStore.Config()
	cfg.Access.CommandTimeout = 5
	cfg.Access.HealthWait = 8
	cfg.Access.RestartCooldown = 0
	if e = hubStore.SaveConfig(cfg, rev, nil); e != nil {
		t.Fatal(e)
	}
	v, _ := hubStore.Server(id)
	v.Config.DockerSocket = socket
	v.Config.CommandPoll = 1
	v.Config.Heartbeat = 10
	v.Config.HeartbeatTimeout = 21
	if _, e = hubStore.SaveServer(id, v.Config, v.Revision); e != nil {
		t.Fatal(e)
	}
	var polls atomic.Int32
	handler := newHubAPI(hubStore, make(chan struct{}, 1))
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/agent/poll" {
			polls.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer hub.Close()
	code, _ := hubStore.Enrollment(id)
	if e = enrollAgent(context.Background(), agentStore, hub.URL, id, code, defaultServer()); e != nil {
		t.Fatal(e)
	}
	job, e := hubStore.CreateJob(id, testContainerID, "restart", "integration", "postgres", "test")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { defer close(finished); finished <- runAgent(ctx, agentStore) }()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	deadline := time.Now().Add(20 * time.Second)
	var result Job
	for time.Now().Before(deadline) {
		result, _ = hubStore.Job(job.ID)
		if result.State == "success" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result.State != "success" || restartCount.Load() != 1 {
		t.Fatalf("end-to-end result %+v, restart count %d", result, restartCount.Load())
	}
	if polls.Load() < 3 {
		t.Fatal("restart blocked agent heartbeats")
	}
	cancel()
	if e := <-finished; e != nil {
		t.Fatal(e)
	}
	// A durable started record left by an interrupted process is reported, never re-executed.
	interrupted, e := hubStore.CreateJob(id, testContainerID, "restart", "interrupted", "postgres", "test")
	if e != nil {
		t.Fatal(e)
	}
	leased, e := hubStore.Lease(id)
	if e != nil || leased == nil {
		t.Fatal("lease", e)
	}
	_, e = agentStore.DB.Exec(`INSERT INTO agent_jobs(id,state,result) VALUES(?,'started',?)`, interrupted.ID, encode(interrupted))
	if e != nil {
		t.Fatal(e)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	finished2 := make(chan error, 1)
	go func() { finished2 <- runAgent(ctx2, agentStore) }()
	defer func() {
		cancel2()
		select {
		case <-finished2:
		case <-time.After(5 * time.Second):
			t.Error("restarted agent did not stop")
		}
	}()
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result, _ = hubStore.Job(interrupted.ID)
		if result.State == "unknown" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result.State != "unknown" || restartCount.Load() != 1 {
		t.Fatal("interrupted operation replayed", result.State, restartCount.Load())
	}
}

func TestLinuxCollectorAndUnixDocker(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			respond(w, 200, []map[string]any{{"Id": testContainerID, "Names": []string{"/postgres"}}})
			return
		}
		respond(w, 200, map[string]any{"Id": testContainerID, "Name": "/postgres", "Config": map[string]string{"Image": "postgres:17"}, "State": map[string]any{"Status": "running", "Running": true}})
	})}
	go server.Serve(listener)
	defer server.Close()
	c := defaultServer()
	c.Name = "linux"
	c.DockerSocket = socket
	col := collector{}
	first := col.collect(context.Background(), c)
	if first.MemoryTotal == 0 || first.Cores == 0 || len(first.Disks) != 1 || len(first.Containers) != 1 || first.Containers[0].Name != "postgres" {
		t.Fatalf("invalid Linux sample %+v", first)
	}
	second := col.collect(context.Background(), c)
	if second.CPU < 0 || second.CPU > 100 {
		t.Fatal("CPU outside range")
	}
}
func TestRealDockerRestartAndLogs(t *testing.T) {
	if os.Getenv("SYSTEM_MONITOR_DOCKER_TEST") != "1" {
		t.Skip("real Docker integration enabled in Linux CI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := "system-monitor-ci-" + strings.ToLower(randomToken()[:10])
	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "--name", name, "--health-cmd", "exit 0", "--health-interval=1s", "--health-timeout=1s", "alpine:3.21", "sleep", "120")
	out, e := cmd.Output()
	if e != nil {
		t.Fatalf("docker run: %s %v", out, e)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	id := strings.TrimSpace(string(out))
	d := newDocker("/var/run/docker.sock")
	defer d.client.CloseIdleConnections()
	before, e := d.inspect(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	c := defaultServer()
	c.Containers[name] = ContainerRule{Monitor: true, Restart: true}
	a := defaultConfig(t.TempDir()).Access
	job := Job{ID: "real-docker", ContainerID: id, ContainerName: name, Action: "restart", Timeout: 15, HealthWait: 20}
	result := executeDocker(ctx, d, c, a, job)
	if result.State != "success" {
		t.Fatalf("restart: %+v", result)
	}
	after, e := d.inspect(ctx, id)
	if e != nil || !after.State.Running || after.State.Health == nil || after.State.Health.Status != "healthy" || before.State.StartedAt == after.State.StartedAt {
		t.Fatal("restart did not produce healthy new start", e)
	}
	job.Action = "logs"
	if result = executeDocker(ctx, d, c, a, job); result.State != "success" {
		t.Fatal("real Docker log request failed", result.Result)
	}
}
