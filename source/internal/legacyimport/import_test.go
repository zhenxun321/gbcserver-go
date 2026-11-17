package legacyimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNormalizeLegacyItemImage(t *testing.T) {
	for _, test := range []struct {
		input, expected string
	}{
		{"https://gbc.242774835.xyz/items/gbcpcb.png", "/items/gbcpcb.png"},
		{"items/gbcm2.jpg", "/items/gbcm2.jpg"},
		{"images/default_item.png", "images/default_item.png"},
	} {
		got, ok := normalizeLegacyItemImage(test.input)
		if !ok || got != test.expected {
			t.Fatalf("旧商品图片 %q 规范化为 %q/%v，预期 %q/true", test.input, got, ok, test.expected)
		}
	}
	if _, ok := normalizeLegacyItemImage("https://example.invalid/items/file.exe"); ok {
		t.Fatal("不支持的旧商品图片格式不应保留")
	}
}

func TestAnalyzeAndImportNormalizedSQLite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.sqlite")
	target := filepath.Join(dir, "normalized.sqlite")
	writeLegacyFixture(t, source)

	report, err := Analyze(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if !report.CanApply || len(report.Errors) != 0 {
		t.Fatalf("expected valid dry-run, got errors: %#v", report.Errors)
	}
	if report.SourceCounts["users"] != 1 || report.SourceCounts["sign_history"] != 1 {
		t.Fatalf("unexpected source counts: %#v", report.SourceCounts)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create target database: %v", err)
	}

	applied, err := Import(ctx, source, target)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.TargetCounts["users"] != 1 {
		t.Fatalf("unexpected import report: %#v", applied)
	}

	db, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var users, accounts, history, items, coupons, ports int
	for _, check := range []struct {
		query string
		dest  *int
	}{
		{"SELECT count(*) FROM users", &users},
		{"SELECT count(*) FROM sign_accounts", &accounts},
		{"SELECT count(*) FROM sign_history", &history},
		{"SELECT count(*) FROM shop_items", &items},
		{"SELECT count(*) FROM coupons", &coupons},
		{"SELECT count(*) FROM ports", &ports},
	} {
		if err := db.QueryRow(check.query).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
	}
	if users != 1 || accounts != 1 || history != 1 || items != 1 || coupons != 1 || ports != 1 {
		t.Fatalf("unexpected normalized row counts: users=%d accounts=%d history=%d items=%d coupons=%d ports=%d",
			users, accounts, history, items, coupons, ports)
	}
}

func TestAnalyzeReportsDuplicateEmailWithoutValues(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "legacy.sqlite")
	writeLegacyFixture(t, source)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := []map[string]any{
		{"name": "PlayerOne", "uuid": "123e4567-e89b-12d3-a456-426614174001", "passwd": "hash", "email": "player@example.invalid", "status": "active", "joinDate": "2025-01-01", "onlineTime": 0},
		{"name": "PlayerTwo", "uuid": "123e4567-e89b-12d3-a456-426614174002", "passwd": "hash", "email": "player@example.invalid", "status": "active", "joinDate": "2025-01-01", "onlineTime": 0},
	}
	raw, _ := json.Marshal(duplicate)
	if _, err := db.Exec("UPDATE kv_json SET v=? WHERE k='whitedata'", string(raw)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	report, err := Analyze(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if report.CanApply || len(report.Errors) == 0 {
		t.Fatalf("expected duplicate email to block import: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || contains(string(encoded), "player@example.invalid") {
		t.Fatal("dry-run report must not contain user-provided values")
	}
}

func TestAnalyzePreservesLegacyNamesAndItemIDs(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "legacy.sqlite")
	target := filepath.Join(t.TempDir(), "normalized.sqlite")
	writeLegacyFixture(t, source)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	var users []map[string]any
	var signs map[string]map[string]any
	var items []map[string]any
	var coupons []map[string]any
	for key, targetValue := range map[string]any{
		"whitedata": &users,
		"signData":  &signs,
		"shopItems": &items,
		"coupons":   &coupons,
	} {
		var raw string
		if err := db.QueryRow("SELECT v FROM kv_json WHERE k=?", key).Scan(&raw); err != nil {
			db.Close()
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), targetValue); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	users[0]["name"] = "历史玩家"
	delete(signs, "PlayerOne")
	signs["历史玩家"] = map[string]any{"totalDays": 1, "consecutiveDays": 1, "lastSign": "2025-01-01", "points": 15, "signHistory": map[string]bool{"2025-01-01": true}}
	items[0]["itemId"] = "LegacyItem"
	coupons[0]["items"] = []map[string]any{{"itemId": "LegacyItem", "amount": 1}}
	updates := map[string]any{"whitedata": users, "signData": signs, "shopItems": items, "coupons": coupons}
	for key, value := range updates {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			db.Close()
			t.Fatal(marshalErr)
		}
		if _, err := db.Exec("UPDATE kv_json SET v=? WHERE k=?", string(raw), key); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := Analyze(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if !report.CanApply || len(report.Errors) != 0 {
		t.Fatalf("legacy values should be preserved with warnings: %#v", report.Errors)
	}
	if !hasIssue(report.Warnings, "legacy_username_preserved") || !hasIssue(report.Warnings, "legacy_item_id_preserved") {
		t.Fatalf("expected preservation warnings: %#v", report.Warnings)
	}
	if _, err := Import(ctx, source, target); err != nil {
		t.Fatal(err)
	}
}

func TestPublishNoReplacePreservesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	tempPath := filepath.Join(dir, "staged.sqlite")
	targetPath := filepath.Join(dir, "target.sqlite")
	if err := os.WriteFile(tempPath, []byte("new database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, []byte("existing database"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := publishNoReplace(tempPath, targetPath); err == nil {
		t.Fatal("expected publication to refuse an existing target")
	}
	contents, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "existing database" {
		t.Fatalf("existing target was changed: %q", contents)
	}
}

func hasIssue(issues []Issue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func writeLegacyFixture(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE kv_json(k TEXT PRIMARY KEY, v TEXT NOT NULL, updated_at INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{
		"whitedata": []map[string]any{{
			"name": "PlayerOne", "uuid": "123e4567-e89b-12d3-a456-426614174000", "passwd": "$2b$12$test",
			"email": "player@example.invalid", "status": "active", "joinDate": "2025-01-01",
			"onlineTime": 1234, "avatarPath": "avatars/test.png",
		}},
		"signData": map[string]any{"PlayerOne": map[string]any{
			"totalDays": 1, "consecutiveDays": 1, "lastSign": "2025-01-01", "points": 15,
			"signHistory": map[string]bool{"2025-01-01": true},
		}},
		"shopItems": []map[string]any{{
			"id": "item-1", "name": "Stone", "itemId": "minecraft:stone", "amount": 1,
			"points": 10, "stock": 3, "image": "images/default_item.png",
			"description": "fixture", "createdAt": "2025-01-01T00:00:00.000Z",
		}},
		"coupons": []map[string]any{{
			"code": "ABCD123456", "type": "item", "items": []map[string]any{{"itemId": "minecraft:stone", "amount": 1}},
			"designatedPlayer": "PlayerOne", "expiresAt": "2099-01-01T00:00:00.000Z",
			"oneTimeUse": true, "createdAt": "2025-01-01T00:00:00.000Z", "used": false, "usedBy": []string{},
		}},
		"ports": map[string]int{"localhost": 8094},
	}
	for key, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO kv_json(k,v,updated_at) VALUES(?,?,0)", key, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func contains(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
