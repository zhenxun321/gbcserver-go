package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLiteService 将白名单在线状态和兑换码事务落在规范化 SQLite 中。
type SQLiteService struct {
	DB                       *sql.DB
	Now                      func() time.Time
	LoginTimezoneOffsetHours int
}

const (
	LoginAttemptTTL = 120 * time.Second
	SessionLeaseTTL = 90 * time.Second
)

// NewSQLiteService 创建 SQLite 插件领域服务。
func NewSQLiteService(db *sql.DB) *SQLiteService {
	return &SQLiteService{DB: db, Now: time.Now, LoginTimezoneOffsetHours: 8}
}

// AuthorizePlayer 由经过网页会话验证的 Web API 调用，创建或延长入服授权。
func (s *SQLiteService) AuthorizePlayer(ctx context.Context, serverID, username string, duration time.Duration) error {
	if s == nil || s.DB == nil {
		return errors.New("plugin SQLite service 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	username = strings.TrimSpace(username)
	if serverID == "" || username == "" {
		return errors.New("server_id 和用户名不能为空")
	}
	if duration <= 0 {
		return errors.New("授权时长必须大于零")
	}
	now := s.now()
	until := now.Add(duration)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID int64
	var status string
	err = tx.QueryRowContext(ctx,
		"SELECT id, status FROM users WHERE name_key = ? LIMIT 1",
		strings.ToLower(username)).Scan(&userID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("用户不存在")
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return errors.New("账号当前不可登录")
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO plugin_online_sessions(server_id,user_id,authorized_until,login_at,updated_at)
		VALUES(?,?,?,NULL,?)
		ON CONFLICT(server_id,user_id) DO UPDATE SET
			authorized_until = excluded.authorized_until,
			updated_at = excluded.updated_at`,
		serverID, userID, until.UnixMilli(), now.UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Check 验证网页授权并签发短期尝试 ID，不开始在线时长计时。
func (s *SQLiteService) Check(ctx context.Context, serverID, username string) (CheckResult, error) {
	if s == nil || s.DB == nil {
		return CheckResult{}, errors.New("plugin SQLite service 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	username = strings.TrimSpace(username)
	if serverID == "" || username == "" {
		return CheckResult{Allowed: false, Reason: "not_authorized"}, nil
	}
	nowTime := s.now()
	now := nowTime.UnixMilli()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CheckResult{}, err
	}
	defer tx.Rollback()
	var userID int64
	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT u.id, u.status
		FROM users u
		JOIN plugin_online_sessions s ON s.user_id = u.id
		WHERE u.name_key = ? AND s.server_id = ? AND s.authorized_until > ?
		LIMIT 1`, strings.ToLower(username), serverID, now).Scan(&userID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return CheckResult{}, err
		}
		return CheckResult{Allowed: false, Reason: "not_authorized"}, nil
	}
	if err != nil {
		return CheckResult{}, err
	}
	if status != "active" {
		return CheckResult{Allowed: false, Reason: accountReason(status)}, nil
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM plugin_login_attempts WHERE expires_at <= ? OR (server_id = ? AND user_id = ? AND confirmed_at IS NULL)",
		now, serverID, userID); err != nil {
		return CheckResult{}, err
	}
	secret := make([]byte, 18)
	if _, err := rand.Read(secret); err != nil {
		return CheckResult{}, err
	}
	attemptID := base64.RawURLEncoding.EncodeToString(secret)
	expiresAt := nowTime.Add(LoginAttemptTTL)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO plugin_login_attempts(attempt_id,server_id,user_id,expires_at,confirmed_at,created_at)
		VALUES(?,?,?,?,NULL,?)`, attemptID, serverID, userID, expiresAt.UnixMilli(), now); err != nil {
		return CheckResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CheckResult{}, err
	}
	return CheckResult{Allowed: true, Reason: "authorized", LoginAttemptID: attemptID, ExpiresAt: expiresAt.UnixMilli()}, nil
}

// Join 消费预登录尝试并在实际入服后才开始计时；同一请求重放是幂等的。
func (s *SQLiteService) Join(ctx context.Context, serverID, username, attemptID string) (JoinResult, error) {
	if s == nil || s.DB == nil {
		return JoinResult{}, errors.New("plugin SQLite service 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	username = strings.TrimSpace(username)
	attemptID = strings.TrimSpace(attemptID)
	if serverID == "" || username == "" || attemptID == "" {
		return JoinResult{Confirmed: false, Reason: "login_attempt_invalid"}, nil
	}
	nowTime := s.now()
	now := nowTime.UnixMilli()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return JoinResult{}, err
	}
	defer tx.Rollback()
	var userID int64
	var status string
	var expiresAt int64
	var confirmedAt sql.NullInt64
	var authorizedUntil int64
	err = tx.QueryRowContext(ctx, `
		SELECT a.user_id,u.status,a.expires_at,a.confirmed_at,s.authorized_until
		FROM plugin_login_attempts a
		JOIN users u ON u.id = a.user_id
		JOIN plugin_online_sessions s ON s.user_id = u.id AND s.server_id = a.server_id
		WHERE a.attempt_id = ? AND a.server_id = ? AND u.name_key = ?
		LIMIT 1`, attemptID, serverID, strings.ToLower(username)).
		Scan(&userID, &status, &expiresAt, &confirmedAt, &authorizedUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return JoinResult{Confirmed: false, Reason: "login_attempt_invalid"}, nil
	}
	if err != nil {
		return JoinResult{}, err
	}
	if status != "active" {
		return JoinResult{Confirmed: false, Reason: accountReason(status)}, nil
	}
	if expiresAt <= now || authorizedUntil <= now {
		return JoinResult{Confirmed: false, Reason: "login_attempt_invalid"}, nil
	}
	if confirmedAt.Valid {
		var loginAt, leaseExpiresAt sql.NullInt64
		err := tx.QueryRowContext(ctx,
			"SELECT login_at,lease_expires_at FROM plugin_online_sessions WHERE server_id = ? AND user_id = ?",
			serverID, userID).Scan(&loginAt, &leaseExpiresAt)
		if err == nil {
			if !loginAt.Valid || (leaseExpiresAt.Valid && leaseExpiresAt.Int64 <= now) {
				return JoinResult{Confirmed: false, Reason: "login_attempt_invalid"}, nil
			}
			if err := tx.Commit(); err != nil {
				return JoinResult{}, err
			}
			return JoinResult{Confirmed: true, AlreadyOpen: true, Reason: "already_open"}, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return JoinResult{}, err
		}
		return JoinResult{Confirmed: false, Reason: "login_attempt_invalid"}, nil
	}
	var previousLoginAt, previousLeaseExpiry sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT login_at,lease_expires_at FROM plugin_online_sessions
		WHERE server_id = ? AND user_id = ?`, serverID, userID).
		Scan(&previousLoginAt, &previousLeaseExpiry)
	if err != nil {
		return JoinResult{}, err
	}
	if previousLoginAt.Valid {
		endAt := now
		if previousLeaseExpiry.Valid && previousLeaseExpiry.Int64 < endAt {
			endAt = previousLeaseExpiry.Int64
		}
		if endAt > previousLoginAt.Int64 {
			if err := s.accrueOnlineTime(ctx, tx, userID, previousLoginAt.Int64, endAt); err != nil {
				return JoinResult{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE plugin_login_attempts SET confirmed_at = ?
		WHERE attempt_id = ? AND confirmed_at IS NULL`, now, attemptID); err != nil {
		return JoinResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE plugin_online_sessions
		SET login_at = ?,last_seen_at = ?,lease_expires_at = ?,updated_at = ?
		WHERE server_id = ? AND user_id = ?`, now, now, nowTime.Add(SessionLeaseTTL).UnixMilli(), now, serverID, userID); err != nil {
		return JoinResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return JoinResult{}, err
	}
	return JoinResult{Confirmed: true, AlreadyOpen: false, Reason: "joined"}, nil
}

// Logout 累计到明确退出时间或最近租约边界；无记录与重复退出均安全返回。
func (s *SQLiteService) Logout(ctx context.Context, serverID, username string) (LogoutResult, error) {
	if s == nil || s.DB == nil {
		return LogoutResult{}, errors.New("plugin SQLite service 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	username = strings.TrimSpace(username)
	if serverID == "" || username == "" {
		return LogoutResult{AlreadyClosed: true}, nil
	}
	now := s.now()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return LogoutResult{}, err
	}
	defer tx.Rollback()
	var userID int64
	var loginAt sql.NullInt64
	var leaseExpiresAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `
		SELECT u.id, s.login_at,s.lease_expires_at
		FROM users u
		JOIN plugin_online_sessions s ON s.user_id = u.id
		WHERE u.name_key = ? AND s.server_id = ?
		LIMIT 1`, strings.ToLower(username), serverID).Scan(&userID, &loginAt, &leaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return LogoutResult{}, err
		}
		return LogoutResult{AlreadyClosed: true}, nil
	}
	if err != nil {
		return LogoutResult{}, err
	}
	wasOnline := loginAt.Valid
	if wasOnline {
		endAt := now.UnixMilli()
		if leaseExpiresAt.Valid && leaseExpiresAt.Int64 < endAt {
			endAt = leaseExpiresAt.Int64
		}
		if endAt > loginAt.Int64 {
			if err := s.accrueOnlineTime(ctx, tx, userID, loginAt.Int64, endAt); err != nil {
				return LogoutResult{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_online_sessions WHERE server_id = ? AND user_id = ?", serverID, userID); err != nil {
		return LogoutResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return LogoutResult{}, err
	}
	return LogoutResult{AlreadyClosed: !wasOnline, AuthorizationClosed: true}, nil
}

// Heartbeat 续租服务器仍确认在线的玩家，并拒绝无效账号或会话。 / Heartbeat renews active sessions and rejects invalid accounts or sessions.
func (s *SQLiteService) Heartbeat(ctx context.Context, serverID string, usernames []string) (HeartbeatResult, error) {
	if s == nil || s.DB == nil {
		return HeartbeatResult{}, errors.New("plugin SQLite service 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return HeartbeatResult{}, errors.New("server_id 不能为空")
	}
	nowTime := s.now()
	now := nowTime.UnixMilli()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return HeartbeatResult{}, err
	}
	defer tx.Rollback()
	result := HeartbeatResult{RejectedPlayers: []string{}}
	seen := make(map[string]struct{}, len(usernames))
	for _, username := range usernames {
		username = strings.TrimSpace(username)
		key := strings.ToLower(username)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		var userID int64
		var status string
		var loginAt, lastSeenAt, leaseExpiresAt sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT u.id,u.status,s.login_at,s.last_seen_at,s.lease_expires_at
			FROM users u JOIN plugin_online_sessions s ON s.user_id = u.id
			WHERE u.name_key = ? AND s.server_id = ? LIMIT 1`, key, serverID).
			Scan(&userID, &status, &loginAt, &lastSeenAt, &leaseExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			result.RejectedPlayers = append(result.RejectedPlayers, username)
			continue
		}
		if err != nil {
			return HeartbeatResult{}, err
		}
		if status != "active" {
			result.RejectedPlayers = append(result.RejectedPlayers, username)
			continue
		}
		// 没有历史入服确认的授权记录不能只靠心跳开启计时。
		if !lastSeenAt.Valid || !leaseExpiresAt.Valid {
			continue
		}
		newLoginAt := now
		if loginAt.Valid && (!leaseExpiresAt.Valid || leaseExpiresAt.Int64 > now) {
			newLoginAt = loginAt.Int64
		} else if loginAt.Valid && leaseExpiresAt.Valid && leaseExpiresAt.Int64 > loginAt.Int64 {
			if err := s.accrueOnlineTime(ctx, tx, userID, loginAt.Int64, leaseExpiresAt.Int64); err != nil {
				return HeartbeatResult{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE plugin_online_sessions
			SET login_at = ?,last_seen_at = ?,lease_expires_at = ?,updated_at = ?
			WHERE server_id = ? AND user_id = ?`,
			newLoginAt, now, nowTime.Add(SessionLeaseTTL).UnixMilli(), now, serverID, userID); err != nil {
			return HeartbeatResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return HeartbeatResult{}, err
	}
	return result, nil
}

// ExpireSessionLeases 在超出租约后封闭计时段，保留授权记录供下次心跳恢复。
func (s *SQLiteService) ExpireSessionLeases(ctx context.Context) error {
	if s == nil || s.DB == nil {
		return errors.New("plugin SQLite service 未初始化")
	}
	now := s.now().UnixMilli()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_login_attempts WHERE expires_at <= ?", now); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT server_id,user_id,login_at,lease_expires_at
		FROM plugin_online_sessions
		WHERE login_at IS NOT NULL AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?`, now)
	if err != nil {
		return err
	}
	type expiredSession struct {
		serverID string
		userID   int64
		loginAt  int64
		leaseAt  int64
	}
	expired := []expiredSession{}
	for rows.Next() {
		var session expiredSession
		if err := rows.Scan(&session.serverID, &session.userID, &session.loginAt, &session.leaseAt); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, session)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, session := range expired {
		if session.leaseAt > session.loginAt {
			if err := s.accrueOnlineTime(ctx, tx, session.userID, session.loginAt, session.leaseAt); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE plugin_online_sessions SET login_at = NULL,updated_at = ?
			WHERE server_id = ? AND user_id = ? AND lease_expires_at <= ?`,
			now, session.serverID, session.userID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteService) accrueOnlineTime(ctx context.Context, tx *sql.Tx, userID, startAt, endAt int64) error {
	if endAt <= startAt {
		return nil
	}
	lastLogin := time.UnixMilli(endAt).UTC().Add(time.Duration(s.LoginTimezoneOffsetHours) * time.Hour).Format("2006-01-02 15:04:05")
	_, err := tx.ExecContext(ctx, `
		UPDATE users SET online_time_ms = online_time_ms + ?,last_login = ?,updated_at = ? WHERE id = ?`,
		endAt-startAt, lastLogin, endAt, userID)
	return err
}

func accountReason(status string) string {
	if status == "banned" {
		return "account_banned"
	}
	return "account_inactive"
}

// UseCoupon 以事务消费兑换码，并把成功或业务拒绝的响应绑定到幂等键。
func (s *SQLiteService) UseCoupon(ctx context.Context, serverID, username, code, requestID string) (CouponResult, error) {
	if s == nil || s.DB == nil {
		return CouponResult{}, errors.New("plugin SQLite service 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	username = strings.TrimSpace(username)
	code = strings.ToUpper(strings.TrimSpace(code))
	requestID = strings.TrimSpace(requestID)
	if serverID == "" || username == "" || code == "" || requestID == "" {
		return CouponResult{}, errors.New("兑换参数不完整")
	}
	payloadHash := couponPayloadHash(username, code)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return CouponResult{}, err
	}
	defer tx.Rollback()

	var storedHash, storedResponse string
	var storedStatus int
	err = tx.QueryRowContext(ctx, `
		SELECT payload_hash, status_code, response_json
		FROM plugin_coupon_requests WHERE server_id = ? AND request_id = ?`,
		serverID, requestID).Scan(&storedHash, &storedStatus, &storedResponse)
	if err == nil {
		if storedHash != payloadHash {
			return CouponResult{Accepted: false, Message: "request_id_conflict", HTTPStatus: 409}, nil
		}
		var snapshot couponResultSnapshot
		if err := json.Unmarshal([]byte(storedResponse), &snapshot); err != nil {
			return CouponResult{}, fmt.Errorf("读取兑换幂等响应失败: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return CouponResult{}, err
		}
		return CouponResult{Accepted: snapshot.Accepted, Message: snapshot.Message, Items: snapshot.Items, HTTPStatus: storedStatus}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CouponResult{}, err
	}

	result, err := s.consumeCoupon(ctx, tx, username, code)
	if err != nil {
		return CouponResult{}, err
	}
	result.HTTPStatus = 200
	snapshot := couponResultSnapshot{Accepted: result.Accepted, Message: result.Message, Items: result.Items}
	responseJSON, err := json.Marshal(snapshot)
	if err != nil {
		return CouponResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO plugin_coupon_requests(server_id,request_id,payload_hash,status_code,response_json,created_at)
		VALUES(?,?,?,?,?,?)`, serverID, requestID, payloadHash, result.HTTPStatus, string(responseJSON), s.now().UnixMilli()); err != nil {
		return CouponResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CouponResult{}, err
	}
	return result, nil
}

func (s *SQLiteService) consumeCoupon(ctx context.Context, tx *sql.Tx, username, code string) (CouponResult, error) {
	var designatedPlayer sql.NullString
	var expiresAt string
	var oneTimeUse, used bool
	err := tx.QueryRowContext(ctx, `
		SELECT designated_player, expires_at, one_time_use, used
		FROM coupons WHERE code = ? LIMIT 1`, code).
		Scan(&designatedPlayer, &expiresAt, &oneTimeUse, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return couponDenied("兑换码无效"), nil
	}
	if err != nil {
		return CouponResult{}, err
	}
	parsedExpiry, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return CouponResult{}, fmt.Errorf("兑换码过期时间损坏: %w", err)
	}
	if !parsedExpiry.After(s.now()) {
		return couponDenied("兑换码已过期"), nil
	}
	if designatedPlayer.Valid && !strings.EqualFold(strings.TrimSpace(designatedPlayer.String), username) {
		return couponDenied("该兑换码不属于您"), nil
	}
	if oneTimeUse && used {
		return couponDenied("兑换码已被使用"), nil
	}
	usernameKey := strings.ToLower(username)
	var existingUsage int
	err = tx.QueryRowContext(ctx,
		"SELECT 1 FROM coupon_usages WHERE coupon_code = ? AND username_key = ? LIMIT 1",
		code, usernameKey).Scan(&existingUsage)
	if err == nil {
		return couponDenied("您已使用过该兑换码"), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CouponResult{}, err
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT item_id, amount FROM coupon_items WHERE coupon_code = ? ORDER BY position", code)
	if err != nil {
		return CouponResult{}, err
	}
	items := make([]CouponReward, 0)
	for rows.Next() {
		var item CouponReward
		if err := rows.Scan(&item.ItemID, &item.Amount); err != nil {
			rows.Close()
			return CouponResult{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return CouponResult{}, err
	}
	if err := rows.Close(); err != nil {
		return CouponResult{}, err
	}
	if len(items) == 0 {
		return couponDenied("兑换码没有可发放物品"), nil
	}
	if oneTimeUse {
		if _, err := tx.ExecContext(ctx, "UPDATE coupons SET used = 1 WHERE code = ? AND used = 0", code); err != nil {
			return CouponResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO coupon_usages(coupon_code,username_key,username,used_at) VALUES(?,?,?,?)",
		code, usernameKey, username, s.now().UnixMilli()); err != nil {
		return CouponResult{}, err
	}
	return CouponResult{Accepted: true, Message: "兑换成功", Items: items}, nil
}

func couponDenied(message string) CouponResult {
	return CouponResult{Accepted: false, Message: message, Items: []CouponReward{}}
}

func couponPayloadHash(username, code string) string {
	canonical := strings.ToLower(strings.TrimSpace(username)) + "\x00" + strings.ToUpper(strings.TrimSpace(code))
	hash := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(hash[:])
}

func (s *SQLiteService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type couponResultSnapshot struct {
	Accepted bool           `json:"accepted"`
	Message  string         `json:"message"`
	Items    []CouponReward `json:"items"`
}

var _ WhitelistService = (*SQLiteService)(nil)
var _ CouponService = (*SQLiteService)(nil)
