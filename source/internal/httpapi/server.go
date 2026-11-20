package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gbcserver/source/internal/plugin"
	"gbcserver/source/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Config 保存公开兼容接口展示的限制值。
type Config struct {
	// StaticFS 是内嵌的只读前端资源；磁盘目录优先，缺失文件再回退到此 FS。
	StaticFS                    fs.FS
	BodyLimitBytes              int64 `json:"bodyLimitBytes"`
	MaxAvatarBytes              int64 `json:"maxAvatarBytes"`
	MaxItemImageBytes           int64 `json:"maxItemImageBytes"`
	MaxCouponItemAmount         int   `json:"maxCouponItemAmount"`
	WebSessionTTL               time.Duration
	SignTimezoneOffset          int
	SignTimezoneExplicit        bool
	MaxOnlineTime               time.Duration
	ServerID                    string
	WebhookPath                 string
	StatusServers               []StatusServerConfig
	StatusAPIs                  []StatusAPIConfig
	StatusTimeout               time.Duration
	StatusCacheTTL              time.Duration
	StatusMonitorInterval       time.Duration
	StatusAlertAfter            time.Duration
	StatusAlertEmailEnabled     bool
	StatusAlertEmail            string
	StatusAlertWebhookEnabled   bool
	StatusAlertWebhookURL       string
	StatusAlertRecoveryEnabled  bool
	StatusAlertRecoveryExplicit bool
	GitHubRelease               bool
	MojangLookup                MojangLookup
	MailSender                  MailSender
	AdminUsername               string
	AdminPasswordHash           string
	AdminTOTPSecret             string
	AdminSessionTTL             time.Duration
	AdminPath                   string
	AdminControl                func(string) error
}

// StatusServerConfig 描述可选的 Minecraft 状态探测目标。
type StatusServerConfig struct {
	Name, Host  string
	DefaultPort int
}

// StatusAPIConfig 描述一个可替换的第三方 Minecraft 状态接口。
// URLTemplate 支持 {host}、{port} 和 {hostport} 占位符。
type StatusAPIConfig struct {
	Name        string
	URLTemplate string
}

// MojangLookup 查询在线模式下的 Minecraft 正版 UUID。
type MojangLookup interface {
	Lookup(ctx context.Context, username string) (string, error)
}

var defaultStatusAPIs = []StatusAPIConfig{
	{Name: "api.mcsrvstat.us", URLTemplate: "https://api.mcsrvstat.us/3/{hostport}"},
	{Name: "mcapi.us", URLTemplate: "https://mcapi.us/server/status?ip={host}&port={port}"},
	{Name: "api.minetools.eu", URLTemplate: "https://api.minetools.eu/ping/{host}/{port}"},
}

// MailSender 是注册和密码找回的可替换邮件接口。
type MailSender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Server 提供 Go Web 服务的 HTTP 路由和旧 /api 参数解析基础。
type Server struct {
	DB                   *sql.DB
	Config               Config
	staticDir            string
	avatarDir            string
	staticFS             fs.FS
	mux                  *http.ServeMux
	sessions             *sessionStore
	adminSessions        *sessionStore
	plugins              *plugin.SQLiteService
	adminSetupMu         sync.Mutex
	failureMu            sync.Mutex
	adminFailures        map[string]adminFailure
	adminTOTPEnrollMu    sync.Mutex
	adminTOTPEnrollments map[string]adminTOTPEnrollment
	configStore          store.ConfigStore
	verificationMu       sync.Mutex
	verifications        map[string]verificationRecord
	statusMu             sync.Mutex
	statusCache          map[string]statusCacheEntry
	statusMonitorStarted bool
}

type verificationRecord struct {
	Code, Name, PasswordHash, UUID string
	ExpiresAt                      time.Time
	Kind                           string
}
type statusCacheEntry struct {
	Data      map[string]any
	ExpiresAt time.Time
}

var legacyMethods = map[string]struct{}{
	"getAllUsers": {}, "activateUser": {}, "banUser": {}, "unbanUser": {},
	"deleteUser": {}, "updateEmail": {}, "changePassword": {}, "addShopItem": {},
	"updateShopItem": {}, "deleteShopItem": {}, "getShopItems": {}, "purchaseItem": {},
	"generateCoupon": {}, "useCoupon": {}, "getCoupons": {}, "deleteCoupon": {},
	"login": {}, "logout": {}, "regist": {}, "verifyRegist": {},
	"repasswd": {}, "verifyCode": {},
}

