package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type dockerClient struct{ client *http.Client }

func newDocker(socket string) *dockerClient {
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}, MaxIdleConnsPerHost: 8}
	return &dockerClient{client: &http.Client{Transport: t, CheckRedirect: noRedirect}}
}
func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
func (d *dockerClient) request(ctx context.Context, method, path string, limit int64) ([]byte, error) {
	if method != "POST" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	r, e := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if e != nil {
		return nil, e
	}
	resp, e := d.client.Do(r)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("Docker response exceeds limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Docker HTTP %d", resp.StatusCode)
	}
	return b, nil
}

type dockerInspect struct {
	ID           string `json:"Id"`
	Name         string
	RestartCount int
	Config       struct {
		Image string
		Tty   bool
	}
	State struct {
		Status    string
		Running   bool
		StartedAt string
		Health    *struct{ Status string }
	}
}

var containerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

func (d *dockerClient) inspect(ctx context.Context, id string) (dockerInspect, error) {
	var v dockerInspect
	if !containerIDPattern.MatchString(id) {
		return v, errors.New("invalid container id")
	}
	b, e := d.request(ctx, "GET", "/containers/"+id+"/json", 1<<20)
	if e == nil {
		e = json.Unmarshal(b, &v)
	}
	return v, e
}
func isDatabase(name, image string) bool {
	s := strings.ToLower(name + " " + image)
	for _, k := range []string{"postgres", "mysql", "mariadb", "mongodb", "redis", "clickhouse", "mssql"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}
func (d *dockerClient) containers(ctx context.Context, c ServerConfig) ([]Container, error) {
	b, e := d.request(ctx, "GET", "/containers/json?all=1&limit=501", 4<<20)
	if e != nil {
		return nil, e
	}
	var list []struct {
		ID    string `json:"Id"`
		Names []string
	}
	if e = json.Unmarshal(b, &list); e != nil {
		return nil, e
	}
	if len(list) > 500 {
		return nil, errors.New("超过 500 个容器，请拆分监控范围")
	}
	out := []Container{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	var firstError error
	for _, v := range list {
		if !c.AutoDiscover {
			known := false
			for _, name := range v.Names {
				if _, ok := c.Containers[strings.TrimPrefix(name, "/")]; ok {
					known = true
				}
			}
			if !known {
				continue
			}
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return out, ctx.Err()
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			item, e := d.inspect(ctx, id)
			if e != nil {
				mu.Lock()
				if firstError == nil {
					firstError = e
				}
				mu.Unlock()
				return
			}
			name := strings.TrimPrefix(item.Name, "/")
			v := Container{ID: item.ID, Name: name, Image: item.Config.Image, State: item.State.Status, Health: "none", StartedAt: item.State.StartedAt, Restarts: item.RestartCount, Database: isDatabase(name, item.Config.Image)}
			if item.State.Health != nil {
				v.Health = item.State.Health.Status
			}
			mu.Lock()
			out = append(out, v)
			mu.Unlock()
		}(v.ID)
	}
	wg.Wait()
	return out, firstError
}
func runHTTP(ctx context.Context, c HTTPCheck, proxy string) CheckResult {
	result := CheckResult{ID: c.ID, Name: c.Name, At: unixNow()}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Timeout)*time.Second)
	defer cancel()
	t := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !c.VerifyTLS}, ResponseHeaderTimeout: time.Duration(c.Timeout) * time.Second}
	defer t.CloseIdleConnections()
	if proxy != "" {
		p, e := url.Parse(proxy)
		if e == nil {
			t.Proxy = http.ProxyURL(p)
		}
	}
	req, e := http.NewRequestWithContext(ctx, "GET", c.URL, nil)
	if e != nil {
		result.Error = "检查地址无效"
		return result
	}
	req.Header.Set("User-Agent", "system-monitor/"+version)
	resp, e := (&http.Client{Transport: t, CheckRedirect: noRedirect}).Do(req)
	result.Millis = time.Since(start).Milliseconds()
	if e != nil {
		result.Error = "连接失败或超时"
		return result
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	result.Status = resp.StatusCode
	result.OK = resp.StatusCode == c.Status
	if !result.OK {
		result.Error = fmt.Sprintf("期望 %d，实际 %d", c.Status, resp.StatusCode)
	}
	return result
}
func decodeDockerLogs(b []byte, tty bool) string {
	if tty {
		return string(bytes.ToValidUTF8(b, []byte("?")))
	}
	var out bytes.Buffer
	for len(b) >= 8 {
		n := int(binary.BigEndian.Uint32(b[4:8]))
		if n > len(b)-8 {
			n = len(b) - 8
		}
		if n < 0 {
			break
		}
		out.Write(b[8 : 8+n])
		b = b[8+n:]
	}
	return string(bytes.ToValidUTF8(out.Bytes(), []byte("?")))
}
func executeDocker(ctx context.Context, d *dockerClient, c ServerConfig, a AccessConfig, j Job) Job {
	result := j
	result.State = "failed"
	result.Logs = ""
	result.Result = "容器或操作权限已变化"
	item, e := d.inspect(ctx, j.ContainerID)
	if e != nil {
		result.Result = "无法核对当前容器"
		return result
	}
	if item.ID != j.ContainerID || strings.TrimPrefix(item.Name, "/") != j.ContainerName {
		return result
	}
	if j.Action == "logs" {
		if !a.AllowLogs {
			return result
		}
		b, e := d.request(ctx, "GET", "/containers/"+j.ContainerID+"/logs?stdout=1&stderr=1&tail=100", 32768)
		if e != nil {
			result.Result = "日志读取失败或超过 32 KiB"
			return result
		}
		result.State = "success"
		result.Logs = decodeDockerLogs(b, item.Config.Tty)
		result.Result = "最近 100 行日志"
		return result
	}
	rule := c.Containers[j.ContainerName]
	if j.Action != "restart" || !a.ManualRestart || !rule.Monitor || !rule.Restart {
		return result
	}
	opCtx, cancel := context.WithTimeout(ctx, time.Duration(j.Timeout)*time.Second)
	_, e = d.request(opCtx, "POST", fmt.Sprintf("/containers/%s/restart?t=%d", j.ContainerID, max(1, j.Timeout-2)), 4096)
	cancel()
	if e != nil {
		result.State = "unknown"
		result.Result = "重启请求结果未确认，请刷新容器状态；没有自动重试"
		return result
	}
	healthCtx, healthCancel := context.WithTimeout(ctx, time.Duration(j.HealthWait)*time.Second)
	defer healthCancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		state, e := d.inspect(healthCtx, j.ContainerID)
		if e == nil && state.ID == j.ContainerID && strings.TrimPrefix(state.Name, "/") == j.ContainerName && state.State.Running {
			configured := state.State.Health != nil
			ok := !configured || state.State.Health.Status == "healthy"
			for _, check := range c.Checks {
				if check.Container == j.ContainerName && check.Location == "agent" {
					configured = true
					if !runHTTP(healthCtx, check, c.Proxy).OK {
						ok = false
					}
				}
			}
			if ok {
				result.State = "success"
				result.Result = "容器已运行，健康检查通过"
				if !configured {
					result.State = "unchecked"
					result.Result = "容器已运行，未配置探针侧健康检查"
				}
				return result
			}
		}
		select {
		case <-healthCtx.Done():
			result.State = "failed"
			result.Result = "重启已提交，但等待期内未通过运行与健康检查"
			return result
		case <-ticker.C:
		}
	}
}
