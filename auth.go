package main

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func passwordHash(password string) (string, error) {
	if len(password) < 12 || len(password) > 256 {
		return "", errors.New("密码需为 12–256 字节")
	}
	salt := randomToken()[:24]
	key, e := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	return "pbkdf2$600000$" + salt + "$" + base64.RawStdEncoding.EncodeToString(key), e
}
func passwordOK(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" || len(password) > 256 {
		return false
	}
	n, e := strconv.Atoi(parts[1])
	if e != nil || n < 100000 || n > 1000000 {
		return false
	}
	key, e := pbkdf2.Key(sha256.New, password, []byte(parts[2]), n, 32)
	return e == nil && constantEqual(parts[3], base64.RawStdEncoding.EncodeToString(key))
}
func usernameValid(name string) bool {
	return len(name) >= 3 && len(name) <= 64 && strings.TrimSpace(name) == name && !strings.ContainsAny(name, "\r\n\x00")
}
func (s *Store) Setup(code, name, password string) error {
	if !usernameValid(name) {
		return errors.New("用户名需为 3–64 字节")
	}
	hash, e := passwordHash(password)
	if e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.NeedsSetup() || !constantEqual(s.Secret("bootstrap:setup"), code) || code == "" {
		return errors.New("初始化已完成或初始化码错误")
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT INTO users(id,username,password,role,enabled) VALUES(?,?,?,'admin',1)`, randomToken()[:16], name, hash); e != nil {
		return e
	}
	if _, e = tx.Exec(`DELETE FROM secrets WHERE k='bootstrap:setup'`); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Login(name, password, ip string) (string, string, User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, _ := s.Config()
	now := unixNow()
	k := digest(ip)
	var failures int
	var until int64
	_ = s.DB.QueryRow(`SELECT failures,until FROM login_limits WHERE k=?`, k).Scan(&failures, &until)
	if until > now {
		return "", "", User{}, errors.New("登录暂时锁定，请稍后重试")
	}
	var u User
	var hash string
	e := s.DB.QueryRow(`SELECT id,username,password,role,enabled FROM users WHERE username=?`, name).Scan(&u.ID, &u.Username, &hash, &u.Role, &u.Enabled)
	if e != nil || !u.Enabled || !passwordOK(hash, password) {
		if until != 0 && until <= now {
			failures = 0
		}
		failures++
		until = 0
		if failures >= c.Access.LoginFailures {
			until = now + int64(c.Access.LockMinutes)*60
		}
		_, _ = s.DB.Exec(`INSERT INTO login_limits(k,failures,until) VALUES(?,?,?) ON CONFLICT(k) DO UPDATE SET failures=excluded.failures,until=excluded.until`, k, failures, until)
		return "", "", User{}, errors.New("用户名或密码错误")
	}
	_, _ = s.DB.Exec(`DELETE FROM login_limits WHERE k=?`, k)
	token, csrf := randomToken(), randomToken()
	_, e = s.DB.Exec(`INSERT INTO sessions(hash,user_id,csrf,expires,touched) VALUES(?,?,?,?,?)`, digest(token), u.ID, csrf, now+int64(c.Access.SessionHours)*3600, now)
	return token, csrf, u, e
}
func (s *Store) Session(token string) (User, string, error) {
	var u User
	var csrf string
	var expires, touched int64
	e := s.DB.QueryRow(`SELECT u.id,u.username,u.role,u.enabled,s.csrf,s.expires,s.touched FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.hash=?`, digest(token)).Scan(&u.ID, &u.Username, &u.Role, &u.Enabled, &csrf, &expires, &touched)
	c, _ := s.Config()
	if e != nil || !u.Enabled || expires < unixNow() || touched < unixNow()-int64(c.Access.IdleMinutes)*60 {
		return u, "", errors.New("请重新登录")
	}
	if touched < unixNow()-30 {
		_, _ = s.DB.Exec(`UPDATE sessions SET touched=? WHERE hash=?`, unixNow(), digest(token))
	}
	return u, csrf, nil
}
func (s *Store) SaveUser(id, name, role, password string, enabled bool) error {
	if !usernameValid(name) || (role != "admin" && role != "viewer") {
		return errors.New("账号参数无效")
	}
	hash := ""
	var e error
	if password != "" {
		hash, e = passwordHash(password)
		if e != nil {
			return e
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		if hash == "" {
			return errors.New("新账号需要密码")
		}
		_, e = s.DB.Exec(`INSERT INTO users(id,username,password,role,enabled) VALUES(?,?,?,?,?)`, randomToken()[:16], name, hash, role, enabled)
		return e
	}
	var oldRole string
	var oldEnabled bool
	if s.DB.QueryRow(`SELECT role,enabled FROM users WHERE id=?`, id).Scan(&oldRole, &oldEnabled) != nil {
		return errors.New("账号不存在")
	}
	if oldRole == "admin" && oldEnabled && (!enabled || role != "admin") {
		var n int
		_ = s.DB.QueryRow(`SELECT count(*) FROM users WHERE enabled=1 AND role='admin'`).Scan(&n)
		if n <= 1 {
			return errors.New("至少保留一个启用的管理员")
		}
	}
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if hash == "" {
		_, e = tx.Exec(`UPDATE users SET username=?,role=?,enabled=? WHERE id=?`, name, role, enabled, id)
	} else {
		_, e = tx.Exec(`UPDATE users SET username=?,role=?,enabled=?,password=? WHERE id=?`, name, role, enabled, hash, id)
	}
	if e != nil {
		return e
	}
	if _, e = tx.Exec(`DELETE FROM sessions WHERE user_id=?`, id); e != nil {
		return e
	}
	return tx.Commit()
}
func withinNetworks(ip string, list string) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, s := range lines(list) {
		if q := net.ParseIP(s); q != nil && q.Equal(p) {
			return true
		}
		_, n, e := net.ParseCIDR(s)
		if e == nil && n.Contains(p) {
			return true
		}
	}
	return false
}
func remoteIP(r *http.Request, c Config) string {
	ip, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		ip = r.RemoteAddr
	}
	if withinNetworks(ip, c.Service.TrustedProxies) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			p := strings.TrimSpace(parts[i])
			if net.ParseIP(p) == nil {
				return ip
			}
			ip = p
			if !withinNetworks(ip, c.Service.TrustedProxies) {
				break
			}
		}
	}
	return ip
}
func secureRequest(r *http.Request, c Config) bool {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return r.TLS != nil || (withinNetworks(ip, c.Service.TrustedProxies) && r.Header.Get("X-Forwarded-Proto") == "https")
}
func (s *Store) RecoverAccess() (string, string, error) {
	password := randomToken()
	hash, e := passwordHash(password)
	if e != nil {
		return "", "", e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := "recovery-admin"
	id := ""
	_ = s.DB.QueryRow(`SELECT id,username FROM users WHERE role='admin' ORDER BY enabled DESC,id LIMIT 1`).Scan(&id, &name)
	c, _ := s.Config()
	c.Access.AllowIPs = ""
	tx, e := s.DB.Begin()
	if e != nil {
		return "", "", e
	}
	defer tx.Rollback()
	if id == "" {
		_, e = tx.Exec(`INSERT INTO users(id,username,password,role,enabled) VALUES(?,?,?,'admin',1)`, randomToken()[:16], name, hash)
	} else {
		_, e = tx.Exec(`UPDATE users SET password=?,enabled=1 WHERE id=?`, hash, id)
	}
	if e != nil {
		return "", "", e
	}
	for _, q := range []string{`DELETE FROM sessions`, `DELETE FROM login_limits`, `DELETE FROM secrets WHERE k='bootstrap:setup'`, `UPDATE meta SET v=CAST(v AS INTEGER)+1 WHERE k='revision'`} {
		if _, e = tx.Exec(q); e != nil {
			return "", "", e
		}
	}
	if _, e = tx.Exec(`UPDATE meta SET v=? WHERE k='config'`, encode(c)); e != nil {
		return "", "", e
	}
	if e = tx.Commit(); e != nil {
		return "", "", e
	}
	s.Event("", "security", "warning", "本机恢复管理员访问", "IP 限制已清除、旧会话已失效", name)
	return name, password, nil
}
