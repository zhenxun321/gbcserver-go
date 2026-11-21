package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type adminFailure struct {
	count     int
	windowAt  time.Time
	lockUntil time.Time
}

func (s *Server) handleAdminLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	params, err := ParseLegacyParams(request, s.Config.BodyLimitBytes)
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"success": false, "message": "参数不完整"})
		return
	}
	name := paramString(params["name"])
	password := paramString(params["passwd"])
	totp := paramString(params["totp"])
	if name == "" || password == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]any{"success": false, "message": "参数不完整"})
		return
	}
	key := request.RemoteAddr
	if host, _, err := net.SplitHostPort(key); err == nil {
		key = host
	}
	if retryAfter := s.adminRetryAfter(key, time.Now()); retryAfter > 0 {
		writer.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retryAfter.Seconds()))))
		writeJSON(writer, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", int(math.Ceil(retryAfter.Seconds())))})
		return
	}
	adminUsername, _, _ := s.configStore.Get(request.Context(), "adminUsername")
	adminPasswordHash, _, _ := s.configStore.Get(request.Context(), "adminPasswordHash")
	if strings.TrimSpace(adminUsername) == "" {
		adminUsername = s.Config.AdminUsername
	}
	if strings.TrimSpace(adminPasswordHash) == "" {
		adminPasswordHash = s.Config.AdminPasswordHash
	}
	if adminUsername == "" || adminPasswordHash == "" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "管理员配置缺失，请配置环境变量"})
		return
	}
	validPassword := bcryptCompare(password, adminPasswordHash) == nil
	validName := subtle.ConstantTimeCompare([]byte(name), []byte(adminUsername)) == 1
	if !validName || !validPassword {
		blocked := s.recordAdminFailure(key, time.Now())
		time.Sleep(150 * time.Millisecond)
		if blocked > 0 {
			writer.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(blocked.Seconds()))))
			writeJSON(writer, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", int(math.Ceil(blocked.Seconds())))})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "管理员账号或密码错误"})
		return
	}
	s.adminTOTPEnrollMu.Lock()
	adminTOTPSecret, err := s.adminTOTPSecret(request.Context())
	if err != nil {
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(writer, http.StatusInternalServerError, map[string]any{"success": false, "message": "读取管理员验证配置失败"})
		return
	}
	if adminTOTPSecret != "" && !s.consumeTOTPCode(request.Context(), adminTOTPSecret, totp, time.Now()) {
		s.adminTOTPEnrollMu.Unlock()
		blocked := s.recordAdminFailure(key, time.Now())
		time.Sleep(150 * time.Millisecond)
		if blocked > 0 {
			writer.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(blocked.Seconds()))))
			writeJSON(writer, http.StatusTooManyRequests, map[string]any{"success": false, "message": fmt.Sprintf("尝试次数过多，请在 %d 秒后再试", int(math.Ceil(blocked.Seconds())))})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "TOTP验证码错误"})
		return
	}
	s.clearAdminFailures(key)
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		s.adminTOTPEnrollMu.Unlock()
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "管理员登录失败: 随机会话生成失败"})
		return
	}
	sessionID := base64.RawURLEncoding.EncodeToString(random[:])
	s.adminSessions.Put(sessionID, adminUsername, time.Now().Add(s.Config.AdminSessionTTL))
	s.adminTOTPEnrollMu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "session": sessionID, "message": "管理员登录成功"})
}

func (s *Server) adminRetryAfter(key string, now time.Time) time.Duration {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	failure := s.adminFailures[key]
	if failure.lockUntil.After(now) {
		return failure.lockUntil.Sub(now)
	}
	return 0
}

func (s *Server) recordAdminFailure(key string, now time.Time) time.Duration {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	failure := s.adminFailures[key]
	if now.Sub(failure.windowAt) > 10*time.Minute {
		failure = adminFailure{windowAt: now}
	}
	failure.count++
	if failure.count >= 8 {
		failure.lockUntil = now.Add(15 * time.Minute)
		s.adminFailures[key] = failure
		return 15 * time.Minute
	}
	s.adminFailures[key] = failure
	return 0
}

