package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB   *sql.DB
	Root string
	key  []byte
	mu   sync.Mutex
}

func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func digest(value string) string     { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:]) }
func constantEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func openStore(root string) (*Store, error) {
	root, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		return nil, e
	}
	keyPath := filepath.Join(root, "identity.key")
	key, e := os.ReadFile(keyPath)
	if errors.Is(e, os.ErrNotExist) {
		if info, dbErr := os.Stat(filepath.Join(root, "monitor.db")); dbErr == nil && info.Size() > 0 {
			return nil, errors.New("identity.key 缺失；请恢复与数据库配对的密钥")
		}
		key = make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, e
		}
		f, err := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, e = f.Write(key)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
	}
	if e != nil {
		return nil, e
	}
	if len(key) != 32 {
		return nil, errors.New("invalid identity key")
	}
	db, e := sql.Open("sqlite", filepath.Join(root, "monitor.db"))
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db, Root: root, key: key}
	schema := `
PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=3000;
CREATE TABLE IF NOT EXISTS meta(k TEXT PRIMARY KEY,v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS secrets(k TEXT PRIMARY KEY,v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,username TEXT UNIQUE NOT NULL,password TEXT NOT NULL,role TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,csrf TEXT NOT NULL,expires INTEGER NOT NULL,touched INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS servers(id TEXT PRIMARY KEY,config TEXT NOT NULL,revision INTEGER NOT NULL DEFAULT 1,token_hash TEXT NOT NULL DEFAULT '',enroll_hash TEXT NOT NULL DEFAULT '',enroll_expires INTEGER NOT NULL DEFAULT 0,last_seen INTEGER NOT NULL DEFAULT 0,sample_at INTEGER NOT NULL DEFAULT 0,snapshot TEXT NOT NULL DEFAULT '{}',maintenance INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,container_id TEXT NOT NULL,container_name TEXT NOT NULL,action TEXT NOT NULL,state TEXT NOT NULL,result TEXT NOT NULL DEFAULT '',logs TEXT NOT NULL DEFAULT '',remark TEXT NOT NULL,actor TEXT NOT NULL,created INTEGER NOT NULL,deadline INTEGER NOT NULL,timeout INTEGER NOT NULL,health_wait INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_pending ON jobs(server_id,state,created);
CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY AUTOINCREMENT,server_id TEXT NOT NULL,kind TEXT NOT NULL,level TEXT NOT NULL,title TEXT NOT NULL,detail TEXT NOT NULL,actor TEXT NOT NULL,at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS events_at ON events(at);
CREATE TABLE IF NOT EXISTS alerts(k TEXT PRIMARY KEY,server_id TEXT NOT NULL,level TEXT NOT NULL,title TEXT NOT NULL,detail TEXT NOT NULL,since INTEGER NOT NULL,last_bad INTEGER NOT NULL,bad_count INTEGER NOT NULL,active INTEGER NOT NULL,acknowledged INTEGER NOT NULL DEFAULT 0,last_notice INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS history(id INTEGER PRIMARY KEY AUTOINCREMENT,server_id TEXT NOT NULL,at INTEGER NOT NULL,cpu REAL NOT NULL,memory REAL NOT NULL,restarts TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS history_server ON history(server_id,at);
CREATE TABLE IF NOT EXISTS network_history(sample_id INTEGER PRIMARY KEY REFERENCES history(id) ON DELETE CASCADE,receive REAL NOT NULL,transmit REAL NOT NULL,packets REAL NOT NULL);
CREATE TABLE IF NOT EXISTS network_learning(sample_id INTEGER PRIMARY KEY REFERENCES network_history(sample_id) ON DELETE CASCADE,interfaces TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS network_status(server_id TEXT PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,status TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS check_results(server_id TEXT NOT NULL,id TEXT NOT NULL,result TEXT NOT NULL,PRIMARY KEY(server_id,id));
CREATE TABLE IF NOT EXISTS outbox(id INTEGER PRIMARY KEY AUTOINCREMENT,message TEXT NOT NULL,revision INTEGER NOT NULL,due INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,state TEXT NOT NULL DEFAULT 'pending',lease INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS agent_jobs(id TEXT PRIMARY KEY,state TEXT NOT NULL,result TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS agent_updates(id TEXT PRIMARY KEY,server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,target TEXT NOT NULL,previous_sha256 TEXT NOT NULL,state TEXT NOT NULL,result TEXT NOT NULL DEFAULT '',actor TEXT NOT NULL,created INTEGER NOT NULL,updated INTEGER NOT NULL,deadline INTEGER NOT NULL);
CREATE UNIQUE INDEX IF NOT EXISTS agent_updates_active ON agent_updates(server_id) WHERE state IN ('queued','downloading','restarting');
CREATE TABLE IF NOT EXISTS login_limits(k TEXT PRIMARY KEY,failures INTEGER NOT NULL,until INTEGER NOT NULL);
`
	if _, e = db.Exec(schema); e != nil {
		db.Close()
		return nil, e
	}
	for k, v := range map[string]string{"config": encode(defaultConfig(root)), "revision": "1", "activeService": encode(defaultConfig(root).Service)} {
		if _, e = db.Exec(`INSERT OR IGNORE INTO meta(k,v) VALUES(?,?)`, k, v); e != nil {
			db.Close()
			return nil, e
		}
	}
	if check := s.meta("identityCheck"); check != "" {
		if v, err := s.decrypt("identity-check", check); err != nil || v != "system-monitor" {
			db.Close()
			return nil, errors.New("identity.key 与数据库不匹配")
		}
	} else {
		check, err := s.encrypt("identity-check", "system-monitor")
		if err != nil {
			db.Close()
			return nil, err
		}
		if err = s.setMeta("identityCheck", check); err != nil {
			db.Close()
			return nil, err
		}
	}
	_ = os.Chmod(filepath.Join(root, "monitor.db"), 0600)
	s.setDatabaseLimit()
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) meta(key string) string {
	var v string
	_ = s.DB.QueryRow(`SELECT v FROM meta WHERE k=?`, key).Scan(&v)
	return v
}
func (s *Store) setMeta(key, value string) error {
	_, e := s.DB.Exec(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, value)
	return e
}
func (s *Store) Config() (Config, int64) {
	c := defaultConfig(s.Root)
	_ = json.Unmarshal([]byte(s.meta("config")), &c)
	var rev int64
	_ = s.DB.QueryRow(`SELECT CAST(v AS INTEGER) FROM meta WHERE k='revision'`).Scan(&rev)
	return c, rev
}
func (s *Store) encrypt(scope, value string) (string, error) {
	block, e := aes.NewCipher(s.key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(g.Seal(nonce, nonce, []byte(value), []byte(scope))), nil
}
func (s *Store) decrypt(scope, value string) (string, error) {
	data, e := base64.RawStdEncoding.DecodeString(value)
	if e != nil {
		return "", e
	}
	b, e := aes.NewCipher(s.key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil || len(data) < g.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	plain, e := g.Open(nil, data[:g.NonceSize()], data[g.NonceSize():], []byte(scope))
	return string(plain), e
}
func (s *Store) Secret(key string) string {
	var v string
	if s.DB.QueryRow(`SELECT v FROM secrets WHERE k=?`, key).Scan(&v) != nil {
		return ""
	}
	plain, _ := s.decrypt(key, v)
	return plain
}
func (s *Store) SetSecret(key, value string) error {
	v, e := s.encrypt(key, value)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(`INSERT INTO secrets(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, v)
	return e
}
func (s *Store) SecretStatus() map[string]bool {
	out := map[string]bool{}
	rows, e := s.DB.Query(`SELECT k FROM secrets WHERE k NOT LIKE 'bootstrap%' AND k NOT LIKE 'agent:%'`)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			if allowedSecrets[k] {
				out[k] = true
			}
		}
	}
	return out
}
func (s *Store) SaveActiveService(c ServiceConfig) error {
	encrypted := map[string]string{}
	if c.TLSMode == "builtin" {
		for _, key := range []string{"tlsCertificate", "tlsPrivateKey"} {
			value, e := s.encrypt("runtime:"+key, s.Secret(key))
			if e != nil {
				return e
			}
			encrypted["runtime:"+key] = value
		}
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE meta SET v=? WHERE k='activeService'`, encode(c)); e != nil {
		return e
	}
	if _, e = tx.Exec(`DELETE FROM secrets WHERE k IN ('runtime:tlsCertificate','runtime:tlsPrivateKey')`); e != nil {
		return e
	}
	for k, v := range encrypted {
		if _, e = tx.Exec(`INSERT INTO secrets(k,v) VALUES(?,?)`, k, v); e != nil {
			return e
		}
	}
	return tx.Commit()
}

var allowedSecrets = map[string]bool{"feishuEndpoint": true, "feishuSecret": true, "wecomEndpoint": true, "barkKey": true, "smtpPassword": true, "webhookEndpoint": true, "webhookSecret": true, "tlsCertificate": true, "tlsPrivateKey": true}

func (s *Store) SaveConfig(c Config, rev int64, secrets map[string]string, clear ...string) error {
	if e := c.Validate(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, current := s.Config()
	if current != rev {
		return errors.New("设置已被其他页面修改，请刷新后重试")
	}
	var pages, pageSize int64
	_ = s.DB.QueryRow(`PRAGMA page_count`).Scan(&pages)
	_ = s.DB.QueryRow(`PRAGMA page_size`).Scan(&pageSize)
	if pages*pageSize > int64(c.Storage.DatabaseCap)*(1<<20) {
		return errors.New("容量上限不能小于当前数据库文件大小")
	}
	encoded := map[string]string{}
	for _, k := range clear {
		if !allowedSecrets[k] {
			return errors.New("凭据字段无效")
		}
	}
	for k, v := range secrets {
		if !allowedSecrets[k] || len(v) > 32000 {
			return errors.New("凭据字段无效")
		}
		if v == "" {
			continue
		}
		value, e := s.encrypt(k, v)
		if e != nil {
			return e
		}
		encoded[k] = value
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE meta SET v=? WHERE k='config'`, encode(c)); e != nil {
		return e
	}
	if _, e = tx.Exec(`UPDATE meta SET v=CAST(v AS INTEGER)+1 WHERE k='revision'`); e != nil {
		return e
	}
	for k, v := range encoded {
		if _, e = tx.Exec(`INSERT INTO secrets(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v); e != nil {
			return e
		}
	}
	for _, k := range clear {
		if _, e = tx.Exec(`DELETE FROM secrets WHERE k=?`, k); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`UPDATE jobs SET state='cancelled',result='设置已更新，请重新确认后提交' WHERE action='restart' AND state='queued'`); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Event(server, kind, level, title, detail, actor string) {
	_, _ = s.DB.Exec(`INSERT INTO events(server_id,kind,level,title,detail,actor,at) VALUES(?,?,?,?,?,?,?)`, server, kind, level, title, detail, actor, unixNow())
}
func (s *Store) Events(server string, limit int) []Event {
	out := []Event{}
	rows, e := s.DB.Query(`SELECT id,server_id,kind,level,title,detail,actor,at FROM events WHERE (?='' OR server_id=?) ORDER BY id DESC LIMIT ?`, server, server, limit)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v Event
		if rows.Scan(&v.ID, &v.ServerID, &v.Kind, &v.Level, &v.Title, &v.Detail, &v.Actor, &v.At) == nil {
			out = append(out, v)
		}
	}
	return out
}
func (s *Store) Users() []User {
	out := []User{}
	rows, e := s.DB.Query(`SELECT id,username,role,enabled FROM users ORDER BY username`)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		if rows.Scan(&u.ID, &u.Username, &u.Role, &u.Enabled) == nil {
			out = append(out, u)
		}
	}
	return out
}
func (s *Store) NeedsSetup() bool {
	var count int
	_ = s.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&count)
	return count == 0
}
func (s *Store) SetupCode() string {
	if !s.NeedsSetup() {
		return ""
	}
	code := s.Secret("bootstrap:setup")
	if code == "" {
		code = randomToken()
		_ = s.SetSecret("bootstrap:setup", code)
	}
	return code
}
func (s *Store) Servers() []Server {
	out := []Server{}
	rows, e := s.DB.Query(`SELECT s.id,s.config,s.revision,s.last_seen,s.sample_at,s.snapshot,s.maintenance,COALESCE(n.status,'{}') FROM servers s LEFT JOIN network_status n ON n.server_id=s.id ORDER BY s.id`)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v Server
		v.Config = defaultServer()
		var c, snap, network string
		if rows.Scan(&v.ID, &c, &v.Revision, &v.LastSeen, &v.SampleAt, &snap, &v.MaintenanceUntil, &network) == nil {
			_ = json.Unmarshal([]byte(c), &v.Config)
			_ = json.Unmarshal([]byte(snap), &v.Snapshot)
			_ = json.Unmarshal([]byte(network), &v.NetworkStatus)
			v.Online = v.LastSeen > unixNow()-int64(v.Config.HeartbeatTimeout)
			out = append(out, v)
		}
	}
	return out
}
func (s *Store) Server(id string) (Server, error) {
	for _, v := range s.Servers() {
		if v.ID == id {
			return v, nil
		}
	}
	return Server{}, sql.ErrNoRows
}
func (s *Store) SaveServer(id string, c ServerConfig, rev int64) (string, error) {
	if c.Containers == nil {
		c.Containers = map[string]ContainerRule{}
	}
	if c.Checks == nil {
		c.Checks = []HTTPCheck{}
	}
	if e := c.Validate(); e != nil {
		return "", e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		id = randomToken()[:16]
		_, e := s.DB.Exec(`INSERT INTO servers(id,config) VALUES(?,?)`, id, encode(c))
		return id, e
	}
	r, e := s.DB.Exec(`UPDATE servers SET config=?,revision=revision+1 WHERE id=? AND revision=?`, encode(c), id, rev)
	if e != nil {
		return "", e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return "", errors.New("服务器设置已变更或不存在，请刷新")
	}
	_, _ = s.DB.Exec(`UPDATE jobs SET state='cancelled',result='服务器设置已更新，请重新确认后提交' WHERE server_id=? AND action='restart' AND state='queued'`, id)
	_, _ = s.DB.Exec(`UPDATE agent_updates SET state='cancelled',result='服务器设置已更新，请重新确认',updated=? WHERE server_id=? AND state='queued'`, unixNow(), id)
	_, _ = s.DB.Exec(`DELETE FROM check_results WHERE server_id=?`, id)
	return id, nil
}
func (s *Store) Enrollment(id string) (string, error) {
	token := randomToken()
	r, e := s.DB.Exec(`UPDATE servers SET enroll_hash=?,enroll_expires=? WHERE id=?`, digest(token), unixNow()+600, id)
	if e != nil {
		return "", e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return "", sql.ErrNoRows
	}
	return token, nil
}
func (s *Store) Enroll(id, code string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var hash string
	var until int64
	if s.DB.QueryRow(`SELECT enroll_hash,enroll_expires FROM servers WHERE id=?`, id).Scan(&hash, &until) != nil || until < unixNow() || hash == "" || !constantEqual(hash, digest(code)) {
		return "", errors.New("接入码无效或已过期")
	}
	token := randomToken()
	_, e := s.DB.Exec(`UPDATE servers SET token_hash=?,enroll_hash='',enroll_expires=0 WHERE id=?`, digest(token), id)
	return token, e
}
func (s *Store) AgentAuthorized(id, token string) bool {
	var hash string
	if token == "" || s.DB.QueryRow(`SELECT token_hash FROM servers WHERE id=?`, id).Scan(&hash) != nil {
		return false
	}
	return hash != "" && constantEqual(hash, digest(token))
}
func (s *Store) SaveSample(id string, snapshot *Snapshot) error {
	if snapshot == nil {
		_, e := s.DB.Exec(`UPDATE servers SET last_seen=? WHERE id=?`, unixNow(), id)
		return e
	}
	if len(snapshot.Containers) > 500 || len(snapshot.Disks) > 12 || len(snapshot.Units) > 24 || len(snapshot.Checks) > 24 {
		return errors.New("sample too large")
	}
	for _, container := range snapshot.Containers {
		if !validContainerSample(container) {
			return errors.New("invalid container sample")
		}
	}
	if !validNetworkSample(snapshot.Network) {
		return errors.New("invalid network sample")
	}
	if a := snapshot.Agent; a != nil && (len(a.Revision) > 128 || len(a.Reason) > 1024 || len(a.Arch) > 16 || len(a.SHA256) > 64 || a.Protocol < 0 || a.Protocol > 1000) {
		return errors.New("invalid agent build information")
	}
	snapshot.At = unixNow()
	snapshot.AgentVersion = strings.TrimSpace(snapshot.AgentVersion)
	restarts := map[string]int{}
	for _, c := range snapshot.Containers {
		restarts[c.ID] = c.Restarts
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _ := s.Config()
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE servers SET last_seen=?,sample_at=?,snapshot=? WHERE id=?`, unixNow(), snapshot.At, encode(snapshot), id); e != nil {
		return e
	}
	memory := 0.0
	if snapshot.MemoryTotal > 0 {
		memory = float64(snapshot.MemoryUsed) / float64(snapshot.MemoryTotal) * 100
	}
	free, freeErr := diskFree(s.Root)
	if freeErr == nil && free < uint64(c.Storage.MinFree)*(1<<30) {
		return tx.Commit()
	}
	row, e := tx.Exec(`INSERT INTO history(server_id,at,cpu,memory,restarts) VALUES(?,?,?,?,?)`, id, snapshot.At, snapshot.CPU, memory, encode(restarts))
	if e != nil {
		return e
	}
	if network, ready := networkTotals(snapshot.Network); ready {
		sampleID, err := row.LastInsertId()
		if err != nil {
			return err
		}
		if _, e = tx.Exec(`INSERT INTO network_history(sample_id,receive,transmit,packets) VALUES(?,?,?,?)`, sampleID, network.ReceiveBytesPerSecond, network.TransmitBytesPerSecond, network.ReceivePacketsPerSecond); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func scanJob(row interface{ Scan(...any) error }) (Job, error) {
	var j Job
	e := row.Scan(&j.ID, &j.ServerID, &j.ContainerID, &j.ContainerName, &j.Action, &j.State, &j.Result, &j.Logs, &j.Remark, &j.Actor, &j.Created, &j.Deadline, &j.Timeout, &j.HealthWait)
	return j, e
}

const jobColumns = `id,server_id,container_id,container_name,action,state,result,logs,remark,actor,created,deadline,timeout,health_wait`

func (s *Store) Job(id string) (Job, error) {
	return scanJob(s.DB.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id=?`, id))
}
func (s *Store) CreateJob(serverID, containerID, action, remark, confirmation, actor string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _ := s.Config()
	server, e := s.Server(serverID)
	if e != nil {
		return Job{}, e
	}
	if !server.Online || !server.Config.Enabled || server.SampleAt < unixNow()-int64(server.Config.HeartbeatTimeout) {
		return Job{}, errors.New("探针离线或状态已过期")
	}
	if s.pendingAgentUpdate(serverID) {
		return Job{}, errors.New("请等待探针更新完成后再操作容器")
	}
	var target *Container
	for _, v := range server.Snapshot.Containers {
		if v.ID == containerID {
			copy := v
			target = &copy
			break
		}
	}
	if target == nil {
		return Job{}, errors.New("容器已变化，请刷新状态")
	}
	rule, exists := server.Config.Containers[target.Name]
	if !exists {
		rule = ContainerRule{Monitor: server.Config.MonitorNew}
	}
	if action == "restart" {
		var pending int
		_ = s.DB.QueryRow(`SELECT count(*) FROM jobs WHERE server_id=? AND container_id=? AND action='restart' AND state IN ('queued','leased') AND deadline>?`, serverID, containerID, unixNow()).Scan(&pending)
		if pending > 0 {
			return Job{}, errors.New("该容器已有重启操作，请查看原操作回执")
		}
		if !c.Access.ManualRestart || !rule.Monitor || !rule.Restart {
			return Job{}, errors.New("此容器未获重启权限")
		}
		if c.Access.DatabaseConfirm && (target.Database || rule.Database) && confirmation != target.Name {
			return Job{}, errors.New("请输入数据库容器名确认")
		}
		if c.Access.RequireReason && strings.TrimSpace(remark) == "" {
			return Job{}, errors.New("请填写重启备注")
		}
		var recent int64
		_ = s.DB.QueryRow(`SELECT COALESCE(MAX(created),0) FROM jobs WHERE server_id=? AND container_id=? AND action='restart'`, serverID, containerID).Scan(&recent)
		if recent > unixNow()-int64(c.Access.RestartCooldown) {
			return Job{}, errors.New("容器仍在重启冷却期")
		}
	} else if action != "logs" || !c.Access.AllowLogs {
		return Job{}, errors.New("操作未获授权")
	}
	var active int
	_ = s.DB.QueryRow(`SELECT (SELECT count(*) FROM jobs WHERE state IN ('queued','leased') AND deadline>?) + (SELECT count(*) FROM agent_updates WHERE state IN ('queued','downloading','restarting') AND deadline>?)`, unixNow(), unixNow()).Scan(&active)
	if active >= c.Service.MaxOps {
		return Job{}, errors.New("已有操作执行中，请稍后重试")
	}
	j := Job{ID: randomToken()[:24], ServerID: serverID, ContainerID: containerID, ContainerName: target.Name, Action: action, State: "queued", Remark: strings.TrimSpace(remark), Actor: actor, Created: unixNow(), Deadline: unixNow() + int64(c.Access.CommandTimeout+c.Access.HealthWait+server.Config.CommandPoll+30), Timeout: c.Access.CommandTimeout, HealthWait: c.Access.HealthWait}
	_, e = s.DB.Exec(`INSERT INTO jobs(`+jobColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.ServerID, j.ContainerID, j.ContainerName, j.Action, j.State, j.Result, j.Logs, j.Remark, j.Actor, j.Created, j.Deadline, j.Timeout, j.HealthWait)
	return j, e
}
func (s *Store) Lease(id string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingAgentUpdate(id) {
		return nil, nil
	}
	server, e := s.Server(id)
	if e != nil || !server.Config.Enabled {
		return nil, e
	}
	c, _ := s.Config()
	var inFlight int
	_ = s.DB.QueryRow(`SELECT count(*) FROM jobs WHERE server_id=? AND state='leased' AND deadline>?`, id, unixNow()).Scan(&inFlight)
	if inFlight > 0 {
		return nil, nil
	}
	rows, e := s.DB.Query(`SELECT `+jobColumns+` FROM jobs WHERE server_id=? AND state='queued' AND deadline>? ORDER BY created LIMIT 1`, id, unixNow())
	if e != nil {
		return nil, e
	}
	var job *Job
	if rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		job = &j
	}
	rows.Close()
	if job == nil {
		return nil, nil
	}
	if job.Action == "restart" {
		rule := server.Config.Containers[job.ContainerName]
		if !c.Access.ManualRestart || !rule.Monitor || !rule.Restart {
			_, e = s.DB.Exec(`UPDATE jobs SET state='cancelled',result='操作权限已撤销' WHERE id=?`, job.ID)
			return nil, e
		}
	}
	_, e = s.DB.Exec(`UPDATE jobs SET state='leased' WHERE id=? AND state='queued'`, job.ID)
	if e != nil {
		return nil, e
	}
	job.State = "leased"
	return job, nil
}
func (s *Store) Complete(serverID string, j Job) error {
	if j.State != "success" && j.State != "failed" && j.State != "unchecked" && j.State != "unknown" {
		return errors.New("invalid result state")
	}
	if len(j.Result) > 2048 || len(j.Logs) > 32768 {
		return errors.New("result too large")
	}
	r, e := s.DB.Exec(`UPDATE jobs SET state=?,result=?,logs=? WHERE id=? AND server_id=? AND state='leased'`, j.State, j.Result, j.Logs, j.ID, serverID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return nil
	}
	job, _ := s.Job(j.ID)
	level := "success"
	if j.State != "success" {
		level = "warning"
	}
	s.Event(serverID, "action", level, job.ContainerName+" · "+job.Action+" · "+j.State, j.Result, job.Actor)
	c, _ := s.Config()
	if c.Notifications.Notices && c.Notifications.Actions && job.Action == "restart" {
		server, _ := s.Server(serverID)
		if server.MaintenanceUntil < unixNow() {
			s.QueueNotice(server.Config.Name + " · " + job.ContainerName + " · 重启回执\n" + j.Result)
		}
	}
	return nil
}
func (s *Store) Alerts(server string) []Alert {
	out := []Alert{}
	rows, e := s.DB.Query(`SELECT k,server_id,level,title,detail,since,active,acknowledged FROM alerts WHERE active=1 AND (?='' OR server_id=?) ORDER BY since DESC`, server, server)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var a Alert
		if rows.Scan(&a.Key, &a.ServerID, &a.Level, &a.Title, &a.Detail, &a.Since, &a.Active, &a.Acknowledged) == nil {
			out = append(out, a)
		}
	}
	return out
}
func (s *Store) QueueNotice(message string) {
	c, rev := s.Config()
	_, _ = s.DB.Exec(`INSERT INTO outbox(message,revision,due) VALUES(?,?,?)`, message, rev, unixNow()+int64(c.Notifications.GroupWindow))
}
func (s *Store) SnapshotTo(destination string) error {
	if _, e := os.Stat(destination); e == nil {
		return errors.New("backup already exists")
	}
	_, e := s.DB.ExecContext(context.Background(), `VACUUM INTO ?`, destination)
	return e
}
func (s *Store) History(id string) []map[string]any {
	out := []map[string]any{}
	rows, e := s.DB.Query(`SELECT h.at,h.cpu,h.memory,n.receive,n.transmit,n.packets FROM history h LEFT JOIN network_history n ON n.sample_id=h.id WHERE h.server_id=? ORDER BY h.at DESC,h.id DESC LIMIT 120`, id)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var at int64
		var cpu, mem float64
		var receive, transmit, packets sql.NullFloat64
		if rows.Scan(&at, &cpu, &mem, &receive, &transmit, &packets) == nil {
			v := map[string]any{"at": at, "cpu": cpu, "memory": mem, "receiveBytesPerSecond": nil, "transmitBytesPerSecond": nil, "receivePacketsPerSecond": nil}
			if receive.Valid && transmit.Valid && packets.Valid {
				v["receiveBytesPerSecond"], v["transmitBytesPerSecond"], v["receivePacketsPerSecond"] = receive.Float64, transmit.Float64, packets.Float64
			}
			out = append(out, v)
		}
	}
	return out
}
func (s *Store) ExpireJobs() {
	s.expireAgentUpdates()
	_, _ = s.DB.Exec(`UPDATE jobs SET state='unknown',result='探针未在期限内确认结果，请核对容器状态后再操作' WHERE state IN ('queued','leased') AND deadline<?`, unixNow())
	_, _ = s.DB.Exec(`DELETE FROM sessions WHERE expires<?`, unixNow())
}
func (s *Store) Prune() {
	c, _ := s.Config()
	now := unixNow()
	_, _ = s.DB.Exec(`DELETE FROM history WHERE at<?`, now-int64(c.Storage.MetricsDays)*86400)
	_, _ = s.DB.Exec(`DELETE FROM events WHERE (kind='action' AND at<?) OR (kind<>'action' AND at<?)`, now-int64(c.Storage.AuditDays)*86400, now-int64(c.Storage.EventsDays)*86400)
	_, _ = s.DB.Exec(`DELETE FROM jobs WHERE state NOT IN ('queued','leased') AND created<?`, now-int64(c.Storage.AuditDays)*86400)
	_, _ = s.DB.Exec(`DELETE FROM agent_updates WHERE state NOT IN ('queued','downloading','restarting') AND created<?`, now-int64(c.Storage.AuditDays)*86400)
	_, _ = s.DB.Exec(`DELETE FROM outbox WHERE state IN ('sent','failed','cancelled') AND due<?`, now-7*86400)
	// Bound retained samples even when the configured retention cannot fit the disk budget.
	var pages, size, freePages int64
	_ = s.DB.QueryRow(`PRAGMA page_count`).Scan(&pages)
	_ = s.DB.QueryRow(`PRAGMA page_size`).Scan(&size)
	_ = s.DB.QueryRow(`PRAGMA freelist_count`).Scan(&freePages)
	free, freeErr := diskFree(s.Root)
	if (pages-freePages)*size > int64(c.Storage.DatabaseCap)*1024*1024*8/10 || (freeErr == nil && free < uint64(c.Storage.MinFree)*(1<<30)) {
		_, _ = s.DB.Exec(`DELETE FROM history WHERE id IN (SELECT id FROM history ORDER BY id LIMIT 5000)`)
		_, _ = s.DB.Exec(`DELETE FROM events WHERE id IN (SELECT id FROM events WHERE at<? ORDER BY id LIMIT 5000)`, now-86400)
		_, _ = s.DB.Exec(`DELETE FROM jobs WHERE id IN (SELECT id FROM jobs WHERE state NOT IN ('queued','leased') AND created<? ORDER BY created LIMIT 1000)`, now-86400)
	}
	_, _ = s.DB.Exec(`DELETE FROM login_limits WHERE until<?`, now-86400)
	_, _ = s.DB.Exec(`PRAGMA wal_checkpoint(PASSIVE)`)
}
func copyExclusive(source, dest string, mode os.FileMode) error {
	data, e := os.ReadFile(source)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	return e
}
func (s *Store) BackupData(now time.Time) error {
	c, _ := s.Config()
	if !c.Storage.DataBackup {
		return nil
	}
	loc, _ := time.LoadLocation(c.Service.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	local := now.In(loc)
	if local.Format("15:04") < c.Storage.DataBackupTime {
		return nil
	}
	day := local.Format("2006-01-02")
	if s.meta("lastDataBackup") == day {
		return nil
	}
	if e := s.backupBundle("auto-" + day); e != nil {
		return e
	}
	return s.setMeta("lastDataBackup", day)
}
func (s *Store) setDatabaseLimit() {
	c, _ := s.Config()
	var size int64
	_ = s.DB.QueryRow(`PRAGMA page_size`).Scan(&size)
	if size > 0 {
		_, _ = s.DB.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", int64(c.Storage.DatabaseCap)*1024*1024/size))
	}
	_, _ = s.DB.Exec(`PRAGMA journal_size_limit=8388608`)
}
func (s *Store) Jobs(limit int) []Job {
	out := []Job{}
	rows, e := s.DB.Query(`SELECT `+jobColumns+` FROM jobs ORDER BY created DESC LIMIT ?`, limit)
	if e != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		j, e := scanJob(rows)
		if e == nil {
			out = append(out, j)
		}
	}
	return out
}
func (s *Store) ManualBackup() error {
	return s.backupBundle("manual-" + time.Now().UTC().Format("20060102T150405") + "-" + randomToken()[:6])
}
func (s *Store) backupBundle(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _ := s.Config()
	dir := filepath.Join(s.Root, "backups")
	if e := os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	dest := filepath.Join(dir, name)
	if _, e := os.Stat(dest); e == nil {
		return validateBundle(dest)
	}
	info, e := os.Stat(filepath.Join(s.Root, "monitor.db"))
	if e != nil {
		return e
	}
	if free, e := diskFree(s.Root); e == nil && free < uint64(info.Size())+uint64(c.Storage.MinFree)*(1<<30) {
		return errors.New("insufficient free space for consistent backup")
	}
	stage, e := os.MkdirTemp(dir, ".pending-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	if e = s.SnapshotTo(filepath.Join(stage, "monitor.db")); e != nil {
		return e
	}
	_ = os.Chmod(filepath.Join(stage, "monitor.db"), 0600)
	if e = copyExclusive(filepath.Join(s.Root, "identity.key"), filepath.Join(stage, "identity.key"), 0600); e != nil {
		return e
	}
	if e = validateBundle(stage); e != nil {
		return e
	}
	if e = os.Rename(stage, dest); e != nil {
		return e
	}
	entries, _ := os.ReadDir(dir)
	sort.Slice(entries, func(i, j int) bool {
		a, ae := entries[i].Info()
		b, be := entries[j].Info()
		return ae == nil && (be != nil || a.ModTime().Before(b.ModTime()))
	})
	kept := []string{}
	for _, v := range entries {
		if v.IsDir() && (strings.HasPrefix(v.Name(), "auto-") || strings.HasPrefix(v.Name(), "manual-")) {
			kept = append(kept, filepath.Join(dir, v.Name()))
		}
	}
	for len(kept) > c.Storage.DataBackupKeep {
		old := kept[0]
		if old == dest {
			old = kept[1]
			kept = append(kept[:1], kept[2:]...)
		} else {
			kept = kept[1:]
		}
		_ = os.RemoveAll(old)
	}
	return nil
}
func validateBundle(dir string) error {
	key, e := os.ReadFile(filepath.Join(dir, "identity.key"))
	if e != nil || len(key) != 32 {
		return errors.New("invalid backup key")
	}
	db, e := sql.Open("sqlite", filepath.Join(dir, "monitor.db"))
	if e != nil {
		return e
	}
	defer db.Close()
	var result string
	if e = db.QueryRow(`PRAGMA quick_check`).Scan(&result); e != nil {
		return e
	}
	if result != "ok" {
		return errors.New("backup integrity check failed")
	}
	return nil
}
