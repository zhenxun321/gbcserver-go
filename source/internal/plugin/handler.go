package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WhitelistService 承载网页授权、在线状态和在线时长的领域逻辑。
// Handler 只负责协议、认证和错误边界，不在 HTTP 层复制业务状态。
type WhitelistService interface {
	Check(ctx context.Context, serverID, username string) (CheckResult, error)
	Join(ctx context.Context, serverID, username, attemptID string) (JoinResult, error)
	Logout(ctx context.Context, serverID, username string) (LogoutResult, error)
	Heartbeat(ctx context.Context, serverID string, usernames []string) (HeartbeatResult, error)
}

// CouponService 承载兑换码事务。Accepted=false 仍是业务拒绝，不是 HTTP 服务器故障。
type CouponService interface {
	UseCoupon(ctx context.Context, serverID, username, code, requestID string) (CouponResult, error)
}

type CheckResult struct {
	Allowed        bool
	Reason         string
	LoginAttemptID string
	ExpiresAt      int64
}

type JoinResult struct {
	Confirmed   bool
	AlreadyOpen bool
	Reason      string
}

type LogoutResult struct {
	AlreadyClosed       bool
	AuthorizationClosed bool
}

type HeartbeatResult struct {
	RejectedPlayers []string
}

type CouponReward struct {
	ItemID string `json:"itemId"`
	Amount int64  `json:"amount"`
}

type CouponResult struct {
	Accepted   bool           `json:"accepted"`
	Message    string         `json:"message"`
	Items      []CouponReward `json:"items"`
	HTTPStatus int            `json:"-"`
}

// Handler 是白名单内网端口和兑换码 /api 端点的协议适配层。
type Handler struct {
	Tokens           *TokenStore
	Whitelist        WhitelistService
	Coupons          CouponService
	ExpectedServerID string
	MaxBodyBytes     int64
	CouponRateLimit  int
	rateMu           sync.Mutex
	rateWindows      map[string]couponRateWindow
}

// ServeHTTP 根据路径分发新插件协议；未识别路径统一返回 404。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/heartbeat":
		h.handleHeartbeat(w, r)
	case strings.HasPrefix(r.URL.Path, "/join/"):
		h.handleJoin(w, r)
	case strings.HasPrefix(r.URL.Path, "/check/"):
		h.handleCheck(w, r)
	case strings.HasPrefix(r.URL.Path, "/logout/"):
		h.handleLogout(w, r)
	case r.URL.Path == "/api":
		h.handleCoupon(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "not found"})
	}
}

// NewWhitelistHTTPHandler 为内网白名单 listener 暴露认证、入服确认、心跳和登出路由。
func NewWhitelistHTTPHandler(handler *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/heartbeat", handler.handleHeartbeat)
	mux.HandleFunc("/join/", handler.handleJoin)
	mux.HandleFunc("/check/", handler.handleCheck)
	mux.HandleFunc("/logout/", handler.handleLogout)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"allowed": false, "reason": "not_found"})
	})
	return mux
}

// NewCouponHTTPHandler 为内网兑换 listener 仅暴露 useCoupon。
func NewCouponHTTPHandler(handler *Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api", handler.handleCoupon)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "message": "not_found"})
	})
	return mux
}

func (h *Handler) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"allowed": false, "reason": "method_not_allowed"})
		return
	}
	if !h.allowedSource(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"allowed": false, "reason": "source_not_allowed"})
		return
	}
	principal, ok := h.authenticate(w, r, ScopeWhitelist, "check")
	if !ok {
		return
	}
	username, ok := pathUsername(r.URL.Path, "/check/")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"allowed": false, "reason": "invalid_username"})
		return
	}
	if h.Whitelist == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"allowed": false, "reason": "service_unavailable"})
		return
	}
	result, err := h.Whitelist.Check(r.Context(), principal.ServerID, username)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"allowed": false, "reason": "service_unavailable"})
		return
	}
	if !result.Allowed {
		writeJSON(w, http.StatusForbidden, map[string]any{"allowed": false, "reason": stableReason(result.Reason, "not_authorized")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"allowed": true, "reason": stableReason(result.Reason, "authorized"),
		"loginAttemptId": result.LoginAttemptID, "expiresAt": result.ExpiresAt,
	})
}

