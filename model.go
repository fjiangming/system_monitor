package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const version = "0.1.0"

var buildRevision = "dev"

type Config struct {
	Service       ServiceConfig      `json:"service"`
	Access        AccessConfig       `json:"access"`
	Alerts        AlertConfig        `json:"alerts"`
	Notifications NotificationConfig `json:"notifications"`
	Storage       StorageConfig      `json:"storage"`
}
type ServiceConfig struct {
	SiteName       string `json:"siteName"`
	PublicURL      string `json:"publicURL"`
	ListenAddress  string `json:"listenAddress"`
	ListenPort     int    `json:"listenPort"`
	Timezone       string `json:"timezone"`
	TLSMode        string `json:"tlsMode"`
	TrustedProxies string `json:"trustedProxies"`
	RequestTimeout int    `json:"requestTimeout"`
	MaxOps         int    `json:"maxOps"`
	PublicStatus   bool   `json:"publicStatus"`
}
type AccessConfig struct {
	SessionHours    int    `json:"sessionHours"`
	IdleMinutes     int    `json:"idleMinutes"`
	LoginFailures   int    `json:"loginFailures"`
	LockMinutes     int    `json:"lockMinutes"`
	AllowIPs        string `json:"allowIPs"`
	ManualRestart   bool   `json:"manualRestart"`
	AllowLogs       bool   `json:"allowLogs"`
	DatabaseConfirm bool   `json:"databaseConfirm"`
	RequireReason   bool   `json:"requireReason"`
	CommandTimeout  int    `json:"commandTimeout"`
	HealthWait      int    `json:"healthWait"`
	RestartCooldown int    `json:"restartCooldown"`
}
type AlertConfig struct {
	Interval           int `json:"interval"`
	Failures           int `json:"failures"`
	DiskWarn           int `json:"diskWarn"`
	DiskCritical       int `json:"diskCritical"`
	CPU                int `json:"cpu"`
	Memory             int `json:"memory"`
	Sustained          int `json:"sustained"`
	Inode              int `json:"inode"`
	RestartCount       int `json:"restartCount"`
	RestartWindow      int `json:"restartWindow"`
	RepeatMinutes      int `json:"repeatMinutes"`
	MaintenanceMinutes int `json:"maintenanceMinutes"`
}
type NotificationConfig struct {
	Channel     string `json:"channel"`
	Name        string `json:"name"`
	Notices     bool   `json:"notices"`
	Recovery    bool   `json:"recovery"`
	Actions     bool   `json:"actions"`
	GroupWindow int    `json:"groupWindow"`
	Timeout     int    `json:"timeout"`
	Retries     int    `json:"retries"`
	BarkServer  string `json:"barkServer"`
	SMTPHost    string `json:"smtpHost"`
	SMTPPort    int    `json:"smtpPort"`
	SMTPTLS     string `json:"smtpTLS"`
	SMTPUser    string `json:"smtpUser"`
	SMTPFrom    string `json:"smtpFrom"`
	SMTPTo      string `json:"smtpTo"`
}
type StorageConfig struct {
	DataDirectory  string `json:"dataDirectory"`
	MetricsDays    int    `json:"metricsDays"`
	EventsDays     int    `json:"eventsDays"`
	AuditDays      int    `json:"auditDays"`
	LogDays        int    `json:"logDays"`
	LogCap         int    `json:"logCap"`
	LogFileCap     int    `json:"logFileCap"`
	DatabaseCap    int    `json:"databaseCap"`
	MinFree        int    `json:"minFree"`
	DataBackup     bool   `json:"dataBackup"`
	DataBackupTime string `json:"dataBackupTime"`
	DataBackupKeep int    `json:"dataBackupKeep"`
}
type ContainerRule struct {
	Monitor  bool `json:"monitor"`
	Restart  bool `json:"restart"`
	Database bool `json:"database"`
}
type HTTPCheck struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    int    `json:"status"`
	Timeout   int    `json:"timeout"`
	Location  string `json:"location"`
	VerifyTLS bool   `json:"verifyTLS"`
	Container string `json:"container"`
}
type BackupConfig struct {
	Enabled        bool   `json:"enabled"`
	Unit           string `json:"unit"`
	ReportPattern  string `json:"reportPattern"`
	Start          string `json:"start"`
	Deadline       string `json:"deadline"`
	MaxMinutes     int    `json:"maxMinutes"`
	MaxAgeHours    int    `json:"maxAgeHours"`
	RequireHealthy bool   `json:"requireHealthy"`
}
type NetworkConfig struct {
	Interfaces string `json:"interfaces"`
	Automatic  bool   `json:"automatic"`
}
type ServerConfig struct {
	Name             string                   `json:"name"`
	Region           string                   `json:"region"`
	Address          string                   `json:"address"`
	Enabled          bool                     `json:"enabled"`
	Heartbeat        int                      `json:"heartbeat"`
	HeartbeatTimeout int                      `json:"heartbeatTimeout"`
	CommandPoll      int                      `json:"commandPoll"`
	VerifyTLS        bool                     `json:"verifyTLS"`
	Proxy            string                   `json:"proxy"`
	DockerSocket     string                   `json:"dockerSocket"`
	AutoDiscover     bool                     `json:"autoDiscover"`
	MonitorNew       bool                     `json:"monitorNew"`
	Disks            string                   `json:"disks"`
	Units            string                   `json:"units"`
	Containers       map[string]ContainerRule `json:"containers"`
	Checks           []HTTPCheck              `json:"checks"`
	Backup           BackupConfig             `json:"backup"`
	Network          NetworkConfig            `json:"network"`
}
type Server struct {
	ID               string          `json:"id"`
	Config           ServerConfig    `json:"config"`
	Revision         int64           `json:"revision"`
	LastSeen         int64           `json:"lastSeen"`
	SampleAt         int64           `json:"sampleAt"`
	Online           bool            `json:"online"`
	MaintenanceUntil int64           `json:"maintenanceUntil"`
	Snapshot         Snapshot        `json:"snapshot"`
	NetworkStatus    NetworkStatus   `json:"networkStatus"`
	AgentUpdate      AgentUpdateView `json:"agentUpdate"`
}
type Container struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Image             string  `json:"image"`
	State             string  `json:"state"`
	Health            string  `json:"health"`
	StartedAt         string  `json:"startedAt"`
	Restarts          int     `json:"restarts"`
	Database          bool    `json:"database"`
	CPUPercent        float64 `json:"cpuPercent"`
	CPUReady          bool    `json:"cpuReady"`
	MemoryUsage       uint64  `json:"memoryUsage"`
	MemoryLimit       uint64  `json:"memoryLimit"`
	MemoryPercent     float64 `json:"memoryPercent"`
	MemoryHostPercent float64 `json:"memoryHostPercent"`
	MemoryReady       bool    `json:"memoryReady"`
}
type Disk struct {
	Path      string  `json:"path"`
	Total     uint64  `json:"total"`
	Free      uint64  `json:"free"`
	InodeFree float64 `json:"inodeFree"`
}
type Unit struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	SubState string `json:"subState"`
}
type CheckResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status int    `json:"status"`
	Millis int64  `json:"millis"`
	Error  string `json:"error"`
	At     int64  `json:"at"`
}
type BackupResult struct {
	SuccessAt       int64  `json:"successAt"`
	StartedAt       int64  `json:"startedAt"`
	Running         bool   `json:"running"`
	Failed          bool   `json:"failed"`
	ServicesHealthy bool   `json:"servicesHealthy"`
	Error           string `json:"error"`
}
type NetworkInterface struct {
	Name                     string  `json:"name"`
	Ready                    bool    `json:"ready"`
	ReceiveBytesPerSecond    float64 `json:"receiveBytesPerSecond"`
	TransmitBytesPerSecond   float64 `json:"transmitBytesPerSecond"`
	ReceivePacketsPerSecond  float64 `json:"receivePacketsPerSecond"`
	TransmitPacketsPerSecond float64 `json:"transmitPacketsPerSecond"`
	ReceiveDroppedPerSecond  float64 `json:"receiveDroppedPerSecond"`
	TransmitDroppedPerSecond float64 `json:"transmitDroppedPerSecond"`
	ReceiveErrorsPerSecond   float64 `json:"receiveErrorsPerSecond"`
	TransmitErrorsPerSecond  float64 `json:"transmitErrorsPerSecond"`
}
type Snapshot struct {
	Hostname     string             `json:"hostname"`
	OS           string             `json:"os"`
	Cores        int                `json:"cores"`
	CPU          float64            `json:"cpu"`
	MemoryTotal  uint64             `json:"memoryTotal"`
	MemoryUsed   uint64             `json:"memoryUsed"`
	Uptime       int64              `json:"uptime"`
	Disks        []Disk             `json:"disks"`
	Network      []NetworkInterface `json:"network"`
	Containers   []Container        `json:"containers"`
	Units        []Unit             `json:"units"`
	Checks       []CheckResult      `json:"checks"`
	Backup       BackupResult       `json:"backup"`
	Errors       []string           `json:"errors"`
	At           int64              `json:"at"`
	AgentVersion string             `json:"agentVersion"`
	Agent        *AgentInfo         `json:"agent,omitempty"`
}
type Job struct {
	ID            string `json:"id"`
	ServerID      string `json:"serverID"`
	ContainerID   string `json:"containerID"`
	ContainerName string `json:"containerName"`
	Action        string `json:"action"`
	State         string `json:"state"`
	Result        string `json:"result"`
	Logs          string `json:"logs,omitempty"`
	Remark        string `json:"remark"`
	Actor         string `json:"actor"`
	Created       int64  `json:"created"`
	Deadline      int64  `json:"deadline"`
	Timeout       int    `json:"timeout"`
	HealthWait    int    `json:"healthWait"`
}
type Event struct {
	ID       int64  `json:"id"`
	ServerID string `json:"serverID"`
	Kind     string `json:"kind"`
	Level    string `json:"level"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Actor    string `json:"actor"`
	At       int64  `json:"at"`
}
type Alert struct {
	Key          string `json:"key"`
	ServerID     string `json:"serverID"`
	Level        string `json:"level"`
	Title        string `json:"title"`
	Detail       string `json:"detail"`
	Since        int64  `json:"since"`
	Active       bool   `json:"active"`
	Acknowledged bool   `json:"acknowledged"`
}
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}

func defaultConfig(root string) Config {
	return Config{
		Service:       ServiceConfig{"轻巡", "", "0.0.0.0", 18080, "Asia/Shanghai", "proxy", "127.0.0.1\n::1", 30, 2, false},
		Access:        AccessConfig{12, 30, 5, 15, "", true, true, true, false, 30, 60, 60},
		Alerts:        AlertConfig{30, 3, 20, 10, 85, 90, 300, 10, 3, 600, 30, 30},
		Notifications: NotificationConfig{Channel: "feishu", Name: "值班通知", Recovery: true, Actions: true, GroupWindow: 30, Timeout: 10, Retries: 3, BarkServer: "https://api.day.app", SMTPPort: 465, SMTPTLS: "implicit"},
		Storage:       StorageConfig{root, 7, 30, 90, 7, 200, 10, 512, 1, true, "04:30", 7},
	}
}
func defaultServer() ServerConfig {
	return ServerConfig{Enabled: true, Heartbeat: 30, HeartbeatTimeout: 90, CommandPoll: 3, VerifyTLS: true, DockerSocket: "/var/run/docker.sock", AutoDiscover: true, Disks: "/", Containers: map[string]ContainerRule{}, Checks: []HTTPCheck{}, Backup: BackupConfig{Start: "03:00", Deadline: "04:00", MaxMinutes: 30, MaxAgeHours: 26, RequireHealthy: true}, Network: NetworkConfig{Automatic: true}}
}
func bounds(label string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("%s需在 %d–%d 之间", label, min, max)
	}
	return nil
}
func clockValid(s string) bool { _, err := time.Parse("15:04", s); return err == nil }
func validHTTP(s string, optional bool) bool {
	if s == "" {
		return optional
	}
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && u.Fragment == ""
}
func validateNetworks(s string) error {
	for _, line := range lines(s) {
		if net.ParseIP(line) == nil {
			if _, _, e := net.ParseCIDR(line); e != nil {
				return fmt.Errorf("无效的 IP / CIDR：%s", line)
			}
		}
	}
	return nil
}
func lines(s string) []string {
	var out []string
	for _, v := range strings.Split(s, "\n") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func (c Config) Validate() error {
	if len(c.Service.SiteName) == 0 || len(c.Service.SiteName) > 120 {
		return fmt.Errorf("站点名称需为 1–120 字节")
	}
	if net.ParseIP(c.Service.ListenAddress) == nil {
		return fmt.Errorf("监听地址必须是 IP 地址")
	}
	if !validHTTP(c.Service.PublicURL, true) {
		return fmt.Errorf("对外访问地址无效")
	}
	if _, e := time.LoadLocation(c.Service.Timezone); e != nil {
		return fmt.Errorf("时区无效")
	}
	if c.Service.TLSMode != "proxy" && c.Service.TLSMode != "builtin" {
		return fmt.Errorf("HTTPS 模式无效")
	}
	if e := validateNetworks(c.Service.TrustedProxies); e != nil {
		return e
	}
	if e := validateNetworks(c.Access.AllowIPs); e != nil {
		return e
	}
	for _, v := range []struct {
		n       string
		v, a, b int
	}{
		{"端口", c.Service.ListenPort, 1, 65535}, {"请求超时", c.Service.RequestTimeout, 5, 300}, {"并发操作", c.Service.MaxOps, 1, 10},
		{"会话期限", c.Access.SessionHours, 1, 720}, {"空闲期限", c.Access.IdleMinutes, 1, 1440}, {"登录失败次数", c.Access.LoginFailures, 3, 20}, {"锁定时间", c.Access.LockMinutes, 1, 1440},
		{"命令超时", c.Access.CommandTimeout, 5, 300}, {"健康等待", c.Access.HealthWait, 5, 300}, {"重启冷却", c.Access.RestartCooldown, 0, 3600},
		{"巡检间隔", c.Alerts.Interval, 10, 300}, {"连续失败", c.Alerts.Failures, 1, 10}, {"磁盘预警", c.Alerts.DiskWarn, 1, 100000}, {"磁盘严重", c.Alerts.DiskCritical, 1, 100000},
		{"CPU", c.Alerts.CPU, 1, 100}, {"内存", c.Alerts.Memory, 1, 100}, {"持续时间", c.Alerts.Sustained, 10, 1800}, {"inode", c.Alerts.Inode, 1, 99},
		{"重启次数", c.Alerts.RestartCount, 1, 30}, {"统计窗口", c.Alerts.RestartWindow, 30, 3600}, {"重复提醒", c.Alerts.RepeatMinutes, 1, 1440}, {"维护时长", c.Alerts.MaintenanceMinutes, 1, 1440},
		{"通知合并窗口", c.Notifications.GroupWindow, 0, 300}, {"发送超时", c.Notifications.Timeout, 1, 60}, {"通知重试", c.Notifications.Retries, 0, 10},
		{"采样保留", c.Storage.MetricsDays, 1, 365}, {"事件保留", c.Storage.EventsDays, 1, 365}, {"审计保留", c.Storage.AuditDays, 1, 730}, {"日志保留", c.Storage.LogDays, 1, 90},
		{"日志总量", c.Storage.LogCap, 10, 2048}, {"单份日志", c.Storage.LogFileCap, 1, 100}, {"数据库上限", c.Storage.DatabaseCap, 32, 10240}, {"磁盘预留", c.Storage.MinFree, 1, 100}, {"数据备份份数", c.Storage.DataBackupKeep, 1, 30},
	} {
		if e := bounds(v.n, v.v, v.a, v.b); e != nil {
			return e
		}
	}
	if c.Alerts.DiskCritical >= c.Alerts.DiskWarn {
		return fmt.Errorf("严重空间阈值必须低于预警阈值")
	}
	if !filepath.IsAbs(c.Storage.DataDirectory) || c.Storage.LogFileCap > c.Storage.LogCap || !clockValid(c.Storage.DataBackupTime) {
		return fmt.Errorf("数据目录、日志容量或备份时间无效")
	}
	if !strings.Contains("|feishu|wecom|bark|smtp|webhook|", "|"+c.Notifications.Channel+"|") {
		return fmt.Errorf("通知渠道无效")
	}
	if !validHTTP(c.Notifications.BarkServer, false) {
		return fmt.Errorf("Bark 服务地址无效")
	}
	if c.Notifications.SMTPTLS != "implicit" && c.Notifications.SMTPTLS != "starttls" {
		return fmt.Errorf("SMTP 必须使用 TLS 或 STARTTLS")
	}
	if c.Notifications.SMTPPort < 1 || c.Notifications.SMTPPort > 65535 {
		return fmt.Errorf("SMTP 端口无效")
	}
	return nil
}

var unitPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]{0,180}\.service$`)
var containerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

