package plugin

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestSQLiteWhitelistAuthorizationAndLogoutAreTransactional(t *testing.T) {
	db := newTestDatabase(t)
	insertTestUser(t, db, "Legacy 玩家", "active")
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	service := NewSQLiteService(db)
	service.Now = func() time.Time { return now }

	if err := service.AuthorizePlayer(context.Background(), "survival-1", "legacy 玩家", time.Minute); err != nil {
		t.Fatal(err)
	}
	result, err := service.Check(context.Background(), "survival-1", "LEGACY 玩家")
	if err != nil || !result.Allowed || result.Reason != "authorized" {
		t.Fatalf("authorized player was denied: result=%+v err=%v", result, err)
	}
	if result.LoginAttemptID == "" || result.ExpiresAt != now.Add(LoginAttemptTTL).UnixMilli() {
		t.Fatalf("pre-login did not return a bounded attempt: result=%+v", result)
	}
	var onlineTime int64
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE name_key = ?", "legacy 玩家").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != 0 {
		t.Fatalf("pre-login must not affect online time: %d", onlineTime)
	}
	joined, err := service.Join(context.Background(), "survival-1", "legacy 玩家", result.LoginAttemptID)
	if err != nil || !joined.Confirmed || joined.AlreadyOpen {
		t.Fatalf("first join confirmation should open the session: result=%+v err=%v", joined, err)
	}
	joined, err = service.Join(context.Background(), "survival-1", "legacy 玩家", result.LoginAttemptID)
	if err != nil || !joined.Confirmed || !joined.AlreadyOpen {
		t.Fatalf("join confirmation retry should be idempotent: result=%+v err=%v", joined, err)
	}
	now = now.Add(10 * time.Second)
	logout, err := service.Logout(context.Background(), "survival-1", "legacy 玩家")
	if err != nil || logout.AlreadyClosed {
		t.Fatalf("first logout should close an online session: result=%+v err=%v", logout, err)
	}
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE name_key = ?", "legacy 玩家").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != 10_000 {
		t.Fatalf("online time=%d, want 10000ms", onlineTime)
	}
	var lastLogin string
	if err := db.QueryRow("SELECT last_login FROM users WHERE name_key = ?", "legacy 玩家").Scan(&lastLogin); err != nil {
		t.Fatal(err)
	}
	if lastLogin != "2026-10-24 08:00:10" {
		t.Fatalf("插件入服记录应使用 GMT+8：%q", lastLogin)
	}
	logout, err = service.Logout(context.Background(), "survival-1", "legacy 玩家")
	if err != nil || !logout.AlreadyClosed {
		t.Fatalf("repeated logout should be idempotent: result=%+v err=%v", logout, err)
	}
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE name_key = ?", "legacy 玩家").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != 10_000 {
		t.Fatalf("repeated logout changed online time to %dms", onlineTime)
	}
}

func TestSQLiteSessionLeaseCapsTimeAndCanResumeOnHeartbeat(t *testing.T) {
	db := newTestDatabase(t)
	insertTestUser(t, db, "LeasePlayer", "active")
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	service := NewSQLiteService(db)
	service.Now = func() time.Time { return now }
	if err := service.AuthorizePlayer(context.Background(), "survival-1", "LeasePlayer", time.Minute); err != nil {
		t.Fatal(err)
	}
	check, err := service.Check(context.Background(), "survival-1", "LeasePlayer")
	if err != nil || !check.Allowed {
		t.Fatalf("pre-login failed: result=%+v err=%v", check, err)
	}
	if _, err := service.Join(context.Background(), "survival-1", "LeasePlayer", check.LoginAttemptID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := service.ExpireSessionLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	var onlineTime int64
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE name_key = ?", "leaseplayer").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != SessionLeaseTTL.Milliseconds() {
		t.Fatalf("expired lease counted %dms, want capped %dms", onlineTime, SessionLeaseTTL.Milliseconds())
	}
	heartbeat, err := service.Heartbeat(context.Background(), "survival-1", []string{"LeasePlayer"})
	if err != nil || len(heartbeat.RejectedPlayers) != 0 {
		t.Fatalf("heartbeat should resume a known active player: result=%+v err=%v", heartbeat, err)
	}
	now = now.Add(5 * time.Second)
	if _, err := service.Logout(context.Background(), "survival-1", "LeasePlayer"); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE name_key = ?", "leaseplayer").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != SessionLeaseTTL.Milliseconds()+5_000 {
		t.Fatalf("resumed session count=%dms, want %dms", onlineTime, SessionLeaseTTL.Milliseconds()+5_000)
	}
}

