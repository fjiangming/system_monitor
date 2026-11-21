package main

import (
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed web/*
var webFiles embed.FS

type hubAPI struct {
	s     *Store
	apply chan struct{}
	slots chan struct{}
}

func newHubAPI(s *Store, apply chan struct{}) http.Handler {
	return &hubAPI{s: s, apply: apply, slots: make(chan struct{}, 16)}
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decodeRequest(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("请求参数无效或过大")
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}
func (h *hubAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.URL.Path == "/healthz" {
		respond(w, 200, map[string]string{"status": "ok"})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "method not allowed", 405)
			return
		}
		sub, _ := fs.Sub(webFiles, "web")
		http.FileServer(http.FS(sub)).ServeHTTP(w, r)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		respond(w, 429, map[string]string{"error": "请求过多，请稍后重试"})
		return
	}
	c, _ := h.s.Config()
	path := r.URL.Path
	var user User
	csrf := ""
	fail := func(status int, e error) { respond(w, status, map[string]string{"error": e.Error()}) }
	bad := func(e error) { fail(400, e) }
	if strings.HasPrefix(path, "/api/agent/") {
		h.agent(w, r)
		return
	}
	if c.Access.AllowIPs != "" && !withinNetworks(remoteIP(r, c), c.Access.AllowIPs) {
		fail(403, errors.New("此 IP 不在访问范围"))
		return
	}
	cookie, _ := r.Cookie("monitor_session")
	if cookie != nil {
		user, csrf, _ = h.s.Session(cookie.Value)
	}
	if r.Method != "GET" {
		if r.Method != "POST" {
			fail(405, errors.New("仅接受 POST"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			scheme := "http"
			if secureRequest(r, c) {
				scheme = "https"
			}
			expected := scheme + "://" + r.Host
			if origin != expected {
				fail(403, errors.New("来源校验失败"))
				return
			}
		}
	}
	if path == "/api/bootstrap" && r.Method == "GET" {
		respond(w, 200, map[string]any{"setup": h.s.NeedsSetup(), "user": user, "csrf": csrf, "siteName": c.Service.SiteName, "version": version})
		return
	}
	if (path == "/api/setup" || path == "/api/login") && r.Method == "POST" {
		var b struct {
			Code     string `json:"code"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		if path == "/api/setup" {
			if e := h.s.Setup(b.Code, b.Username, b.Password); e != nil {
				bad(e)
				return
			}
			h.s.Event("", "account", "info", "初始化管理员", "", b.Username)
		}
		token, newCSRF, u, e := h.s.Login(b.Username, b.Password, remoteIP(r, c))
		if e != nil {
			fail(401, e)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "monitor_session", Value: token, Path: "/", HttpOnly: true, Secure: secureRequest(r, c), SameSite: http.SameSiteStrictMode, MaxAge: c.Access.SessionHours * 3600})
		respond(w, 200, map[string]any{"user": u, "csrf": newCSRF})
		return
	}
	if path == "/api/public" && r.Method == "GET" && c.Service.PublicStatus {
		servers := h.s.Servers()
		online := 0
		for _, v := range servers {
			if v.Online && v.Config.Enabled {
				online++
			}
		}
		respond(w, 200, map[string]any{"siteName": c.Service.SiteName, "total": len(servers), "online": online, "alerts": len(h.s.Alerts(""))})
		return
	}
	if user.ID == "" {
		fail(401, errors.New("请先登录"))
		return
	}
	if r.Method == "POST" && !constantEqual(r.Header.Get("X-CSRF-Token"), csrf) {
		fail(403, errors.New("会话校验失败，请刷新页面"))
		return
	}
	if path == "/api/logout" && r.Method == "POST" {
		_, _ = h.s.DB.Exec(`DELETE FROM sessions WHERE hash=?`, digest(cookie.Value))
		http.SetCookie(w, &http.Cookie{Name: "monitor_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if path == "/api/state" && r.Method == "GET" {
		servers := h.s.Servers()
		for i := range servers {
			rows, e := h.s.DB.Query(`SELECT result FROM check_results WHERE server_id=?`, servers[i].ID)
			if e == nil {
				for rows.Next() {
					var raw string
					var check CheckResult
					if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &check) == nil {
						servers[i].Snapshot.Checks = append(servers[i].Snapshot.Checks, check)
					}
				}
				rows.Close()
			}
		}
		jobs := h.s.Jobs(50)
		for i := range jobs {
			jobs[i].Logs = ""
		}
		respond(w, 200, map[string]any{"servers": servers, "alerts": h.s.Alerts(""), "events": h.s.Events("", 100), "jobs": jobs, "now": unixNow(), "manualRestart": c.Access.ManualRestart, "allowLogs": c.Access.AllowLogs, "databaseConfirm": c.Access.DatabaseConfirm})
		return
	}
	if path == "/api/history" && r.Method == "GET" {
		respond(w, 200, h.s.History(r.URL.Query().Get("id")))
		return
	}
	if path == "/api/job" && r.Method == "GET" {
		j, e := h.s.Job(r.URL.Query().Get("id"))
		if e != nil {
			fail(404, errors.New("操作不存在"))
			return
		}
		if user.Role != "admin" {
			j.Logs = ""
		}
		respond(w, 200, j)
		return
	}
	if user.Role != "admin" {
		fail(403, errors.New("此操作需要管理员权限"))
		return
	}
	if path == "/api/config" && r.Method == "GET" {
		cfg, rev := h.s.Config()
		respond(w, 200, map[string]any{"config": cfg, "revision": rev, "secrets": h.s.SecretStatus(), "activeService": json.RawMessage(h.s.meta("activeService")), "activeData": h.s.Root, "applyStatus": h.s.meta("applyStatus"), "defaultServer": defaultServer(), "users": h.s.Users()})
		return
	}
	if r.Method != "POST" {
		fail(404, errors.New("接口不存在"))
		return
	}
	switch path {
	case "/api/config/save":
		var b struct {
			Config       Config            `json:"config"`
			Revision     int64             `json:"revision"`
			Secrets      map[string]string `json:"secrets"`
			ClearSecrets []string          `json:"clearSecrets"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		for _, k := range b.ClearSecrets {
			if !allowedSecrets[k] {
				bad(errors.New("凭据字段无效"))
				return
			}
		}
		for k, v := range b.Secrets {
			if (strings.HasSuffix(k, "Endpoint") && v != "" && !validHTTP(v, false)) || len(v) > 32000 {
				bad(errors.New("通知地址或凭据无效"))
				return
			}
		}
		if b.Config.Service.TLSMode == "builtin" {
			cert, key := h.s.Secret("tlsCertificate"), h.s.Secret("tlsPrivateKey")
			if b.Secrets["tlsCertificate"] != "" {
				cert = b.Secrets["tlsCertificate"]
			}
			if b.Secrets["tlsPrivateKey"] != "" {
				key = b.Secrets["tlsPrivateKey"]
			}
			if _, e := tls.X509KeyPair([]byte(cert), []byte(key)); e != nil {
				bad(errors.New("TLS 证书与私钥无效或不匹配"))
				return
			}
			for _, k := range b.ClearSecrets {
				if k == "tlsCertificate" || k == "tlsPrivateKey" {
					bad(errors.New("内置 HTTPS 模式需要证书和私钥"))
					return
				}
			}
		}
		if e := h.s.SaveConfig(b.Config, b.Revision, b.Secrets, b.ClearSecrets...); e != nil {
			bad(e)
			return
		}
		h.s.setDatabaseLimit()
		h.s.Event("", "settings", "info", "更新系统设置", "", user.Username)
	case "/api/server/save":
		var b struct {
			ID       string       `json:"id"`
			Revision int64        `json:"revision"`
			Config   ServerConfig `json:"config"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		id, e := h.s.SaveServer(b.ID, b.Config, b.Revision)
		if e != nil {
			bad(e)
			return
		}
		h.s.Event(id, "settings", "info", "更新服务器设置", b.Config.Name, user.Username)
		respond(w, 200, map[string]string{"id": id})
		return
	case "/api/server/enrollment":
		var b struct {
			ID string `json:"id"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		code, e := h.s.Enrollment(b.ID)
		if e != nil {
			bad(errors.New("服务器不存在"))
			return
		}
		h.s.Event(b.ID, "security", "info", "生成探针接入码", "有效期 10 分钟，仅可使用一次", user.Username)
		respond(w, 200, map[string]any{"code": code, "expires": unixNow() + 600})
		return
	case "/api/server/revoke":
		var b struct {
			ID string `json:"id"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		_, _ = h.s.DB.Exec(`UPDATE servers SET token_hash='',enroll_hash='',enroll_expires=0,last_seen=0 WHERE id=?`, b.ID)
		_, _ = h.s.DB.Exec(`UPDATE jobs SET state='cancelled',result='探针凭据已撤销' WHERE server_id=? AND state='queued'`, b.ID)
		h.s.Event(b.ID, "security", "warning", "撤销探针凭据", "在途操作需人工核对结果", user.Username)
	case "/api/server/delete":
		var b struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		v, e := h.s.Server(b.ID)
		if e != nil || v.Config.Name != b.Name {
			bad(errors.New("请输入服务器名称确认"))
			return
		}
		_, _ = h.s.DB.Exec(`DELETE FROM servers WHERE id=?`, b.ID)
		_, _ = h.s.DB.Exec(`DELETE FROM alerts WHERE server_id=?`, b.ID)
		_, _ = h.s.DB.Exec(`DELETE FROM check_results WHERE server_id=?`, b.ID)
		h.s.Event(b.ID, "settings", "warning", "移除服务器", b.Name, user.Username)
	case "/api/server/maintenance":
		var b struct {
			ID      string `json:"id"`
			Enabled bool   `json:"enabled"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		until := int64(0)
		if b.Enabled {
			until = unixNow() + int64(c.Alerts.MaintenanceMinutes)*60
		}
		_, _ = h.s.DB.Exec(`UPDATE servers SET maintenance=? WHERE id=?`, until, b.ID)
		h.s.Event(b.ID, "maintenance", "info", "调整维护窗口", "", user.Username)
	case "/api/job/create":
		var b struct {
			ServerID     string `json:"serverID"`
			ContainerID  string `json:"containerID"`
			Action       string `json:"action"`
			Remark       string `json:"remark"`
			Confirmation string `json:"confirmation"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		if len(b.Remark) > 500 {
			bad(errors.New("备注最多 500 字节"))
			return
		}
		j, e := h.s.CreateJob(b.ServerID, b.ContainerID, b.Action, b.Remark, b.Confirmation, user.Username)
		if e != nil {
			bad(e)
			return
		}
		h.s.Event(j.ServerID, "action", "info", "提交 "+j.Action, j.ContainerName, user.Username)
		respond(w, 200, j)
		return
	case "/api/alert/ack":
		var b struct {
			Key string `json:"key"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		_, _ = h.s.DB.Exec(`UPDATE alerts SET acknowledged=1 WHERE k=? AND active=1`, b.Key)
		h.s.Event("", "alert", "info", "确认告警", b.Key, user.Username)
	case "/api/user/save":
		var b struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			Enabled  bool   `json:"enabled"`
			Password string `json:"password"`
		}
		if e := decodeRequest(w, r, &b); e != nil {
			bad(e)
			return
		}
		if e := h.s.SaveUser(b.ID, b.Username, b.Role, b.Password, b.Enabled); e != nil {
			bad(errors.New("保存失败：请检查密码、用户名是否重复及至少保留一个管理员"))
			return
		}
		h.s.Event("", "account", "info", "更新账号", b.Username, user.Username)
	case "/api/notification/test":
		if e := h.s.notify(r.Context(), c.Service.SiteName+" · 测试通知\n发送时间："+time.Now().Format(time.RFC3339)); e != nil {
			bad(e)
			return
		}
		h.s.Event("", "notification", "success", "测试通知已发送", "", user.Username)
	case "/api/storage/backup":
		if e := h.s.ManualBackup(); e != nil {
			bad(errors.New("数据备份失败，请检查存储空间"))
			return
		}
		h.s.Event("", "storage", "success", "监控数据备份完成", "数据库和密钥已保存至数据目录 backups", user.Username)
	case "/api/runtime/apply":
		var active int
		_ = h.s.DB.QueryRow(`SELECT count(*) FROM jobs WHERE state IN ('queued','leased') AND deadline>?`, unixNow()).Scan(&active)
		if active > 0 {
			bad(errors.New("请等待容器操作完成后再应用服务设置"))
			return
		}
		select {
		case h.apply <- struct{}{}:
		default:
			bad(errors.New("正在应用，请稍后查看"))
			return
		}
		respond(w, 202, map[string]string{"message": "正在应用，请使用新的访问地址重新连接"})
		return
	default:
		fail(404, errors.New("接口不存在"))
		return
	}
	respond(w, 200, map[string]bool{"ok": true})
}
func (h *hubAPI) agent(w http.ResponseWriter, r *http.Request) {
	bad := func(status int, message string) { respond(w, status, map[string]string{"error": message}) }
	if r.Method != "POST" {
		bad(405, "仅接受 POST")
		return
	}
	if r.URL.Path == "/api/agent/enroll" {
		var b struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		}
		if decodeRequest(w, r, &b) != nil {
			bad(400, "请求无效")
			return
		}
		token, e := h.s.Enroll(b.ID, b.Code)
		if e != nil {
			bad(401, "接入码无效或已过期")
			return
		}
		h.s.Event(b.ID, "security", "info", "探针已接入", "", "")
		respond(w, 200, map[string]string{"token": token})
		return
	}
	var b struct {
		ID       string    `json:"id"`
		Snapshot *Snapshot `json:"snapshot,omitempty"`
		Job      Job       `json:"job,omitempty"`
	}
	if decodeRequest(w, r, &b) != nil {
		bad(400, "请求无效")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !h.s.AgentAuthorized(b.ID, token) {
		bad(401, "探针身份无效")
		return
	}
	switch r.URL.Path {
	case "/api/agent/poll":
		if e := h.s.SaveSample(b.ID, b.Snapshot); e != nil {
			bad(400, "采样写入失败，请检查监控端存储")
			return
		}
		v, e := h.s.Server(b.ID)
		if e != nil {
			bad(404, "服务器不存在")
			return
		}
		j, e := h.s.Lease(b.ID)
		if e != nil {
			bad(503, "操作队列不可用")
			return
		}
		c, _ := h.s.Config()
		respond(w, 200, agentReply{Config: v.Config, Access: c.Access, Job: j, Timezone: c.Service.Timezone})
	case "/api/agent/result":
		if e := h.s.Complete(b.ID, b.Job); e != nil {
			bad(400, "操作回执无效")
			return
		}
		respond(w, 200, map[string]bool{"ok": true})
	default:
		bad(404, "接口不存在")
	}
}