func (h *Handler) handleJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "reason": "method_not_allowed"})
		return
	}
	if !h.allowedSource(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "reason": "source_not_allowed"})
		return
	}
	principal, ok := h.authenticate(w, r, ScopeWhitelist, "join")
	if !ok {
		return
	}
	username, ok := pathUsername(r.URL.Path, "/join/")
	attemptID := strings.TrimSpace(r.Header.Get("X-Login-Attempt-ID"))
	if !ok || attemptID == "" || len(attemptID) > 128 || strings.ContainsAny(attemptID, "\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "reason": "invalid_login_attempt"})
		return
	}
	if h.Whitelist == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "service_unavailable"})
		return
	}
	result, err := h.Whitelist.Join(r.Context(), principal.ServerID, username, attemptID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "service_unavailable"})
		return
	}
	if !result.Confirmed {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "reason": stableReason(result.Reason, "login_attempt_invalid")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alreadyOpen": result.AlreadyOpen})
}

func (h *Handler) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "reason": "method_not_allowed"})
		return
	}
	if !h.allowedSource(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "reason": "source_not_allowed"})
		return
	}
	principal, ok := h.authenticate(w, r, ScopeWhitelist, "heartbeat")
	if !ok {
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"ok": false, "reason": "unsupported_media_type"})
		return
	}
	if h.Whitelist == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "service_unavailable"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	var request heartbeatRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "reason": "request_too_large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "reason": "invalid_request"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "reason": "invalid_request"})
		return
	}
	if request.Players == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "reason": "players_required"})
		return
	}
	if len(request.Players) > 5000 {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"ok": false, "reason": "too_many_players"})
		return
	}
	for _, username := range request.Players {
		if strings.TrimSpace(username) == "" || len(username) > 128 || strings.ContainsAny(username, "\r\n") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "reason": "invalid_player_name"})
			return
		}
	}
	result, err := h.Whitelist.Heartbeat(r.Context(), principal.ServerID, request.Players)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "service_unavailable"})
		return
	}
	if result.RejectedPlayers == nil {
		result.RejectedPlayers = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rejectedPlayers": result.RejectedPlayers})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "reason": "method_not_allowed"})
		return
	}
	if !h.allowedSource(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "reason": "source_not_allowed"})
		return
	}
	principal, ok := h.authenticate(w, r, ScopeWhitelist, "logout")
	if !ok {
		return
	}
	username, ok := pathUsername(r.URL.Path, "/logout/")
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "reason": "invalid_username"})
		return
	}
	if h.Whitelist == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "service_unavailable"})
		return
	}
	result, err := h.Whitelist.Logout(r.Context(), principal.ServerID, username)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "reason": "service_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "alreadyClosed": result.AlreadyClosed})
}

func (h *Handler) handleCoupon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"success": false, "message": "method_not_allowed"})
		return
	}
	if !h.allowedSource(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"success": false, "message": "source_not_allowed"})
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || !strings.EqualFold(mediaType, "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"success": false, "message": "unsupported_media_type"})
		return
	}
	principal, ok := h.authenticate(w, r, ScopeCouponRedeem, "coupon")
	if !ok {
		return
	}
	if !h.allowCouponRequest(principal.ServerID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": "rate_limited"})
		return
	}
	if h.Coupons == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "兑换服务暂不可用"})
		return
	}
	maxBytes := h.MaxBodyBytes
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	var request couponRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"success": false, "message": "request_too_large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式不正确"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式不正确"})
		return
	}
	username := strings.TrimSpace(request.Username)
	code := strings.TrimSpace(request.Code)
	requestID := strings.TrimSpace(request.RequestID)
	headerRequestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	if request.Method != "useCoupon" || username == "" || code == "" || requestID == "" ||
		len(username) > 128 || len(code) > 512 || len(requestID) > 128 ||
		strings.ContainsAny(requestID, "\r\n") ||
		(headerRequestID != "" && headerRequestID != requestID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "参数不完整"})
		return
	}
	result, err := h.Coupons.UseCoupon(r.Context(), principal.ServerID, username, code, requestID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"success": false, "message": "兑换服务暂不可用"})
		return
	}
	status := result.HTTPStatus
	if status < 100 || status > 599 {
		status = http.StatusOK
	}
	if !result.Accepted {
		writeJSON(w, status, map[string]any{"success": false, "message": stableReason(result.Message, "兑换失败")})
		return
	}
	writeJSON(w, status, map[string]any{"success": true, "message": stableReason(result.Message, "兑换成功"), "items": result.Items})
}

