package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gbcserver/source/internal/store"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func testServer(t *testing.T) (*Server, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	server, err := New(db, "", "", Config{})
	if err != nil {
		t.Fatal(err)
	}
	return server, db
}

func TestParseLegacyParamsBodyOverridesQueryAndHeaderFillsSession(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api?method=login&name=Query", strings.NewReader(`{"name":"Body","passwd":"secret"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Web-Session", "web-session")
	params, err := ParseLegacyParams(request, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if params["name"] != "Body" || params["method"] != "login" || params["session"] != "web-session" {
		t.Fatalf("解析结果不符合旧合并顺序: %#v", params)
	}
}

func TestParseLegacyParamsPrefersAdminHeaderAndBodySession(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(url.Values{"session": {"body-session"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("X-Admin-Session", "admin-session")
	request.Header.Set("X-Web-Session", "web-session")
	params, err := ParseLegacyParams(request, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if params["session"] != "body-session" {
		t.Fatalf("body session 应优先于 header: %#v", params)
	}

	request = httptest.NewRequest(http.MethodGet, "/api", nil)
	request.Header.Set("X-Admin-Session", "admin-session")
	request.Header.Set("X-Web-Session", "web-session")
	params, err = ParseLegacyParams(request, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if params["session"] != "admin-session" {
		t.Fatalf("缺省时应优先取管理员 header: %#v", params)
	}
}

func TestParseLegacyParamsBodyLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(`{"method":"login","name":"a long enough body"}`))
	request.Header.Set("Content-Type", "application/json")
	if _, err := ParseLegacyParams(request, 8); err == nil {
		t.Fatal("超出请求体限制时应返回错误")
	}
}

func TestLegacyAPIGuardsAndMigrationMarker(t *testing.T) {
	server, _ := testServer(t)
	server.Config.BodyLimitBytes = 8
	tests := []struct {
		name       string
		target     string
		body       string
		content    string
		statusCode int
		contains   string
	}{
		{"空格保护", "/api?method=login&name=two%20words", "", "", http.StatusBadRequest, "禁止在关键参数"},
		{"拒绝外部 UUID", "/api?method=login&uuid=123", "", "", http.StatusBadRequest, "禁止传入UUID"},
		{"未知方法旧语义", "/api?method=missing", "", "", http.StatusOK, "传入参数错误"},
		{"已登记方法缺少管理员 session", "/api?method=activateUser&name=Player&session=admin", "", "", http.StatusOK, "管理员会话无效"},
		{"请求体超限", "/api", `{"method":"login","name":"a long enough body"}`, "application/json", http.StatusBadRequest, "请求参数不合法"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.target, strings.NewReader(test.body))
			request.Header.Set("Content-Type", test.content)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.statusCode || !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("得到 %d %q，预期 %d 且包含 %q", response.Code, response.Body.String(), test.statusCode, test.contains)
			}
		})
	}
}

func TestLegacyMethodDispatcherRespondsForEveryMethod(t *testing.T) {
	server, _ := testServer(t)
	if len(legacyMethods) != 22 {
		t.Fatalf("兼容 method 数量变化：当前 %d 个，需同步核对 Node 契约和迁移矩阵", len(legacyMethods))
	}
	for method := range legacyMethods {
		t.Run(method, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api?method="+url.QueryEscape(method), nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code == http.StatusNotImplemented {
				t.Fatalf("%s 仍返回迁移占位状态: %s", method, response.Body.String())
			}
			if response.Body.Len() == 0 {
				t.Fatalf("%s 未产生兼容响应", method)
			}
		})
	}
}

func TestCouponMethodIsRejectedOnWebListener(t *testing.T) {
	server, _ := testServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api", strings.NewReader(`{"method":"useCoupon","username":"Player","code":"ABC"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), "兑换接口需要独立令牌") {
		t.Fatalf("Web listener 应拒绝未认证兑换: %d %s", response.Code, response.Body.String())
	}
}

