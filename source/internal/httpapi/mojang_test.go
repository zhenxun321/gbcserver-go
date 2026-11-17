package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPMojangLookupNormalizesUUID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/users/profiles/minecraft/Notch" {
			http.Error(writer, "bad path", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"b50ad385829d3141a2167e7d7539ba7f"}`))
	}))
	defer server.Close()

	lookup := NewHTTPMojangLookup(server.URL+"/users/profiles/minecraft/", time.Second)
	got, err := lookup.Lookup(context.Background(), "Notch")
	if err != nil {
		t.Fatal(err)
	}
	if got != "b50ad385-829d-3141-a216-7e7d7539ba7f" {
		t.Fatalf("UUID 规范化错误: %s", got)
	}
}

func TestMojangLookupRejectsInvalidUUID(t *testing.T) {
	if _, err := normalizeMojangUUID("not-a-uuid"); err == nil {
		t.Fatal("无效 UUID 应被拒绝")
	}
}

func TestRegistrationUsesInjectedMojangLookup(t *testing.T) {
	server, _ := testServer(t)
	mail := &captureMailSender{}
	server.Config.MailSender = mail
	server.Config.MojangLookup = MojangLookupFunc(func(_ context.Context, username string) (string, error) {
		if username != "Notch" {
			return "", context.Canceled
		}
		return "12345678-1234-1234-1234-123456789abc", nil
	})
	request := httptest.NewRequest(http.MethodPost, "/api?method=regist", strings.NewReader(url.Values{
		"name": {"Notch"}, "passwd": {"secret-password"}, "email": {"mojang@example.test"},
	}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "验证码已发送") {
		t.Fatalf("Mojang UUID 注册失败: %d %s", response.Code, response.Body.String())
	}
	mail.mu.Lock()
	code := strings.TrimSpace(strings.TrimPrefix(mail.body, "验证码："))
	mail.mu.Unlock()
	record, ok := server.readVerification("mojang@example.test", code, "register")
	if !ok || record.UUID != "12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("注册没有保存 Mojang UUID: ok=%v record=%+v", ok, record)
	}
}
