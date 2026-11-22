//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type collector struct {
	total, idle uint64
	network     networkSampler
}

func diskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	e := syscall.Statfs(path, &st)
	return st.Bavail * uint64(st.Bsize), e
}
func (p *collector) collect(ctx context.Context, c ServerConfig) Snapshot {
	s := Snapshot{OS: "linux", Cores: runtime.NumCPU(), AgentVersion: version, At: unixNow(), Containers: []Container{}, Disks: []Disk{}, Units: []Unit{}, Checks: []CheckResult{}, Errors: []string{}}
	s.Hostname, _ = os.Hostname()
	var networkErr error
	s.Network, networkErr = p.network.collect(c)
	if networkErr != nil {
		s.Errors = append(s.Errors, networkErr.Error())
	}
	b, e := os.ReadFile("/proc/stat")
	if e == nil {
		fields := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
		var total, idle uint64
		for i, v := range fields[1:] {
			n, _ := strconv.ParseUint(v, 10, 64)
			if i < 8 {
				total += n
			}
			if i == 3 || i == 4 {
				idle += n
			}
		}
		if p.total > 0 && total > p.total {
			s.CPU = 100 * (1 - float64(idle-p.idle)/float64(total-p.total))
		}
		p.total, p.idle = total, idle
	} else {
		s.Errors = append(s.Errors, "CPU 采集失败")
	}
	b, e = os.ReadFile("/proc/meminfo")
	if e == nil {
		values := map[string]uint64{}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				n, _ := strconv.ParseUint(f[1], 10, 64)
				values[strings.TrimSuffix(f[0], ":")] = n * 1024
			}
		}
		s.MemoryTotal = values["MemTotal"]
		available := values["MemAvailable"]
		if available == 0 {
			available = values["MemFree"] + values["Buffers"] + values["Cached"]
		}
		if available < s.MemoryTotal {
			s.MemoryUsed = s.MemoryTotal - available
		}
	}
	b, _ = os.ReadFile("/proc/uptime")
	if f := strings.Fields(string(b)); len(f) > 0 {
		n, _ := strconv.ParseFloat(f[0], 64)
		s.Uptime = int64(n)
	}
	for _, path := range lines(c.Disks) {
		var st syscall.Statfs_t
		if syscall.Statfs(path, &st) != nil {
			s.Errors = append(s.Errors, "磁盘采集失败："+path)
			continue
		}
		inodes := 100.0
		if st.Files > 0 {
			inodes = float64(st.Ffree) / float64(st.Files) * 100
		}
		s.Disks = append(s.Disks, Disk{Path: path, Total: st.Blocks * uint64(st.Bsize), Free: st.Bavail * uint64(st.Bsize), InodeFree: inodes})
	}
	d := newDocker(c.DockerSocket)
	defer d.client.CloseIdleConnections()
	s.Containers, e = d.containers(ctx, c)
	if e != nil {
		s.Errors = append(s.Errors, "Docker 采集失败")
	}
	for _, u := range lines(c.Units) {
		v, err := readUnit(ctx, u)
		s.Units = append(s.Units, v)
		if err != nil {
			s.Errors = append(s.Errors, "systemd 采集失败："+u)
		}
	}
	for _, check := range c.Checks {
		if check.Location == "agent" {
			s.Checks = append(s.Checks, runHTTP(ctx, check, c.Proxy))
		}
	}
	if c.Backup.Enabled {
		s.Backup = readBackup(ctx, c.Backup)
	}
	return s
}
func readUnit(ctx context.Context, name string) (Unit, error) {
	v := Unit{Name: name, State: "unknown"}
	if !unitPattern.MatchString(name) {
		return v, fmt.Errorf("invalid unit")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveState,SubState", "--", name).Output()
	if e != nil {
		return v, e
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, val, _ := strings.Cut(line, "=")
		if k == "ActiveState" {
			v.State = val
		}
		if k == "SubState" {
			v.SubState = val
		}
	}
	return v, nil
}
func readBackup(ctx context.Context, c BackupConfig) BackupResult {
	r := BackupResult{}
	u, e := readUnit(ctx, c.Unit)
	if e != nil {
		r.Error = "备份服务状态不可读取"
	} else {
		r.Running = u.State == "activating" || u.State == "active"
		r.Failed = u.State == "failed"
	}
	if r.Running {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		b, _ := exec.CommandContext(ctx, "systemctl", "show", "--property=ActiveEnterTimestampMonotonic,InactiveExitTimestampMonotonic", "--", c.Unit).Output()
		uptimeData, _ := os.ReadFile("/proc/uptime")
		uptimeFields := strings.Fields(string(uptimeData))
		uptime := 0.0
		if len(uptimeFields) > 0 {
			uptime, _ = strconv.ParseFloat(uptimeFields[0], 64)
		}
		for _, line := range strings.Split(string(b), "\n") {
			_, v, _ := strings.Cut(line, "=")
			monotonic, _ := strconv.ParseFloat(v, 64)
			if uptime > 0 && monotonic > 0 {
				started := unixNow() - int64(uptime) + int64(monotonic/1e6)
				if started > r.StartedAt {
					r.StartedAt = started
				}
			}
		}
	}
	files, e := filepath.Glob(c.ReportPattern)
	if e != nil {
		r.Error = "报告路径无效"
		return r
	}
	if len(files) > 1000 {
		r.Error = "报告数量超过 1000"
		return r
	}
	var latest time.Time
	var best string
	for _, f := range files {
		info, e := os.Stat(f)
		if e == nil && info.Mode().IsRegular() && info.Size() <= 65536 && info.ModTime().After(latest) {
			latest = info.ModTime()
			best = f
		}
	}
	if best == "" {
		r.Error = "没有可读取的备份报告"
		return r
	}
	b, e := os.ReadFile(best)
	if e != nil {
		r.Error = "报告读取失败"
		return r
	}
	var report struct {
		Completed       bool   `json:"completed"`
		ServicesHealthy bool   `json:"services_healthy"`
		FinishedAt      string `json:"finished_at"`
	}
	if json.Unmarshal(b, &report) != nil {
		r.Error = "报告 JSON 无效"
		return r
	}
	t, e := time.Parse(time.RFC3339, report.FinishedAt)
	if e != nil || t.After(time.Now().Add(time.Minute)) {
		r.Error = "报告完成时间无效"
		return r
	}
	r.ServicesHealthy = report.ServicesHealthy
	if report.Completed {
		r.SuccessAt = t.Unix()
	} else {
		r.Failed = true
	}
	return r
}
