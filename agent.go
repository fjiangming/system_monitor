package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"
)

type agentIdentity struct {
	Hub   string `json:"hub"`
	ID    string `json:"id"`
	Token string `json:"token"`
}
type agentReply struct {
	Config   ServerConfig `json:"config"`
	Access   AccessConfig `json:"access"`
	Job      *Job         `json:"job"`
	Timezone string       `json:"timezone"`
	Update   *AgentUpdate `json:"update,omitempty"`
}

func agentHTTPClient(c ServerConfig) (*http.Client, *http.Transport, error) {
	t := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: !c.VerifyTLS}}
	if c.Proxy != "" {
		p, e := url.Parse(c.Proxy)
		if e != nil {
			return nil, nil, e
		}
		t.Proxy = http.ProxyURL(p)
	}
	return &http.Client{Transport: t, CheckRedirect: noRedirect}, t, nil
}

func agentRequest(ctx context.Context, identity agentIdentity, path string, body any, output any, c ServerConfig) error {
	client, transport, e := agentHTTPClient(c)
	if e != nil {
		return e
	}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(identity.Hub, "/")+path, bytes.NewBufferString(encode(body)))
	if e != nil {
		return e
	}
	r.Header.Set("Content-Type", "application/json")
	if identity.Token != "" {
		r.Header.Set("Authorization", "Bearer "+identity.Token)
	}
	resp, e := client.Do(r)
	if e != nil {
		return errors.New("监控端连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("监控端 HTTP %d", resp.StatusCode)
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output)
}
func enrollAgent(ctx context.Context, s *Store, hub, id, code string, bootstrap ServerConfig) error {
	if runtime.GOOS != "linux" {
		return errors.New("探针仅支持 Linux")
	}
	if !validHTTP(hub, false) || id == "" || code == "" {
		return errors.New("请使用网页生成的接入命令")
	}
	identity := agentIdentity{Hub: hub, ID: id}
	var reply struct {
		Token string `json:"token"`
	}
	if e := agentRequest(ctx, identity, "/api/agent/enroll", map[string]string{"id": id, "code": code}, &reply, bootstrap); e != nil {
		return e
	}
	identity.Token = reply.Token
	if e := s.setMeta("agentConfig", encode(bootstrap)); e != nil {
		return e
	}
	_, _ = s.DB.Exec(`DELETE FROM agent_jobs`)
	return s.SetSecret("agent:identity", encode(identity))
}
func runAgent(ctx context.Context, s *Store) error {
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if runtime.GOOS != "linux" {
		return errors.New("探针仅支持 Linux")
	}
	var identity agentIdentity
	if json.Unmarshal([]byte(s.Secret("agent:identity")), &identity) != nil || identity.Token == "" {
		return errors.New("探针尚未接入，请使用网页接入命令")
	}
	// A started command is never replayed after a crash. Its uncertain result is reported instead.
	_, _ = s.DB.Exec(`UPDATE agent_jobs SET state='done',result=json_set(result,'$.state','unknown','$.result','探针在操作期间中断，请人工核对；没有重复重启') WHERE state='started'`)
	col := collector{}
	config := defaultServer()
	_ = json.Unmarshal([]byte(s.meta("agentConfig")), &config)
	var jobs sync.WaitGroup
	defer func() { cancelRun(); jobs.Wait() }()
	finished := make(chan struct{}, 1)
	updateDone := make(chan error, 1)
	updating := false
	var pending *Snapshot
	var nextSample time.Time
	var failures int
	for ctx.Err() == nil {
		reportAgentUpdateRecovery(ctx, s, identity, config)
		var reply agentReply
		body := map[string]any{"id": identity.ID, "snapshot": pending}
		if e := agentRequest(ctx, identity, "/api/agent/poll", body, &reply, config); e != nil {
			failures++
			if failures == 1 || failures%30 == 0 {
				fmt.Println("探针暂时无法连接监控端，将继续重连")
			}
			if !pause(ctx, 10*time.Second) {
				break
			}
			continue
		}
		failures = 0
		acknowledgeAgentUpdate(s, pending)
		pending = nil
		if encode(config) != encode(reply.Config) {
			nextSample = time.Time{}
		}
		config = reply.Config
		if e := config.Validate(); e != nil {
			return errors.New("监控端下发的探针设置无效")
		}
		_ = s.setMeta("agentConfig", encode(config))
		select {
		case <-finished:
			nextSample = time.Time{}
		default:
		}
		rows, e := s.DB.Query(`SELECT id,result FROM agent_jobs WHERE state='done' LIMIT 10`)
		type resultRow struct{ id, result string }
		done := []resultRow{}
		if e == nil {
			for rows.Next() {
				var row resultRow
				if rows.Scan(&row.id, &row.result) == nil {
					done = append(done, row)
				}
			}
			rows.Close()
		}
		for _, row := range done {
			var j Job
			if json.Unmarshal([]byte(row.result), &j) == nil && agentRequest(ctx, identity, "/api/agent/result", map[string]any{"id": identity.ID, "job": j}, nil, config) == nil {
				_, _ = s.DB.Exec(`UPDATE agent_jobs SET state='reported' WHERE id=?`, row.id)
			}
		}
		if reply.Update != nil && !updating {
			jobs.Wait()
			updating = true
			jobs.Add(1)
			go func(update AgentUpdate, settings ServerConfig) {
				defer jobs.Done()
				updateDone <- performAgentUpdate(ctx, s, identity, settings, update)
			}(*reply.Update, config)
		}
		if j := reply.Job; j != nil && !updating {
			if j.Deadline < unixNow() {
				j.State = "unknown"
				j.Result = "命令已超期"
				_ = agentRequest(ctx, identity, "/api/agent/result", map[string]any{"id": identity.ID, "job": j}, nil, config)
			} else {
				r, e := s.DB.Exec(`INSERT OR IGNORE INTO agent_jobs(id,state,result) VALUES(?,'started',?)`, j.ID, encode(j))
				if e == nil {
					n, _ := r.RowsAffected()
					if n == 1 {
						jobs.Add(1)
						go func(job Job, settings ServerConfig, access AccessConfig) {
							defer jobs.Done()
							d := newDocker(settings.DockerSocket)
							jobCtx, cancel := context.WithDeadline(ctx, time.Unix(job.Deadline, 0))
							result := executeDocker(jobCtx, d, settings, access, job)
							cancel()
							d.client.CloseIdleConnections()
							_, _ = s.DB.Exec(`UPDATE agent_jobs SET state='done',result=? WHERE id=?`, encode(result), job.ID)
							select {
							case finished <- struct{}{}:
							default:
							}
						}(*j, config, reply.Access)
					}
				}
			}
		}
		if config.Enabled && time.Now().After(nextSample) {
			sampleCtx, cancel := context.WithTimeout(ctx, time.Duration(min(config.HeartbeatTimeout/2, 60))*time.Second)
			sample := col.collect(sampleCtx, config)
			cancel()
			if sample.Agent != nil && !secureAgentUpdateHub(identity.Hub, config) {
				info := *sample.Agent
				info.Updatable = false
				info.Reason = "探针更新需要经过证书校验的 HTTPS 连接"
				sample.Agent = &info
			}
			pending = &sample
			nextSample = time.Now().Add(time.Duration(config.Heartbeat) * time.Second)
		}
		_, _ = s.DB.Exec(`DELETE FROM agent_jobs WHERE state='reported' AND rowid NOT IN (SELECT rowid FROM agent_jobs ORDER BY rowid DESC LIMIT 1000)`)
		pollTimer := time.NewTimer(time.Duration(config.CommandPoll) * time.Second)
		select {
		case <-ctx.Done():
			pollTimer.Stop()
		case e := <-updateDone:
			pollTimer.Stop()
			updating = false
			if errors.Is(e, errAgentUpdateRestart) {
				return e
			}
		case <-pollTimer.C:
		}
	}
	return nil
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