func TestWebLoginSignAndAuthorizePlayer(t *testing.T) {
	server, db := testServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users(id,name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,created_at,updated_at)
		VALUES(1,'Legacy_Name','legacy_name','legacy-uuid',?,'legacy@example.test','legacy@example.test','active','2024-01-01',0,1,1)`, string(hash)); err != nil {
		t.Fatal(err)
	}
	loginStarted := time.Now()
	login := httptest.NewRequest(http.MethodPost, "/api/webLogin", strings.NewReader(`{"username":"LEGACY_NAME","password":"correct horse"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	server.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("网页登录返回 %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	var loginBody struct {
		Success  bool   `json:"success"`
		Session  string `json:"session"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
		t.Fatal(err)
	}
	if !loginBody.Success || loginBody.Session == "" || loginBody.Username != "Legacy_Name" {
		t.Fatalf("网页登录输出错误: %+v", loginBody)
	}
	var lastLogin string
	if err := db.QueryRow("SELECT last_login FROM users WHERE id=1").Scan(&lastLogin); err != nil {
		t.Fatal(err)
	}
	parsedLogin, err := time.ParseInLocation("2006-01-02 15:04:05", lastLogin, time.FixedZone("GMT+8", 8*60*60))
	if err != nil || parsedLogin.Before(loginStarted.Add(-time.Second)) || parsedLogin.After(time.Now().Add(time.Second)) {
		t.Fatalf("网页登录时间不是当前 GMT+8 时间：%q，解析错误：%v", lastLogin, err)
	}

	sign := httptest.NewRequest(http.MethodPost, "/api/sign", strings.NewReader(`{"username":"legacy_name"}`))
	sign.Header.Set("Content-Type", "application/json")
	sign.Header.Set("X-Web-Session", loginBody.Session)
	signResponse := httptest.NewRecorder()
	server.ServeHTTP(signResponse, sign)
	if signResponse.Code != http.StatusOK || !strings.Contains(signResponse.Body.String(), `"pointsAwarded":15`) {
		t.Fatalf("首次签到奖励错误: %d %s", signResponse.Code, signResponse.Body.String())
	}

	duplicate := httptest.NewRequest(http.MethodGet, "/api/sign?username=Legacy_Name&session="+url.QueryEscape(loginBody.Session), nil)
	duplicateResponse := httptest.NewRecorder()
	server.ServeHTTP(duplicateResponse, duplicate)
	if duplicateResponse.Code != http.StatusOK || !strings.Contains(duplicateResponse.Body.String(), "今天已经签到过了") {
		t.Fatalf("重复签到未按兼容语义拒绝: %d %s", duplicateResponse.Code, duplicateResponse.Body.String())
	}

	info := httptest.NewRequest(http.MethodGet, "/api/userInfo", nil)
	info.Header.Set("X-Web-Session", loginBody.Session)
	infoResponse := httptest.NewRecorder()
	server.ServeHTTP(infoResponse, info)
	if infoResponse.Code != http.StatusOK || !strings.Contains(infoResponse.Body.String(), `"points":15`) {
		t.Fatalf("用户资料积分未同步: %d %s", infoResponse.Code, infoResponse.Body.String())
	}

	grant := httptest.NewRequest(http.MethodGet, "/api?method=login&name=Legacy_Name&session="+url.QueryEscape(loginBody.Session), nil)
	grantResponse := httptest.NewRecorder()
	server.ServeHTTP(grantResponse, grant)
	if grantResponse.Code != http.StatusOK || !strings.Contains(grantResponse.Body.String(), "玩家名称Legacy_Name已确认登录") {
		t.Fatalf("网页登录授权输出错误: %d %s", grantResponse.Code, grantResponse.Body.String())
	}
	var authorizedUntil int64
	if err := db.QueryRow("SELECT authorized_until FROM plugin_online_sessions WHERE server_id='default' AND user_id=1").Scan(&authorizedUntil); err != nil {
		t.Fatal(err)
	}
	if time.UnixMilli(authorizedUntil).Before(time.Now()) {
		t.Fatal("网页授权没有写入白名单服务")
	}
	info = httptest.NewRequest(http.MethodGet, "/api/userInfo", nil)
	info.Header.Set("X-Web-Session", loginBody.Session)
	infoResponse = httptest.NewRecorder()
	server.ServeHTTP(infoResponse, info)
	if infoResponse.Code != http.StatusOK || !strings.Contains(infoResponse.Body.String(), `"serverStatus":"authorized"`) {
		t.Fatalf("网页授权后应显示已授权而不是离线: %d %s", infoResponse.Code, infoResponse.Body.String())
	}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`UPDATE plugin_online_sessions
		SET login_at=?,last_seen_at=?,lease_expires_at=?,updated_at=?
		WHERE server_id='default' AND user_id=1`, now, now, now+90_000, now); err != nil {
		t.Fatal(err)
	}
	info = httptest.NewRequest(http.MethodGet, "/api/userInfo", nil)
	info.Header.Set("X-Web-Session", loginBody.Session)
	infoResponse = httptest.NewRecorder()
	server.ServeHTTP(infoResponse, info)
	if infoResponse.Code != http.StatusOK || !strings.Contains(infoResponse.Body.String(), `"serverStatus":"online"`) {
		t.Fatalf("插件确认入服后应显示在线: %d %s", infoResponse.Code, infoResponse.Body.String())
	}
}

func TestTimeFormattingUsesConfiguredFixedOffset(t *testing.T) {
	instant := time.Date(2026, 10, 31, 20, 15, 30, 0, time.UTC)
	if got := localSignDate(instant, 8); got != "2026-11-01" {
		t.Fatalf("GMT+8 日期=%q，want 2026-11-01", got)
	}
	if got := localSignTimestamp(instant, 8); got != "2026-11-01 04:15:30" {
		t.Fatalf("GMT+8 时间=%q，want 2026-11-01 04:15:30", got)
	}
	legacyUTC := sql.NullString{String: "2026-10-31T20:15:30Z", Valid: true}
	if got := displayLastLogin(legacyUTC, 8); got != "2026-11-01 04:15:30" {
		t.Fatalf("旧 UTC 登录时间未转换为 GMT+8：%v", got)
	}
	if got := displayLastLogin(sql.NullString{}, 8); got != nil {
		t.Fatalf("空登录时间应返回 null：%v", got)
	}
}

func TestNewDefaultsLoginAndSignTimezoneToGMT8(t *testing.T) {
	server, _ := testServer(t)
	if server.Config.SignTimezoneOffset != 8 || server.plugins.LoginTimezoneOffsetHours != 8 {
		t.Fatalf("默认时区不一致：sign=%d login=%d", server.Config.SignTimezoneOffset, server.plugins.LoginTimezoneOffsetHours)
	}
}

func TestPortsPublicConfigAndHealth(t *testing.T) {
	server, db := testServer(t)
	if _, err := db.Exec("INSERT INTO ports(key,port,updated_at) VALUES(?,?,?)", "survival.example", 25575, 1); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path     string
		contains string
	}{
		{"/healthz", `"status":"ok"`},
		{"/api/publicConfig", `"maxCouponItemAmount":2304`},
		{"/ports", `"survival.example":25575`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.contains) {
			t.Fatalf("%s 得到 %d %q", test.path, response.Code, response.Body.String())
		}
	}
}

func TestLeaderboardAcceptsAdminSession(t *testing.T) {
	server, _ := testServer(t)
	server.adminSessions.Put("admin-session", "admin", time.Now().Add(time.Hour))
	request := httptest.NewRequest(http.MethodGet, "/api/leaderboard?session=admin-session", nil)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"success":true`) {
		t.Fatalf("排行榜不接受管理员会话: %d %s", response.Code, response.Body.String())
	}
}

func TestAdminSetupAndConfigPersistWithoutExposingSecrets(t *testing.T) {
	server, db := testServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"root-admin","password":"correct-horse","totpSecret":"JBSWY3DPEHPK3PXP"}`))
	setup.RemoteAddr = "127.0.0.1:1234"
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	server.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK || !strings.Contains(setupResponse.Body.String(), `"success":true`) {
		t.Fatalf("管理员初始化失败: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	server.adminSessions.Put("admin-session", "root-admin", time.Now().Add(time.Hour))
	update := httptest.NewRequest(http.MethodPut, "/api/admin/config", strings.NewReader(`{"httpPort":"18080","adminPath":"secure-admin","adminUsername":"new-admin","adminPassword":"new-password"}`))
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("X-Admin-Session", "admin-session")
	updateResponse := httptest.NewRecorder()
	server.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK || strings.Contains(updateResponse.Body.String(), "new-password") || strings.Contains(updateResponse.Body.String(), "adminPasswordHash") {
		t.Fatalf("管理员配置响应泄露敏感值或保存失败: %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	var port, adminPath, username, passwordHash string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='httpPort'").Scan(&port); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminPath'").Scan(&adminPath); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminUsername'").Scan(&username); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminPasswordHash'").Scan(&passwordHash); err != nil {
		t.Fatal(err)
	}
	if port != "18080" || adminPath != "/secure-admin" || username != "new-admin" || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte("new-password")) != nil {
		t.Fatalf("管理员配置未正确持久化: port=%q path=%q username=%q", port, adminPath, username)
	}
}

func TestAdminSetupAllowsOptionalTOTPAndPasswordOnlyLogin(t *testing.T) {
	server, db := testServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"root-admin","password":"correct-horse"}`))
	setup.RemoteAddr = "127.0.0.1:1234"
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	server.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("无 TOTP 初始化失败: %d %s", setupResponse.Code, setupResponse.Body.String())
	}

	var secret string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&secret); err == nil && secret != "" {
		t.Fatalf("初始化时未提供的 TOTP 不应写入数据库: %q", secret)
	}
	login := httptest.NewRequest(http.MethodPost, "/api/adminLogin", strings.NewReader(`{"name":"root-admin","passwd":"correct-horse"}`))
	login.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	server.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || !strings.Contains(loginResponse.Body.String(), `"success":true`) {
		t.Fatalf("未启用 TOTP 时密码登录失败: %d %s", loginResponse.Code, loginResponse.Body.String())
	}
	status := httptest.NewRecorder()
	server.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/admin/status", nil))
	if strings.Contains(status.Body.String(), `"totpEnabled":true`) {
		t.Fatalf("状态响应错误地标记 TOTP 已启用: %s", status.Body.String())
	}
}