// New 创建 Web 路由。静态目录为空时只启用 API，不提供文件服务。
func New(db *sql.DB, staticDir, avatarDir string, config Config) (*Server, error) {
	if db == nil {
		return nil, errors.New("Web SQLite 未初始化")
	}
	if config.BodyLimitBytes <= 0 {
		config.BodyLimitBytes = 25_000_000
	}
	if config.MaxAvatarBytes <= 0 {
		config.MaxAvatarBytes = 10 * 1024 * 1024
	}
	if config.MaxItemImageBytes <= 0 {
		config.MaxItemImageBytes = 5 * 1024 * 1024
	}
	if config.MaxCouponItemAmount <= 0 {
		config.MaxCouponItemAmount = 2304
	}
	if config.WebSessionTTL <= 0 {
		config.WebSessionTTL = time.Hour
	}
	if config.AdminSessionTTL <= 0 {
		config.AdminSessionTTL = time.Hour
	}
	if !config.SignTimezoneExplicit {
		config.SignTimezoneOffset = 8
	}
	if config.MaxOnlineTime <= 0 {
		config.MaxOnlineTime = time.Duration(3214080000000) * time.Millisecond
	}
	if strings.TrimSpace(config.ServerID) == "" {
		config.ServerID = "default"
	}
	if strings.TrimSpace(config.WebhookPath) == "" {
		config.WebhookPath = "/webhook/update-port"
	}
	if config.StatusTimeout <= 0 {
		config.StatusTimeout = 5 * time.Second
	}
	if config.StatusCacheTTL <= 0 {
		config.StatusCacheTTL = 25 * time.Second
	}
	if config.StatusMonitorInterval <= 0 {
		config.StatusMonitorInterval = time.Minute
	}
	if config.StatusAlertAfter <= 0 {
		config.StatusAlertAfter = 2 * time.Minute
	}
	if !config.StatusAlertRecoveryExplicit {
		config.StatusAlertRecoveryEnabled = true
	}
	if config.StatusAPIs == nil {
		config.StatusAPIs = append([]StatusAPIConfig(nil), defaultStatusAPIs...)
	}
	if strings.TrimSpace(staticDir) != "" {
		abs, err := filepath.Abs(staticDir)
		if err != nil {
			return nil, fmt.Errorf("解析静态目录失败: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("静态目录不可用: %w", err)
		}
		if !info.IsDir() {
			return nil, errors.New("静态资源路径必须是目录")
		}
		staticDir = abs
	}
	if strings.TrimSpace(avatarDir) != "" {
		abs, err := filepath.Abs(avatarDir)
		if err != nil {
			return nil, fmt.Errorf("解析头像目录失败: %w", err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("头像目录不可用: %w", err)
		}
		if !info.IsDir() {
			return nil, errors.New("头像资源路径必须是目录")
		}
		avatarDir = abs
	}
	plugins := plugin.NewSQLiteService(db)
	plugins.LoginTimezoneOffsetHours = config.SignTimezoneOffset
	server := &Server{DB: db, Config: config, staticDir: staticDir, avatarDir: avatarDir, staticFS: config.StaticFS, mux: http.NewServeMux(), sessions: newSessionStore(), adminSessions: newSessionStore(), plugins: plugins, adminFailures: make(map[string]adminFailure), adminTOTPEnrollments: make(map[string]adminTOTPEnrollment), verifications: make(map[string]verificationRecord), statusCache: make(map[string]statusCacheEntry), configStore: store.ConfigStore{DB: db}}
	if err := repairLegacyShopImages(context.Background(), db); err != nil {
		return nil, fmt.Errorf("修复旧商品图片引用失败: %w", err)
	}
	server.routes()
	return server, nil
}

// ServeHTTP 使 Server 可直接交给 net/http。
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.mux.ServeHTTP(writer, request)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/api/publicConfig", s.handlePublicConfig)
	s.mux.HandleFunc("/api/webLogin", s.handleWebLogin)
	s.mux.HandleFunc("/api/adminLogin", s.handleAdminLogin)
	s.mux.HandleFunc("/api/admin/status", s.handleAdminStatus)
	s.mux.HandleFunc("/api/admin/setup", s.handleAdminSetup)
	s.mux.HandleFunc("/api/admin/totp/begin", s.handleAdminTOTPBegin)
	s.mux.HandleFunc("/api/admin/totp/confirm", s.handleAdminTOTPConfirm)
	s.mux.HandleFunc("/api/admin/totp/disable", s.handleAdminTOTPDisable)
	s.mux.HandleFunc("/api/admin/config", s.handleAdminConfig)
	s.mux.HandleFunc("/api/admin/plugin-tokens", s.handleAdminPluginTokens)
	s.mux.HandleFunc("/api/admin/plugin-tokens/", s.handleAdminPluginToken)
	s.mux.HandleFunc("/api/admin/control", s.handleAdminControl)
	s.mux.HandleFunc("/api/userInfo", s.handleUserInfo)
	s.mux.HandleFunc("/api/sign", s.handleSign)
	s.mux.HandleFunc("/api/signHistory", s.handleSignHistory)
	s.mux.HandleFunc("/api/leaderboard", s.handleLeaderboard)
	s.mux.HandleFunc("/api/uploadAvatar", s.handleUploadAvatar)
	s.mux.HandleFunc("/api/uploadItemImage", s.handleUploadItemImage)
	s.mux.HandleFunc("/api/serverStatus", s.handleServerStatus)
	s.mux.HandleFunc(s.Config.WebhookPath+"/health", s.handleWebhookHealth)
	s.mux.HandleFunc(s.Config.WebhookPath, s.handleWebhook)
	s.mux.HandleFunc("/api", s.handleLegacyAPI)
	s.mux.HandleFunc("/ports", s.handlePorts)
	if s.avatarDir != "" {
		s.mux.Handle("/avatars/", http.StripPrefix("/avatars/", http.FileServer(http.Dir(s.avatarDir))))
	}
	var fileServer http.Handler
	if s.staticDir != "" || s.staticFS != nil {
		var staticFiles fs.FS
		switch {
		case s.staticFS != nil && s.staticDir != "":
			staticFiles = fallbackFS{primary: os.DirFS(s.staticDir), fallback: s.staticFS, embeddedAdmin: s.Config.GitHubRelease}
		case s.staticFS != nil:
			staticFiles = s.staticFS
		default:
			staticFiles = os.DirFS(s.staticDir)
		}
		fileServer = http.FileServer(http.FS(staticFiles))
		s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/login.html", http.StatusFound)
				return
			}
			if isIndexHomepageAsset(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			// 管理页面只能经由管理员入口访问，不能直接猜测静态文件名绕过入口。
			if isProtectedAdminAsset(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			fileServer.ServeHTTP(w, r)
		}))
	}
	adminPath := strings.TrimSuffix(strings.TrimSpace(s.Config.AdminPath), "/")
	if adminPath == "" {
		adminPath = "/admin"
	}
	// 管理员安全入口只由配置决定，禁止把入口暴露到玩家页面或静态文件索引。
	if !strings.HasPrefix(adminPath, "/") {
		adminPath = "/" + adminPath
	}
	s.mux.HandleFunc(adminPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, adminPath+"/", http.StatusPermanentRedirect)
	})
	s.mux.Handle(adminPath+"/", http.StripPrefix(adminPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		if r.URL.Path == "" || r.URL.Path == "/" {
			r.URL.Path = "/admin.html"
		}
		if !isProtectedAdminAsset(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if fileServer != nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})))
}