func TestHeartbeatRejectsOnlinePlayersWhoseAccountOrSessionWasDeleted(t *testing.T) {
	db := newTestDatabase(t)
	insertTestUser(t, db, "RemovedPlayer", "active")
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service := NewSQLiteService(db)
	service.Now = func() time.Time { return now }
	if err := service.AuthorizePlayer(context.Background(), "survival-1", "RemovedPlayer", time.Minute); err != nil {
		t.Fatal(err)
	}
	check, err := service.Check(context.Background(), "survival-1", "RemovedPlayer")
	if err != nil || !check.Allowed {
		t.Fatalf("test player authorization failed: result=%+v err=%v", check, err)
	}
	if _, err := service.Join(context.Background(), "survival-1", "RemovedPlayer", check.LoginAttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), "DELETE FROM users WHERE name_key = ?", "removedplayer"); err != nil {
		t.Fatal(err)
	}

	result, err := service.Heartbeat(context.Background(), "survival-1", []string{"RemovedPlayer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RejectedPlayers) != 1 || result.RejectedPlayers[0] != "RemovedPlayer" {
		t.Fatalf("deleted account must be rejected by heartbeat: %+v", result.RejectedPlayers)
	}
}

func TestSQLiteJoinRejectsExpiredAttemptWithoutStartingSession(t *testing.T) {
	db := newTestDatabase(t)
	insertTestUser(t, db, "ExpiryPlayer", "active")
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	service := NewSQLiteService(db)
	service.Now = func() time.Time { return now }
	if err := service.AuthorizePlayer(context.Background(), "survival-1", "ExpiryPlayer", 5*time.Minute); err != nil {
		t.Fatal(err)
	}
	check, err := service.Check(context.Background(), "survival-1", "ExpiryPlayer")
	if err != nil || !check.Allowed {
		t.Fatalf("pre-login failed: result=%+v err=%v", check, err)
	}
	now = now.Add(LoginAttemptTTL + time.Millisecond)
	join, err := service.Join(context.Background(), "survival-1", "ExpiryPlayer", check.LoginAttemptID)
	if err != nil || join.Confirmed || join.Reason != "login_attempt_invalid" {
		t.Fatalf("expired attempt must be rejected: result=%+v err=%v", join, err)
	}
	var loginAt sql.NullInt64
	if err := db.QueryRow(`
		SELECT login_at FROM plugin_online_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.server_id='survival-1' AND u.name_key='expiryplayer'`).Scan(&loginAt); err != nil {
		t.Fatal(err)
	}
	if loginAt.Valid {
		t.Fatalf("expired attempt must not start a session: %d", loginAt.Int64)
	}
	logout, err := service.Logout(context.Background(), "survival-1", "ExpiryPlayer")
	if err != nil || !logout.AlreadyClosed {
		t.Fatalf("unconfirmed login must not open a session: result=%+v err=%v", logout, err)
	}
	if _, err := service.Heartbeat(context.Background(), "survival-1", []string{"ExpiryPlayer"}); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := db.QueryRow(`
		SELECT count(*) FROM plugin_online_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.server_id='survival-1' AND u.name_key='expiryplayer'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("heartbeat must not recreate a closed session: %d", sessions)
	}
	var onlineTime int64
	if err := db.QueryRow("SELECT online_time_ms FROM users WHERE name_key='expiryplayer'").Scan(&onlineTime); err != nil {
		t.Fatal(err)
	}
	if onlineTime != 0 {
		t.Fatalf("unconfirmed login must not accrue online time: %d", onlineTime)
	}
}

func TestSQLiteWhitelistCheckIsFailClosedForAccountStateAndExpiry(t *testing.T) {
	db := newTestDatabase(t)
	insertTestUser(t, db, "InactivePlayer", "active")
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	service := NewSQLiteService(db)
	service.Now = func() time.Time { return now }
	if err := service.AuthorizePlayer(context.Background(), "survival-1", "InactivePlayer", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE users SET status = 'inactive' WHERE name_key = ?", "inactiveplayer"); err != nil {
		t.Fatal(err)
	}
	result, err := service.Check(context.Background(), "survival-1", "InactivePlayer")
	if err != nil || result.Allowed || result.Reason != "account_inactive" {
		t.Fatalf("inactive account must fail closed: result=%+v err=%v", result, err)
	}
	if _, err := db.Exec("UPDATE users SET status = 'active' WHERE name_key = ?", "inactiveplayer"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	result, err = service.Check(context.Background(), "survival-1", "InactivePlayer")
	if err != nil || result.Allowed || result.Reason != "not_authorized" {
		t.Fatalf("expired authorization must fail closed: result=%+v err=%v", result, err)
	}
}

func TestSQLiteCouponRequestIsIdempotentAndBoundToPayload(t *testing.T) {
	db := newTestDatabase(t)
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
		INSERT INTO coupons(code,type,designated_player,expires_at,one_time_use,used,created_at,updated_at)
		VALUES('GIFT-1','item',NULL,?,1,0,?,?)`,
		now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO coupon_items(coupon_code,position,item_id,amount) VALUES('GIFT-1',0,'LegacyItem',2)"); err != nil {
		t.Fatal(err)
	}
	service := NewSQLiteService(db)
	service.Now = func() time.Time { return now }

	first, err := service.UseCoupon(context.Background(), "survival-1", "PlayerOne", "gift-1", "request-1")
	if err != nil || !first.Accepted || len(first.Items) != 1 || first.Items[0].ItemID != "LegacyItem" || first.Items[0].Amount != 2 {
		t.Fatalf("first redemption failed: result=%+v err=%v", first, err)
	}
	var usageCount, requestCount int
	if err := db.QueryRow("SELECT count(*) FROM coupon_usages WHERE coupon_code = 'GIFT-1'").Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM plugin_coupon_requests WHERE server_id = 'survival-1' AND request_id = 'request-1'").Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if usageCount != 1 || requestCount != 1 {
		t.Fatalf("usage count=%d request count=%d", usageCount, requestCount)
	}

	retry, err := service.UseCoupon(context.Background(), "survival-1", "PlayerOne", "GIFT-1", "request-1")
	if err != nil || !retry.Accepted || retry.Items[0].ItemID != "LegacyItem" {
		t.Fatalf("retry should replay original response: result=%+v err=%v", retry, err)
	}
	conflict, err := service.UseCoupon(context.Background(), "survival-1", "AnotherPlayer", "GIFT-1", "request-1")
	if err != nil || conflict.Accepted || conflict.HTTPStatus != 409 || conflict.Message != "request_id_conflict" {
		t.Fatalf("reused ID with different payload should conflict: result=%+v err=%v", conflict, err)
	}
	duplicate, err := service.UseCoupon(context.Background(), "survival-1", "PlayerOne", "GIFT-1", "request-2")
	if err != nil || duplicate.Accepted || duplicate.Message != "兑换码已被使用" {
		t.Fatalf("new ID should see already-used coupon: result=%+v err=%v", duplicate, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM coupon_usages WHERE coupon_code = 'GIFT-1'").Scan(&usageCount); err != nil {
		t.Fatal(err)
	}
	if usageCount != 1 {
		t.Fatalf("duplicate redemption added an extra usage: %d", usageCount)
	}
}

func insertTestUser(t *testing.T, db *sql.DB, name, status string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO users(name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,0,0,0)`,
		name, strings.ToLower(name), "123e4567-e89b-12d3-a456-426614174000",
		"$2b$12$placeholder", name+"@example.invalid", strings.ToLower(name)+"@example.invalid", status, "2025-01-01")
	if err != nil {
		t.Fatal(err)
	}
}
