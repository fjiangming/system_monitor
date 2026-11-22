package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

func (s *Store) observe(server Server, key, level, title, detail string, bad bool, at int64, failures int, sustained int, c Config) {
	key = server.ID + ":" + key
	var since, last, notice int64
	var count int
	var active, ack bool
	_ = s.DB.QueryRow(`SELECT since,last_bad,bad_count,active,acknowledged,last_notice FROM alerts WHERE k=?`, key).Scan(&since, &last, &count, &active, &ack, &notice)
	if !bad {
		if active {
			s.Event(server.ID, "recovery", "success", title+" · 已恢复", detail, "")
			if c.Notifications.Notices && c.Notifications.Recovery && server.MaintenanceUntil < unixNow() {
				s.QueueNotice(server.Config.Name + " · 已恢复\n" + title)
			}
		}
		_, _ = s.DB.Exec(`DELETE FROM alerts WHERE k=?`, key)
		return
	}
	if since == 0 {
		since = at
	}
	if at > last {
		count++
		last = at
	}
	wasActive := active
	active = active || (count >= failures && at-since >= int64(sustained))
	if active && !wasActive {
		s.Event(server.ID, "alert", level, title, detail, "")
	}
	if active && c.Notifications.Notices && server.MaintenanceUntil < unixNow() && (!wasActive || notice == 0 || (!ack && unixNow()-notice >= int64(c.Alerts.RepeatMinutes)*60)) {
		s.QueueNotice(server.Config.Name + " · " + title + "\n" + detail)
		notice = unixNow()
	}
	_, _ = s.DB.Exec(`INSERT INTO alerts(k,server_id,level,title,detail,since,last_bad,bad_count,active,acknowledged,last_notice) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(k) DO UPDATE SET level=excluded.level,title=excluded.title,detail=excluded.detail,last_bad=excluded.last_bad,bad_count=excluded.bad_count,active=excluded.active,last_notice=excluded.last_notice`, key, server.ID, level, title, detail, since, last, count, active, ack, notice)
}
func (s *Store) inspectServer(ctx context.Context, v Server, c Config) {
	if !v.Config.Enabled {
		_, _ = s.DB.Exec(`DELETE FROM alerts WHERE server_id=?`, v.ID)
		return
	}
	seen := map[string]bool{}
	now := unixNow()
	offline := !v.Online
	observe := func(key, level, title, detail string, bad bool, at int64, n, delay int) {
		seen[v.ID+":"+key] = true
		s.observe(v, key, level, title, detail, bad, at, n, delay, c)
	}
	observe("offline", "critical", "探针失联", "最后联系："+time.Unix(v.LastSeen, 0).Format(time.RFC3339), offline, now/int64(c.Alerts.Interval)*int64(c.Alerts.Interval), 1, 0)
	if offline {
		return
	}
	stale := v.SampleAt == 0 || now-v.SampleAt > int64(v.Config.HeartbeatTimeout)
	observe("stale", "warning", "状态采样已过期", "心跳在线，但未收到有效状态采样", stale, now/int64(c.Alerts.Interval)*int64(c.Alerts.Interval), 1, 0)
	if stale {
		return
	}
	at := v.SampleAt
	snap := v.Snapshot
	observe("collector", "warning", "探针采集异常", strings.Join(snap.Errors, "；"), len(snap.Errors) > 0, at, c.Alerts.Failures, 0)
	observe("cpu", "warning", "CPU 持续偏高", fmt.Sprintf("%.1f%%", snap.CPU), snap.CPU >= float64(c.Alerts.CPU), at, c.Alerts.Failures, c.Alerts.Sustained)
	mem := 0.0
	if snap.MemoryTotal > 0 {
		mem = float64(snap.MemoryUsed) / float64(snap.MemoryTotal) * 100
	}
	observe("memory", "warning", "内存持续偏高", fmt.Sprintf("%.1f%%", mem), mem >= float64(c.Alerts.Memory), at, c.Alerts.Failures, c.Alerts.Sustained)
	s.inspectNetwork(v, c, seen)
	for _, d := range snap.Disks {
		gib := float64(d.Free) / (1 << 30)
		level := "warning"
		if gib <= float64(c.Alerts.DiskCritical) {
			level = "critical"
		}
		observe("disk:"+d.Path, level, "磁盘可用空间不足 · "+d.Path, fmt.Sprintf("可用 %.2f GiB", gib), gib <= float64(c.Alerts.DiskWarn), at, c.Alerts.Failures, 0)
		observe("inode:"+d.Path, "critical", "inode 可用量不足 · "+d.Path, fmt.Sprintf("剩余 %.1f%%", d.InodeFree), d.InodeFree <= float64(c.Alerts.Inode), at, c.Alerts.Failures, 0)
	}
	containers := map[string]Container{}
	for _, x := range snap.Containers {
		containers[x.Name] = x
	}
	rules := map[string]ContainerRule{}
	for name, r := range v.Config.Containers {
		rules[name] = r
	}
	if v.Config.MonitorNew {
		for name := range containers {
			if _, ok := rules[name]; !ok {
				rules[name] = ContainerRule{Monitor: true}
			}
		}
	}
	for name, rule := range rules {
		if !rule.Monitor {
			continue
		}
		x, exists := containers[name]
		bad := !exists || x.State != "running" || x.Health == "unhealthy"
		detail := x.State + " / " + x.Health
		if !exists {
			detail = "未找到已配置的容器"
		}
		observe("container:"+name, "critical", "容器异常 · "+name, detail, bad, at, c.Alerts.Failures, 0)
		if exists {
			var before string
			_ = s.DB.QueryRow(`SELECT restarts FROM history WHERE server_id=? AND at>=? ORDER BY at LIMIT 1`, v.ID, at-int64(c.Alerts.RestartWindow)).Scan(&before)
			m := map[string]int{}
			_ = json.Unmarshal([]byte(before), &m)
			old, ok := m[x.ID]
			delta := 0
			if ok {
				delta = x.Restarts - old
			}
			observe("restarts:"+name, "warning", "容器频繁重启 · "+name, fmt.Sprintf("%d 秒内观测到 %d 次重启", c.Alerts.RestartWindow, delta), ok && delta >= c.Alerts.RestartCount, at, 1, 0)
		}
	}
	for _, u := range snap.Units {
		observe("unit:"+u.Name, "critical", "系统服务异常 · "+u.Name, u.State+" / "+u.SubState, u.State != "active", at, c.Alerts.Failures, 0)
	}
	results := map[string]CheckResult{}
	for _, r := range snap.Checks {
		results[r.ID] = r
	}
	for _, check := range v.Config.Checks {
		r, ok := results[check.ID]
		if check.Location == "hub" {
			var raw string
			ok = s.DB.QueryRow(`SELECT result FROM check_results WHERE server_id=? AND id=?`, v.ID, check.ID).Scan(&raw) == nil && json.Unmarshal([]byte(raw), &r) == nil
			if !ok {
				seen[v.ID+":http:"+check.ID] = true
				continue
			}
			stale := now-r.At > int64(max(c.Alerts.Interval*4, check.Timeout*2))
			observe("http-stale:"+check.ID, "warning", "HTTP 检查结果已过期 · "+check.Name, "尚未收到新的外部检查结果", stale, now/int64(c.Alerts.Interval)*int64(c.Alerts.Interval), 1, 0)
			if stale {
				seen[v.ID+":http:"+check.ID] = true
				continue
			}
		}
		detail := r.Error
		if !ok {
			detail = "探针未返回此检查结果"
		}
		sampleAt := r.At
		if sampleAt == 0 {
			sampleAt = at
		}
		observe("http:"+check.ID, "critical", "HTTP 检查失败 · "+check.Name, detail, !ok || !r.OK, sampleAt, c.Alerts.Failures, 0)
	}
	if b := v.Config.Backup; b.Enabled {
		r := snap.Backup
		loc, _ := time.LoadLocation(c.Service.Timezone)
		local := time.Now().In(loc)
		today := local.Format("2006-01-02")
		deadline, _ := time.ParseInLocation("2006-01-02 15:04", today+" "+b.Deadline, loc)
		start, _ := time.ParseInLocation("2006-01-02 15:04", today+" "+b.Start, loc)
		if !deadline.After(start) {
			deadline = deadline.AddDate(0, 0, 1)
		}
		if local.Before(start) {
			start = start.AddDate(0, 0, -1)
			deadline = deadline.AddDate(0, 0, -1)
		}
		expired := r.SuccessAt == 0 || now-r.SuccessAt > int64(b.MaxAgeHours)*3600
		missed := local.After(deadline) && r.SuccessAt < start.Unix()
		runningLong := r.Running && (r.StartedAt == 0 || now-r.StartedAt > int64(b.MaxMinutes)*60)
		bad := r.Failed || r.Error != "" || expired || missed || runningLong || (b.RequireHealthy && r.SuccessAt > 0 && !r.ServicesHealthy)
		detail := r.Error
		if detail == "" {
			detail = fmt.Sprintf("最后完成：%s；运行中：%t；恢复健康：%t", time.Unix(r.SuccessAt, 0).In(loc).Format(time.RFC3339), r.Running, r.ServicesHealthy)
		}
		observe("backup", "critical", "备份或服务恢复异常", detail, bad, at, c.Alerts.Failures, 0)
	}
	// Removing a target clears its state only after a fresh, complete observation.
	rows, e := s.DB.Query(`SELECT k FROM alerts WHERE server_id=?`, v.ID)
	obsolete := []string{}
	if e == nil {
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil && !seen[k] {
				obsolete = append(obsolete, k)
			}
		}
		rows.Close()
	}
	for _, k := range obsolete {
		_, _ = s.DB.Exec(`DELETE FROM alerts WHERE k=?`, k)
	}
}

