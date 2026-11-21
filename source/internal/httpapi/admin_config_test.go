package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbcserver/source/internal/store"
)

func TestValidateAdminConfigRejectsSiteBrandingFields(t *testing.T) {
	for _, key := range []string{"siteName", "loginPageTitle", "statusPageTitle", "statusPageFooter"} {
		if _, err := validateAdminConfigValue(key, "自定义文案"); err == nil {
			t.Errorf("%s 不应再作为管理员配置项", key)
		}
	}
}

func TestValidateAdminStatusListsSupportsChineseNames(t *testing.T) {
	values := []struct {
		key   string
		value string
	}{
		{"statusServers", `[{"name":"电信线路","host":"v4.242774835.xyz","port":1259},{"name":"移动IPv6","host":"mc.242774835.xyz","defaultPort":25565}]`},
		{"statusAPIs", `[{"name":"默认状态接口","url":"https://api.mcsrvstat.us/3/{hostport}"}]`},
	}
	for _, item := range values {
		if _, err := validateAdminConfigValue(item.key, item.value); err != nil {
			t.Fatalf("%s 不应拒绝中文名称或合法列表: %v", item.key, err)
		}
	}
	for _, item := range []struct{ key, value string }{
		{"statusServers", `[{"name":"","host":"127.0.0.1","port":25565}]`},
		{"statusServers", `[{"name":"无效主机","host":"127.0.0.1/path","port":25565}]`},
		{"statusServers", `[{"name":"坏端口","host":"127.0.0.1","port":70000}]`},
		{"statusAPIs", `[{"name":"错误 URL","url":"file:///etc/passwd"}]`},
	} {
		if _, err := validateAdminConfigValue(item.key, item.value); err == nil {
			t.Fatalf("%s 应拒绝无效状态列表: %s", item.key, item.value)
		}
	}
}

func TestValidateStatusAlertSettings(t *testing.T) {
	for key, value := range map[string]string{
		"statusMonitorInterval":      "1m",
		"statusAlertAfter":           "2m",
		"statusAlertEmail":           "ops@example.test",
		"statusAlertWebhookURL":      "https://hooks.example.test/status?token=secret",
		"statusAlertEmailEnabled":    "true",
		"statusAlertWebhookEnabled":  "false",
		"statusAlertRecoveryEnabled": "true",
	} {
		if _, err := validateAdminConfigValue(key, value); err != nil {
			t.Errorf("%s 应接受有效告警配置: %v", key, err)
		}
	}
	for _, item := range []struct{ key, value string }{
		{"statusMonitorInterval", "1s"},
		{"statusAlertAfter", "31d"},
		{"statusAlertEmail", "not-an-email"},
		{"statusAlertWebhookURL", "file:///invalid"},
		{"statusAlertEmailEnabled", "on"},
	} {
		if _, err := validateAdminConfigValue(item.key, item.value); err == nil {
			t.Errorf("%s 应拒绝无效告警配置 %q", item.key, item.value)
		}
	}
}

func TestGitHubReleaseKeepsAdminConfigAndServerFeatures(t *testing.T) {
	server, db := testServer(t)
	server.Config.GitHubRelease = true
	server.adminSessions.Put("test-session", "admin", time.Now().Add(time.Hour))
	config := store.ConfigStore{DB: db}
	for key, value := range map[string]string{
		"httpPort": "80", "serverID": "survival", "siteName": "嘎嘣脆综合管理系统",
		"statusAlertWebhookURL": "https://hooks.example.test/status?token=private",
	} {
		if err := config.Set(context.Background(), key, value); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/config", nil)
	request.Header.Set("X-Admin-Session", "test-session")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	var result struct {
		Success      bool           `json:"success"`
		GitHubRelease bool          `json:"githubRelease"`
		Config       map[string]any `json:"config"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !result.Success || !result.GitHubRelease {
		t.Fatalf("GitHub 发布版配置读取失败: %d %s", response.Code, response.Body.String())
	}
	for _, key := range []string{"httpPort", "serverID"} {
		if result.Config[key] == nil {
			t.Errorf("GitHub 发布版应保留配置字段 %s", key)
		}
	}
	if result.Config["statusAlertWebhookConfigured"] != true {
		t.Fatalf("发布版应保留 Webhook 配置状态: %#v", result.Config)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/admin/config", strings.NewReader(`{"serverID":"changed"}`))
	request.Header.Set("X-Admin-Session", "test-session")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GitHub 发布版应保留服务器与插件配置功能: %d %s", response.Code, response.Body.String())
	}
	if value, exists, err := config.Get(context.Background(), "serverID"); err != nil || !exists || value != "changed" {
		t.Fatalf("GitHub 发布版没有保存配置字段: value=%q exists=%v err=%v", value, exists, err)
	}
}

func TestStatusAlertChannelsRequireTargetsAndSMTP(t *testing.T) {
	server, _ := testServer(t)
	if err := server.validateStatusAlertChannels(context.Background(), map[string]string{
		"statusAlertEmailEnabled": "true", "statusAlertEmail": "ops@example.test",
	}); err == nil {
		t.Fatal("邮件告警启用时应要求 SMTP 配置")
	}
	if err := server.validateStatusAlertChannels(context.Background(), map[string]string{
		"statusAlertWebhookEnabled": "true", "statusAlertWebhookURL": "",
	}); err == nil {
		t.Fatal("Webhook 告警启用时应要求目标地址")
	}
	if err := server.validateStatusAlertChannels(context.Background(), map[string]string{
		"statusAlertEmailEnabled": "true", "statusAlertEmail": "ops@example.test",
		"mailHost": "smtp.example.test", "mailFromAddress": "system@example.test",
		"statusAlertWebhookEnabled": "false", "statusAlertWebhookURL": "",
	}); err != nil {
		t.Fatalf("完整邮件告警配置不应被拒绝: %v", err)
	}
}
