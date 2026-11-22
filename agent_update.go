package main

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const agentUpdateProtocol = 1
const agentUpdateMaxSize = 128 << 20

var errAgentUpdateRestart = errors.New("agent update restart")

type AgentInfo struct {
	Protocol  int    `json:"protocol"`
	Arch      string `json:"arch"`
	Revision  string `json:"revision"`
	SHA256    string `json:"sha256"`
	Updatable bool   `json:"updatable"`
	Reason    string `json:"reason,omitempty"`
}

type AgentArtifact struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Arch     string `json:"arch"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

type AgentUpdate struct {
	ID       string        `json:"id"`
	ServerID string        `json:"serverID"`
	Target   AgentArtifact `json:"target"`
	Previous string        `json:"previous"`
	State    string        `json:"state"`
	Result   string        `json:"result"`
	Actor    string        `json:"actor"`
	Created  int64         `json:"created"`
	Updated  int64         `json:"updated"`
	Deadline int64         `json:"deadline"`
}

type AgentUpdateView struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason"`
	Target    *AgentArtifact `json:"target,omitempty"`
	Latest    *AgentUpdate   `json:"latest,omitempty"`
}

type agentArtifactFile struct {
	AgentArtifact
	path string
}

type agentArtifacts struct {
	root  string
	mu    sync.Mutex
	files map[string]agentArtifactFile
}

func buildInfo() map[string]any {
	return map[string]any{"version": version, "revision": buildRevision, "os": runtime.GOOS, "arch": runtime.GOARCH, "updateProtocol": agentUpdateProtocol}
}

func fileSHA256(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, agentUpdateMaxSize+1))
	if e != nil {
		return "", e
	}
	if n > agentUpdateMaxSize {
		return "", errors.New("探针程序大小超出更新限制")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checkAgentELF(path, arch string) error {
	f, e := elf.Open(path)
	if e != nil {
		return errors.New("更新包不是有效的 Linux 程序")
	}
	defer f.Close()
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[arch]
	if machine == elf.EM_NONE || f.Machine != machine || f.Class != elf.ELFCLASS64 || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) {
		return errors.New("更新包与探针架构不匹配")
	}
	return nil
}

func (a *agentArtifacts) artifact(arch string) (agentArtifactFile, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if arch != "amd64" && arch != "arm64" {
		return agentArtifactFile{}, errors.New("暂不支持此探针架构")
	}
	if f, ok := a.files[arch]; ok {
		return f, nil
	}
	path := filepath.Join(a.root, "system-monitor-linux-"+arch)
	if _, e := os.Stat(path); errors.Is(e, os.ErrNotExist) && runtime.GOOS == "linux" && runtime.GOARCH == arch {
		path, _ = os.Executable()
		if strings.HasSuffix(path, ".test") {
			return agentArtifactFile{}, errors.New("面板未打包此架构的探针")
		}
	}
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > agentUpdateMaxSize {
		return agentArtifactFile{}, errors.New("面板未打包此架构的探针")
	}
	if e = checkAgentELF(path, arch); e != nil {
		return agentArtifactFile{}, e
	}
	hash, e := fileSHA256(path)
	if e != nil {
		return agentArtifactFile{}, errors.New("探针更新包不可读取")
	}
	f := agentArtifactFile{AgentArtifact: AgentArtifact{Version: version, Revision: buildRevision, Arch: arch, SHA256: hash, Size: info.Size()}, path: path}
	if a.files == nil {
		a.files = map[string]agentArtifactFile{}
	}
	a.files[arch] = f
	return f, nil
}

func defaultAgentArtifacts() *agentArtifacts {
	root := "/usr/local/share/system-monitor/agents"
	if exe, e := os.Executable(); e == nil {
		if dir := filepath.Join(filepath.Dir(exe), "agents"); isDirectory(dir) {
			root = dir
		}
	}
	return &agentArtifacts{root: root}
}

func isDirectory(path string) bool { info, e := os.Stat(path); return e == nil && info.IsDir() }

func secureAgentUpdateHub(hub string, c ServerConfig) bool {
	u, e := url.Parse(hub)
	if e != nil || !c.VerifyTLS {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1" || u.Hostname() == "localhost")
}

func validAgentUpdateID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validAgentArtifact(a AgentArtifact) bool {
	b, e := hex.DecodeString(a.SHA256)
	return e == nil && len(b) == 32 && (a.Arch == "amd64" || a.Arch == "arm64") && a.Size > 0 && a.Size <= agentUpdateMaxSize && len(a.Revision) <= 128 && a.Version != "" && len(a.Version) <= 64
}

func agentBuildLabel(version, revision string) string {
	if len(revision) > 12 {
		revision = revision[:12]
	}
	return fmt.Sprintf("%s (%s)", version, revision)
}