func (h *Handler) allowCouponRequest(serverID string) bool {
	limit := h.CouponRateLimit
	if limit <= 0 {
		limit = 120
	}
	now := time.Now().Unix() / 60
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	if h.rateWindows == nil {
		h.rateWindows = make(map[string]couponRateWindow)
	}
	window := h.rateWindows[serverID]
	if window.Minute != now {
		window = couponRateWindow{Minute: now}
	}
	if window.Count >= limit {
		return false
	}
	window.Count++
	h.rateWindows[serverID] = window
	if len(h.rateWindows) > 4096 {
		for key, candidate := range h.rateWindows {
			if candidate.Minute < now {
				delete(h.rateWindows, key)
			}
		}
	}
	return true
}

type couponRateWindow struct {
	Minute int64
	Count  int
}

type couponRequest struct {
	Method    string `json:"method"`
	Username  string `json:"username"`
	Code      string `json:"code"`
	RequestID string `json:"requestId"`
}

type heartbeatRequest struct {
	Players []string `json:"players"`
}

func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request, scope Scope, endpoint string) (Principal, bool) {
	if h.Tokens == nil {
		writeAuthFailure(w, scope, endpoint, http.StatusServiceUnavailable, "service_unavailable")
		return Principal{}, false
	}
	principal, err := h.Tokens.AuthenticateRequest(r.Context(), r, scope)
	if err == nil {
		if expected := strings.TrimSpace(h.ExpectedServerID); expected != "" && principal.ServerID != expected {
			writeAuthFailure(w, scope, endpoint, http.StatusForbidden, "server_id_mismatch")
			return Principal{}, false
		}
		return principal, true
	}
	if errors.Is(err, ErrScopeForbidden) {
		writeAuthFailure(w, scope, endpoint, http.StatusForbidden, "permission_denied")
		return Principal{}, false
	}
	if errors.Is(err, ErrMissingToken) || errors.Is(err, ErrInvalidToken) || errors.Is(err, ErrRevokedToken) || errors.Is(err, ErrExpiredToken) {
		writeAuthFailure(w, scope, endpoint, http.StatusUnauthorized, "authentication_failed")
		return Principal{}, false
	}
	writeAuthFailure(w, scope, endpoint, http.StatusServiceUnavailable, "service_unavailable")
	return Principal{}, false
}

func writeAuthFailure(w http.ResponseWriter, scope Scope, endpoint string, status int, reason string) {
	if scope == ScopeWhitelist && endpoint != "check" {
		writeJSON(w, status, map[string]any{"ok": false, "reason": reason})
		return
	}
	if scope == ScopeWhitelist {
		writeJSON(w, status, map[string]any{"allowed": false, "reason": reason})
		return
	}
	writeJSON(w, status, map[string]any{"success": false, "message": reason})
}

func (h *Handler) allowedSource(r *http.Request) bool {
	// 两类 listener 都只允许内网 TCP 对端，不读取 X-Forwarded-For。
	if r == nil {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return isPrivateAddress(ip)
}

func isPrivateAddress(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func pathUsername(path, prefix string) (string, bool) {
	encoded := strings.TrimPrefix(path, prefix)
	if encoded == path || encoded == "" || strings.Contains(encoded, "/") {
		return "", false
	}
	username, err := url.PathUnescape(encoded)
	if err != nil || strings.TrimSpace(username) == "" || strings.Contains(username, "/") || strings.ContainsAny(username, "\r\n") {
		return "", false
	}
	return username, true
}

func stableReason(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") {
		return fallback
	}
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return
	}
}
