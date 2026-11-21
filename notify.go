package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

func (s *Store) notify(ctx context.Context, message string) error {
	c, _ := s.Config()
	n := c.Notifications
	ctx, cancel := context.WithTimeout(ctx, time.Duration(n.Timeout)*time.Second)
	defer cancel()
	if n.Channel == "smtp" {
		return s.sendMail(ctx, n, message)
	}
	endpoint := ""
	payload := map[string]any{}
	now := strconv.FormatInt(unixNow(), 10)
	switch n.Channel {
	case "feishu":
		endpoint = s.Secret("feishuEndpoint")
		payload = map[string]any{"msg_type": "text", "content": map[string]string{"text": message}}
		if secret := s.Secret("feishuSecret"); secret != "" {
			mac := hmac.New(sha256.New, []byte(now+"\n"+secret))
			payload["timestamp"] = now
			payload["sign"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
	case "wecom":
		endpoint = s.Secret("wecomEndpoint")
		payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": message}}
	case "bark":
		endpoint = strings.TrimRight(n.BarkServer, "/") + "/push"
		payload = map[string]any{"device_key": s.Secret("barkKey"), "title": c.Service.SiteName, "body": message, "group": "system-monitor"}
		if s.Secret("barkKey") == "" {
			return errors.New("Bark 密钥未配置")
		}
	case "webhook":
		endpoint = s.Secret("webhookEndpoint")
		payload = map[string]any{"title": c.Service.SiteName, "message": message, "at": unixNow()}
	default:
		return errors.New("通知渠道无效")
	}
	if !validHTTP(endpoint, false) {
		return errors.New("通知地址尚未配置或无效")
	}
	body := []byte(encode(payload))
	r, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if e != nil {
		return errors.New("通知地址无效")
	}
	r.Header.Set("Content-Type", "application/json")
	if n.Channel == "webhook" {
		if secret := s.Secret("webhookSecret"); secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(now + "."))
			mac.Write(body)
			r.Header.Set("X-Monitor-Timestamp", now)
			r.Header.Set("X-Monitor-Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
		}
	}
	t := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	defer t.CloseIdleConnections()
	resp, e := (&http.Client{Transport: t, CheckRedirect: noRedirect}).Do(r)
	if e != nil {
		return errors.New("通知连接失败或超时")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if e != nil || len(b) > 16384 || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("通知接口返回异常（HTTP %d）", resp.StatusCode)
	}
	if n.Channel != "webhook" {
		var result map[string]json.RawMessage
		if json.Unmarshal(b, &result) != nil {
			return errors.New("通知回执不是有效 JSON")
		}
		key := "code"
		if n.Channel == "wecom" {
			key = "errcode"
		}
		raw, ok := result[key]
		if !ok && n.Channel == "feishu" {
			raw, ok = result["StatusCode"]
		}
		var code int
		if !ok || json.Unmarshal(raw, &code) != nil || (n.Channel == "bark" && code != 200) || (n.Channel != "bark" && code != 0) {
			return errors.New("通知平台拒绝了消息，请核对渠道凭据")
		}
	}
	return nil
}
func (s *Store) sendMail(ctx context.Context, n NotificationConfig, message string) error {
	if n.SMTPHost == "" || strings.ContainsAny(n.SMTPHost+n.SMTPFrom+n.SMTPTo, "\r\n") {
		return errors.New("邮件参数无效")
	}
	from, e := mail.ParseAddress(n.SMTPFrom)
	if e != nil {
		return errors.New("发件地址无效")
	}
	to, e := mail.ParseAddressList(n.SMTPTo)
	if e != nil || len(to) == 0 {
		return errors.New("收件地址无效")
	}
	address := net.JoinHostPort(n.SMTPHost, strconv.Itoa(n.SMTPPort))
	conn, e := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if e != nil {
		return errors.New("SMTP 连接失败")
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	cfg := &tls.Config{ServerName: n.SMTPHost, MinVersion: tls.VersionTLS12}
	if n.SMTPTLS == "implicit" {
		secure := tls.Client(conn, cfg)
		if e = secure.HandshakeContext(ctx); e != nil {
			return errors.New("SMTP TLS 失败")
		}
		conn = secure
	}
	client, e := smtp.NewClient(conn, n.SMTPHost)
	if e != nil {
		return errors.New("SMTP 握手失败")
	}
	defer client.Close()
	if n.SMTPTLS == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP 不支持 STARTTLS")
		}
		if client.StartTLS(cfg) != nil {
			return errors.New("SMTP STARTTLS 失败")
		}
	}
	if n.SMTPUser != "" {
		if client.Auth(smtp.PlainAuth("", n.SMTPUser, s.Secret("smtpPassword"), n.SMTPHost)) != nil {
			return errors.New("SMTP 认证失败")
		}
	}
	if client.Mail(from.Address) != nil {
		return errors.New("SMTP 拒绝发件人")
	}
	recipients := []string{}
	for _, a := range to {
		if client.Rcpt(a.Address) != nil {
			return errors.New("SMTP 拒绝收件人")
		}
		recipients = append(recipients, a.Address)
	}
	writer, e := client.Data()
	if e != nil {
		return errors.New("SMTP DATA 失败")
	}
	_, e = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: System monitor\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", from.Address, strings.Join(recipients, ", "), strings.ReplaceAll(message, "\n", "\r\n"))
	if e != nil {
		return errors.New("SMTP 发送失败")
	}
	if writer.Close() != nil {
		return errors.New("SMTP 未确认接收")
	}
	_ = client.Quit()
	return nil
}
func notificationLoop(ctx context.Context, s *Store) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c, rev := s.Config()
			_, _ = s.DB.Exec(`UPDATE outbox SET state='cancelled' WHERE state='pending' AND revision<>?`, rev)
			if !c.Notifications.Notices {
				continue
			}
			rows, e := s.DB.Query(`SELECT id,message,attempts FROM outbox WHERE state='pending' AND due<=? AND revision=? ORDER BY id LIMIT 20`, unixNow(), rev)
			type entry struct {
				id       int64
				message  string
				attempts int
			}
			entries := []entry{}
			if e == nil {
				for rows.Next() {
					var v entry
					if rows.Scan(&v.id, &v.message, &v.attempts) == nil {
						entries = append(entries, v)
					}
				}
				rows.Close()
			}
			if len(entries) == 0 {
				continue
			}
			messages := []string{}
			for _, v := range entries {
				messages = append(messages, v.message)
			}
			message := strings.Join(messages, "\n\n")
			if len(message) > 12000 {
				message = string([]rune(message)[:min(len([]rune(message)), 3500)])
			}
			err := s.notify(ctx, message)
			for _, v := range entries {
				state := "sent"
				due := unixNow()
				if err != nil {
					state = "pending"
					due += int64(30 * (v.attempts + 1))
					if v.attempts >= c.Notifications.Retries {
						state = "failed"
					}
				}
				_, _ = s.DB.Exec(`UPDATE outbox SET state=?,attempts=attempts+1,due=? WHERE id=?`, state, due, v.id)
			}
			if err != nil {
				s.Event("", "notification", "warning", "通知发送失败", err.Error(), "")
			}
		}
	}
}