func (s *Store) inspectNetwork(v Server, c Config, seen map[string]bool) {
	n := v.Config.Network
	if !n.Automatic {
		return
	}
	previous := s.previousNetworkStatus(v.ID)
	total, ready := networkTotals(v.Snapshot.Network)
	selected := lines(n.Interfaces)
	if len(selected) > 0 {
		names := map[string]bool{}
		for _, iface := range v.Snapshot.Network {
			names[iface.Name] = true
		}
		for _, name := range selected {
			ready = ready && names[name]
		}
		ready = ready && len(names) == len(selected)
	}
	seen[v.ID+":network:unavailable"] = true
	s.observe(v, "network:unavailable", "warning", "流量采样不可用", "尚无完整网卡速率，请检查网卡选择及探针版本", !ready, v.SampleAt, networkAutoFailures, 0, c)
	status := previous
	analysisFailed := false
	status.At = v.SampleAt
	status.State = "unknown"
	if ready {
		signature := networkSignature(v.Snapshot.Network)
		if !previous.Frozen || previous.Interfaces != signature {
			samples, e := s.networkReference(v.ID, signature, v.SampleAt)
			if e == nil {
				status = automaticNetworkBaseline(samples)
			} else {
				ready = false
				analysisFailed = true
			}
		} else {
			status.State = "normal"
		}
		status.Interfaces, status.At = signature, v.SampleAt
		if ready && status.State != "learning" {
			status = assessNetwork(status, previous, total, v.Config.Backup.Enabled && v.Snapshot.Backup.Running)
		}
	}
	if e := s.saveNetworkStatus(v, status, ready && !status.Frozen, total); e != nil {
		ready = false
		analysisFailed = true
	}
	seen[v.ID+":network:analysis"] = true
	s.observe(v, "network:analysis", "warning", "流量自动分析不可用", "请检查监控数据存储和历史采样", analysisFailed, v.SampleAt, networkAutoFailures, 0, c)
	detect := ready && status.State != "learning"
	names := strings.ReplaceAll(status.Interfaces, "\n", "、")
	for _, rule := range []struct {
		key, title, unit string
		value, baseline  float64
	}{
		{"receive", "入站流量持续异常", "Mbps", total.ReceiveBytesPerSecond * 8 / 1e6, status.Baseline.Receive * 8 / 1e6},
		{"transmit", "出站流量持续异常", "Mbps", total.TransmitBytesPerSecond * 8 / 1e6, status.Baseline.Transmit * 8 / 1e6},
		{"packets", "入站包速率持续异常", "包/秒", total.ReceivePacketsPerSecond, status.Baseline.Packets},
	} {
		key := "network:" + rule.key
		seen[v.ID+":"+key] = true
		if !detect {
			// Unknown rates interrupt a pending threshold but cannot establish recovery.
			_, _ = s.DB.Exec(`DELETE FROM alerts WHERE k=? AND active=0`, v.ID+":"+key)
			continue
		}
		detail := fmt.Sprintf("%s：当前 %.2f %s，平时约 %.2f %s；自动识别为持续偏高，请核查业务请求与网络状态", names, rule.value, rule.unit, rule.baseline, rule.unit)
		s.observe(v, key, "warning", rule.title, detail, networkWasElevated(status, rule.key), v.SampleAt, networkAutoFailures, networkAutoSustained, c)
	}
}