func TestAdminSetupRejectsRemoteAndMalformedTOTPWithoutPartialConfiguration(t *testing.T) {
	server, db := testServer(t)
	remote := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"remote-admin","password":"correct-horse"}`))
	remote.RemoteAddr = "203.0.113.10:1234"
	remoteResponse := httptest.NewRecorder()
	server.ServeHTTP(remoteResponse, remote)
	if remoteResponse.Code != http.StatusForbidden {
		t.Fatalf("远程初始化应被拒绝: %d %s", remoteResponse.Code, remoteResponse.Body.String())
	}
	spoofedForward := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"remote-admin","password":"correct-horse"}`))
	spoofedForward.RemoteAddr = "127.0.0.1:1234"
	spoofedForward.Header.Set("X-Forwarded-For", "203.0.113.10")
	spoofedResponse := httptest.NewRecorder()
	server.ServeHTTP(spoofedResponse, spoofedForward)
	if spoofedResponse.Code != http.StatusForbidden {
		t.Fatalf("非本机转发请求应被拒绝: %d %s", spoofedResponse.Code, spoofedResponse.Body.String())
	}

	invalid := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"root-admin","password":"correct-horse","totpSecret":"invalid-secret"}`))
	invalid.RemoteAddr = "127.0.0.1:1234"
	invalidResponse := httptest.NewRecorder()
	server.ServeHTTP(invalidResponse, invalid)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("无效 TOTP 密钥应被拒绝: %d %s", invalidResponse.Code, invalidResponse.Body.String())
	}
	if server.adminConfigured() {
		t.Fatal("拒绝无效 TOTP 密钥后不应留下半初始化的管理员账户")
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM app_config WHERE key IN ('adminUsername','adminPasswordHash')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("拒绝无效 TOTP 密钥后仍写入管理员配置: %d", count)
	}
}

func TestAdminTOTPEnrollmentRebindDisableAndSecretPrivacy(t *testing.T) {
	server, db := testServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"root-admin","password":"correct-horse"}`))
	setup.RemoteAddr = "127.0.0.1:1234"
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	server.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("管理员初始化失败: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	server.adminSessions.Put("admin-session", "root-admin", time.Now().Add(time.Hour))

	begin := func(currentCode string) map[string]any {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"currentCode": currentCode})
		request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/begin", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Admin-Session", "admin-session")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("开始绑定 TOTP 失败: %d %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("含 TOTP secret 的响应必须禁止缓存: %v", response.Header())
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		secret := result["secret"].(string)
		if !strings.HasPrefix(result["otpauthURI"].(string), "otpauth://totp/") || !strings.Contains(result["otpauthURI"].(string), url.QueryEscape(secret)) {
			t.Fatalf("绑定响应缺少本地 otpauth URI 或密钥: %#v", result)
		}
		qrDataURI := result["qrDataURI"].(string)
		const qrPrefix = "data:image/png;base64,"
		if !strings.HasPrefix(qrDataURI, qrPrefix) {
			t.Fatalf("绑定响应缺少内嵌 PNG 二维码: %#v", result)
		}
		qrBytes, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qrDataURI, qrPrefix))
		if err != nil {
			t.Fatalf("二维码 data URI 的 base64 无效: %v", err)
		}
		if _, err := png.Decode(bytes.NewReader(qrBytes)); err != nil {
			t.Fatalf("二维码不是有效 PNG 图片: %v", err)
		}
		return result
	}
	codeFor := func(secret string) string {
		t.Helper()
		decoded, err := decodeTOTPSecret(secret)
		if err != nil {
			t.Fatal(err)
		}
		return calculateTOTP(decoded, totpCounter(time.Now()))
	}
	codeForCounter := func(secret string, counter int64) string {
		t.Helper()
		decoded, err := decodeTOTPSecret(secret)
		if err != nil {
			t.Fatal(err)
		}
		return calculateTOTP(decoded, uint64(counter))
	}
	confirm := func(code string, expectedStatus int, currentCodes ...string) string {
		t.Helper()
		input := map[string]string{"code": code}
		if len(currentCodes) > 0 {
			input["currentCode"] = currentCodes[0]
		}
		body, _ := json.Marshal(input)
		request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/confirm", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Admin-Session", "admin-session")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != expectedStatus {
			t.Fatalf("确认绑定得到 %d，期望 %d: %s", response.Code, expectedStatus, response.Body.String())
		}
		if expectedStatus != http.StatusOK {
			return ""
		}
		return response.Body.String()
	}

	first := begin("")
	firstSecret := first["secret"].(string)
	server.adminSessions.Put("other-admin-session", "root-admin", time.Now().Add(time.Hour))
	otherSession := httptest.NewRequest(http.MethodPost, "/api/admin/totp/confirm", strings.NewReader(`{"code":"000000"}`))
	otherSession.Header.Set("Content-Type", "application/json")
	otherSession.Header.Set("X-Admin-Session", "other-admin-session")
	otherSessionResponse := httptest.NewRecorder()
	server.ServeHTTP(otherSessionResponse, otherSession)
	if otherSessionResponse.Code != http.StatusConflict {
		t.Fatalf("pending secret 不应跨管理员 session 使用: %d %s", otherSessionResponse.Code, otherSessionResponse.Body.String())
	}
	var persisted string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&persisted); err == nil && persisted != "" {
		t.Fatalf("尚未确认的新密钥已持久化: %q", persisted)
	}
	if strings.Contains(first["otpauthURI"].(string), "https://") {
		t.Fatalf("otpauth URI 不应依赖第三方服务: %s", first["otpauthURI"])
	}
	confirm("000000", http.StatusUnauthorized)
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&persisted); err == nil && persisted != "" {
		t.Fatalf("验证码错误后 TOTP 被持久化: %q", persisted)
	}
	confirm(codeForCounter(firstSecret, time.Now().Unix()/30-1), http.StatusOK)
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&persisted); err != nil || persisted != firstSecret {
		t.Fatalf("确认后新密钥未正确保存: got=%q err=%v", persisted, err)
	}

	login := func(secret, code string) bool {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"name": "root-admin", "passwd": "correct-horse", "totp": code})
		request := httptest.NewRequest(http.MethodPost, "/api/adminLogin", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response.Code == http.StatusOK && strings.Contains(response.Body.String(), `"success":true`)
	}
	if login(firstSecret, "") || login(firstSecret, "000000") {
		t.Fatal("启用 TOTP 后缺少或错误的验证码不应登录成功")
	}
	currentCounter := time.Now().Unix() / 30
	oldCode := codeForCounter(firstSecret, currentCounter)
	if !login(firstSecret, oldCode) {
		t.Fatal("启用 TOTP 后有效验证码登录失败")
	}
	if login(firstSecret, oldCode) {
		t.Fatal("已用于登录的 TOTP 验证码不应再次签发会话")
	}

	currentCodeForBegin := codeForCounter(firstSecret, currentCounter+1)
	second := begin(currentCodeForBegin)
	secondSecret := second["secret"].(string)
	if secondSecret == firstSecret {
		t.Fatal("重新绑定应生成新密钥")
	}
	confirm("000000", http.StatusUnauthorized)
	confirm(codeFor(secondSecret), http.StatusOK)
	if login(firstSecret, codeFor(firstSecret)) {
		t.Fatal("换绑后旧 TOTP 密钥仍可登录")
	}
	if !login(secondSecret, codeForCounter(secondSecret, currentCounter+1)) {
		t.Fatal("换绑后新 TOTP 密钥不能登录")
	}
	server.adminSessions.Put("other-admin-session", "root-admin", time.Now().Add(time.Hour))
	secretUpdate := httptest.NewRequest(http.MethodPut, "/api/admin/config", strings.NewReader(`{"adminTOTPSecret":""}`))
	secretUpdate.Header.Set("Content-Type", "application/json")
	secretUpdate.Header.Set("X-Admin-Session", "admin-session")
	secretUpdateResponse := httptest.NewRecorder()
	server.ServeHTTP(secretUpdateResponse, secretUpdate)
	if secretUpdateResponse.Code != http.StatusBadRequest {
		t.Fatalf("普通配置接口不应接受 TOTP 密钥修改: %d %s", secretUpdateResponse.Code, secretUpdateResponse.Body.String())
	}
	configRequest := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	configRequest.Header.Set("X-Admin-Session", "admin-session")
	configResponse := httptest.NewRecorder()
	server.ServeHTTP(configResponse, configRequest)
	if strings.Contains(configResponse.Body.String(), firstSecret) || strings.Contains(configResponse.Body.String(), secondSecret) || strings.Contains(configResponse.Body.String(), "adminTOTPSecret") || !strings.Contains(configResponse.Body.String(), `"adminTOTPConfigured":true`) {
		t.Fatalf("启用状态下配置响应未正确隐藏 TOTP 密钥: %s", configResponse.Body.String())
	}

	disable := func(password, code string, expectedStatus int) string {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"password": password, "code": code})
		request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/disable", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Admin-Session", "admin-session")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != expectedStatus {
			t.Fatalf("关闭 TOTP 得到 %d，期望 %d: %s", response.Code, expectedStatus, response.Body.String())
		}
		return response.Body.String()
	}
	disable("incorrect-password", codeFor(secondSecret), http.StatusUnauthorized)
	disable("correct-horse", "000000", http.StatusUnauthorized)
	if body := disable("correct-horse", codeForCounter(secondSecret, currentCounter-1), http.StatusOK); strings.Contains(body, secondSecret) {
		t.Fatalf("关闭 TOTP 响应泄露密钥: %s", body)
	}
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&persisted); err != nil || persisted != "" {
		t.Fatalf("关闭后密钥未清除: got=%q err=%v", persisted, err)
	}
	if _, ok := server.adminSessions.Get("other-admin-session"); ok {
		t.Fatal("关闭双重验证后其他管理员 session 应失效")
	}
	if !login("", "") {
		t.Fatal("关闭 TOTP 后密码登录失败")
	}

	server.adminSessions.Put("admin-session", "root-admin", time.Now().Add(time.Hour))
	request := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	request.Header.Set("X-Admin-Session", "admin-session")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), firstSecret) || strings.Contains(response.Body.String(), secondSecret) || strings.Contains(response.Body.String(), "adminTOTPSecret") {
		t.Fatalf("管理员配置响应泄露 TOTP 密钥: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"adminTOTPConfigured":false`) {
		t.Fatalf("管理员配置未返回 TOTP 状态: %s", response.Body.String())
	}
}

