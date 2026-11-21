package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const adminTOTPEnrollmentTTL = 5 * time.Minute

func (s *Server) consumeTOTPCode(ctx context.Context, secret, code string, now time.Time) bool {
	counter, ok := matchingTOTPCounter(code, secret, now)
	if !ok {
		return false
	}
	decoded, err := decodeTOTPSecret(secret)
	if err != nil {
		return false
	}
	secretHash := sha256.Sum256(decoded)
	prefix := fmt.Sprintf("adminTOTPUsed:%x:", secretHash)
	key := fmt.Sprintf("%s%d", prefix, counter)
	result, err := s.DB.ExecContext(ctx, `INSERT INTO app_config(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO NOTHING`, key, "1", now.UnixMilli())
	if err != nil {
		return false
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return false
	}
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM app_config WHERE key LIKE ? AND CAST(SUBSTR(key, ?) AS INTEGER) < ?`, prefix+"%", len(prefix)+1, now.Unix()/30-1)
	return true
}

type adminTOTPEnrollment struct {
	Secret        string
	CurrentSecret string
	ExpiresAt     time.Time
}

func adminRequestFailureKey(request *http.Request) string {
	key := request.RemoteAddr
	if host, _, err := net.SplitHostPort(key); err == nil {
		key = host
	}
	return key
}

func (s *Server) requireAdminTOTPWrite(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", false
	}
	sessionID := strings.TrimSpace(r.Header.Get("X-Admin-Session"))
	if _, ok := s.adminSessions.Get(sessionID); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效或已过期"})
		return "", false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	return sessionID, true
}

func (s *Server) rejectAdminTOTPCode(w http.ResponseWriter, r *http.Request, key, message string) {
	blocked := s.recordAdminFailure(key, time.Now())
	time.Sleep(150 * time.Millisecond)
	if blocked > 0 {
		seconds := int(math.Ceil(blocked.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", seconds)})
		return
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": message})
}

func (s *Server) handleAdminTOTPBegin(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := s.requireAdminTOTPWrite(w, r)
	if !ok {
		return
	}
	key := adminRequestFailureKey(r)
	if retryAfter := s.adminRetryAfter(key, time.Now()); retryAfter > 0 {
		seconds := int(math.Ceil(retryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", seconds)})
		return
	}
	var input struct {
		CurrentCode string `json:"currentCode"`
		Secret      string `json:"secret"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
		return
	}
	secret := ""
	if strings.TrimSpace(input.Secret) != "" {
		decoded, err := decodeTOTPSecret(input.Secret)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "TOTP 密钥格式无效"})
			return
		}
		secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(decoded)
	}
	s.adminTOTPEnrollMu.Lock()
	currentSecret, err := s.adminTOTPSecret(r.Context())
	if err != nil {
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取双重验证状态失败"})
		return
	}
	if currentSecret != "" && !verifyTOTP(input.CurrentCode, currentSecret, time.Now()) {
		s.adminTOTPEnrollMu.Unlock()
		s.rejectAdminTOTPCode(w, r, key, "当前验证码错误")
		return
	}
	if currentSecret != "" && !s.consumeTOTPCode(r.Context(), currentSecret, input.CurrentCode, time.Now()) {
		s.adminTOTPEnrollMu.Unlock()
		s.rejectAdminTOTPCode(w, r, key, "当前验证码错误")
		return
	}

	if secret == "" {
		var randomSecret [20]byte
		if _, err := io.ReadFull(rand.Reader, randomSecret[:]); err != nil {
			s.adminTOTPEnrollMu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "生成双重验证密钥失败"})
			return
		}
		secret = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomSecret[:])
	}

	now := time.Now()
	for id, enrollment := range s.adminTOTPEnrollments {
		if !enrollment.ExpiresAt.After(now) {
			delete(s.adminTOTPEnrollments, id)
		}
	}
	s.adminTOTPEnrollments[sessionID] = adminTOTPEnrollment{Secret: secret, CurrentSecret: currentSecret, ExpiresAt: now.Add(adminTOTPEnrollmentTTL)}
	s.adminTOTPEnrollMu.Unlock()

	username, _, _ := s.configStore.Get(r.Context(), "adminUsername")
	if strings.TrimSpace(username) == "" {
		username = s.Config.AdminUsername
	}
	label := url.PathEscape("Gbcserver-Go:" + username)
	query := url.Values{
		"secret":    {secret},
		"issuer":    {"Gbcserver-Go"},
		"algorithm": {"SHA1"},
		"digits":    {"6"},
		"period":    {"30"},
	}
	otpauthURI := "otpauth://totp/" + label + "?" + query.Encode()
	qr, err := qrcode.Encode(otpauthURI, qrcode.Medium, 256)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "生成双重验证二维码失败"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"secret":     secret,
		"otpauthURI": otpauthURI,
		"qrDataURI":  "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr),
		"expiresAt":  now.Add(adminTOTPEnrollmentTTL).Unix(),
	})
}