func (s *Server) clearAdminFailures(key string) {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	delete(s.adminFailures, key)
}

func verifyTOTP(code, secret string, now time.Time) bool {
	_, ok := matchingTOTPCounter(code, secret, now)
	return ok
}

func matchingTOTPCounter(code, secret string, now time.Time) (uint64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	decoded, err := decodeTOTPSecret(secret)
	if err != nil || len(decoded) == 0 {
		return 0, false
	}
	currentCounter := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		matchedCounter := currentCounter + offset
		if matchedCounter < 0 {
			continue
		}
		counter := uint64(matchedCounter)
		var message [8]byte
		binary.BigEndian.PutUint64(message[:], counter)
		mac := hmac.New(sha1.New, decoded)
		_, _ = mac.Write(message[:])
		sum := mac.Sum(nil)
		index := sum[len(sum)-1] & 0x0f
		value := (uint32(sum[index])&0x7f)<<24 | uint32(sum[index+1])<<16 | uint32(sum[index+2])<<8 | uint32(sum[index+3])
		expected := fmt.Sprintf("%06d", value%1_000_000)
		if subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			return counter, true
		}
	}
	return 0, false
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		decoded, err = base32.StdEncoding.DecodeString(secret)
	}
	if err == nil && len(decoded) == 0 {
		err = errors.New("empty TOTP secret")
	}
	return decoded, err
}

func (s *Server) handleAdminLegacyMethod(writer http.ResponseWriter, request *http.Request, method string, params map[string]any) {
	webUser, webOK := s.sessions.Get(paramString(params["session"]))
	_, adminOK := s.adminSessions.Get(paramString(params["session"]))
	if !adminOK {
		adminSession := request.Header.Get("X-Admin-Session")
		_, adminOK = s.adminSessions.Get(adminSession)
	}
	if !adminOK && method == "changePassword" {
		webSession := request.Header.Get("X-Web-Session")
		if webSession == "" {
			webSession = paramString(params["session"])
		}
		webUser, webOK = s.sessions.Get(webSession)
	}
	if method != "changePassword" && method != "purchaseItem" && !adminOK {
		message := "管理员会话无效或已过期"
		if method == "getCoupons" {
			message = "需要有效的管理员会话"
		}
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": message})
		return
	}
	if (method == "changePassword" || method == "purchaseItem") && !webOK {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	ctx := request.Context()
	switch method {
	case "getAllUsers":
		s.adminGetUsers(writer, ctx)
	case "createUser":
		s.adminCreateUser(writer, request, paramString(params["name"]), paramString(params["passwd"]), paramString(params["email"]))
	case "activateUser", "banUser", "unbanUser":
		s.adminSetUserStatus(writer, ctx, method, paramString(params["name"]))
	case "deleteUser":
		s.adminDeleteUser(writer, ctx, paramString(params["name"]))
	case "updateEmail":
		s.adminUpdateEmail(writer, ctx, paramString(params["name"]), paramString(params["newEmail"]))
	case "changePassword":
		s.changePassword(writer, request, webUser, paramString(params["name"]), paramString(params["oldpasswd"]), paramString(params["newpasswd"]))
	case "addShopItem":
		s.adminAddShopItem(writer, request)
	case "updateShopItem":
		s.adminUpdateShopItem(writer, request)
	case "deleteShopItem":
		s.adminDeleteShopItem(writer, ctx, paramString(params["itemId"]))
	case "purchaseItem":
		s.purchaseItem(writer, request, webUser, paramString(params["username"]), paramString(params["itemId"]))
	case "generateCoupon":
		s.adminGenerateCoupon(writer, request)
	case "getCoupons":
		s.adminGetCoupons(writer, ctx)
	case "deleteCoupon":
		s.adminDeleteCoupon(writer, ctx, paramString(params["code"]))
	}
}

func (s *Server) adminCreateUser(writer http.ResponseWriter, request *http.Request, name, password, email string) {
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if !minecraftNamePattern.MatchString(name) || strings.EqualFold(name, "admin") {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户名格式不合法"})
		return
	}
	if !emailPattern.MatchString(email) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "邮箱格式不合法"})
		return
	}
	if password == "" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "初始密码不能为空"})
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "初始密码无效"})
		return
	}
	userUUID := offlineUUID(name)
	if s.Config.MojangLookup != nil {
		userUUID, err = s.Config.MojangLookup.Lookup(request.Context(), name)
		if err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取 Minecraft UUID 失败"})
			return
		}
	}

	now := time.Now()
	tx, err := s.DB.BeginTx(request.Context(), nil)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "创建用户失败"})
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(request.Context(), `INSERT INTO users
		(name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'active',?,0,?,?)`,
		name, strings.ToLower(name), userUUID, string(passwordHash), email, strings.ToLower(email),
		localSignDate(now, s.Config.SignTimezoneOffset), now.UnixMilli(), now.UnixMilli())
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户名、邮箱或 UUID 已被使用"})
		return
	}
	userID, err := result.LastInsertId()
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "创建用户失败"})
		return
	}
	if _, err := tx.ExecContext(request.Context(), `INSERT INTO sign_accounts
		(user_id,total_days,consecutive_days,last_sign,points,updated_at) VALUES(?,0,0,NULL,0,?)`, userID, now.UnixMilli()); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "创建用户积分账户失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "创建用户失败"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "用户已创建并激活"})
}