func TestAdminTOTPConfirmRejectsStaleEnrollmentFromAnotherSession(t *testing.T) {
	server, db := testServer(t)
	setup := httptest.NewRequest(http.MethodPost, "/api/admin/setup", strings.NewReader(`{"username":"root-admin","password":"correct-horse"}`))
	setup.RemoteAddr = "127.0.0.1:1234"
	setup.Header.Set("Content-Type", "application/json")
	setupResponse := httptest.NewRecorder()
	server.ServeHTTP(setupResponse, setup)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("管理员初始化失败: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	server.adminSessions.Put("admin-session-a", "root-admin", time.Now().Add(time.Hour))
	server.adminSessions.Put("admin-session-b", "root-admin", time.Now().Add(time.Hour))

	begin := func(sessionID string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/begin", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Admin-Session", sessionID)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("开始绑定 TOTP 失败: %d %s", response.Code, response.Body.String())
		}
		var result struct {
			Secret string `json:"secret"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Secret
	}
	confirm := func(sessionID, secret string) *httptest.ResponseRecorder {
		t.Helper()
		decoded, err := decodeTOTPSecret(secret)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"code": calculateTOTP(decoded, totpCounter(time.Now()))})
		request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/confirm", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Admin-Session", sessionID)
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}

	secretA := begin("admin-session-a")
	secretB := begin("admin-session-b")
	if response := confirm("admin-session-a", secretA); response.Code != http.StatusOK {
		t.Fatalf("首次启用 TOTP 失败: %d %s", response.Code, response.Body.String())
	}
	if response := confirm("admin-session-b", secretB); response.Code != http.StatusUnauthorized {
		t.Fatalf("启用双重验证后旧管理员 session 应失效: %d %s", response.Code, response.Body.String())
	}
	if _, exists := server.adminTOTPEnrollments["admin-session-b"]; exists {
		t.Fatal("启用双重验证后其他 session 的 pending secret 应清除")
	}
	var persisted string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&persisted); err != nil || persisted != secretA {
		t.Fatalf("旧 pending 覆盖了已启用的 TOTP: got=%q err=%v", persisted, err)
	}
}

func TestAdminTOTPEnrollmentRequiresSessionAndExpires(t *testing.T) {
	server, _ := testServer(t)
	server.adminTOTPEnrollments["expired-session"] = adminTOTPEnrollment{Secret: "JBSWY3DPEHPK3PXP", ExpiresAt: time.Now().Add(-time.Second)}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/begin", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	unauthorized := httptest.NewRecorder()
	server.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录开始绑定应拒绝: %d %s", unauthorized.Code, unauthorized.Body.String())
	}

	server.adminSessions.Put("expired-session", "admin", time.Now().Add(time.Hour))
	confirm := httptest.NewRequest(http.MethodPost, "/api/admin/totp/confirm", strings.NewReader(`{"code":"000000"}`))
	confirm.Header.Set("Content-Type", "application/json")
	confirm.Header.Set("X-Admin-Session", "expired-session")
	expired := httptest.NewRecorder()
	server.ServeHTTP(expired, confirm)
	if expired.Code != http.StatusConflict {
		t.Fatalf("过期绑定应拒绝: %d %s", expired.Code, expired.Body.String())
	}
}

func TestAdminTOTPRebindRequiresCurrentCode(t *testing.T) {
	server, db := testServer(t)
	server.adminSessions.Put("admin-session", "admin", time.Now().Add(time.Hour))
	oldSecret := "JBSWY3DPEHPK3PXP"
	if err := (store.ConfigStore{DB: db}).Set(context.Background(), "adminTOTPSecret", oldSecret); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/totp/begin", strings.NewReader(`{"currentCode":"000000"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Admin-Session", "admin-session")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("换绑未校验旧 TOTP: %d %s", response.Code, response.Body.String())
	}

	var persisted string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='adminTOTPSecret'").Scan(&persisted); err != nil || persisted != oldSecret {
		t.Fatalf("错误的当前验证码影响了现有 TOTP: secret=%q err=%v", persisted, err)
	}
}

