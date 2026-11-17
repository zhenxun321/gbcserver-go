package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gbcserver/source/internal/store"
	_ "modernc.org/sqlite"
)

func TestParseBearer(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{name: "valid", header: "Bearer abc123", want: "abc123"},
		{name: "case insensitive scheme", header: "bEaReR abc123", want: "abc123"},
		{name: "missing", wantErr: true},
		{name: "wrong scheme", header: "Basic abc123", wantErr: true},
		{name: "missing secret", header: "Bearer", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseBearer(test.header)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected parse error")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got token %q, error %v", got, err)
			}
		})
	}
}

func TestPathUsername(t *testing.T) {
	if got, ok := pathUsername("/check/Player_1", "/check/"); !ok || got != "Player_1" {
		t.Fatalf("unexpected username: %q, ok=%v", got, ok)
	}
	if _, ok := pathUsername("/check/a%2Fb", "/check/"); ok {
		t.Fatal("encoded slash must be rejected")
	}
	if _, ok := pathUsername("/check/", "/check/"); ok {
		t.Fatal("empty username must be rejected")
	}
}

func TestAllowedSourceDoesNotTrustForwardedHeader(t *testing.T) {
	h := &Handler{}
	r := httptest.NewRequest("POST", "http://plugin.local/check/Player_1", nil)
	r.RemoteAddr = "192.0.2.10:12345"
	r.Header.Set("X-Forwarded-For", "10.1.2.3")
	if h.allowedSource(r) {
		t.Fatal("forwarded header must not make a public source internal")
	}
	r.RemoteAddr = "10.1.2.3:12345"
	if !h.allowedSource(r) {
		t.Fatal("private source was rejected")
	}
	r.RemoteAddr = "[fd00::10]:12345"
	if !h.allowedSource(r) {
		t.Fatal("IPv6 ULA source was rejected")
	}
}