func (s *Server) handleAdminTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := s.requireAdminTOTPWrite(w, r)
	if !ok {
		return
	}
	key := adminRequestFailureKey(r)
	if retryAfter := s.adminRetryAfter(key, time.Now()); retryAfter > 0 {
		seconds := int(math.Ceil(retryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", seconds)})
		return
	}
	var input struct {
		Code        string `json:"code"`
		CurrentCode string `json:"currentCode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
		return
	}

	s.adminTOTPEnrollMu.Lock()
	enrollment, exists := s.adminTOTPEnrollments[sessionID]
	if !exists || !enrollment.ExpiresAt.After(time.Now()) {
		delete(s.adminTOTPEnrollments, sessionID)
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "双重验证绑定已过期，请重新生成密钥"})
		return
	}
	currentSecret, err := s.adminTOTPSecret(r.Context())
	if err != nil {
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取双重验证状态失败"})
		return
	}
	if currentSecret != enrollment.CurrentSecret {
		delete(s.adminTOTPEnrollments, sessionID)
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "双重验证状态已变化，请重新生成密钥"})
		return
	}
	if !s.consumeTOTPCode(r.Context(), enrollment.Secret, input.Code, time.Now()) {
		s.adminTOTPEnrollMu.Unlock()
		s.rejectAdminTOTPCode(w, r, key, "验证码错误")
		return
	}
	if _, ok := s.adminSessions.Get(sessionID); !ok {
		delete(s.adminTOTPEnrollments, sessionID)
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效或已过期"})
		return
	}
	if err := s.configStore.Set(r.Context(), "adminTOTPSecret", enrollment.Secret); err != nil {
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "保存双重验证设置失败"})
		return
	}
	s.Config.AdminTOTPSecret = enrollment.Secret
	delete(s.adminTOTPEnrollments, sessionID)
	clear(s.adminTOTPEnrollments)
	s.adminSessions.DeleteExcept(sessionID)
	s.adminTOTPEnrollMu.Unlock()
	s.clearAdminFailures(key)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "enabled": true, "message": "双重验证已启用"})
}

func (s *Server) handleAdminTOTPDisable(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := s.requireAdminTOTPWrite(w, r)
	if !ok {
		return
	}
	key := adminRequestFailureKey(r)
	if retryAfter := s.adminRetryAfter(key, time.Now()); retryAfter > 0 {
		seconds := int(math.Ceil(retryAfter.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", seconds)})
		return
	}
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024)).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"success": false, "message": "请求格式无效"})
		return
	}
	s.adminTOTPEnrollMu.Lock()
	defer s.adminTOTPEnrollMu.Unlock()
	secret, err := s.adminTOTPSecret(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取双重验证状态失败"})
		return
	}
	if secret == "" {
		writeJSON(w, http.StatusConflict, map[string]any{"success": false, "message": "双重验证尚未启用"})
		return
	}
	passwordHash, _, err := s.configStore.Get(r.Context(), "adminPasswordHash")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取管理员账户失败"})
		return
	}
	if strings.TrimSpace(passwordHash) == "" {
		passwordHash = s.Config.AdminPasswordHash
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(input.Password)) != nil || !verifyTOTP(input.Code, secret, time.Now()) {
		s.rejectAdminTOTPCode(w, r, key, "管理员密码或当前验证码错误")
		return
	}
	if _, ok := s.adminSessions.Get(sessionID); !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "message": "管理员会话无效或已过期"})
		return
	}
	if !s.consumeTOTPCode(r.Context(), secret, input.Code, time.Now()) {
		s.rejectAdminTOTPCode(w, r, key, "管理员密码或当前验证码错误")
		return
	}
	if err := s.configStore.Set(r.Context(), "adminTOTPSecret", ""); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"success": false, "message": "保存双重验证设置失败"})
		return
	}
	s.Config.AdminTOTPSecret = ""
	delete(s.adminTOTPEnrollments, sessionID)
	clear(s.adminTOTPEnrollments)
	s.adminSessions.DeleteExcept(sessionID)
	s.clearAdminFailures(key)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "enabled": false, "message": "双重验证已关闭"})
}
