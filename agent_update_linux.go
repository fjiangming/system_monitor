//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

type agentRecovery struct {
	ID       string `json:"id"`
	Target   string `json:"target"`
	Previous string `json:"previous"`
	Deadline int64  `json:"deadline"`
	State    string `json:"state"`
	Launched bool   `json:"launched"`
}

func managedAgentPath(root string) string {
	return filepath.Join(filepath.Dir(root), "bin", "system-monitor")
}
func recoveryPath(root string) string {
	return filepath.Join(filepath.Dir(managedAgentPath(root)), ".agent-update.json")
}
func previousAgentPath(root, id string) string { return managedAgentPath(root) + ".previous-" + id }

func readAgentRecovery(root string) (agentRecovery, error) {
	var state agentRecovery
	f, e := os.Open(recoveryPath(root))
	if e != nil {
		return state, e
	}
	defer f.Close()
	if e = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&state); e != nil {
		return state, e
	}
	if !validAgentUpdateID(state.ID) || len(state.Target) != 64 || len(state.Previous) != 64 || (state.State != "pending" && state.State != "rolled_back") {
		return state, errors.New("探针更新恢复记录无效")
	}
	return state, nil
}

func syncAgentDirectory(root string) error {
	f, e := os.Open(filepath.Dir(managedAgentPath(root)))
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

func writeAgentRecovery(root string, state agentRecovery) error {
	f, e := os.CreateTemp(filepath.Dir(managedAgentPath(root)), ".agent-recovery-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write([]byte(encode(state))); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), recoveryPath(root)); e != nil {
		return e
	}
	return syncAgentDirectory(root)
}

func rollbackManagedAgent(root string, state agentRecovery) error {
	current, e := fileSHA256(managedAgentPath(root))
	if e != nil {
		return e
	}
	if current != state.Previous {
		previous, e := fileSHA256(previousAgentPath(root, state.ID))
		if e != nil || current != state.Target || previous != state.Previous {
			return errors.New("回退程序校验失败，已保留现场，请人工检查")
		}
		if e = os.Rename(previousAgentPath(root, state.ID), managedAgentPath(root)); e != nil {
			return e
		}
	}
	state.State = "rolled_back"
	return writeAgentRecovery(root, state)
}

func stopManagedChild(cmd *exec.Cmd, done <-chan error) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func superviseAgent(ctx context.Context, root string) error {
	if os.Geteuid() != 0 {
		return errors.New("探针托管服务需要 root 权限")
	}
	release, e := lockDirectory(filepath.Join(filepath.Dir(root), ".supervisor"))
	if e != nil {
		return e
	}
	defer release()
	for ctx.Err() == nil {
		state, e := readAgentRecovery(root)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e == nil && state.State == "pending" && state.Deadline < unixNow() {
			if e = rollbackManagedAgent(root, state); e != nil {
				return e
			}
			state, _ = readAgentRecovery(root)
		}
		if e == nil && state.State == "pending" && !state.Launched {
			current, hashErr := fileSHA256(managedAgentPath(root))
			if hashErr != nil {
				return hashErr
			}
			if current == state.Target {
				state.Launched = true
				state.Deadline = unixNow() + 120
				if e = writeAgentRecovery(root, state); e != nil {
					return e
				}
			}
		}
		cmd := exec.Command(managedAgentPath(root), "agent", "managed", "--data", root)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		if e = cmd.Start(); e != nil {
			if state, readErr := readAgentRecovery(root); readErr == nil && state.State == "pending" {
				if e = rollbackManagedAgent(root, state); e == nil {
					continue
				}
			}
			return e
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		ticker := time.NewTicker(time.Second)
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				ticker.Stop()
				stopManagedChild(cmd, done)
				return nil
			case e = <-done:
				if ctx.Err() != nil {
					ticker.Stop()
					return nil
				}
				state, readErr := readAgentRecovery(root)
				if readErr == nil && state.State == "pending" {
					var exit *exec.ExitError
					planned := errors.As(e, &exit) && exit.ExitCode() == 75 && !state.Launched
					if !planned {
						if e = rollbackManagedAgent(root, state); e != nil {
							ticker.Stop()
							return e
						}
					}
					restart = true
				} else {
					ticker.Stop()
					return e
				}
			case <-ticker.C:
				if state, readErr := readAgentRecovery(root); readErr == nil && state.State == "pending" && state.Deadline < unixNow() {
					stopManagedChild(cmd, done)
					if e = rollbackManagedAgent(root, state); e != nil {
						ticker.Stop()
						return e
					}
					restart = true
				}
			}
		}
		ticker.Stop()
	}
	return nil
}