func TestSiteBrandingSettingsAreEditableAndPublic(t *testing.T) {
	server, _ := testServer(t)
	server.adminSessions.Put("admin-session", "admin", time.Now().Add(time.Hour))

	adminGet := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	adminGet.Header.Set("X-Admin-Session", "admin-session")
	adminGetResponse := httptest.NewRecorder()
	server.ServeHTTP(adminGetResponse, adminGet)
	var initialAdmin struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(adminGetResponse.Body.Bytes(), &initialAdmin); err != nil {
		t.Fatal(err)
	}
	if initialAdmin.Config["siteName"] != "嘎嘣脆综合管理系统" || initialAdmin.Config["loginPageTitle"] != "嘎嘣脆综合管理系统" {
		t.Fatalf("管理员配置未提供站点文案默认值: %#v", initialAdmin.Config)
	}

	publicGet := httptest.NewRequest(http.MethodGet, "/api/publicConfig", nil)
	publicGetResponse := httptest.NewRecorder()
	server.ServeHTTP(publicGetResponse, publicGet)
	var initialPublic struct {
		Branding map[string]string `json:"branding"`
	}
	if err := json.Unmarshal(publicGetResponse.Body.Bytes(), &initialPublic); err != nil {
		t.Fatal(err)
	}
	if initialPublic.Branding["siteName"] != "嘎嘣脆综合管理系统" || initialPublic.Branding["statusPageTitle"] != "实时状态监控" {
		t.Fatalf("公开配置未提供站点文案默认值: %#v", initialPublic.Branding)
	}

	values := `{"siteName":"自定义综合管理系统","loginPageTitle":"成员登录","statusPageTitle":"线路运行状态","statusPageFooter":"© 自定义页脚"}`
	update := httptest.NewRequest(http.MethodPut, "/api/admin/config", strings.NewReader(values))
	update.Header.Set("Content-Type", "application/json")
	update.Header.Set("X-Admin-Session", "admin-session")
	updateResponse := httptest.NewRecorder()
	server.ServeHTTP(updateResponse, update)
	var updated struct {
		ReloadRequired bool           `json:"reloadRequired"`
		Config         map[string]any `json:"config"`
	}
	if updateResponse.Code != http.StatusOK || json.Unmarshal(updateResponse.Body.Bytes(), &updated) != nil {
		t.Fatalf("保存站点文案失败: %d %s", updateResponse.Code, updateResponse.Body.String())
	}
	if updated.ReloadRequired || updated.Config["siteName"] != "自定义综合管理系统" {
		t.Fatalf("站点文案保存结果不正确: %#v", updated)
	}

	publicGet = httptest.NewRequest(http.MethodGet, "/api/publicConfig", nil)
	publicGetResponse = httptest.NewRecorder()
	server.ServeHTTP(publicGetResponse, publicGet)
	var finalPublic struct {
		Branding map[string]string `json:"branding"`
	}
	if err := json.Unmarshal(publicGetResponse.Body.Bytes(), &finalPublic); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"siteName": "自定义综合管理系统", "loginPageTitle": "成员登录",
		"statusPageTitle": "线路运行状态", "statusPageFooter": "© 自定义页脚",
	}
	for key, value := range want {
		if finalPublic.Branding[key] != value {
			t.Errorf("公开配置 %s=%q，期望 %q", key, finalPublic.Branding[key], value)
		}
	}
}

