package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestContainerMetricsUseHostRelativeCPUAndWorkingSetMemory(t *testing.T) {
	var current dockerStats
	current.CPUStats.CPUUsage.TotalUsage = 300
	current.CPUStats.SystemCPUUsage = 4000
	current.MemoryStats.Usage = 350
	current.MemoryStats.Limit = 1000
	current.MemoryStats.Stats.Cache = 50
	got := containerMetrics(current, dockerCPUStats{totalUsage: 100, systemUsage: 2000}, true, 2000)
	if !got.cpuReady || got.cpuPercent != 10 {
		t.Fatalf("unexpected host-relative CPU: %+v", got)
	}
	if !got.memoryReady || got.memoryUsage != 300 || got.memoryLimit != 1000 || got.memoryPercent != 30 || got.memoryHostPercent != 15 {
		t.Fatalf("unexpected memory metrics: %+v", got)
	}

	first := containerMetrics(current, dockerCPUStats{}, false, 2000)
	if first.cpuReady || !first.memoryReady {
		t.Fatalf("first sample should only lack a CPU rate: %+v", first)
	}
	reset := current
	reset.CPUStats.CPUUsage.TotalUsage = 10
	reset.CPUStats.SystemCPUUsage = 20
	if metrics := containerMetrics(reset, got.cpuStats, true, 2000); metrics.cpuReady {
		t.Fatal("counter reset reported a CPU rate")
	}
}

func TestDockerStatsUsesOneShotEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/containers/"+testContainerID+"/stats" || r.URL.Query().Get("stream") != "false" {
			http.NotFound(w, r)
			return
		}
		respond(w, 200, map[string]any{
			"cpu_stats": map[string]any{
				"cpu_usage":        map[string]uint64{"total_usage": 123},
				"system_cpu_usage": uint64(456),
			},
			"memory_stats": map[string]any{
				"usage": uint64(700), "limit": uint64(1000),
				"stats": map[string]uint64{"inactive_file": 100},
			},
		})
	}))
	defer server.Close()
	client := server.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(r)
	})
	d := &dockerClient{client: client}
	got, err := d.stats(context.Background(), testContainerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CPUStats.CPUUsage.TotalUsage != 123 || got.CPUStats.SystemCPUUsage != 456 || got.MemoryStats.Stats.InactiveFile != 100 {
		t.Fatalf("unexpected Docker stats: %+v", got)
	}
}

func fakeDocker(t *testing.T, health string, restartCode int, mismatch bool) (*dockerClient, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/restart"):
			count.Add(1)
			w.WriteHeader(restartCode)
		case strings.HasSuffix(r.URL.Path, "/json"):
			name := "/postgres"
			if mismatch {
				name = "/different"
			}
			state := map[string]any{"Status": "running", "Running": true}
			if health != "none" {
				state["Health"] = map[string]string{"Status": health}
			}
			respond(w, 200, map[string]any{"Id": testContainerID, "Name": name, "Config": map[string]any{"Image": "postgres:17"}, "State": state})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme = "http"
		r.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(r)
	})
	return &dockerClient{client: client}, count
}
func TestActualRestartProtocolAndHealth(t *testing.T) {
	for _, tc := range []struct {
		name, health, want string
		code               int
		mismatch           bool
	}{{"healthy", "healthy", "success", 204, false}, {"without-healthcheck", "none", "unchecked", 204, false}, {"uncertain-request", "healthy", "unknown", 500, false}, {"changed-target", "healthy", "failed", 204, true}} {
		t.Run(tc.name, func(t *testing.T) {
			d, count := fakeDocker(t, tc.health, tc.code, tc.mismatch)
			c := defaultServer()
			c.Containers["postgres"] = ContainerRule{Monitor: true, Restart: true}
			j := Job{ID: "job", ContainerID: testContainerID, ContainerName: "postgres", Action: "restart", Timeout: 5, HealthWait: 1}
			got := executeDocker(context.Background(), d, c, defaultConfig(t.TempDir()).Access, j)
			if got.State != tc.want {
				t.Fatalf("got %s: %s", got.State, got.Result)
			}
			want := int32(1)
			if tc.mismatch {
				want = 0
			}
			if count.Load() != want {
				t.Fatal("unexpected restart count")
			}
		})
	}
}
func TestRestartCannotBypassPermissionsOrUnhealthyCheck(t *testing.T) {
	d, count := fakeDocker(t, "unhealthy", 204, false)
	c := defaultServer()
	j := Job{ContainerID: testContainerID, ContainerName: "postgres", Action: "restart", Timeout: 5, HealthWait: 1}
	a := defaultConfig(t.TempDir()).Access
	if got := executeDocker(context.Background(), d, c, a, j); got.State != "failed" || count.Load() != 0 {
		t.Fatal("permission bypass")
	}
	c.Containers["postgres"] = ContainerRule{Monitor: true, Restart: true}
	if got := executeDocker(context.Background(), d, c, a, j); got.State != "failed" || count.Load() != 1 {
		t.Fatal("unhealthy was reported healthy")
	}
}
func TestDockerLogFramingAndHTTPRedirect(t *testing.T) {
	var framed []byte
	for _, line := range []string{"hello\n", "<script>not executable</script>\n"} {
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(line)))
		framed = append(framed, header...)
		framed = append(framed, line...)
	}
	if got := decodeDockerLogs(framed, false); got != "hello\n<script>not executable</script>\n" {
		t.Fatal(got)
	}
	if got := decodeDockerLogs([]byte("plain"), true); got != "plain" {
		t.Fatal(got)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "https://example.org/secret", 302) }))
	defer server.Close()
	check := HTTPCheck{ID: "check", URL: server.URL, Status: 200, Timeout: 2, VerifyTLS: true}
	result := runHTTP(context.Background(), check, "")
	if result.OK || result.Status != 302 {
		t.Fatal("redirect incorrectly treated as health", fmt.Sprint(result))
	}
}
func TestNotificationProviderRejectionAndSignature(t *testing.T) {
	s := testStore(t)
	var signature atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Monitor-Signature") != "" {
			signature.Store(true)
		}
		respond(w, 200, map[string]int{"errcode": 93000})
	}))
	defer server.Close()
	c, rev := s.Config()
	c.Notifications.Channel = "wecom"
	if e := s.SaveConfig(c, rev, map[string]string{"wecomEndpoint": server.URL}); e != nil {
		t.Fatal(e)
	}
	if e := s.notify(context.Background(), "test"); e == nil {
		t.Fatal("provider errcode ignored")
	}
	c, rev = s.Config()
	c.Notifications.Channel = "webhook"
	_ = s.SaveConfig(c, rev, map[string]string{"webhookEndpoint": server.URL, "webhookSecret": "sign-me"})
	if e := s.notify(context.Background(), "test"); e != nil {
		t.Fatal(e)
	}
	if !signature.Load() {
		t.Fatal("missing HMAC signature")
	}
}