var agentInfoOnce sync.Once
var processAgentInfo AgentInfo

func currentAgentInfo() *AgentInfo {
	agentInfoOnce.Do(func() {
		processAgentInfo = AgentInfo{Protocol: agentUpdateProtocol, Arch: runtime.GOARCH, Revision: buildRevision}
		exe, e := os.Executable()
		if e != nil {
			processAgentInfo.Reason = "探针程序路径不可读取"
			return
		}
		processAgentInfo.SHA256, e = fileSHA256(exe)
		if e != nil {
			processAgentInfo.Reason = "探针程序摘要不可读取"
			return
		}
		if len(os.Args) < 3 || os.Args[1] != "agent" || os.Args[2] != "managed" {
			processAgentInfo.Reason = "需要先安装新版探针托管服务"
			return
		}
		if os.Geteuid() != 0 || syscall.Access(filepath.Dir(exe), 2) != nil {
			processAgentInfo.Reason = "探针程序目录不可写"
			return
		}
		processAgentInfo.Updatable = true
	})
	return &processAgentInfo
}

func reportAgentUpdateRecovery(ctx context.Context, s *Store, identity agentIdentity, c ServerConfig) {
	var report AgentUpdate
	if state, e := readAgentRecovery(s.Root); e == nil && state.State == "rolled_back" {
		report = AgentUpdate{ID: state.ID, State: "failed", Result: "新版探针启动失败或未在 2 分钟内恢复采样，已自动回退旧程序"}
	} else {
		_ = json.Unmarshal([]byte(s.meta("agentUpdateFailure")), &report)
	}
	if report.ID == "" {
		return
	}
	if agentRequest(ctx, identity, "/api/agent/update-status", map[string]any{"id": identity.ID, "update": report}, nil, c) == nil {
		_ = s.setMeta("agentUpdateFailure", "")
		if state, e := readAgentRecovery(s.Root); e == nil && state.ID == report.ID && state.State == "rolled_back" {
			_ = os.Remove(recoveryPath(s.Root))
		}
	}
}

func acknowledgeAgentUpdate(s *Store, snapshot *Snapshot) {
	if snapshot == nil || snapshot.Agent == nil {
		return
	}
	state, e := readAgentRecovery(s.Root)
	if e == nil && state.State == "pending" && snapshot.Agent.SHA256 == state.Target {
		oldID := s.meta("agentUpdateBackup")
		if e = s.setMeta("agentUpdateBackup", state.ID); e != nil {
			return
		}
		if os.Remove(recoveryPath(s.Root)) != nil {
			return
		}
		_ = syncAgentDirectory(s.Root)
		if validAgentUpdateID(oldID) && oldID != state.ID {
			_ = os.Remove(previousAgentPath(s.Root, oldID))
		}
	}
}

