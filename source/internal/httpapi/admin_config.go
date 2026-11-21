package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"gbcserver/source/internal/plugin"
	"golang.org/x/crypto/bcrypt"
)

// 管理员页面可以修改的配置键。密码哈希、控制通道和数据库连接信息永不开放给页面。
var adminEditableConfigKeys = map[string]struct{}{
	"httpPort": {}, "httpsPort": {}, "adminPath": {}, "adminUsername": {},
	"whitelistListen": {}, "couponListen": {}, "staticDir": {}, "avatarDir": {}, "tlsKeyFile": {}, "tlsCertFile": {},
	"bodyLimitBytes": {}, "serverID": {}, "webhookPath": {}, "signTimezoneOffset": {}, "maxOnlineTime": {}, "onlineMode": {},
	"mailHost": {}, "mailPort": {}, "mailUsername": {}, "mailPassword": {}, "mailFromName": {}, "mailFromAddress": {}, "mailSecure": {},
	"statusServers": {}, "statusAPIs": {}, "statusTimeout": {}, "statusCacheTTL": {}, "mojangEndpoint": {}, "mojangTimeout": {},
	"statusMonitorInterval": {}, "statusAlertAfter": {}, "statusAlertEmailEnabled": {}, "statusAlertEmail": {},
	"statusAlertWebhookEnabled": {}, "statusAlertWebhookURL": {}, "statusAlertRecoveryEnabled": {},
}

var statusMonitorConfigDefaults = map[string]string{
	"statusMonitorInterval": "1m", "statusAlertAfter": "2m", "statusAlertEmailEnabled": "false",
	"statusAlertEmail": "", "statusAlertWebhookEnabled": "false", "statusAlertWebhookURL": "",
	"statusAlertRecoveryEnabled": "true",
}

func (s *Server) adminConfigured() bool {
	username, _, _ := s.configStore.Get(context.Background(), "adminUsername")
	hash, _, _ := s.configStore.Get(context.Background(), "adminPasswordHash")
	if strings.TrimSpace(username) == "" {
		username = s.Config.AdminUsername
	}
	if strings.TrimSpace(hash) == "" {
		hash = s.Config.AdminPasswordHash
	}
	return strings.TrimSpace(username) != "" && strings.TrimSpace(hash) != ""
}

func (s *Server) adminTOTPSecret(ctx context.Context) (string, error) {
	secret, exists, err := s.configStore.Get(ctx, "adminTOTPSecret")
	if err != nil {
		return "", err
	}
	if exists {
		return strings.TrimSpace(secret), nil
	}
	return strings.TrimSpace(s.Config.AdminTOTPSecret), nil
}

func (s *Server) configuredAdminPath(ctx context.Context) string {
	adminPath, _, err := s.configStore.Get(ctx, "adminPath")
	if err != nil || strings.TrimSpace(adminPath) == "" {
		adminPath = s.Config.AdminPath
	}
	adminPath = path.Clean("/" + strings.TrimSpace(adminPath))
	if adminPath == "/" {
		return "/admin"
	}
	return adminPath
}

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	totpSecret, err := s.adminTOTPSecret(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取管理员状态失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "initialized": s.adminConfigured(), "totpEnabled": strings.TrimSpace(totpSecret) != ""})
}

