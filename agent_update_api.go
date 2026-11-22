package main

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func (h *hubAPI) serveAgentArtifact(w http.ResponseWriter, r *http.Request, serverID, updateID string) {
	u, e := h.s.AgentUpdate(updateID)
	if e != nil || u.ServerID != serverID || u.State != "downloading" || u.Deadline < unixNow() {
		respond(w, 403, map[string]string{"error": "更新任务未获授权或已过期"})
		return
	}
	f, e := h.artifacts.artifact(u.Target.Arch)
	if e != nil || f.AgentArtifact != u.Target {
		respond(w, 409, map[string]string{"error": "面板更新包已变化，请重新发起更新"})
		return
	}
	source, e := os.Open(f.path)
	if e != nil {
		respond(w, 503, map[string]string{"error": "更新包不可读取"})
		return
	}
	defer source.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Minute))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="system-monitor"`)
	w.WriteHeader(200)
	_, _ = io.CopyN(w, source, f.Size)
}
