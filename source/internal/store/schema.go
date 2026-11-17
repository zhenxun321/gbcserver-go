package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Schema 是 Go 版规范化 SQLite 的初始结构。
const Schema = `CREATE TABLE schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL UNIQUE,
    uuid TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    email TEXT NOT NULL,
    email_key TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('active', 'inactive', 'banned')),
    join_date TEXT NOT NULL,
    online_time_ms INTEGER NOT NULL DEFAULT 0 CHECK (online_time_ms >= 0),
    last_login TEXT,
    avatar_filename TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE sign_accounts (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    total_days INTEGER NOT NULL DEFAULT 0 CHECK (total_days >= 0),
    consecutive_days INTEGER NOT NULL DEFAULT 0 CHECK (consecutive_days >= 0),
    last_sign TEXT,
    points INTEGER NOT NULL DEFAULT 0 CHECK (points >= 0),
    updated_at INTEGER NOT NULL
);
CREATE TABLE sign_history (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    sign_date TEXT NOT NULL,
    signed_at INTEGER,
    PRIMARY KEY (user_id, sign_date)
);
CREATE TABLE shop_items (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    item_id TEXT NOT NULL,
    amount INTEGER NOT NULL CHECK (amount >= 1),
    points INTEGER NOT NULL CHECK (points >= 0),
    stock INTEGER CHECK (stock IS NULL OR stock >= 0),
    image TEXT NOT NULL,
    description TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT
);
CREATE TABLE coupons (
    code TEXT PRIMARY KEY COLLATE NOCASE,
    type TEXT NOT NULL CHECK (type IN ('item', 'bundle')),
    designated_player TEXT,
    expires_at TEXT NOT NULL,
    one_time_use INTEGER NOT NULL CHECK (one_time_use IN (0, 1)),
    used INTEGER NOT NULL DEFAULT 0 CHECK (used IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE coupon_items (
    coupon_code TEXT NOT NULL REFERENCES coupons(code) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    item_id TEXT NOT NULL,
    amount INTEGER NOT NULL CHECK (amount >= 1),
    PRIMARY KEY (coupon_code, position)
);
CREATE TABLE coupon_usages (
    coupon_code TEXT NOT NULL REFERENCES coupons(code) ON DELETE CASCADE,
    username_key TEXT NOT NULL,
    username TEXT NOT NULL,
    used_at INTEGER,
    PRIMARY KEY (coupon_code, username_key)
);
CREATE TABLE plugin_coupon_requests (
    server_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    status_code INTEGER NOT NULL,
    response_json TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (server_id, request_id)
);
CREATE TABLE plugin_online_sessions (
    server_id TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    authorized_until INTEGER NOT NULL,
    login_at INTEGER,
    last_seen_at INTEGER,
    lease_expires_at INTEGER,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (server_id, user_id)
);
CREATE TABLE plugin_login_attempts (
    attempt_id TEXT PRIMARY KEY,
    server_id TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    confirmed_at INTEGER,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_plugin_login_attempts_expiry ON plugin_login_attempts(expires_at);
CREATE INDEX idx_plugin_login_attempts_server_user ON plugin_login_attempts(server_id,user_id);
CREATE TABLE ports (
    key TEXT PRIMARY KEY,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    updated_at INTEGER NOT NULL
);
CREATE TABLE plugin_tokens (
    token_id TEXT PRIMARY KEY,
    server_id TEXT NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('whitelist', 'coupon.redeem')),
    token_hash TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER
);
CREATE INDEX idx_plugin_tokens_server_scope ON plugin_tokens(server_id, scope);
CREATE TABLE app_config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE status_samples (
    id INTEGER PRIMARY KEY,
    server_key TEXT NOT NULL,
    server_name TEXT NOT NULL,
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    online INTEGER NOT NULL CHECK (online IN (0, 1)),
    latency_ms INTEGER,
    status_json TEXT NOT NULL,
    checked_at INTEGER NOT NULL
);
CREATE INDEX idx_status_samples_server_time ON status_samples(server_key, checked_at DESC);
CREATE INDEX idx_status_samples_checked_at ON status_samples(checked_at);
CREATE TABLE status_alert_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    outage_started_at INTEGER,
    alert_sent_at INTEGER,
    updated_at INTEGER NOT NULL
);
CREATE INDEX idx_sign_history_date ON sign_history(sign_date);
`