func isProtectedAdminAsset(requestPath string) bool {
	switch strings.ToLower(path.Clean(requestPath)) {
	case "/admin.html", "/admin.css", "/admin-config.js", "/admin-legacy.js", "/admin-tokens.js", "/i18n-admin.js":
		return true
	default:
		return false
	}
}

func isIndexHomepageAsset(requestPath string) bool {
	switch strings.ToLower(path.Clean(requestPath)) {
	case "/index.html":
		return true
	default:
		return false
	}
}

// fallbackFS 让部署目录中的可写上传文件覆盖内嵌只读资源，其他文件从内嵌 FS 提供。
type fallbackFS struct {
	primary       fs.FS
	fallback      fs.FS
	embeddedAdmin bool
}

func (f fallbackFS) Open(name string) (fs.File, error) {
	if f.embeddedAdmin && isProtectedAdminAsset("/"+name) {
		if file, err := f.fallback.Open(name); err == nil {
			return file, nil
		}
	}
	if file, err := f.primary.Open(name); err == nil {
		return file, nil
	}
	return f.fallback.Open(name)
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.PingContext(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handlePublicConfig(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	branding, err := s.siteBrandingConfig(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取站点配置失败"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"success":       true,
		"branding":      branding,
		"githubRelease": s.Config.GitHubRelease,
		"limits": map[string]any{
			"maxAvatarBytes":              s.Config.MaxAvatarBytes,
			"maxItemImageBytes":           s.Config.MaxItemImageBytes,
			"maxCouponItemAmount":         s.Config.MaxCouponItemAmount,
			"configuredMaxAvatarBytes":    s.Config.MaxAvatarBytes,
			"configuredMaxItemImageBytes": s.Config.MaxItemImageBytes,
			"bodyLimitBytes":              s.Config.BodyLimitBytes,
		},
	})
}

func (s *Server) handlePorts(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows, err := s.DB.QueryContext(request.Context(), "SELECT key, port FROM ports ORDER BY key")
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "Internal Server Error"})
		return
	}
	defer rows.Close()
	ports := make(map[string]int)
	for rows.Next() {
		var key string
		var port int
		if err := rows.Scan(&key, &port); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "Internal Server Error"})
			return
		}
		ports[key] = port
	}
	if err := rows.Err(); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "Internal Server Error"})
		return
	}
	writeJSON(writer, http.StatusOK, ports)
}