func (s *Server) adminGetUsers(writer http.ResponseWriter, ctx context.Context) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name,email,status,join_date,online_time_ms,last_login,avatar_filename
		FROM users ORDER BY id`)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "读取用户数据失败"})
		return
	}
	defer rows.Close()
	users := make([]map[string]any, 0)
	for rows.Next() {
		var name, email, status, joinDate string
		var onlineTime int64
		var lastLogin, avatar sql.NullString
		if err := rows.Scan(&name, &email, &status, &joinDate, &onlineTime, &lastLogin, &avatar); err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "读取用户数据失败"})
			return
		}
		var avatarURL any
		if avatar.Valid {
			avatarURL = "/avatars/" + filepath.Base(avatar.String)
		}
		users = append(users, map[string]any{"name": name, "email": email, "status": status, "joinDate": joinDate, "onlineTime": onlineTime, "lastLogin": displayLastLogin(lastLogin, s.Config.SignTimezoneOffset), "avatarPath": avatarURL})
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "users": users})
}

func (s *Server) adminSetUserStatus(writer http.ResponseWriter, ctx context.Context, method, name string) {
	status := map[string]string{"activateUser": "active", "banUser": "banned", "unbanUser": "active"}[method]
	result, err := s.DB.ExecContext(ctx, "UPDATE users SET status=?,updated_at=? WHERE name_key=?", status, time.Now().UnixMilli(), strings.ToLower(name))
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户状态更新失败"})
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	message := map[string]string{"activateUser": "用户已激活", "banUser": "用户已封禁", "unbanUser": "用户已解封"}[method]
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": message})
}

func (s *Server) adminDeleteUser(writer http.ResponseWriter, ctx context.Context, name string) {
	var canonical string
	err := s.DB.QueryRowContext(ctx, "SELECT name FROM users WHERE name_key=?", strings.ToLower(name)).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "删除用户失败"})
		return
	}
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM users WHERE name_key=?", strings.ToLower(name)); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "删除用户失败: " + err.Error()})
		return
	}
	s.sessions.DeleteUser(canonical)
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "用户已删除"})
}

func (s *Server) adminUpdateEmail(writer http.ResponseWriter, ctx context.Context, name, email string) {
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE email_key=? AND name_key<>?", strings.ToLower(strings.TrimSpace(email)), strings.ToLower(name)).Scan(&exists); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "更新邮箱失败"})
		return
	}
	if exists > 0 {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "该邮箱已被其他用户使用"})
		return
	}
	result, err := s.DB.ExecContext(ctx, "UPDATE users SET email=?,email_key=?,updated_at=? WHERE name_key=?", email, strings.ToLower(strings.TrimSpace(email)), time.Now().UnixMilli(), strings.ToLower(name))
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "更新邮箱失败"})
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "邮箱更新成功"})
}

func (s *Server) changePassword(writer http.ResponseWriter, request *http.Request, sessionUser, requestedUser, oldPassword, newPassword string) {
	if requestedUser == "" || !strings.EqualFold(requestedUser, sessionUser) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	var userID int64
	var name, hash string
	err := s.DB.QueryRowContext(request.Context(), "SELECT id,name,password_hash FROM users WHERE name_key=?", strings.ToLower(sessionUser)).Scan(&userID, &name, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "用户不存在"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	if bcryptCompare(oldPassword, hash) != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "原密码错误"})
		return
	}
	newHash, err := bcryptHash(newPassword)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	_, err = s.DB.ExecContext(request.Context(), "UPDATE users SET password_hash=?,updated_at=? WHERE id=?", newHash, time.Now().UnixMilli(), userID)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "服务器内部错误"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "密码修改成功"})
}

type shopItemPayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ItemID      string `json:"itemId"`
	Points      any    `json:"points"`
	Stock       any    `json:"stock"`
	Amount      any    `json:"amount"`
	Image       string `json:"image"`
	Description string `json:"description"`
}

func decodeBody(request *http.Request, limit int64, target any) error {
	body, err := io.ReadAll(http.MaxBytesReader(nil, request.Body, limit))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("请求体为空")
	}
	return json.Unmarshal(body, target)
}

func (s *Server) adminAddShopItem(writer http.ResponseWriter, request *http.Request) {
	var item shopItemPayload
	if err := decodeBody(request, s.Config.BodyLimitBytes, &item); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "添加商品失败"})
		return
	}
	points, ok := parseNonnegative(item.Points)
	if item.Name == "" || item.ItemID == "" || !ok {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "参数不完整或积分(points)不合法"})
		return
	}
	amount := positiveOrDefault(item.Amount, 1)
	stock, err := optionalNonnegative(item.Stock)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "库存(stock)不合法"})
		return
	}
	id := strconv.FormatInt(time.Now().UnixMilli(), 10)
	createdAt := time.Now().UTC().Format(time.RFC3339Nano)
	image, validImage := normalizeShopImageForWrite(item.Image)
	if !validImage {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": errInvalidShopImageReference.Error()})
		return
	}
	description := strings.TrimSpace(item.Description)
	if description == "" {
		description = "暂无描述"
	}
	var stockValue any
	if stock != nil {
		stockValue = *stock
	}
	if _, err := s.DB.ExecContext(request.Context(), `INSERT INTO shop_items(id,name,item_id,amount,points,stock,image,description,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,NULL)`, id, item.Name, item.ItemID, amount, points, stockValue, image, description, createdAt); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "添加商品失败"})
		return
	}
	result := map[string]any{"id": id, "name": item.Name, "itemId": item.ItemID, "amount": amount, "points": points, "stock": stockValue, "image": image, "description": description, "createdAt": createdAt}
	var total int
	_ = s.DB.QueryRowContext(request.Context(), "SELECT COUNT(*) FROM shop_items").Scan(&total)
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "商品添加成功", "item": result, "total": total})
}

func (s *Server) adminUpdateShopItem(writer http.ResponseWriter, request *http.Request) {
	var item shopItemPayload
	if err := decodeBody(request, s.Config.BodyLimitBytes, &item); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "更新商品失败"})
		return
	}
	if item.ID == "" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "缺少商品ID"})
		return
	}
	points, ok := parseNonnegative(item.Points)
	if !ok {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "积分(points)不合法"})
		return
	}
	stock, err := optionalNonnegative(item.Stock)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "库存(stock)不合法"})
		return
	}
	amount := positiveOrDefault(item.Amount, 1)
	var current shopItemPayload
	var currentStock sql.NullInt64
	var createdAt string
	err = s.DB.QueryRowContext(request.Context(), "SELECT id,name,item_id,amount,points,stock,image,description,created_at FROM shop_items WHERE id=?", item.ID).
		Scan(&current.ID, &current.Name, &current.ItemID, &current.Amount, &current.Points, &currentStock, &current.Image, &current.Description, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "商品不存在"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "更新商品失败"})
		return
	}
	if item.Name == "" {
		item.Name = current.Name
	}
	if item.ItemID == "" {
		item.ItemID = current.ItemID
	}
	if item.Image == "" {
		item.Image = current.Image
	}
	image, validImage := normalizeShopImageForWrite(item.Image)
	if !validImage {
		if repaired, ok := normalizeLegacyShopImage(item.Image); ok {
			image = repaired
		} else {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": errInvalidShopImageReference.Error()})
			return
		}
	}
	if item.Description == "" {
		item.Description = current.Description
	}
	var stockValue any
	if stock != nil {
		stockValue = *stock
	}
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.DB.ExecContext(request.Context(), `UPDATE shop_items SET name=?,item_id=?,amount=?,points=?,stock=?,image=?,description=?,updated_at=? WHERE id=?`,
		item.Name, item.ItemID, amount, points, stockValue, image, item.Description, updatedAt, item.ID); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "更新商品失败"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "商品更新成功", "item": map[string]any{"id": item.ID, "name": item.Name, "itemId": item.ItemID, "amount": amount, "points": points, "stock": stockValue, "image": image, "description": item.Description, "createdAt": createdAt, "updatedAt": updatedAt}})
}

func (s *Server) adminDeleteShopItem(writer http.ResponseWriter, ctx context.Context, id string) {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM shop_items WHERE id=?", id)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "删除商品失败"})
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "商品不存在"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "商品删除成功"})
}

func (s *Server) purchaseItem(writer http.ResponseWriter, request *http.Request, sessionUser, requestedUser, itemID string) {
	if requestedUser == "" || !strings.EqualFold(requestedUser, sessionUser) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	tx, err := s.DB.BeginTx(request.Context(), nil)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	defer tx.Rollback()
	var userID int64
	var username string
	var points int64
	var status string
	if err := tx.QueryRowContext(request.Context(), `SELECT u.id,u.name,u.status,COALESCE(a.points,0)
		FROM users u LEFT JOIN sign_accounts a ON a.user_id=u.id WHERE u.name_key=?`, strings.ToLower(sessionUser)).
		Scan(&userID, &username, &status, &points); err != nil || status != "active" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "会话无效或已过期，请重新登录"})
		return
	}
	var name, gameItem string
	var amount, cost int64
	var stock sql.NullInt64
	var image, description, createdAt string
	err = tx.QueryRowContext(request.Context(), "SELECT name,item_id,amount,points,stock,image,description,created_at FROM shop_items WHERE id=?", itemID).
		Scan(&name, &gameItem, &amount, &cost, &stock, &image, &description, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "商品不存在"})
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	if stock.Valid && stock.Int64 <= 0 {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "商品已售罄"})
		return
	}
	if points < cost {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "积分不足"})
		return
	}
	code, err := newCouponCode()
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	now := time.Now()
	var currentPoints int64
	if err := tx.QueryRowContext(request.Context(), "SELECT points FROM sign_accounts WHERE user_id=?", userID).Scan(&currentPoints); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	if currentPoints < cost {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "积分不足"})
		return
	}
	if _, err := tx.ExecContext(request.Context(), "UPDATE sign_accounts SET points=points-?,updated_at=? WHERE user_id=? AND points>=?", cost, now.UnixMilli(), userID, cost); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	if stock.Valid {
		if _, err := tx.ExecContext(request.Context(), "UPDATE shop_items SET stock=stock-1 WHERE id=? AND stock>0", itemID); err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
			return
		}
	}
	expiresAt := now.Add(72 * time.Hour).UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(request.Context(), `INSERT INTO coupons(code,type,designated_player,expires_at,one_time_use,used,created_at,updated_at)
		VALUES(?,'item',?,?,1,0,?,?)`, code, username, expiresAt, now.UTC().Format(time.RFC3339Nano), now.UnixMilli())
	if err == nil {
		_, err = tx.ExecContext(request.Context(), "INSERT INTO coupon_items(coupon_code,position,item_id,amount) VALUES(?,0,?,?)", code, gameItem, amount)
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换失败"})
		return
	}
	if stock.Valid {
		stock.Int64--
	}
	item := map[string]any{"id": itemID, "name": name, "itemId": gameItem, "amount": amount, "points": cost, "stock": nil, "image": image, "description": description, "createdAt": createdAt}
	if stock.Valid {
		item["stock"] = stock.Int64
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "coupon": code, "item": item, "message": "兑换成功！有效期3天，请尽快使用"})
}

type couponItemPayload struct {
	ItemID string `json:"itemId"`
	Amount int64  `json:"amount"`
}

type couponPayload struct {
	Type             string              `json:"type"`
	Items            []couponItemPayload `json:"items"`
	ExpiresAt        string              `json:"expiresAt"`
	DesignatedPlayer string              `json:"designatedPlayer"`
	OneTimeUse       any                 `json:"oneTimeUse"`
}

func (s *Server) adminGenerateCoupon(writer http.ResponseWriter, request *http.Request) {
	var payload couponPayload
	if err := decodeBody(request, s.Config.BodyLimitBytes, &payload); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "生成兑换码失败"})
		return
	}
	expiry, err := time.Parse(time.RFC3339, payload.ExpiresAt)
	if err != nil {
		expiry, err = time.Parse(time.RFC3339Nano, payload.ExpiresAt)
	}
	if err != nil {
		var couponDate time.Time
		couponDate, err = time.Parse("2006-01-02", payload.ExpiresAt)
		if err == nil {
			location := time.FixedZone("configured", s.Config.SignTimezoneOffset*60*60)
			expiry = time.Date(couponDate.Year(), couponDate.Month(), couponDate.Day(), 23, 59, 59, int(time.Second-time.Nanosecond), location)
		}
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "expiresAt 不合法"})
		return
	}
	if payload.Type != "item" && payload.Type != "bundle" {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换码类型不合法"})
		return
	}
	if len(payload.Items) == 0 || (payload.Type == "item" && len(payload.Items) != 1) {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换码物品列表不合法"})
		return
	}
	for _, item := range payload.Items {
		if strings.TrimSpace(item.ItemID) == "" || item.Amount < 1 || item.Amount > int64(s.Config.MaxCouponItemAmount) {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换码物品数量或物品ID不合法"})
			return
		}
	}
	code, err := newCouponCode()
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "生成兑换码失败"})
		return
	}
	now := time.Now()
	oneTime := payload.OneTimeUse == true || payload.OneTimeUse == "true"
	tx, err := s.DB.BeginTx(request.Context(), nil)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "生成兑换码失败"})
		return
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(request.Context(), `INSERT INTO coupons(code,type,designated_player,expires_at,one_time_use,used,created_at,updated_at)
		VALUES(?,?,?,?,?,0,?,?)`, code, payload.Type, nullableText(payload.DesignatedPlayer), expiry.UTC().Format(time.RFC3339Nano), boolInt(oneTime), now.UTC().Format(time.RFC3339Nano), now.UnixMilli())
	for index, item := range payload.Items {
		if err != nil {
			break
		}
		_, err = tx.ExecContext(request.Context(), "INSERT INTO coupon_items(coupon_code,position,item_id,amount) VALUES(?,?,?,?)", code, index, item.ItemID, item.Amount)
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "生成兑换码失败"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "生成兑换码失败"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "couponCode": code, "message": "兑换码生成成功"})
}

func (s *Server) adminGetCoupons(writer http.ResponseWriter, ctx context.Context) {
	rows, err := s.DB.QueryContext(ctx, `SELECT code,type,designated_player,expires_at,one_time_use,used,created_at FROM coupons ORDER BY rowid`)
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
		return
	}
	defer rows.Close()
	type couponRow struct {
		code, kind, expires, created string
		designated                   sql.NullString
		oneTime, used                int
	}
	var storedCoupons []couponRow
	for rows.Next() {
		var coupon couponRow
		if err := rows.Scan(&coupon.code, &coupon.kind, &coupon.designated, &coupon.expires, &coupon.oneTime, &coupon.used, &coupon.created); err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
			return
		}
		storedCoupons = append(storedCoupons, coupon)
	}
	if err := rows.Err(); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
		return
	}
	// 先释放外层查询占用的连接，避免单连接 SQLite 的嵌套查询永久等待。
	if err := rows.Close(); err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
		return
	}
	coupons := make([]map[string]any, 0, len(storedCoupons))
	for _, coupon := range storedCoupons {
		code, kind, expires, created := coupon.code, coupon.kind, coupon.expires, coupon.created
		designated, oneTime, used := coupon.designated, coupon.oneTime, coupon.used
		itemRows, err := s.DB.QueryContext(ctx, "SELECT item_id,amount FROM coupon_items WHERE coupon_code=? ORDER BY position", code)
		if err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
			return
		}
		items := make([]map[string]any, 0)
		for itemRows.Next() {
			var itemID string
			var amount int64
			if err := itemRows.Scan(&itemID, &amount); err != nil {
				itemRows.Close()
				writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
				return
			}
			items = append(items, map[string]any{"itemId": itemID, "amount": amount})
		}
		itemRows.Close()
		usageRows, err := s.DB.QueryContext(ctx, "SELECT username FROM coupon_usages WHERE coupon_code=? ORDER BY used_at", code)
		if err != nil {
			writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
			return
		}
		usedBy := make([]string, 0)
		for usageRows.Next() {
			var username string
			if err := usageRows.Scan(&username); err != nil {
				usageRows.Close()
				writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "获取兑换码失败"})
				return
			}
			usedBy = append(usedBy, username)
		}
		usageRows.Close()
		coupons = append(coupons, map[string]any{"code": code, "type": kind, "items": items, "designatedPlayer": nullStringValue(designated), "expiresAt": expires, "oneTimeUse": oneTime == 1, "createdAt": created, "used": used == 1, "usedBy": usedBy})
	}
	writer.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Expires", "0")
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "coupons": coupons})
}

func (s *Server) adminDeleteCoupon(writer http.ResponseWriter, ctx context.Context, code string) {
	result, err := s.DB.ExecContext(ctx, "DELETE FROM coupons WHERE code=? COLLATE NOCASE", strings.ToUpper(code))
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "删除兑换码失败"})
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		writeJSON(writer, http.StatusOK, map[string]any{"success": false, "message": "兑换码不存在"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"success": true, "message": "兑换码删除成功"})
}

func parseNonnegative(value any) (int64, bool) {
	text := strings.TrimSpace(paramString(value))
	if text == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	return parsed, err == nil && parsed >= 0
}

func optionalNonnegative(value any) (*int64, error) {
	if value == nil || strings.TrimSpace(paramString(value)) == "" {
		return nil, nil
	}
	parsed, ok := parseNonnegative(value)
	if !ok {
		return nil, errors.New("数值不合法")
	}
	return &parsed, nil
}

func positiveOrDefault(value any, fallback int64) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(paramString(value)), 10, 64)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func newCouponCode() (string, error) {
	bytes := make([]byte, 5)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(bytes)), nil
}

func bcryptCompare(password, encoded string) error {
	return bcrypt.CompareHashAndPassword([]byte(encoded), []byte(password))
}

func bcryptHash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// 此处使用薄函数便于把密码算法与业务 handler 分开测试。
func totpCounter(now time.Time) uint64 { return uint64(now.Unix() / 30) }

var _ = totpCounter

func calculateTOTP(secret []byte, counter uint64) string {
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], counter)
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(message[:])
	sum := mac.Sum(nil)
	index := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[index])&0x7f)<<24 | uint32(sum[index+1])<<16 | uint32(sum[index+2])<<8 | uint32(sum[index+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

var _ = calculateTOTP
