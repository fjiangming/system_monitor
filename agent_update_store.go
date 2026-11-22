package main

import (
	"database/sql"
	"encoding/json"
	"errors"
)

const agentUpdateColumns = `id,server_id,target,previous_sha256,state,result,actor,created,updated,deadline`

func scanAgentUpdate(row interface{ Scan(...any) error }) (AgentUpdate, error) {
	var u AgentUpdate
	var raw string
	e := row.Scan(&u.ID, &u.ServerID, &raw, &u.Previous, &u.State, &u.Result, &u.Actor, &u.Created, &u.Updated, &u.Deadline)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &u.Target)
	}
	return u, e
}

func (s *Store) AgentUpdate(id string) (AgentUpdate, error) {
	return scanAgentUpdate(s.DB.QueryRow(`SELECT `+agentUpdateColumns+` FROM agent_updates WHERE id=?`, id))
}

func (s *Store) latestAgentUpdate(id string) *AgentUpdate {
	u, e := scanAgentUpdate(s.DB.QueryRow(`SELECT `+agentUpdateColumns+` FROM agent_updates WHERE server_id=? ORDER BY created DESC,rowid DESC LIMIT 1`, id))
	if e != nil {
		return nil
	}
	return &u
}

func (s *Store) pendingAgentUpdate(id string) bool {
	var count int
	_ = s.DB.QueryRow(`SELECT count(*) FROM agent_updates WHERE server_id=? AND state IN ('queued','downloading','restarting') AND deadline>=?`, id, unixNow()).Scan(&count)
	return count != 0
}

func (s *Store) agentUpdateView(v Server, artifacts *agentArtifacts) AgentUpdateView {
	view := AgentUpdateView{Latest: s.latestAgentUpdate(v.ID)}
	if v.SampleAt == 0 {
		view.Reason = "等待探针接入并上报版本"
		return view
	}
	info := v.Snapshot.Agent
	if info == nil || info.Protocol < agentUpdateProtocol {
		view.Reason = "旧探针需先手动安装一次新版托管服务"
		return view
	}
	if !info.Updatable {
		view.Reason = info.Reason
		if view.Reason == "" {
			view.Reason = "当前探针尚未启用托管更新"
		}
		return view
	}
	if !v.Config.VerifyTLS {
		view.Reason = "请先启用探针的 HTTPS 证书校验"
		return view
	}
	f, e := artifacts.artifact(info.Arch)
	if e != nil {
		view.Reason = e.Error()
		return view
	}
	view.Target = &f.AgentArtifact
	if f.SHA256 == info.SHA256 {
		view.Reason = "已与面板版本一致"
		return view
	}
	if !v.Online || !v.Config.Enabled || v.SampleAt < unixNow()-int64(v.Config.HeartbeatTimeout) {
		view.Reason = "等待探针在线并上报新状态"
		return view
	}
	if s.pendingAgentUpdate(v.ID) {
		view.Reason = "探针更新进行中"
		return view
	}
	view.Available = true
	return view
}