func (s *Server) handleLegacyAPI(writer http.ResponseWriter, request *http.Request) {
	params, err := ParseLegacyParams(request, s.Config.BodyLimitBytes)
	if err != nil {
		writePlain(writer, http.StatusBadRequest, "请求参数不合法")
		return
	}
	for _, key := range []string{"method", "name", "username", "session", "code", "token", "itemId", "id"} {
		if value, ok := params[key].(string); ok && strings.Contains(value, " ") {
			writePlain(writer, http.StatusBadRequest, "禁止在关键参数中传入空格！")
			return
		}
	}
	if jsTruthy(params["uuid"]) {
		writePlain(writer, http.StatusBadRequest, "禁止传入UUID！")
		return
	}
	if jsTruthy(params["loginTime"]) || jsTruthy(params["onlineTime"]) {
		writePlain(writer, http.StatusBadRequest, "禁止传入时间参数！")
		return
	}
	method, _ := params["method"].(string)
	if _, ok := legacyMethods[method]; !ok {
		writePlain(writer, http.StatusOK, "传入参数错误！")
		return
	}
	// 保留旧分发器的必填字段门槛；缺参请求不进入鉴权或业务分支。
	var required []string
	switch method {
	case "getAllUsers", "generateCoupon":
		required = []string{"session"}
	case "activateUser", "banUser", "unbanUser", "deleteUser":
		required = []string{"name", "session"}
	case "updateEmail":
		required = []string{"name", "newEmail", "session"}
	case "changePassword":
		required = []string{"name", "oldpasswd", "newpasswd"}
	case "deleteShopItem":
		required = []string{"itemId"}
	case "purchaseItem":
		required = []string{"username", "itemId"}
	case "deleteCoupon":
		required = []string{"code", "session"}
	case "logout":
		required = []string{"name"}
	case "verifyRegist", "verifyCode":
		required = []string{"email", "code"}
	case "repasswd":
		required = []string{"name", "newpasswd"}
	case "regist":
		required = []string{"name", "passwd", "email"}
	}
	for _, key := range required {
		if !jsTruthy(params[key]) {
			writePlain(writer, http.StatusOK, "传入参数错误！")
			return
		}
	}
	switch method {
	case "login":
		s.handleAuthorize(writer, request, params)
		return
	case "logout":
		s.handleLogout(writer, request, params)
		return
	case "getShopItems":
		s.handleShopItems(writer, request)
		return
	case "regist", "verifyRegist", "repasswd", "verifyCode":
		s.handleVerificationMethod(writer, request, method, params)
		return
	case "useCoupon":
		// 兑换码必须走独立 coupon listener，不能在 Web listener 上绕过 scope 与来源 ACL。
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"success": false, "message": "兑换接口需要独立令牌"})
		return
	case "getAllUsers", "activateUser", "banUser", "unbanUser", "deleteUser", "updateEmail", "changePassword",
		"addShopItem", "updateShopItem", "deleteShopItem", "purchaseItem", "generateCoupon", "getCoupons", "deleteCoupon":
		s.handleAdminLegacyMethod(writer, request, method, params)
		return
	}
	writeJSON(writer, http.StatusNotImplemented, map[string]any{
		"success": false,
		"message": "功能迁移中: " + method,
	})
}