const SchemaVersion = "0004_status_monitor"

// Migrate 创建新 schema，或把早期规范化 schema 升级到当前版本。
func Migrate(ctx context.Context, db *sql.DB) error {
	var migrationTable int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&migrationTable); err != nil {
		return err
	}
	if migrationTable != 0 {
		var currentVersion string
		err := db.QueryRowContext(ctx,
			"SELECT version FROM schema_migrations ORDER BY applied_at DESC, rowid DESC LIMIT 1").Scan(&currentVersion)
		if err != nil {
			return err
		}
		if currentVersion == SchemaVersion {
			return nil
		}
		if currentVersion == "0003_app_config" {
			return migrateFrom0003(ctx, db)
		}
		if currentVersion == "0002_plugin_login_attempts_leases" {
			if err := migrateFrom0002(ctx, db); err != nil {
				return err
			}
			return migrateFrom0003(ctx, db)
		}
		if currentVersion != "0001_normalized" {
			return fmt.Errorf("不支持的 SQLite schema 版本: %s", currentVersion)
		}
		if err := migrateFrom0001(ctx, db); err != nil {
			return err
		}
		return migrateFrom0003(ctx, db)
	}
	var existingTables int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&existingTables); err != nil {
		return err
	}
	if existingTables != 0 {
		return fmt.Errorf("SQLite 文件已有业务表但缺少 schema_migrations；拒绝初始化覆盖")
	}
	return createInitialSchema(ctx, db)
}

func createInitialSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, statement := range strings.Split(Schema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, applied_at) VALUES(?, unixepoch('subsec') * 1000)",
		SchemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateFrom0001(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		"ALTER TABLE plugin_online_sessions ADD COLUMN last_seen_at INTEGER",
		"ALTER TABLE plugin_online_sessions ADD COLUMN lease_expires_at INTEGER",
		"UPDATE plugin_online_sessions SET login_at = NULL,last_seen_at = NULL,lease_expires_at = NULL",
		`CREATE TABLE plugin_login_attempts (
			attempt_id TEXT PRIMARY KEY,
			server_id TEXT NOT NULL,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at INTEGER NOT NULL,
			confirmed_at INTEGER,
			created_at INTEGER NOT NULL
		)`,
		"CREATE INDEX idx_plugin_login_attempts_expiry ON plugin_login_attempts(expires_at)",
		"CREATE INDEX idx_plugin_login_attempts_server_user ON plugin_login_attempts(server_id,user_id)",
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "CREATE TABLE app_config (key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at INTEGER NOT NULL)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, unixepoch('subsec') * 1000)", "0003_app_config"); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateFrom0002(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE app_config (key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at INTEGER NOT NULL)"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, unixepoch('subsec') * 1000)", "0003_app_config"); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateFrom0003(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE status_samples (
			id INTEGER PRIMARY KEY,
			server_key TEXT NOT NULL,
			server_name TEXT NOT NULL,
			host TEXT NOT NULL,
			port INTEGER NOT NULL,
			online INTEGER NOT NULL CHECK (online IN (0, 1)),
			latency_ms INTEGER,
			status_json TEXT NOT NULL,
			checked_at INTEGER NOT NULL
		)`,
		"CREATE INDEX idx_status_samples_server_time ON status_samples(server_key, checked_at DESC)",
		"CREATE INDEX idx_status_samples_checked_at ON status_samples(checked_at)",
		`CREATE TABLE status_alert_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			outage_started_at INTEGER,
			alert_sent_at INTEGER,
			updated_at INTEGER NOT NULL
		)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(?, unixepoch('subsec') * 1000)", SchemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}
