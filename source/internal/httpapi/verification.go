package httpapi

import (
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
var minecraftNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{3,16}$`)

func (s *Server) handleVerificationMethod(writer http.ResponseWriter, request *http.Request, method string, params map[string]any) {
	switch method {
	case "regist":
		name, password, email := paramString(params["name"]), paramString(params["passwd"]), strings.TrimSpace(paramString(params["email"]))
		if name == "" || password == "" || email == "" {
			writePlain(writer, http.StatusOK, "传入参数错误！")
			return
		}
		if strings.EqualFold(name, "admin") {
			writePlain(writer, http.StatusOK, "禁止注册管理员账号")
			return
		}
		if strings.Contains(name, "@") {
			writePlain(writer, http.StatusOK, "用户名不能包含@符号！")
			return
		}
		if !minecraftNamePattern.MatchString(name) {
			writePlain(writer, http.StatusOK, "用户名格式不合法！")
			return
		}
		if !emailPattern.MatchString(email) {
			writePlain(writer, http.StatusOK, "邮箱格式不合法！")
			return
		}
		var count int
		if err := s.DB.QueryRowContext(request.Context(), "SELECT COUNT(*) FROM users WHERE name_key=? OR email_key=?", strings.ToLower(name), strings.ToLower(email)).Scan(&count); err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		if count > 0 {
			writePlain(writer, http.StatusOK, "请勿重复注册！")
			return
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		code, err := randomCode()
		if err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		if s.Config.MailSender == nil {
			writePlain(writer, http.StatusOK, "发送验证邮件失败，请稍后再试。")
			return
		}
		uuid := offlineUUID(name)
		if s.Config.MojangLookup != nil {
			uuid, err = s.Config.MojangLookup.Lookup(request.Context(), name)
			if err != nil {
				writePlain(writer, http.StatusOK, "获取Minecraft UUID失败，请稍后再试。")
				return
			}
		}
		if err := s.Config.MailSender.Send(request.Context(), email, "邮箱验证", fmt.Sprintf("验证码：%s", code)); err != nil {
			writePlain(writer, http.StatusOK, "发送验证邮件失败，请稍后再试。")
			return
		}
		s.storeVerification(email, verificationRecord{Code: code, Name: name, PasswordHash: string(passwordHash), UUID: uuid, ExpiresAt: time.Now().Add(5 * time.Minute), Kind: "register"})
		writePlain(writer, http.StatusOK, "验证码已发送至您的邮箱，请使用验证码完成注册。")
	case "verifyRegist":
		email, code := strings.TrimSpace(paramString(params["email"])), strings.TrimSpace(paramString(params["code"]))
		record, ok := s.readVerification(email, code, "register")
		if !ok {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "验证码无效或已过期"})
			return
		}
		if !minecraftNamePattern.MatchString(record.Name) {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户名格式不合法！"})
			return
		}
		now := time.Now()
		_, err := s.DB.ExecContext(request.Context(), `INSERT INTO users(name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,created_at,updated_at) VALUES(?,?,?,?,?,?, 'inactive', ?,0,?,?)`, record.Name, strings.ToLower(record.Name), record.UUID, record.PasswordHash, email, strings.ToLower(email), localSignDate(now, s.Config.SignTimezoneOffset), now.UnixMilli(), now.UnixMilli())
		if err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "该用户名或邮箱已被注册！"})
			return
		}
		s.deleteVerification(email, "register")
		writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "注册成功！您的账号需要管理员激活后方可使用。"})
	case "repasswd":
		name, newPassword := paramString(params["name"]), paramString(params["newpasswd"])
		var email string
		if err := s.DB.QueryRowContext(request.Context(), "SELECT email FROM users WHERE name_key=?", strings.ToLower(name)).Scan(&email); errors.Is(err, sql.ErrNoRows) {
			writePlain(writer, http.StatusOK, "该用户不存在！")
			return
		} else if err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		if given := strings.TrimSpace(paramString(params["email"])); given != "" && !strings.EqualFold(given, email) {
			writePlain(writer, http.StatusOK, "邮箱与注册邮箱不匹配")
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		code, err := randomCode()
		if err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		if s.Config.MailSender == nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		if err := s.Config.MailSender.Send(request.Context(), email, "密码修改验证码", fmt.Sprintf("验证码：%s", code)); err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		s.storeVerification(email, verificationRecord{Code: code, Name: name, PasswordHash: string(hash), ExpiresAt: time.Now().Add(5 * time.Minute), Kind: "reset"})
		writePlain(writer, http.StatusOK, "验证码已发送至您的注册邮箱，请使用验证码完成密码修改。")
	case "verifyCode":
		email, code := strings.TrimSpace(paramString(params["email"])), strings.TrimSpace(paramString(params["code"]))
		record, ok := s.readVerification(email, code, "reset")
		if !ok {
			writePlain(writer, http.StatusOK, "验证码无效或已过期")
			return
		}
		if _, err := s.DB.ExecContext(request.Context(), "UPDATE users SET password_hash=?,updated_at=? WHERE name_key=?", record.PasswordHash, time.Now().UnixMilli(), strings.ToLower(record.Name)); err != nil {
			writePlain(writer, http.StatusOK, "服务器内部错误")
			return
		}
		s.deleteVerification(email, "reset")
		writePlain(writer, http.StatusOK, "密码修改成功！")
	}
}

func randomCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}
func (s *Server) storeVerification(email string, record verificationRecord) {
	s.verificationMu.Lock()
	defer s.verificationMu.Unlock()
	s.verifications[strings.ToLower(email)] = record
}
func (s *Server) takeVerification(email, code, kind string) (verificationRecord, bool) {
	record, ok := s.readVerification(email, code, kind)
	if ok {
		s.deleteVerification(email, kind)
	}
	return record, ok
}

func (s *Server) readVerification(email, code, kind string) (verificationRecord, bool) {
	s.verificationMu.Lock()
	defer s.verificationMu.Unlock()
	key := strings.ToLower(email)
	record, ok := s.verifications[key]
	if !ok || record.Kind != kind || record.Code != code || time.Now().After(record.ExpiresAt) {
		return verificationRecord{}, false
	}
	return record, true
}

func (s *Server) deleteVerification(email, kind string) {
	s.verificationMu.Lock()
	defer s.verificationMu.Unlock()
	key := strings.ToLower(email)
	if record, ok := s.verifications[key]; ok && record.Kind == kind {
		delete(s.verifications, key)
	}
}
func offlineUUID(name string) string {
	sum := md5.Sum([]byte("OfflinePlayer:" + name))
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(sum[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexValue[:8], hexValue[8:12], hexValue[12:16], hexValue[16:20], hexValue[20:32])
}