func (s *Server) handleAuthorize(writer http.ResponseWriter, request *http.Request, params map[string]any) {
	name := strings.TrimSpace(paramString(params["name"]))
	if name == "" {
		writePlain(writer, http.StatusOK, "传入参数错误！")
		return
	}
	if strings.EqualFold(name, "admin") {
		writePlain(writer, http.StatusOK, "管理员账号请使用管理员登录")
		return
	}
	if strings.Contains(name, "@") {
		writePlain(writer, http.StatusOK, "用户名不能包含@符号！")
		return
	}
	username, ok := s.sessions.Get(paramString(params["session"]))
	if !ok || !strings.EqualFold(username, name) {
		writePlain(writer, http.StatusOK, "会话无效或已过期，请重新登录")
		return
	}
	var userID int64
	var canonicalName, passwordHash, status string
	err := s.DB.QueryRowContext(request.Context(),
		"SELECT id,name,password_hash,status FROM users WHERE name_key = ? LIMIT 1", strings.ToLower(username)).
		Scan(&userID, &canonicalName, &passwordHash, &status)
	if errors.Is(err, sql.ErrNoRows) {
		writePlain(writer, http.StatusOK, "您不在数据库中，请注册！")
		return
	}
	if err != nil {
		writePlain(writer, http.StatusOK, "服务器内部错误")
		return
	}
	if status == "banned" {
		writePlain(writer, http.StatusOK, "登录失败：账号已被封禁！")
		return
	}
	if status == "inactive" {
		writePlain(writer, http.StatusOK, "登录失败：账号未激活，请联系管理员！")
		return
	}
	if password := paramString(params["passwd"]); password != "" && bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		writePlain(writer, http.StatusOK, "登录失败：密码错误！")
		return
	}
	duration := s.Config.MaxOnlineTime
	if requested, err := parseInteger(params["time"]); err == nil && requested > 0 && requested <= int64(s.Config.MaxOnlineTime/time.Millisecond) {
		duration = time.Duration(requested) * time.Millisecond
	}
	var previouslyAuthorized bool
	if err := s.DB.QueryRowContext(request.Context(),
		"SELECT EXISTS(SELECT 1 FROM plugin_online_sessions WHERE server_id=? AND user_id=? AND authorized_until>?)",
		s.Config.ServerID, userID, time.Now().UnixMilli()).Scan(&previouslyAuthorized); err != nil {
		writePlain(writer, http.StatusOK, "服务器内部错误")
		return
	}
	if err := s.plugins.AuthorizePlayer(request.Context(), s.Config.ServerID, canonicalName, duration); err != nil {
		if err.Error() == "账号当前不可登录" {
			writePlain(writer, http.StatusOK, "登录失败：账号未激活，请联系管理员！")
		} else {
			writePlain(writer, http.StatusOK, "服务器内部错误")
		}
		return
	}
	loginAt := time.Now()
	if _, err := s.DB.ExecContext(request.Context(),
		"UPDATE users SET last_login = ?,updated_at = ? WHERE id = ?",
		localSignTimestamp(loginAt, s.Config.SignTimezoneOffset), loginAt.UnixMilli(), userID); err != nil {
		writePlain(writer, http.StatusOK, "服务器内部错误")
		return
	}
	if previouslyAuthorized {
		writePlain(writer, http.StatusOK, "你的再次登录已确认，时间已延长，当前在线时间"+formatDuration(duration))
	} else {
		writePlain(writer, http.StatusOK, "玩家名称"+canonicalName+"已确认登录"+formatDuration(duration))
	}
}

func (s *Server) handleLogout(writer http.ResponseWriter, request *http.Request, params map[string]any) {
	name := strings.TrimSpace(paramString(params["name"]))
	username, ok := s.sessions.Get(paramString(params["session"]))
	if !ok || !strings.EqualFold(username, name) {
		writePlain(writer, http.StatusOK, "会话无效或已过期，请重新登录")
		return
	}
	result, err := s.plugins.Logout(request.Context(), s.Config.ServerID, username)
	if err != nil {
		writePlain(writer, http.StatusOK, "服务器内部错误")
		return
	}
	if !result.AuthorizationClosed {
		writePlain(writer, http.StatusOK, "玩家"+username+"现在不是登录状态！")
		return
	}
	writePlain(writer, http.StatusOK, "玩家"+username+"已退出登录！")
}

