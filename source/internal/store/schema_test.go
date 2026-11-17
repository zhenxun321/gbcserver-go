package store

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateFromInitialNormalizedSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)",
		"INSERT INTO schema_migrations(version,applied_at) VALUES('0001_normalized',1)",
		"CREATE TABLE users(id INTEGER PRIMARY KEY)",
		"CREATE TABLE plugin_online_sessions(server_id TEXT NOT NULL,user_id INTEGER NOT NULL,authorized_until INTEGER NOT NULL,login_at INTEGER,updated_at INTEGER NOT NULL,PRIMARY KEY(server_id,user_id))",
		"INSERT INTO plugin_online_sessions(server_id,user_id,authorized_until,login_at,updated_at) VALUES('survival-1',1,9999999999999,1234,1234)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := db.QueryRow("SELECT version FROM schema_migrations ORDER BY applied_at DESC,rowid DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version=%q, want %q", version, SchemaVersion)
	}
	columns := map[string]bool{}
	rows, err := db.Query("PRAGMA table_info(plugin_online_sessions)")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&ordinal, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !columns["last_seen_at"] || !columns["lease_expires_at"] {
		t.Fatalf("session lease columns missing: %#v", columns)
	}
	var legacyLoginAt sql.NullInt64
	if err := db.QueryRow("SELECT login_at FROM plugin_online_sessions WHERE server_id='survival-1'").Scan(&legacyLoginAt); err != nil {
		t.Fatal(err)
	}
	if legacyLoginAt.Valid {
		t.Fatalf("early prototype session must not be treated as confirmed online: %d", legacyLoginAt.Int64)
	}
	var attemptsTable int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_login_attempts'").Scan(&attemptsTable); err != nil {
		t.Fatal(err)
	}
	if attemptsTable != 1 {
		t.Fatal("login-attempt table was not created")
	}
}

func TestMigrateFrom0003AddsStatusMonitorTables(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"CREATE TABLE schema_migrations(version TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)",
		"INSERT INTO schema_migrations(version,applied_at) VALUES('0003_app_config',1)",
		"CREATE TABLE app_config(key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at INTEGER NOT NULL)",
		"INSERT INTO app_config(key,value,updated_at) VALUES('siteName','嘎嘣脆综合管理系统',1)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"status_samples", "status_alert_state"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("迁移后缺少表 %s", table)
		}
	}
	var value string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='siteName'").Scan(&value); err != nil || value != "嘎嘣脆综合管理系统" {
		t.Fatalf("迁移不应改动管理员配置: value=%q err=%v", value, err)
	}
}