func performAgentUpdate(ctx context.Context, s *Store, identity agentIdentity, c ServerConfig, u AgentUpdate) (result error) {
	defer func() {
		if result != nil && !errors.Is(result, errAgentUpdateRestart) {
			report := AgentUpdate{ID: u.ID, State: "failed", Result: result.Error()}
			_ = s.setMeta("agentUpdateFailure", encode(report))
		}
	}()
	if !validAgentUpdateID(u.ID) || !validAgentArtifact(u.Target) || u.Target.Arch != runtime.GOARCH || u.Deadline < unixNow() {
		return errors.New("探针更新任务无效或已过期")
	}
	if !secureAgentUpdateHub(identity.Hub, c) {
		return errors.New("探针更新需要经过证书校验的 HTTPS 连接")
	}
	info := currentAgentInfo()
	path := managedAgentPath(s.Root)
	exe, e := os.Executable()
	if !info.Updatable || exe != path || info.SHA256 != u.Previous {
		return errors.New("探针托管状态或当前版本已变化")
	}
	if _, e = readAgentRecovery(s.Root); !errors.Is(e, os.ErrNotExist) {
		return errors.New("存在尚未确认的探针更新，请先检查结果")
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(min(u.Deadline, unixNow()+300), 0))
	defer cancel()
	stage, e := os.CreateTemp(filepath.Dir(path), ".agent-download-*")
	if e != nil {
		return errors.New("探针程序目录不可写或空间不足")
	}
	defer os.Remove(stage.Name())
	defer stage.Close()
	client, transport, e := agentHTTPClient(c)
	if e != nil {
		return e
	}
	defer transport.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(identity.Hub, "/")+"/api/agent/update-download", strings.NewReader(encode(map[string]any{"id": identity.ID, "update": AgentUpdate{ID: u.ID}})))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+identity.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("探针更新包下载失败，旧程序未替换")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("更新包下载被拒绝（HTTP %d），旧程序未替换", resp.StatusCode)
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(stage, h), io.LimitReader(resp.Body, u.Target.Size+1))
	if e != nil || n != u.Target.Size || hex.EncodeToString(h.Sum(nil)) != u.Target.SHA256 {
		return errors.New("更新包大小或完整性校验失败，旧程序未替换")
	}
	if e = stage.Chmod(0755); e != nil {
		return e
	}
	if e = stage.Sync(); e != nil {
		return e
	}
	if e = stage.Close(); e != nil {
		return e
	}
	if e = checkAgentELF(stage.Name(), runtime.GOARCH); e != nil {
		return e
	}
	checkCtx, stop := context.WithTimeout(ctx, 10*time.Second)
	output, e := exec.CommandContext(checkCtx, stage.Name(), "build-info").Output()
	stop()
	var check struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		Protocol int    `json:"updateProtocol"`
	}
	if e != nil || json.Unmarshal(output, &check) != nil || check.Version != u.Target.Version || check.Revision != u.Target.Revision || check.OS != "linux" || check.Arch != runtime.GOARCH || check.Protocol < agentUpdateProtocol {
		return errors.New("新版探针预检查失败，旧程序未替换")
	}
	if current, e := fileSHA256(path); e != nil || current != u.Previous {
		return errors.New("探针程序已被外部替换，已取消更新")
	}
	if e = os.Link(path, previousAgentPath(s.Root, u.ID)); e != nil {
		return errors.New("无法保留旧探针程序，已取消更新")
	}
	state := agentRecovery{ID: u.ID, Target: u.Target.SHA256, Previous: u.Previous, Deadline: unixNow() + 120, State: "pending"}
	if e = writeAgentRecovery(s.Root, state); e != nil {
		_ = os.Remove(previousAgentPath(s.Root, u.ID))
		return e
	}
	if e = agentRequest(ctx, identity, "/api/agent/update-status", map[string]any{"id": identity.ID, "update": AgentUpdate{ID: u.ID, State: "restarting", Result: "更新包已校验，正在切换并等待新版采样"}}, nil, c); e != nil {
		_ = os.Remove(recoveryPath(s.Root))
		_ = os.Remove(previousAgentPath(s.Root, u.ID))
		return errors.New("面板未确认版本切换，旧程序未替换")
	}
	if e = os.Rename(stage.Name(), path); e != nil {
		_ = os.Remove(recoveryPath(s.Root))
		_ = os.Remove(previousAgentPath(s.Root, u.ID))
		return errors.New("探针程序替换失败，旧程序保持运行")
	}
	if e = syncAgentDirectory(s.Root); e != nil {
		_ = rollbackManagedAgent(s.Root, state)
		return errAgentUpdateRestart
	}
	return errAgentUpdateRestart
}