func (s *Server) handleSign(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	params, err := ParseLegacyParams(request, s.Config.BodyLimitBytes)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"success": false, "message": "请求参数不合法"})
		return
	}
	sessionID := request.Header.Get("X-Web-Session")
	if sessionID == "" {
		sessionID = paramString(params["session"])
	}
	username, ok := s.sessions.Get(sessionID)
	if !ok || (paramString(params["username"]) != "" && !strings.EqualFold(paramString(params["username"]), username)) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	now := time.Now()
	date := localSignDate(now, s.Config.SignTimezoneOffset)
	yesterday := localSignDate(now.Add(-24*time.Hour), s.Config.SignTimezoneOffset)
	tx, err := s.DB.BeginTx(request.Context(), nil)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	defer tx.Rollback()
	var userID int64
	if err := tx.QueryRowContext(request.Context(), "SELECT id FROM users WHERE name_key = ?", strings.ToLower(username)).Scan(&userID); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	var totalDays, consecutiveDays, points int64
	var lastSign sql.NullString
	err = tx.QueryRowContext(request.Context(), `
		SELECT total_days,consecutive_days,last_sign,points
		FROM sign_accounts WHERE user_id = ?`, userID).Scan(&totalDays, &consecutiveDays, &lastSign, &points)
	if errors.Is(err, sql.ErrNoRows) {
		totalDays, consecutiveDays, points = 0, 0, 0
	} else if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	var existing int
	if err := tx.QueryRowContext(request.Context(), "SELECT COUNT(*) FROM sign_history WHERE user_id = ? AND sign_date = ?", userID, date).Scan(&existing); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	if existing > 0 {
		if err := tx.Commit(); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
			return
		}
		state, err := s.signState(request.Context(), userID, date)
		if err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
			return
		}
		state["success"], state["message"] = false, "今天已经签到过了"
		writeJSON(writer, http.StatusOK, state)
		return
	}
	streak := int64(1)
	if lastSign.Valid && lastSign.String == yesterday {
		streak = consecutiveDays + 1
	}
	bonusDays := streak
	if bonusDays > 7 {
		bonusDays = 7
	}
	awarded := int64(10) + bonusDays*5
	totalDays++
	points += awarded
	_, err = tx.ExecContext(request.Context(), `
		INSERT INTO sign_accounts(user_id,total_days,consecutive_days,last_sign,points,updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET total_days=excluded.total_days,
		consecutive_days=excluded.consecutive_days,last_sign=excluded.last_sign,
		points=excluded.points,updated_at=excluded.updated_at`, userID, totalDays, streak, date, points, now.UnixMilli())
	if err == nil {
		_, err = tx.ExecContext(request.Context(), "INSERT INTO sign_history(user_id,sign_date,signed_at) VALUES(?,?,?)", userID, date, now.UnixMilli())
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	state, err := s.signState(request.Context(), userID, date)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	state["success"], state["message"] = true, "签到成功"
	state["pointsAwarded"] = awarded
	state["totalPoints"] = points
	writeJSON(writer, http.StatusOK, state)
}

func (s *Server) handleSignHistory(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username, ok := s.webSessionFromRequest(request)
	requested := request.URL.Query().Get("username")
	if !ok || (requested != "" && !strings.EqualFold(requested, username)) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	var userID int64
	if err := s.DB.QueryRowContext(request.Context(), "SELECT id FROM users WHERE name_key = ?", strings.ToLower(username)).Scan(&userID); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	state, err := s.signState(request.Context(), userID, localSignDate(time.Now(), s.Config.SignTimezoneOffset))
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	state["success"], state["username"] = true, username
	writeJSON(writer, http.StatusOK, state)
}

func (s *Server) handleLeaderboard(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := request.Header.Get("X-Web-Session")
	if sessionID == "" {
		sessionID = request.Header.Get("X-Admin-Session")
	}
	if sessionID == "" {
		sessionID = request.URL.Query().Get("session")
	}
	_, webOK := s.sessions.Get(sessionID)
	_, adminOK := s.adminSessions.Get(sessionID)
	if !webOK && !adminOK {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	rows, err := s.DB.QueryContext(request.Context(), `
		SELECT u.name,u.avatar_filename,u.online_time_ms,u.last_login,
		COALESCE(a.consecutive_days,0),COALESCE(a.points,0)
		FROM users u LEFT JOIN sign_accounts a ON a.user_id=u.id
		WHERE u.status='active'
		ORDER BY COALESCE(a.points,0) DESC,u.online_time_ms DESC,u.name COLLATE NOCASE ASC
		LIMIT 100`)
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	defer rows.Close()
	players := make([]map[string]any, 0)
	for rows.Next() {
		var name string
		var avatar, lastLogin sql.NullString
		var onlineTime, consecutiveDays, points int64
		if err := rows.Scan(&name, &avatar, &onlineTime, &lastLogin, &consecutiveDays, &points); err != nil {
			writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
			return
		}
		var avatarURL any
		if avatar.Valid {
			avatarURL = "/avatars/" + filepath.Base(avatar.String)
		}
		players = append(players, map[string]any{"name": name, "avatar": avatarURL, "onlineTime": onlineTime, "lastLogin": displayLastLogin(lastLogin, s.Config.SignTimezoneOffset), "consecutiveDays": consecutiveDays, "points": points})
	}
	if err := rows.Err(); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "players": players})
}

