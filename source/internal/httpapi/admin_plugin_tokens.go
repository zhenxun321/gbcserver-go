package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"gbcserver/source/internal/plugin"
)

func (s *Server) handleAdminPluginTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.adminSessions.Get(r.Header.Get("X-Admin-Session")); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效"})
		return
	}

	tokens := plugin.NewTokenStore(s.DB)
	if r.Method == http.MethodGet {
		items, err := tokens.List(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取插件令牌失败"})
			return
		}
		serverID, err := s.pluginServerID(r)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取插件服务 ID 失败"})
			return
		}
		whitelistListen, err := s.pluginConfigValue(r, "whitelistListen", "127.0.0.1:18094")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取白名单监听地址失败"})
			return
		}
		couponListen, err := s.pluginConfigValue(r, "couponListen", "127.0.0.1:18095")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取兑换监听地址失败"})
			return
		}
		whitelistURL, err := pluginDefaultURL(whitelistListen, plugin.ScopeWhitelist)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "白名单监听地址无效"})
			return
		}
		couponURL, err := pluginDefaultURL(couponListen, plugin.ScopeCouponRedeem)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "兑换监听地址无效"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true, "serverID": serverID, "tokens": items,
			"whitelistURL": whitelistURL, "couponURL": couponURL,
		})
		return
	}

	var input struct {
		Scope     string `json:"scope"`
		APIHost   string `json:"apiHost"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
		return
	}
	scope := plugin.Scope(strings.TrimSpace(input.Scope))
	if scope != plugin.ScopeWhitelist && scope != plugin.ScopeCouponRedeem {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "令牌权限无效"})
		return
	}
	serverID, err := s.pluginServerID(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取插件服务 ID 失败"})
		return
	}
	listenKey, listenFallback := "whitelistListen", "127.0.0.1:18094"
	if scope == plugin.ScopeCouponRedeem {
		listenKey, listenFallback = "couponListen", "127.0.0.1:18095"
	}
	listen, err := s.pluginConfigValue(r, listenKey, listenFallback)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取插件监听地址失败"})
		return
	}
	apiURL, err := pluginURLForHost(listen, scope, input.APIHost)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
		return
	}
	var expiresAt *time.Time
	if strings.TrimSpace(input.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(input.ExpiresAt))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "过期时间格式无效"})
			return
		}
		expiresAt = &parsed
		if !expiresAt.After(time.Now()) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "过期时间必须晚于当前时间"})
			return
		}
	}
	issued, err := tokens.Issue(r.Context(), serverID, scope, expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "签发插件令牌失败"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "apiURL": apiURL, "token": issued})
}

func (s *Server) handleAdminPluginToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodDelete {
		w.Header().Set("Allow", "DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.adminSessions.Get(r.Header.Get("X-Admin-Session")); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效"})
		return
	}
	tokenID := strings.TrimPrefix(r.URL.Path, "/api/admin/plugin-tokens/")
	if tokenID == "" || strings.Contains(tokenID, "/") {
		http.NotFound(w, r)
		return
	}
	if err := plugin.NewTokenStore(s.DB).Revoke(r.Context(), tokenID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "撤销插件令牌失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "token_id": tokenID})
}

func (s *Server) pluginServerID(r *http.Request) (string, error) {
	serverID, exists, err := s.configStore.Get(r.Context(), "serverID")
	if err != nil {
		return "", err
	}
	if !exists || strings.TrimSpace(serverID) == "" {
		serverID = strings.TrimSpace(s.Config.ServerID)
	}
	if serverID == "" {
		serverID = "default"
	}
	return serverID, nil
}

func (s *Server) pluginConfigValue(r *http.Request, key, fallback string) (string, error) {
	value, exists, err := s.configStore.Get(r.Context(), key)
	if err != nil {
		return "", err
	}
	if !exists || strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	return strings.TrimSpace(value), nil
}

func pluginDefaultURL(listen string, scope plugin.Scope) (string, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return pluginURLForHost(listen, scope, host)
}

func pluginURLForHost(listen string, scope plugin.Scope, host string) (string, error) {
	_, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", err
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()) {
		return "", errors.New("API 主机必须是 loopback、RFC1918、IPv6 ULA 或 link-local 私网 IP")
	}
	address := "http://" + net.JoinHostPort(ip.String(), port)
	if scope == plugin.ScopeCouponRedeem {
		address += "/api"
	}
	return address, nil
}