// External probes have their own bounded worker pool so slow endpoints cannot delay host alerts.
func httpProbeLoop(ctx context.Context, s *Store) {
	for ctx.Err() == nil {
		c, _ := s.Config()
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for _, server := range s.Servers() {
			if !server.Config.Enabled {
				continue
			}
			for _, check := range server.Config.Checks {
				if check.Location != "hub" {
					continue
				}
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					wg.Wait()
					return
				}
				wg.Add(1)
				go func(v Server, check HTTPCheck) {
					defer wg.Done()
					defer func() { <-sem }()
					r := runHTTP(ctx, check, "")
					if ctx.Err() != nil {
						return
					}
					current, e := s.Server(v.ID)
					if e != nil || current.Revision != v.Revision {
						return
					}
					_, _ = s.DB.Exec(`INSERT INTO check_results(server_id,id,result) VALUES(?,?,?) ON CONFLICT(server_id,id) DO UPDATE SET result=excluded.result`, v.ID, check.ID, encode(r))
				}(server, check)
			}
		}
		wg.Wait()
		if !pause(ctx, time.Duration(c.Alerts.Interval)*time.Second) {
			return
		}
	}
}
func monitorLoop(ctx context.Context, s *Store) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var next, housekeeping time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c, _ := s.Config()
			if time.Now().After(next) {
				for _, v := range s.Servers() {
					if ctx.Err() != nil {
						return
					}
					s.inspectServer(ctx, v, c)
				}
				next = time.Now().Add(time.Duration(c.Alerts.Interval) * time.Second)
			}
			s.ExpireJobs()
			if time.Now().After(housekeeping) {
				s.Prune()
				if e := s.BackupData(time.Now()); e != nil {
					s.Event("", "storage", "warning", "监控数据备份失败", "请检查数据目录空间与权限", "")
				}
				housekeeping = time.Now().Add(time.Minute)
			}
		}
	}
}