func (s *Server) webSessionFromRequest(request *http.Request) (string, bool) {
	sessionID := request.Header.Get("X-Web-Session")
	if sessionID == "" {
		sessionID = request.URL.Query().Get("session")
	}
	return s.sessions.Get(sessionID)
}

func (s *Server) signState(ctx context.Context, userID int64, today string) (map[string]any, error) {
	var totalDays, consecutiveDays, points int64
	var lastSign sql.NullString
	err := s.DB.QueryRowContext(ctx, "SELECT total_days,consecutive_days,last_sign,points FROM sign_accounts WHERE user_id = ?", userID).Scan(&totalDays, &consecutiveDays, &lastSign, &points)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	history := make(map[string]bool)
	rows, err := s.DB.QueryContext(ctx, "SELECT sign_date FROM sign_history WHERE user_id = ? ORDER BY sign_date", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var date string
		if err := rows.Scan(&date); err != nil {
			return nil, err
		}
		history[date] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	last := ""
	if lastSign.Valid {
		last = lastSign.String
	}
	return map[string]any{
		"today": today, "timezoneOffsetHours": s.Config.SignTimezoneOffset,
		"signHistory": history, "consecutiveDays": consecutiveDays,
		"totalDays": totalDays, "points": points, "lastSign": last,
	}, nil
}

func localSignDate(value time.Time, offsetHours int) string {
	return value.UTC().Add(time.Duration(offsetHours) * time.Hour).Format("2006-01-02")
}

func localSignTimestamp(value time.Time, offsetHours int) string {
	return value.UTC().Add(time.Duration(offsetHours) * time.Hour).Format("2006-01-02 15:04:05")
}

func displayLastLogin(value sql.NullString, offsetHours int) any {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value.String)); err == nil {
		return localSignTimestamp(parsed, offsetHours)
	}
	return value.String
}

func paramString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(typed)
	}
}

func parseInteger(value any) (int64, error) {
	if number, ok := value.(json.Number); ok {
		return number.Int64()
	}
	return strconv.ParseInt(strings.TrimSpace(paramString(value)), 10, 64)
}

func formatDuration(value time.Duration) string {
	seconds := int64(value / time.Second)
	if seconds <= 0 {
		return "0秒"
	}
	days := seconds / 86400
	seconds %= 86400
	hours := seconds / 3600
	seconds %= 3600
	minutes := seconds / 60
	seconds %= 60
	var parts []string
	if days > 0 {
		parts = append(parts, strconv.FormatInt(days, 10)+"天")
	}
	if hours > 0 {
		parts = append(parts, strconv.FormatInt(hours, 10)+"小时")
	}
	if minutes > 0 {
		parts = append(parts, strconv.FormatInt(minutes, 10)+"分钟")
	}
	if seconds > 0 {
		parts = append(parts, strconv.FormatInt(seconds, 10)+"秒")
	}
	return strings.Join(parts, "")
}

func (s *Server) handleWebLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	params, err := ParseLegacyParams(request, s.Config.BodyLimitBytes)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"success": false, "message": "用户名和密码不能为空"})
		return
	}
	username, _ := params["username"].(string)
	password, _ := params["password"].(string)
	if username == "" || password == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"success": false, "message": "用户名和密码不能为空"})
		return
	}
	var userID int64
	var canonicalName, passwordHash, status string
	var email string
	err = s.DB.QueryRowContext(request.Context(), `
		SELECT id,name,password_hash,status,email
		FROM users WHERE name_key = ? LIMIT 1`, strings.ToLower(strings.TrimSpace(username))).
		Scan(&userID, &canonicalName, &passwordHash, &status, &email)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	if status == "banned" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "账号已被封禁"})
		return
	}
	if status == "inactive" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "账号未激活"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "密码错误"})
		return
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	sessionID := base64.RawURLEncoding.EncodeToString(random[:])
	loginAt := time.Now()
	expireAt := loginAt.Add(s.Config.WebSessionTTL)
	s.sessions.Put(sessionID, canonicalName, expireAt)
	if _, err := s.DB.ExecContext(request.Context(),
		"UPDATE users SET last_login = ?,updated_at = ? WHERE id = ?",
		localSignTimestamp(loginAt, s.Config.SignTimezoneOffset), loginAt.UnixMilli(), userID); err != nil {
		s.sessions.Delete(sessionID)
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "session": sessionID, "username": canonicalName, "message": "登录成功"})
}