func (s *Store) CreateAgentUpdate(serverID, expectedSHA, actor string, artifacts *agentArtifacts) (AgentUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireAgentUpdates()
	v, e := s.Server(serverID)
	if e != nil {
		return AgentUpdate{}, errors.New("服务器不存在")
	}
	view := s.agentUpdateView(v, artifacts)
	if !view.Available {
		return AgentUpdate{}, errors.New(view.Reason)
	}
	if view.Target.SHA256 != expectedSHA {
		return AgentUpdate{}, errors.New("面板更新包已变化，请刷新后重新确认")
	}
	var active int
	if e = s.DB.QueryRow(`SELECT count(*) FROM jobs WHERE server_id=? AND state IN ('queued','leased') AND deadline>=?`, serverID, unixNow()).Scan(&active); e != nil {
		return AgentUpdate{}, e
	}
	if active > 0 {
		return AgentUpdate{}, errors.New("请等待此服务器的容器操作完成")
	}
	c, _ := s.Config()
	if e = s.DB.QueryRow(`SELECT (SELECT count(*) FROM jobs WHERE state IN ('queued','leased') AND deadline>=?) + (SELECT count(*) FROM agent_updates WHERE state IN ('queued','downloading','restarting') AND deadline>=?)`, unixNow(), unixNow()).Scan(&active); e != nil {
		return AgentUpdate{}, e
	}
	if active >= c.Service.MaxOps {
		return AgentUpdate{}, errors.New("已有操作执行中，请稍后重试")
	}
	u := AgentUpdate{ID: randomToken()[:24], ServerID: serverID, Target: *view.Target, Previous: v.Snapshot.Agent.SHA256, State: "queued", Actor: actor, Created: unixNow(), Updated: unixNow(), Deadline: unixNow() + 600}
	_, e = s.DB.Exec(`INSERT INTO agent_updates(`+agentUpdateColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?)`, u.ID, u.ServerID, encode(u.Target), u.Previous, u.State, u.Result, u.Actor, u.Created, u.Updated, u.Deadline)
	if e == nil {
		s.Event(serverID, "action", "info", "提交探针更新", agentBuildLabel(u.Target.Version, u.Target.Revision), actor)
	}
	return u, e
}

func (s *Store) LeaseAgentUpdate(id string) (*AgentUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.Server(id)
	if e != nil || !v.Config.Enabled || v.Snapshot.Agent == nil || !v.Snapshot.Agent.Updatable {
		return nil, e
	}
	var active int
	if e = s.DB.QueryRow(`SELECT count(*) FROM jobs WHERE server_id=? AND state='leased' AND deadline>=?`, id, unixNow()).Scan(&active); e != nil {
		return nil, e
	}
	if active != 0 {
		return nil, nil
	}
	u, e := scanAgentUpdate(s.DB.QueryRow(`SELECT `+agentUpdateColumns+` FROM agent_updates WHERE server_id=? AND state='queued' AND deadline>=? ORDER BY created LIMIT 1`, id, unixNow()))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if v.Snapshot.Agent.SHA256 != u.Previous || v.Snapshot.Agent.Arch != u.Target.Arch || !v.Config.VerifyTLS || v.SampleAt < unixNow()-int64(v.Config.HeartbeatTimeout) {
		_, e = s.DB.Exec(`UPDATE agent_updates SET state='cancelled',result='探针版本或连接设置已变化，请重新确认',updated=? WHERE id=?`, unixNow(), u.ID)
		return nil, e
	}
	u.State = "downloading"
	u.Updated = unixNow()
	_, e = s.DB.Exec(`UPDATE agent_updates SET state=?,updated=? WHERE id=? AND state='queued'`, u.State, u.Updated, u.ID)
	return &u, e
}

func (s *Store) ReportAgentUpdate(serverID string, report AgentUpdate) error {
	if report.State != "restarting" && report.State != "failed" {
		return errors.New("更新回执状态无效")
	}
	if len(report.Result) > 2048 {
		return errors.New("更新回执过大")
	}
	r, e := s.DB.Exec(`UPDATE agent_updates SET state=?,result=?,updated=? WHERE id=? AND server_id=? AND state IN ('downloading','restarting','unknown')`, report.State, report.Result, unixNow(), report.ID, serverID)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n > 0 && report.State == "failed" {
		u, _ := s.AgentUpdate(report.ID)
		s.Event(serverID, "action", "warning", "探针更新失败", report.Result, u.Actor)
	}
	return nil
}

func (s *Store) confirmAgentUpdate(v Server) {
	if v.Snapshot.Agent == nil {
		return
	}
	u := s.latestAgentUpdate(v.ID)
	if u == nil || (u.State != "restarting" && u.State != "unknown") || v.Snapshot.Agent.SHA256 != u.Target.SHA256 || v.SampleAt < u.Updated {
		return
	}
	r, e := s.DB.Exec(`UPDATE agent_updates SET state='success',result='新版探针已连接并上报采样',updated=? WHERE id=? AND state IN ('restarting','unknown')`, unixNow(), u.ID)
	if e == nil {
		if n, _ := r.RowsAffected(); n > 0 {
			s.Event(v.ID, "action", "success", "探针更新完成", agentBuildLabel(u.Target.Version, u.Target.Revision), u.Actor)
		}
	}
}

func (s *Store) expireAgentUpdates() {
	_, _ = s.DB.Exec(`UPDATE agent_updates SET state=CASE WHEN state='queued' THEN 'cancelled' ELSE 'unknown' END,result='未在期限内确认更新结果，请核对探针状态',updated=? WHERE state IN ('queued','downloading','restarting') AND deadline<?`, unixNow(), unixNow())
}