func TestTokenStoreScopeExpiryRevocationAndHash(t *testing.T) {
	db := newTestDatabase(t)
	store := NewTokenStore(db)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	store.Now = func() time.Time { return now }

	expiresAt := now.Add(time.Hour)
	issued, err := store.Issue(context.Background(), "survival-1", ScopeWhitelist, &expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	var storedHash string
	if err := db.QueryRow("SELECT token_hash FROM plugin_tokens WHERE token_id = ?", issued.TokenID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash == issued.Secret || storedHash != hashToken(issued.Secret) {
		t.Fatal("数据库应只保存 bearer secret 的摘要")
	}
	principal, err := store.Authenticate(context.Background(), issued.Secret, ScopeWhitelist)
	if err != nil || principal.ServerID != "survival-1" {
		t.Fatalf("whitelist token 验证失败: principal=%+v err=%v", principal, err)
	}
	if _, err := store.Authenticate(context.Background(), issued.Secret, ScopeCouponRedeem); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("whitelist token 不得用于兑换，得到错误 %v", err)
	}
	couponIssued, err := store.Issue(context.Background(), "survival-1", ScopeCouponRedeem, nil)
	if err != nil {
		t.Fatal(err)
	}
	if couponIssued.ServerID != issued.ServerID || couponIssued.TokenID == issued.TokenID || couponIssued.Secret == issued.Secret {
		t.Fatalf("同一服务的两个 scope 应共用 server_id，但必须使用独立 token：whitelist=%+v coupon=%+v", issued, couponIssued)
	}

	if err := store.Revoke(context.Background(), issued.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(context.Background(), issued.Secret, ScopeWhitelist); !errors.Is(err, ErrRevokedToken) {
		t.Fatalf("撤销后的 token 应失效，得到错误 %v", err)
	}

	expiredAt := now.Add(2 * time.Hour)
	expiring, err := store.Issue(context.Background(), "survival-1", ScopeCouponRedeem, &expiredAt)
	if err != nil {
		t.Fatal(err)
	}
	now = expiredAt
	if _, err := store.Authenticate(context.Background(), expiring.Secret, ScopeCouponRedeem); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("过期 token 应失效，得到错误 %v", err)
	}
}

func TestPluginListenerRejectsTokenForMismatchedServerID(t *testing.T) {
	db := newTestDatabase(t)
	tokens := NewTokenStore(db)
	whitelistToken, err := tokens.Issue(context.Background(), "other-server", ScopeWhitelist, nil)
	if err != nil {
		t.Fatal(err)
	}
	couponToken, err := tokens.Issue(context.Background(), "other-server", ScopeCouponRedeem, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := &stubWhitelistService{check: CheckResult{Allowed: true, Reason: "authorized"}}
	couponService := &stubCouponService{result: CouponResult{Accepted: true, Message: "兑换成功"}}
	h := &Handler{Tokens: tokens, Whitelist: service, Coupons: couponService, ExpectedServerID: "configured-server"}
	for _, test := range []struct {
		name, path, token, body string
	}{
		{name: "whitelist", path: "/check/Player_1", token: whitelistToken.Secret},
		{name: "coupon", path: "/api", token: couponToken.Secret, body: `{"method":"useCoupon","username":"Player_1","code":"GIFT","requestId":"req-1"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://plugin.local"+test.path, strings.NewReader(test.body))
			request.RemoteAddr = "127.0.0.1:12345"
			request.Header.Set("Authorization", "Bearer "+test.token)
			if test.name == "coupon" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "server_id_mismatch") {
				t.Fatalf("错配服务 ID 应返回明确拒绝：%d %s", response.Code, response.Body.String())
			}
		})
	}
	if service.checkServerID != "" || couponService.calls != 0 {
		t.Fatalf("错配 token 不应进入业务服务：whitelist=%+v coupon=%+v", service, couponService)
	}
}

func TestWhitelistAuthErrorsUseEndpointResponseShape(t *testing.T) {
	db := newTestDatabase(t)
	h := &Handler{Tokens: NewTokenStore(db)}

	for _, test := range []struct {
		path  string
		field string
	}{{"/check/Player_1", "allowed"}, {"/join/Player_1", "ok"}, {"/logout/Player_1", "ok"}, {"/heartbeat", "ok"}} {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "http://plugin.local"+test.path, nil)
			req.RemoteAddr = "10.1.2.3:12345"
			req.Header.Set("Authorization", "Bearer invalid")
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", response.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body[test.field] != false || body["reason"] != "authentication_failed" {
				t.Fatalf("unexpected auth response: %#v", body)
			}
		})
	}
}

func TestWhitelistCheckJoinAndHeartbeatProtocol(t *testing.T) {
	db := newTestDatabase(t)
	tokens := NewTokenStore(db)
	issued, err := tokens.Issue(context.Background(), "survival-1", ScopeWhitelist, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := &stubWhitelistService{
		check:     CheckResult{Allowed: true, Reason: "authorized", LoginAttemptID: "attempt-123", ExpiresAt: 1_798_000_000_000},
		join:      JoinResult{Confirmed: true},
		heartbeat: HeartbeatResult{RejectedPlayers: []string{}},
	}
	h := &Handler{Tokens: tokens, Whitelist: service}
	makeRequest := func(method, path, body string) *http.Request {
		request := httptest.NewRequest(method, "http://plugin.local"+path, strings.NewReader(body))
		request.RemoteAddr = "10.1.2.3:12345"
		request.Header.Set("Authorization", "Bearer "+issued.Secret)
		return request
	}

	check := httptest.NewRecorder()
	h.ServeHTTP(check, makeRequest(http.MethodPost, "/check/LegacyPlayer", ""))
	var checkBody map[string]any
	if err := json.Unmarshal(check.Body.Bytes(), &checkBody); err != nil {
		t.Fatal(err)
	}
	if check.Code != http.StatusOK || checkBody["allowed"] != true || checkBody["loginAttemptId"] != "attempt-123" {
		t.Fatalf("unexpected check response: status=%d body=%#v", check.Code, checkBody)
	}
	if service.checkServerID != "survival-1" || service.checkUsername != "LegacyPlayer" {
		t.Fatalf("check identity was not passed to service: %+v", service)
	}

	joinRequest := makeRequest(http.MethodPost, "/join/LegacyPlayer", "")
	joinRequest.Header.Set("X-Login-Attempt-ID", "attempt-123")
	join := httptest.NewRecorder()
	h.ServeHTTP(join, joinRequest)
	if join.Code != http.StatusOK || !strings.Contains(join.Body.String(), `"alreadyOpen":false`) {
		t.Fatalf("unexpected join response: status=%d body=%s", join.Code, join.Body.String())
	}
	if service.joinServerID != "survival-1" || service.joinUsername != "LegacyPlayer" || service.joinAttemptID != "attempt-123" {
		t.Fatalf("join attempt binding was not passed to service: %+v", service)
	}

	heartbeatRequest := makeRequest(http.MethodPost, "/heartbeat", `{"players":["LegacyPlayer"]}`)
	heartbeatRequest.Header.Set("Content-Type", "application/json")
	heartbeat := httptest.NewRecorder()
	h.ServeHTTP(heartbeat, heartbeatRequest)
	if heartbeat.Code != http.StatusOK || service.heartbeatServerID != "survival-1" || len(service.heartbeatNames) != 1 || service.heartbeatNames[0] != "LegacyPlayer" {
		t.Fatalf("unexpected heartbeat: status=%d body=%s service=%+v", heartbeat.Code, heartbeat.Body.String(), service)
	}
}

func TestCouponHandlerRequiresMatchingIdempotencyID(t *testing.T) {
	db := newTestDatabase(t)
	tokens := NewTokenStore(db)
	issued, err := tokens.Issue(context.Background(), "survival-1", ScopeCouponRedeem, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := &stubCouponService{result: CouponResult{Accepted: true, Message: "兑换成功"}}
	h := &Handler{Tokens: tokens, Coupons: service}

	for _, test := range []struct {
		name      string
		body      string
		headerID  string
		wantCode  int
		wantCalls int
	}{
		{name: "missing id", body: `{"method":"useCoupon","username":"Player_1","code":"GIFT"}`, wantCode: http.StatusBadRequest},
		{name: "mismatched id", body: `{"method":"useCoupon","username":"Player_1","code":"GIFT","requestId":"body-id"}`, headerID: "header-id", wantCode: http.StatusBadRequest},
		{name: "valid id", body: `{"method":"useCoupon","username":"Player_1","code":"GIFT","requestId":"stable-id"}`, headerID: "stable-id", wantCode: http.StatusOK, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.calls = 0
			req := httptest.NewRequest(http.MethodPost, "http://api.local/api", strings.NewReader(test.body))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+issued.Secret)
			if test.headerID != "" {
				req.Header.Set("X-Request-ID", test.headerID)
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != test.wantCode {
				t.Fatalf("status=%d, want %d; body=%s", response.Code, test.wantCode, response.Body.String())
			}
			if service.calls != test.wantCalls {
				t.Fatalf("service calls=%d, want %d", service.calls, test.wantCalls)
			}
			if test.wantCalls == 1 && (service.requestID != "stable-id" || service.serverID != "survival-1") {
				t.Fatalf("idempotency key or server identity lost: %+v", service)
			}
		})
	}
}

func TestCouponRateLimitIsScopedToServer(t *testing.T) {
	db := newTestDatabase(t)
	tokens := NewTokenStore(db)
	issued, err := tokens.Issue(context.Background(), "survival-1", ScopeCouponRedeem, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := &stubCouponService{result: CouponResult{Accepted: true, Message: "兑换成功"}}
	h := &Handler{Tokens: tokens, Coupons: service, CouponRateLimit: 1}
	makeRequest := func(requestID string) *httptest.ResponseRecorder {
		body := `{"method":"useCoupon","username":"Player_1","code":"GIFT","requestId":"` + requestID + `"}`
		req := httptest.NewRequest(http.MethodPost, "http://api.local/api", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+issued.Secret)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		return response
	}
	if response := makeRequest("request-1"); response.Code != http.StatusOK {
		t.Fatalf("first request status=%d body=%s", response.Code, response.Body.String())
	}
	if response := makeRequest("request-2"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status=%d body=%s", response.Code, response.Body.String())
	}
	if service.calls != 1 {
		t.Fatalf("rate-limited request reached domain service; calls=%d", service.calls)
	}
}

func TestCouponHandlerRequiresInternalSource(t *testing.T) {
	db := newTestDatabase(t)
	tokens := NewTokenStore(db)
	issued, err := tokens.Issue(context.Background(), "survival-1", ScopeCouponRedeem, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := &stubCouponService{result: CouponResult{Accepted: true, Message: "兑换成功"}}
	h := &Handler{Tokens: tokens, Coupons: service}
	body := "{\"method\":\"useCoupon\",\"username\":\"Player_1\",\"code\":\"GIFT\",\"requestId\":\"stable-id\"}"
	makeRequest := func(source string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://api.local/api", strings.NewReader(body))
		req.RemoteAddr = source
		req.Header.Set("X-Forwarded-For", "10.20.0.8")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+issued.Secret)
		return req
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, makeRequest("192.0.2.25:54321"))
	if response.Code != http.StatusForbidden || service.calls != 0 {
		t.Fatalf("untrusted source reached coupon service: status=%d calls=%d", response.Code, service.calls)
	}

	response = httptest.NewRecorder()
	h.ServeHTTP(response, makeRequest("10.20.0.8:54321"))
	if response.Code != http.StatusOK || service.calls != 1 {
		t.Fatalf("internal game server was rejected: status=%d calls=%d body=%s", response.Code, service.calls, response.Body.String())
	}
}

func TestValidateListenAddressRequiresExplicitPrivateIP(t *testing.T) {
	for _, address := range []string{"0.0.0.0:18094", "[::]:18094", "8.8.8.8:18094", "localhost:18094", "127.0.0.1:0", "10.20.0.10", "10.20.0.10/24:18094"} {
		if err := ValidateListenAddress(address); err == nil {
			t.Fatalf("unsafe listener address %q was accepted", address)
		}
	}
	for _, address := range []string{"127.0.0.1:18095", "10.20.0.10:18094", "[fd00::10]:18094"} {
		if err := ValidateListenAddress(address); err != nil {
			t.Fatalf("private listener address %q was rejected: %v", address, err)
		}
	}
}

type stubCouponService struct {
	result    CouponResult
	calls     int
	serverID  string
	requestID string
}

type stubWhitelistService struct {
	check             CheckResult
	join              JoinResult
	logout            LogoutResult
	heartbeat         HeartbeatResult
	checkServerID     string
	checkUsername     string
	joinServerID      string
	joinUsername      string
	joinAttemptID     string
	heartbeatServerID string
	heartbeatNames    []string
}

func (s *stubWhitelistService) Check(_ context.Context, serverID, username string) (CheckResult, error) {
	s.checkServerID = serverID
	s.checkUsername = username
	return s.check, nil
}

func (s *stubWhitelistService) Join(_ context.Context, serverID, username, attemptID string) (JoinResult, error) {
	s.joinServerID = serverID
	s.joinUsername = username
	s.joinAttemptID = attemptID
	return s.join, nil
}

func (s *stubWhitelistService) Logout(_ context.Context, _, _ string) (LogoutResult, error) {
	return s.logout, nil
}

func (s *stubWhitelistService) Heartbeat(_ context.Context, serverID string, names []string) (HeartbeatResult, error) {
	s.heartbeatServerID = serverID
	s.heartbeatNames = append([]string(nil), names...)
	return s.heartbeat, nil
}

func (s *stubCouponService) UseCoupon(_ context.Context, serverID, _, _, requestID string) (CouponResult, error) {
	s.calls++
	s.serverID = serverID
	s.requestID = requestID
	return s.result, nil
}

func newTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