func (s *Server) handleUserInfo(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	requestedName := strings.TrimSpace(request.URL.Query().Get("username"))
	sessionID := request.Header.Get("X-Web-Session")
	if sessionID == "" {
		sessionID = request.URL.Query().Get("session")
	}
	username, ok := s.sessions.Get(sessionID)
	if !ok || (requestedName != "" && !strings.EqualFold(requestedName, username)) {
		writeJSON(writer, http.StatusUnauthorized, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	var userID int64
	var joinDate string
	var onlineTime int64
	var lastLogin sql.NullString
	var avatar sql.NullString
	err := s.DB.QueryRowContext(request.Context(), `
		SELECT id,join_date,online_time_ms,last_login,avatar_filename
		FROM users WHERE name_key = ? LIMIT 1`, strings.ToLower(username)).
		Scan(&userID, &joinDate, &onlineTime, &lastLogin, &avatar)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	var points int64
	if err := s.DB.QueryRowContext(request.Context(), "SELECT points FROM sign_accounts WHERE user_id = ?", userID).Scan(&points); err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	var online, authorized int
	now := time.Now().UnixMilli()
	if err := s.DB.QueryRowContext(request.Context(), `
		SELECT
			COALESCE(SUM(CASE WHEN lease_expires_at > ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN authorized_until > ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?) THEN 1 ELSE 0 END), 0)
		FROM plugin_online_sessions
		WHERE server_id = ? AND user_id = ?`, now, now, now, s.Config.ServerID, userID).Scan(&online, &authorized); err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	serverStatus := "offline"
	if online > 0 {
		serverStatus = "online"
	} else if authorized > 0 {
		serverStatus = "authorized"
	}
	var avatarPath any
	if avatar.Valid && avatar.String != "" {
		avatarPath = "/avatars/" + filepath.Base(avatar.String)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"success": true, "username": username, "points": points, "joinDate": joinDate,
		"onlineTime": onlineTime, "lastLogin": displayLastLogin(lastLogin, s.Config.SignTimezoneOffset), "avatarPath": avatarPath,
		"serverStatus": serverStatus,
	})
}

func (s *Server) handleShopItems(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writer.Header().Set("Allow", "GET, POST")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows, err := s.DB.QueryContext(request.Context(), `
		SELECT id,name,item_id,amount,points,stock,image,description,created_at,updated_at
		FROM shop_items ORDER BY rowid`)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取商品列表失败"})
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, itemID, image, description, createdAt string
		var amount, points int64
		var stock sql.NullInt64
		var updatedAt sql.NullString
		if err := rows.Scan(&id, &name, &itemID, &amount, &points, &stock, &image, &description, &createdAt, &updatedAt); err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取商品列表失败"})
			return
		}
		var stockValue any
		if stock.Valid {
			stockValue = stock.Int64
		}
		item := map[string]any{"id": id, "name": name, "itemId": itemID, "amount": amount, "points": points, "stock": stockValue, "image": image, "description": description, "createdAt": createdAt}
		if updatedAt.Valid {
			item["updatedAt"] = updatedAt.String
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取商品列表失败"})
		return
	}
	writer.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Expires", "0")
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "items": items})
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

// ParseLegacyParams 按旧 Express 顺序合并 query、body 和 session header。
func ParseLegacyParams(request *http.Request, bodyLimit int64) (map[string]any, error) {
	if bodyLimit <= 0 {
		bodyLimit = 25_000_000
	}
	params := make(map[string]any)
	for key, values := range request.URL.Query() {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}
	if request.Body != nil {
		limited := http.MaxBytesReader(nil, request.Body, bodyLimit)
		body, err := io.ReadAll(limited)
		request.Body = io.NopCloser(strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		if len(body) > 0 {
			contentType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
			switch {
			case contentType == "application/json" || strings.HasSuffix(contentType, "+json"):
				var bodyParams map[string]any
				decoder := json.NewDecoder(strings.NewReader(string(body)))
				decoder.UseNumber()
				if err := decoder.Decode(&bodyParams); err != nil {
					return nil, err
				}
				for key, value := range bodyParams {
					params[key] = value
				}
			case contentType == "application/x-www-form-urlencoded":
				bodyParams, err := url.ParseQuery(string(body))
				if err != nil {
					return nil, err
				}
				for key, values := range bodyParams {
					if len(values) > 0 {
						params[key] = values[0]
					}
				}
			}
		}
	}
	if !jsTruthy(params["session"]) {
		if value := request.Header.Get("X-Admin-Session"); value != "" {
			params["session"] = value
		} else if value := request.Header.Get("X-Web-Session"); value != "" {
			params["session"] = value
		}
	}
	return params, nil
}

func jsTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		return typed.String() != "0" && typed.String() != "0.0"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return true
	}
}

func writePlain(writer http.ResponseWriter, status int, value string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, value)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