func (s *Server) handleAdminSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.adminSetupMu.Lock()
	defer s.adminSetupMu.Unlock()
	if s.adminConfigured() {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "管理员已初始化"})
		return
	}
	if !isLocalAdminSetupRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"success": false, "message": "首次管理员初始化仅允许本机访问，请使用本机浏览器或 SSH 端口转发"})
		return
	}
	var input struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		TOTPSecret string `json:"totpSecret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	input.TOTPSecret = strings.TrimSpace(input.TOTPSecret)
	if input.Username == "" || len([]byte(input.Password)) < 8 || len([]byte(input.Password)) > 72 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "账户和密码不能为空，密码必须为 8-72 字节"})
		return
	}
	if input.TOTPSecret != "" {
		if _, err := decodeTOTPSecret(input.TOTPSecret); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "TOTP 密钥格式无效"})
			return
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "生成密码哈希失败"})
		return
	}
	if err := s.configStore.SetDefaults(r.Context(), map[string]string{"adminPath": "/admin", "httpPort": "80", "httpsPort": "443"}); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "保存初始配置失败"})
		return
	}
	initialValues := map[string]string{"adminUsername": input.Username, "adminPasswordHash": string(hash)}
	if input.TOTPSecret != "" {
		initialValues["adminTOTPSecret"] = strings.ToUpper(strings.ReplaceAll(input.TOTPSecret, " ", ""))
	}
	for key, value := range initialValues {
		if err := s.configStore.Set(r.Context(), key, value); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "保存管理员配置失败"})
			return
		}
	}
	s.Config.AdminUsername, s.Config.AdminPasswordHash = input.Username, string(hash)
	if input.TOTPSecret != "" {
		s.Config.AdminTOTPSecret = initialValues["adminTOTPSecret"]
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "管理员初始化完成"})
}

func isLocalAdminSetupRequest(r *http.Request) bool {
	remote := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	ip := net.ParseIP(strings.Trim(remote, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded == "" {
		return true
	}
	for _, value := range strings.Split(forwarded, ",") {
		forwardedIP := net.ParseIP(strings.TrimSpace(value))
		if forwardedIP == nil || !forwardedIP.IsLoopback() {
			return false
		}
	}
	return true
}

func (s *Server) adminConfigSnapshot(ctx context.Context) (map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT key,value FROM app_config")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		if key == "adminTOTPSecret" {
			continue
		}
		if _, ok := adminEditableConfigKeys[key]; ok {
			values[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for key, fallback := range statusMonitorConfigDefaults {
		if _, exists := values[key]; !exists {
			values[key] = fallback
		}
	}
	result := make(map[string]any, len(values)+3)
	for key, value := range values {
		switch key {
		case "adminTOTPSecret", "mailPassword":
			result[key+"Configured"] = strings.TrimSpace(value) != ""
		case "statusAlertWebhookURL":
			result["statusAlertWebhookConfigured"] = strings.TrimSpace(value) != ""
		default:
			result[key] = value
		}
	}
	var configured string
	if err := s.DB.QueryRowContext(ctx, "SELECT value FROM app_config WHERE key='adminPasswordHash'").Scan(&configured); err == nil {
		result["adminPasswordConfigured"] = strings.TrimSpace(configured) != ""
	} else {
		result["adminPasswordConfigured"] = strings.TrimSpace(s.Config.AdminPasswordHash) != ""
	}
	totpSecret, err := s.adminTOTPSecret(ctx)
	if err != nil {
		return nil, err
	}
	result["adminTOTPConfigured"] = strings.TrimSpace(totpSecret) != ""
	return result, nil
}

func validateAdminConfigValue(key, value string) (string, error) {
	if _, ok := adminEditableConfigKeys[key]; !ok {
		return "", fmt.Errorf("不允许修改配置键 %q", key)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("配置键 %s 不能包含换行", key)
	}
	trimmed := strings.TrimSpace(value)
	switch key {
	case "httpPort", "httpsPort", "mailPort":
		port, err := strconv.Atoi(trimmed)
		if err != nil || port < 1 || port > 65535 {
			return "", fmt.Errorf("%s 必须是 1-65535 之间的整数", key)
		}
		return strconv.Itoa(port), nil
	case "adminPath":
		adminPath := path.Clean("/" + trimmed)
		if adminPath == "/" || strings.ContainsAny(trimmed, " ?#") {
			return "", errors.New("管理员入口必须是非根路径，且不能包含空格、? 或 #")
		}
		return adminPath, nil
	case "adminUsername", "serverID", "webhookPath", "staticDir", "avatarDir", "tlsKeyFile", "tlsCertFile", "mojangEndpoint":
		if trimmed == "" {
			return "", fmt.Errorf("%s 不能为空", key)
		}
		return trimmed, nil
	case "mailHost", "mailUsername", "mailFromName", "mailFromAddress":
		return trimmed, nil
	case "statusAlertEmail":
		return validateStatusAlertEmail(trimmed)
	case "statusAlertWebhookURL":
		return validateStatusAlertWebhook(trimmed)
	case "whitelistListen", "couponListen":
		if err := plugin.ValidateListenAddress(trimmed); err != nil {
			return "", fmt.Errorf("%s 无效: %w", key, err)
		}
		return trimmed, nil
	case "bodyLimitBytes":
		limit, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || limit < 1024 || limit > 256*1024*1024 {
			return "", errors.New("bodyLimitBytes 必须在 1 KiB 至 256 MiB 之间")
		}
		return strconv.FormatInt(limit, 10), nil
	case "signTimezoneOffset":
		offset, err := strconv.Atoi(trimmed)
		if err != nil || offset < -12 || offset > 14 {
			return "", errors.New("signTimezoneOffset 必须在 -12 至 14 之间")
		}
		return strconv.Itoa(offset), nil
	case "maxOnlineTime", "statusTimeout", "statusCacheTTL", "mojangTimeout", "statusMonitorInterval", "statusAlertAfter":
		duration, err := time.ParseDuration(trimmed)
		if err != nil || duration <= 0 {
			return "", fmt.Errorf("%s 必须是大于 0 的 duration，例如 5s", key)
		}
		if key == "statusMonitorInterval" && (duration < 5*time.Second || duration > 24*time.Hour) {
			return "", errors.New("statusMonitorInterval 必须在 5s 至 24h 之间")
		}
		if key == "statusAlertAfter" && (duration < 5*time.Second || duration > 30*24*time.Hour) {
			return "", errors.New("statusAlertAfter 必须在 5s 至 30d 之间")
		}
		return duration.String(), nil
	case "onlineMode", "mailSecure", "statusAlertEmailEnabled", "statusAlertWebhookEnabled", "statusAlertRecoveryEnabled":
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return "", fmt.Errorf("%s 必须是 true 或 false", key)
		}
		return strconv.FormatBool(parsed), nil
	case "mailPassword", "statusServers", "statusAPIs":
		if key == "statusServers" || key == "statusAPIs" {
			if err := validateStatusListJSON(key, trimmed); err != nil {
				return "", err
			}
		}
		return value, nil
	default:
		return trimmed, nil
	}
}

func validateStatusListJSON(key, value string) error {
	if value == "" || strings.EqualFold(value, "none") {
		return nil
	}
	if !strings.HasPrefix(strings.TrimSpace(value), "[") {
		// 命令行和旧数据库仍可使用 name=value 的兼容格式，启动时会继续解析。
		return nil
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(value), &entries); err != nil {
		return fmt.Errorf("%s 必须是有效的 JSON 列表: %w", key, err)
	}
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, "\r\n") {
			return fmt.Errorf("%s 中的名称不能为空且不能包含换行", key)
		}
		if key == "statusServers" {
			host, _ := entry["host"].(string)
			if strings.TrimSpace(host) == "" || strings.ContainsAny(host, " \t\r\n/?#@\\") {
				return fmt.Errorf("%s 中的主机不能为空，且不能包含空白或路径字符", key)
			}
			port, ok := statusListPort(entry["port"])
			if !ok || port < 1 || port > 65535 {
				if fallback, fallbackOK := statusListPort(entry["defaultPort"]); !fallbackOK || fallback < 1 || fallback > 65535 {
					return fmt.Errorf("%s 中的端口必须是 1-65535", key)
				}
			}
			continue
		}
		endpoint, _ := entry["url"].(string)
		if endpoint == "" {
			for _, field := range []string{"URL", "URLTemplate", "template"} {
				if fallback, ok := entry[field].(string); ok && fallback != "" {
					endpoint = fallback
					break
				}
			}
		}
		lower := strings.ToLower(strings.TrimSpace(endpoint))
		if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
			return fmt.Errorf("%s 中的 URL 必须使用 HTTP 或 HTTPS", key)
		}
		probe := strings.NewReplacer("{hostport}", "example.com:25565", "{host}", "example.com", "{port}", "25565").Replace(endpoint)
		parsed, err := url.Parse(probe)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("%s 中的 URL 主机无效", key)
		}
	}
	return nil
}

func statusListPort(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), typed == float64(int(typed))
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (s *Server) validateStatusAlertChannels(ctx context.Context, updates map[string]string) error {
	getValue := func(key, fallback string) (string, error) {
		if value, exists := updates[key]; exists {
			return value, nil
		}
		value, exists, err := s.configStore.Get(ctx, key)
		if err != nil {
			return "", err
		}
		if !exists {
			return fallback, nil
		}
		return value, nil
	}
	mailEnabledValue, err := getValue("statusAlertEmailEnabled", "false")
	if err != nil {
		return err
	}
	mailEnabled, err := strconv.ParseBool(strings.TrimSpace(mailEnabledValue))
	if err != nil {
		return errors.New("statusAlertEmailEnabled 必须是 true 或 false")
	}
	mailTarget, err := getValue("statusAlertEmail", "")
	if err != nil {
		return err
	}
	if mailEnabled {
		if _, err := validateStatusAlertEmail(mailTarget); err != nil || strings.TrimSpace(mailTarget) == "" {
			return errors.New("启用邮件告警时必须填写有效的收件邮箱")
		}
		smtpHost, err := getValue("mailHost", "")
		if err != nil {
			return err
		}
		fromAddress, err := getValue("mailFromAddress", "")
		if err != nil {
			return err
		}
		if strings.TrimSpace(smtpHost) == "" || strings.TrimSpace(fromAddress) == "" {
			return errors.New("启用邮件告警前请先配置 SMTP 主机和发件地址")
		}
	}
	webhookEnabledValue, err := getValue("statusAlertWebhookEnabled", "false")
	if err != nil {
		return err
	}
	webhookEnabled, err := strconv.ParseBool(strings.TrimSpace(webhookEnabledValue))
	if err != nil {
		return errors.New("statusAlertWebhookEnabled 必须是 true 或 false")
	}
	webhookTarget, err := getValue("statusAlertWebhookURL", "")
	if err != nil {
		return err
	}
	if webhookEnabled {
		if _, err := validateStatusAlertWebhook(webhookTarget); err != nil || strings.TrimSpace(webhookTarget) == "" {
			return errors.New("启用 Webhook 告警时必须填写有效的 HTTP 或 HTTPS 地址")
		}
	}
	return nil
}

func (s *Server) handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.adminSessions.Get(r.Header.Get("X-Admin-Session")); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效"})
		return
	}
	reloadRequired := false
	if r.Method == http.MethodPut {
		var input map[string]string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
			return
		}
		updates := make(map[string]string, len(input))
		for key, value := range input {
			if key == "adminPassword" {
				if len([]byte(value)) < 8 || len([]byte(value)) > 72 {
					writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "管理员密码必须为 8-72 字节"})
					return
				}
				hash, err := bcrypt.GenerateFromPassword([]byte(value), bcrypt.DefaultCost)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "密码无效"})
					return
				}
				updates["adminPasswordHash"] = string(hash)
				continue
			}
			if key == "adminPasswordHash" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "不能直接写入管理员密码哈希"})
				return
			}
			normalized, err := validateAdminConfigValue(key, value)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
				return
			}
			updates[key] = normalized
		}
		if err := s.validateStatusAlertChannels(r.Context(), updates); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": err.Error()})
			return
		}
		for key, value := range updates {
			if err := s.configStore.Set(r.Context(), key, value); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "保存配置失败"})
				return
			}
		}
		reloadRequired = len(updates) > 0
		if value, ok := updates["adminPath"]; ok {
			s.Config.AdminPath = value
		}
		if value, ok := updates["adminUsername"]; ok {
			s.Config.AdminUsername = value
		}
		if value, ok := updates["adminPasswordHash"]; ok {
			s.Config.AdminPasswordHash = value
		}
		if value, ok := updates["adminTOTPSecret"]; ok {
			s.Config.AdminTOTPSecret = value
		}
	}
	config, err := s.adminConfigSnapshot(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取配置失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "config": config, "reloadRequired": reloadRequired, "githubRelease": s.Config.GitHubRelease, "updatedAt": time.Now().UnixMilli()})
}