func validContainerSample(c Container) bool {
	for _, value := range []float64{c.CPUPercent, c.MemoryPercent, c.MemoryHostPercent} {
		if value < 0 || value > 100 || math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func (s ServerConfig) Validate() error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > 128 || len(s.Region) > 128 || len(s.Address) > 256 {
		return fmt.Errorf("服务器名称或地址无效")
	}
	if s.Heartbeat < 10 || s.Heartbeat > 300 || s.CommandPoll < 1 || s.CommandPoll > 60 || s.HeartbeatTimeout <= s.Heartbeat*2 || s.HeartbeatTimeout > 1800 {
		return fmt.Errorf("心跳间隔为 10–300 秒，失联时间应大于两次心跳")
	}
	if !strings.HasPrefix(s.DockerSocket, "/") || len(s.DockerSocket) > 512 {
		return fmt.Errorf("Docker Socket 必须为绝对路径")
	}
	if !validHTTP(s.Proxy, true) {
		return fmt.Errorf("出站代理地址无效")
	}
	if len(s.Network.Interfaces) > 512 || len(lines(s.Network.Interfaces)) > 16 {
		return fmt.Errorf("最多选择 16 张网卡")
	}
	interfaces := map[string]bool{}
	for _, name := range lines(s.Network.Interfaces) {
		if !interfaceNamePattern.MatchString(name) || interfaces[name] {
			return fmt.Errorf("网卡名称无效或重复")
		}
		interfaces[name] = true
	}
	if len(lines(s.Disks)) > 12 || len(lines(s.Units)) > 24 || len(s.Checks) > 24 || len(s.Containers) > 500 {
		return fmt.Errorf("监控对象超过单服务器限制")
	}
	for name := range s.Containers {
		if !containerNamePattern.MatchString(name) {
			return fmt.Errorf("容器名称无效")
		}
	}
	for _, p := range lines(s.Disks) {
		if !strings.HasPrefix(p, "/") || len(p) > 512 {
			return fmt.Errorf("磁盘路径无效")
		}
	}
	for _, u := range lines(s.Units) {
		if !unitPattern.MatchString(u) {
			return fmt.Errorf("systemd 服务名无效")
		}
	}
	ids := map[string]bool{}
	for _, c := range s.Checks {
		if c.ID == "" || ids[c.ID] || len(c.ID) > 64 || c.Name == "" || len(c.Name) > 128 || !validHTTP(c.URL, false) || c.Status < 100 || c.Status > 599 || c.Timeout < 1 || c.Timeout > 60 || (c.Location != "hub" && c.Location != "agent") {
			return fmt.Errorf("HTTP 检查参数无效")
		}
		ids[c.ID] = true
	}
	if s.Backup.Enabled {
		if !unitPattern.MatchString(s.Backup.Unit) || !strings.HasPrefix(s.Backup.ReportPattern, "/") || len(s.Backup.ReportPattern) > 512 || !clockValid(s.Backup.Start) || !clockValid(s.Backup.Deadline) || s.Backup.MaxMinutes < 1 || s.Backup.MaxMinutes > 720 || s.Backup.MaxAgeHours < 1 || s.Backup.MaxAgeHours > 168 {
			return fmt.Errorf("备份监控参数无效")
		}
	}
	return nil
}
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }
func unixNow() int64      { return time.Now().Unix() }