func TestAdminPageIsOnlyAvailableAtConfiguredEntry(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := (store.ConfigStore{DB: db}).Set(context.Background(), "adminPath", "/private/console"); err != nil {
		t.Fatal(err)
	}
	server, err := New(db, "", "", Config{AdminPath: "/private/console", StaticFS: fstest.MapFS{
		"login.html":      &fstest.MapFile{Data: []byte("player page")},
		"admin.html":      &fstest.MapFile{Data: []byte("admin page")},
		"admin.css":       &fstest.MapFile{Data: []byte("admin style")},
		"admin-config.js": &fstest.MapFile{Data: []byte("admin config script")},
		"player.js":       &fstest.MapFile{Data: []byte("player script")},
		"admin-legacy.js": &fstest.MapFile{Data: []byte("admin script")},
		"i18n-admin.js":   &fstest.MapFile{Data: []byte("admin translations")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path         string
		wantStatus   int
		wantBody     string
		wantLocation string
	}{
		{path: "/", wantStatus: http.StatusFound, wantLocation: "/login.html"},
		{path: "/admin.html", wantStatus: http.StatusNotFound},
		{path: "/ADMIN.HTML", wantStatus: http.StatusNotFound},
		{path: "/admin.css", wantStatus: http.StatusNotFound},
		{path: "/admin-config.js", wantStatus: http.StatusNotFound},
		{path: "/admin-legacy.js", wantStatus: http.StatusNotFound},
		{path: "/i18n-admin.js", wantStatus: http.StatusNotFound},
		{path: "/admin/", wantStatus: http.StatusNotFound},
		{path: "/private/console", wantStatus: http.StatusPermanentRedirect, wantLocation: "/private/console/"},
		{path: "/private/console/", wantStatus: http.StatusOK, wantBody: "admin page"},
		{path: "/private/console/admin.css", wantStatus: http.StatusOK, wantBody: "admin style"},
		{path: "/private/console/admin-config.js", wantStatus: http.StatusOK, wantBody: "admin config script"},
		{path: "/private/console/admin-legacy.js", wantStatus: http.StatusOK, wantBody: "admin script"},
		{path: "/private/console/i18n-admin.js", wantStatus: http.StatusOK, wantBody: "admin translations"},
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.wantStatus || (test.wantBody != "" && !strings.Contains(response.Body.String(), test.wantBody)) ||
			(test.wantLocation != "" && response.Header().Get("Location") != test.wantLocation) {
			t.Errorf("%s: got %d %q, want %d containing %q", test.path, response.Code, response.Body.String(), test.wantStatus, test.wantBody)
		}
	}
}

func TestGitHubReleaseUsesEmbeddedAdminAssetsOverStaleDeploymentFiles(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	staticDir := t.TempDir()
	for name, contents := range map[string]string{
		"admin.html":      "stale setup with TOTP",
		"material.css":    "stale material styles",
		"admin-config.js": "stale setup request",
		"admin-legacy.js": "stale login script",
		"admin-tokens.js": "stale token script",
		"i18n-admin.js":   "stale translation script",
		"login.html":      "custom player page",
	} {
		if err := os.WriteFile(filepath.Join(staticDir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	embedded := fstest.MapFS{
		"admin.html":      &fstest.MapFile{Data: []byte("current setup without TOTP")},
		"material.css":    &fstest.MapFile{Data: []byte("current material styles")},
		"admin-config.js": &fstest.MapFile{Data: []byte("current setup request")},
		"admin-legacy.js": &fstest.MapFile{Data: []byte("current login script")},
		"admin-tokens.js": &fstest.MapFile{Data: []byte("current token script")},
		"i18n-admin.js":   &fstest.MapFile{Data: []byte("current translation script")},
		"login.html":      &fstest.MapFile{Data: []byte("embedded player page")},
	}
	server, err := New(db, staticDir, "", Config{GitHubRelease: true, StaticFS: embedded})
	if err != nil {
		t.Fatal(err)
	}
	for requestPath, expected := range map[string]string{
		"/admin/":                "current setup without TOTP",
		"/material.css":          "current material styles",
		"/admin/admin-config.js": "current setup request",
		"/admin/admin-legacy.js": "current login script",
		"/admin/admin-tokens.js": "current token script",
		"/admin/i18n-admin.js":   "current translation script",
		"/login.html":            "custom player page",
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, requestPath, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
			t.Errorf("%s returned %d %q, want %q", requestPath, response.Code, response.Body.String(), expected)
		}
		if strings.HasPrefix(requestPath, "/admin/") && response.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s must not be cached, got Cache-Control=%q", requestPath, response.Header().Get("Cache-Control"))
		}
	}
}

func TestAdminStatusDoesNotRevealConfiguredEntry(t *testing.T) {
	server, db := testServer(t)
	if err := (store.ConfigStore{DB: db}).Set(context.Background(), "adminPath", "/private/console"); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/status", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "/private/console") || strings.Contains(response.Body.String(), "adminPath") {
		t.Fatalf("管理员状态接口泄露安全入口: %d %s", response.Code, response.Body.String())
	}
}

func TestConcurrentPurchaseCannotOversell(t *testing.T) {
	server, db := testServer(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("purchase-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO users(id,name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,created_at,updated_at)
		VALUES(1,'Buyer','buyer','buyer-uuid',?,'buyer@example.test','buyer@example.test','active','2024-01-01',0,1,1);
		INSERT INTO sign_accounts(user_id,total_days,consecutive_days,last_sign,points,updated_at) VALUES(1,0,0,NULL,100,1);
		INSERT INTO shop_items(id,name,item_id,amount,points,stock,image,description,created_at,updated_at) VALUES('item-1','测试商品','minecraft:stone',1,10,1,'images/default_item.png','测试商品','2024-01-01',NULL);`, string(hash))
	if err != nil {
		t.Fatal(err)
	}
	server.sessions.Put("buyer-session", "Buyer", time.Now().Add(time.Hour))

	const attempts = 8
	var wait sync.WaitGroup
	results := make(chan string, attempts)
	for i := 0; i < attempts; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodPost, "/api?method=purchaseItem&username=Buyer&itemId=item-1&session=buyer-session", nil)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			results <- response.Body.String()
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	var responses []string
	for body := range results {
		responses = append(responses, body)
		if strings.Contains(body, `"success":true`) {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("并发购买成功次数=%d，预期恰好 1 次，响应=%v", successes, responses)
	}
	var points, stock, coupons int
	if err := db.QueryRow("SELECT points FROM sign_accounts WHERE user_id=1").Scan(&points); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT stock FROM shop_items WHERE id='item-1'").Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM coupons WHERE designated_player='Buyer'").Scan(&coupons); err != nil {
		t.Fatal(err)
	}
	if points != 90 || stock != 0 || coupons != 1 {
		t.Fatalf("并发购买状态错误: points=%d stock=%d coupons=%d", points, stock, coupons)
	}
}
