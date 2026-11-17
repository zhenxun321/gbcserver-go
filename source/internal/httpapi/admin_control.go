package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) handleAdminControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.adminSessions.Get(r.Header.Get("X-Admin-Session")); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效"})
		return
	}
	if s.Config.AdminControl == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "当前启动方式不支持网页服务控制"})
		return
	}
	var input struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8*1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
		return
	}
	input.Action = strings.TrimSpace(input.Action)
	if input.Action != "reload" && input.Action != "stop" && input.Action != "install-autostart" && input.Action != "uninstall-autostart" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "不支持的服务操作"})
		return
	}
	if input.Action == "install-autostart" || input.Action == "uninstall-autostart" {
		if err := s.Config.AdminControl(input.Action); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": err.Error()})
			return
		}
		message := "已安装当前用户开机自启"
		if input.Action == "uninstall-autostart" {
			message = "已移除当前用户开机自启"
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": message})
		return
	}
	message := "重新加载请求已发送"
	if input.Action == "stop" {
		message = "停止请求已发送"
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"success": true, "message": message})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	_ = s.Config.AdminControl(input.Action)
}
