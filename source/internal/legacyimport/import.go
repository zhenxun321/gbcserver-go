package legacyimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"gbcserver/source/internal/store"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var (
	usernamePattern = regexp.MustCompile("^[A-Za-z0-9_]{3,16}$")
	uuidPattern     = regexp.MustCompile("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
	itemIDPattern   = regexp.MustCompile("^[a-z0-9._-]+:[a-z0-9/._-]+$")
	bcryptPattern   = regexp.MustCompile("^\\$2[aby]\\$")
)

// Issue 只报告问题类型和来源行号，不包含任何用户输入。
type Issue struct {
	Severity string `json:"severity"`
	Key      string `json:"key"`
	Index    int    `json:"index,omitempty"`
	Code     string `json:"code"`
	Field    string `json:"field,omitempty"`
}

// Report 是可安全落盘或打印的脱敏迁移报告。
type Report struct {
	SourceCounts map[string]int `json:"source_counts"`
	TargetCounts map[string]int `json:"target_counts,omitempty"`
	Warnings     []Issue        `json:"warnings"`
	Errors       []Issue        `json:"errors"`
	CanApply     bool           `json:"can_apply"`
	Applied      bool           `json:"applied"`
}

type legacyUser struct {
	Name         string  `json:"name"`
	UUID         string  `json:"uuid"`
	Password     string  `json:"passwd"`
	Email        string  `json:"email"`
	Status       string  `json:"status"`
	JoinDate     string  `json:"joinDate"`
	OnlineTimeMS int64   `json:"onlineTime"`
	LastLogin    *string `json:"lastLogin"`
	AvatarPath   *string `json:"avatarPath"`
	Points       *int64  `json:"points"`
}

type legacySign struct {
	TotalDays       int64           `json:"totalDays"`
	ConsecutiveDays int64           `json:"consecutiveDays"`
	LastSign        string          `json:"lastSign"`
	Points          int64           `json:"points"`
	History         map[string]bool `json:"signHistory"`
}

type signAccount struct {
	Username        string
	TotalDays       int64
	ConsecutiveDays int64
	LastSign        *string
	Points          int64
	History         []string
}

type legacyItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	ItemID      string  `json:"itemId"`
	Amount      int64   `json:"amount"`
	Points      int64   `json:"points"`
	Stock       *int64  `json:"stock"`
	Image       string  `json:"image"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"createdAt"`
	UpdatedAt   *string `json:"updatedAt"`
}

type legacyCoupon struct {
	Code             string       `json:"code"`
	Type             string       `json:"type"`
	Items            []couponItem `json:"items"`
	DesignatedPlayer *string      `json:"designatedPlayer"`
	ExpiresAt        string       `json:"expiresAt"`
	OneTimeUse       bool         `json:"oneTimeUse"`
	CreatedAt        string       `json:"createdAt"`
	Used             bool         `json:"used"`
	UsedBy           []string     `json:"usedBy"`
}

type couponItem struct {
	ItemID string `json:"itemId"`
	Amount int64  `json:"amount"`
}

type sourceData struct {
	Users   []legacyUser
	Signs   []signAccount
	Items   []legacyItem
	Coupons []legacyCoupon
	Ports   map[string]int64
}

// Analyze 以只读模式读取旧库并执行脱敏校验。
func Analyze(ctx context.Context, sourcePath string) (Report, error) {
	_, report, err := loadAndValidate(ctx, sourcePath)
	return report, err
}

// Import 仅在 dry-run 无阻塞问题且目标路径不存在时创建新库。
func Import(ctx context.Context, sourcePath, targetPath string) (Report, error) {
	data, report, err := loadAndValidate(ctx, sourcePath)
	if err != nil {
		return report, err
	}
	if len(report.Errors) != 0 {
		return report, errors.New("dry-run 存在阻塞问题，未执行导入")
	}
	if err := importNewDatabase(ctx, targetPath, data, &report); err != nil {
		return report, err
	}
	report.Applied = true
	return report, nil
}

func loadAndValidate(ctx context.Context, sourcePath string) (sourceData, Report, error) {
	data := sourceData{Ports: map[string]int64{}}
	report := Report{SourceCounts: map[string]int{}, Warnings: []Issue{}, Errors: []Issue{}}
	db, err := openReadOnly(sourcePath)
	if err != nil {
		return data, report, fmt.Errorf("打开旧数据库失败: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, "SELECT k, v FROM kv_json ORDER BY k")
	if err != nil {
		return data, report, fmt.Errorf("读取旧 kv_json 失败: %w", err)
	}
	values := make(map[string]json.RawMessage)
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return data, report, fmt.Errorf("读取旧数据库行失败: %w", err)
		}
		values[key] = append(json.RawMessage(nil), raw...)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return data, report, fmt.Errorf("遍历旧数据库失败: %w", err)
	}
	rows.Close()

	for _, key := range []string{"whitedata", "signData", "shopItems", "coupons", "ports"} {
		if _, ok := values[key]; !ok {
			addIssue(&report, "error", key, 0, "missing_key", "")
		}
	}
	decode(values, "whitedata", &data.Users, &report)
	decode(values, "shopItems", &data.Items, &report)
	decode(values, "coupons", &data.Coupons, &report)
	decode(values, "ports", &data.Ports, &report)

	rawSigns := map[string]json.RawMessage{}
	decode(values, "signData", &rawSigns, &report)
	data.Signs = validateSigns(rawSigns, data.Users, &report)
	report.SourceCounts["users"] = len(data.Users)
	report.SourceCounts["sign_accounts"] = len(rawSigns)
	report.SourceCounts["sign_history"] = countHistory(data.Signs)
	report.SourceCounts["shop_items"] = len(data.Items)
	report.SourceCounts["coupons"] = len(data.Coupons)
	report.SourceCounts["ports"] = len(data.Ports)
	validateUsers(&data, &report)
	validateItems(&data, &report)
	validateCoupons(&data, &report)
	validatePorts(&data, &report)
	report.CanApply = len(report.Errors) == 0
	return data, report, nil
}

func openReadOnly(path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uriURL := url.URL{Scheme: "file", Path: uriPath}
	query := uriURL.Query()
	query.Set("mode", "ro")
	uriURL.RawQuery = query.Encode()
	uri := uriURL.String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func decode[T any](values map[string]json.RawMessage, key string, target *T, report *Report) {
	raw, ok := values[key]
	if !ok {
		return
	}
	if err := json.Unmarshal(raw, target); err != nil {
		addIssue(report, "error", key, 0, "invalid_json_shape", "")
	}
}

func validateUsers(data *sourceData, report *Report) {
	names, emails, uuids := map[string]bool{}, map[string]bool{}, map[string]bool{}
	userIndex := map[string]int{}
	for i := range data.Users {
		u := &data.Users[i]
		nameKey := strings.ToLower(strings.TrimSpace(u.Name))
		if strings.TrimSpace(u.Name) == "" {
			addIssue(report, "error", "whitedata", i, "empty_username", "name")
		} else if !usernamePattern.MatchString(u.Name) {
			// 历史账号可能早于当前注册规则；保留原名，登录路径按 name_key 查找。
			addIssue(report, "warning", "whitedata", i, "legacy_username_preserved", "name")
		}
		if nameKey == "" || names[nameKey] {
			addIssue(report, "error", "whitedata", i, "duplicate_or_empty_username", "name")
		}
		names[nameKey] = true
		userIndex[nameKey] = i
		emailKey := strings.ToLower(strings.TrimSpace(u.Email))
		if emailKey == "" {
			addIssue(report, "error", "whitedata", i, "empty_email", "email")
		} else if emails[emailKey] {
			addIssue(report, "error", "whitedata", i, "duplicate_email", "email")
		}
		emails[emailKey] = true
		uuidKey := strings.ToLower(strings.TrimSpace(u.UUID))
		if !uuidPattern.MatchString(u.UUID) {
			addIssue(report, "error", "whitedata", i, "invalid_uuid", "uuid")
		}
		if uuidKey != "" && uuids[uuidKey] {
			addIssue(report, "error", "whitedata", i, "duplicate_uuid", "uuid")
		}
		uuids[uuidKey] = true
		if u.Status != "active" && u.Status != "inactive" && u.Status != "banned" {
			addIssue(report, "error", "whitedata", i, "invalid_status", "status")
		}
		if u.OnlineTimeMS < 0 || (u.Points != nil && *u.Points < 0) {
			addIssue(report, "error", "whitedata", i, "negative_user_value", "")
		}
		if u.Password == "" {
			addIssue(report, "error", "whitedata", i, "empty_password", "passwd")
		} else if !bcryptPattern.MatchString(u.Password) {
			addIssue(report, "warning", "whitedata", i, "legacy_plaintext_password_will_be_hashed", "passwd")
		}
		if strings.TrimSpace(u.JoinDate) == "" {
			addIssue(report, "error", "whitedata", i, "empty_join_date", "joinDate")
		}
		u.AvatarPath = safeAvatarFilename(u.AvatarPath, report, i)
	}
	for _, account := range data.Signs {
		key := strings.ToLower(strings.TrimSpace(account.Username))
		if i, ok := userIndex[key]; ok {
			if data.Users[i].Points != nil && *data.Users[i].Points != account.Points {
				addIssue(report, "warning", "signData", i, "points_conflict_signData_authoritative", "points")
			}
			points := account.Points
			data.Users[i].Points = &points
		}
	}
}

func safeAvatarFilename(path *string, report *Report, index int) *string {
	if path == nil || strings.TrimSpace(*path) == "" {
		return nil
	}
	value := strings.ReplaceAll(*path, "\\", "/")
	base := filepath.Base(value)
	if strings.EqualFold(filepath.Ext(base), ".png") && !strings.Contains(base, "..") {
		return &base
	}
	addIssue(report, "warning", "whitedata", index, "unsafe_avatar_reference_omitted", "avatarPath")
	return nil
}

func validateSigns(raw map[string]json.RawMessage, users []legacyUser, report *Report) []signAccount {
	userNames := make(map[string]bool, len(users))
	for _, u := range users {
		userNames[strings.ToLower(strings.TrimSpace(u.Name))] = true
	}
	out := make([]signAccount, 0, len(raw))
	for name, value := range raw {
		var legacy legacySign
		if err := json.Unmarshal(value, &legacy); err != nil {
			addIssue(report, "error", "signData", 0, "invalid_record", "")
			continue
		}
		if !userNames[strings.ToLower(strings.TrimSpace(name))] {
			addIssue(report, "warning", "signData", 0, "orphan_sign_account_skipped", "")
			continue
		}
		if legacy.TotalDays < 0 || legacy.ConsecutiveDays < 0 || legacy.Points < 0 {
			addIssue(report, "error", "signData", 0, "negative_sign_value", "")
		}
		account := signAccount{
			Username: name, TotalDays: legacy.TotalDays,
			ConsecutiveDays: legacy.ConsecutiveDays, Points: legacy.Points,
		}
		if legacy.LastSign != "" {
			account.LastSign = &legacy.LastSign
		}
		for date, signed := range legacy.History {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				addIssue(report, "error", "signData", 0, "invalid_sign_date", "signHistory")
			} else if signed {
				account.History = append(account.History, date)
			}
		}
		sort.Strings(account.History)
		out = append(out, account)
	}
	return out
}

func validateItems(data *sourceData, report *Report) {
	ids := map[string]bool{}
	for i := range data.Items {
		item := &data.Items[i]
		if strings.TrimSpace(item.ID) == "" || ids[item.ID] {
			addIssue(report, "error", "shopItems", i, "duplicate_or_empty_item_id", "id")
		}
		ids[item.ID] = true
		if strings.TrimSpace(item.ItemID) == "" {
			addIssue(report, "error", "shopItems", i, "empty_item_id", "itemId")
		} else if !itemIDPattern.MatchString(item.ItemID) {
			// 旧版本把物品 ID 当作不透明字符串保存；迁移时原样保留。
			addIssue(report, "warning", "shopItems", i, "legacy_item_id_preserved", "itemId")
		}
		// 旧版没有 amount 字段，旧购买代码按 1 个物品处理。
		if item.Amount <= 0 {
			item.Amount = 1
		}
		if item.Points < 0 || (item.Stock != nil && *item.Stock < 0) {
			addIssue(report, "error", "shopItems", i, "negative_price_or_stock", "")
		}
		if normalized, ok := normalizeLegacyItemImage(item.Image); ok {
			item.Image = normalized
		} else if strings.TrimSpace(item.Image) == "" {
			item.Image = "images/default_item.png"
		} else {
			// 无法安全映射的旧链接不再写入新库，使用内置默认图避免继续依赖旧站点。
			item.Image = "images/default_item.png"
			addIssue(report, "warning", "shopItems", i, "legacy_image_reference_omitted", "image")
		}
		if item.CreatedAt == "" {
			addIssue(report, "error", "shopItems", i, "empty_created_at", "createdAt")
		}
	}
}

// normalizeLegacyItemImage 将旧版 public/items 图片引用规范为部署包中的本地 URL。
func normalizeLegacyItemImage(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || value == "images/default_item.png" || value == "/images/default_item.png" {
		return "images/default_item.png", true
	}
	parsed, err := url.Parse(value)
	if err == nil && (parsed.IsAbs() || strings.HasPrefix(value, "//")) {
		if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "" {
			return "", false
		}
		value = parsed.Path
	}
	if strings.HasPrefix(value, "items/") {
		value = "/" + value
	}
	if !strings.HasPrefix(value, "/items/") {
		return "", false
	}
	name := path.Base(value)
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") {
		return "", false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return "/items/" + name, true
	default:
		return "", false
	}
}

func validateCoupons(data *sourceData, report *Report) {
	codes := map[string]bool{}
	for i := range data.Coupons {
		coupon := &data.Coupons[i]
		coupon.Code = strings.ToUpper(strings.TrimSpace(coupon.Code))
		if coupon.Code == "" || codes[coupon.Code] {
			addIssue(report, "error", "coupons", i, "duplicate_or_empty_code", "code")
		}
		codes[coupon.Code] = true
		if coupon.Type != "item" && coupon.Type != "bundle" {
			addIssue(report, "error", "coupons", i, "invalid_coupon_type", "type")
		}
		if _, err := time.Parse(time.RFC3339Nano, coupon.ExpiresAt); err != nil {
			addIssue(report, "error", "coupons", i, "invalid_expiration", "expiresAt")
		}
		if coupon.CreatedAt == "" {
			addIssue(report, "error", "coupons", i, "empty_created_at", "createdAt")
		}
		for _, item := range coupon.Items {
			if strings.TrimSpace(item.ItemID) == "" || item.Amount <= 0 {
				addIssue(report, "error", "coupons", i, "invalid_coupon_item", "items")
			} else if !itemIDPattern.MatchString(item.ItemID) {
				// 兑换码中的历史物品 ID 与商品表一样按不透明字符串处理。
				addIssue(report, "warning", "coupons", i, "legacy_item_id_preserved", "items")
			}
		}
		seen := map[string]bool{}
		for _, username := range coupon.UsedBy {
			key := strings.ToLower(strings.TrimSpace(username))
			if key == "" || seen[key] {
				addIssue(report, "error", "coupons", i, "duplicate_or_empty_usage", "usedBy")
			}
			seen[key] = true
		}
	}
}

func validatePorts(data *sourceData, report *Report) {
	for key, port := range data.Ports {
		if strings.TrimSpace(key) == "" || port < 1 || port > 65535 {
			addIssue(report, "error", "ports", 0, "invalid_port", "")
		}
	}
}

func importNewDatabase(ctx context.Context, targetPath string, data sourceData, report *Report) error {
	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absolute); err == nil {
		return errors.New("目标数据库已存在；为避免覆盖，导入已停止")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp(filepath.Dir(absolute), ".gbc-migrate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	tempPath := filepath.Join(tempDir, "target.sqlite")
	db, err := sql.Open("sqlite", tempPath)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return err
	}
	if err := store.Migrate(ctx, db); err != nil {
		db.Close()
		return fmt.Errorf("创建目标 schema 失败: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if err := insertData(ctx, tx, data, now); err != nil {
		tx.Rollback()
		db.Close()
		return err
	}
	if err := tx.Commit(); err != nil {
		db.Close()
		return err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		db.Close()
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	// 导入库只允许服务账户读写；Windows 的权限仍需由部署 ACL 控制。
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("设置导入数据库权限失败: %w", err)
	}
	if err := publishNoReplace(tempPath, absolute); err != nil {
		return err
	}
	report.TargetCounts = map[string]int{
		"users": len(data.Users), "sign_accounts": len(data.Users),
		"sign_history": countHistory(data.Signs), "shop_items": len(data.Items),
		"coupons": len(data.Coupons), "ports": len(data.Ports),
	}
	return nil
}

// publishNoReplace 使用同卷硬链接原子发布，目标已存在时绝不覆盖。
func publishNoReplace(tempPath, targetPath string) error {
	if err := os.Link(tempPath, targetPath); err != nil {
		return fmt.Errorf("原子发布导入数据库失败；目标未覆盖: %w", err)
	}
	return nil
}

func insertData(ctx context.Context, tx *sql.Tx, data sourceData, now int64) error {
	userIDs := make(map[string]int64, len(data.Users))
	signByName := make(map[string]signAccount, len(data.Signs))
	for _, account := range data.Signs {
		signByName[strings.ToLower(strings.TrimSpace(account.Username))] = account
	}
	for _, user := range data.Users {
		password := user.Password
		if !bcryptPattern.MatchString(password) {
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("旧密码升级失败: %w", err)
			}
			password = string(hash)
		}
		result, err := tx.ExecContext(ctx, "INSERT INTO users(name,name_key,uuid,password_hash,email,email_key,status,join_date,online_time_ms,last_login,avatar_filename,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)",
			user.Name, strings.ToLower(strings.TrimSpace(user.Name)), user.UUID, password,
			user.Email, strings.ToLower(strings.TrimSpace(user.Email)), user.Status, user.JoinDate,
			user.OnlineTimeMS, user.LastLogin, user.AvatarPath, now, now)
		if err != nil {
			return fmt.Errorf("写入用户失败: %w", err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		userIDs[strings.ToLower(strings.TrimSpace(user.Name))] = id
	}
	for _, user := range data.Users {
		key := strings.ToLower(strings.TrimSpace(user.Name))
		account := signByName[key]
		if _, err := tx.ExecContext(ctx, "INSERT INTO sign_accounts(user_id,total_days,consecutive_days,last_sign,points,updated_at) VALUES(?,?,?,?,?,?)",
			userIDs[key], account.TotalDays, account.ConsecutiveDays, account.LastSign, account.Points, now); err != nil {
			return fmt.Errorf("写入签到账户失败: %w", err)
		}
		for _, date := range account.History {
			if _, err := tx.ExecContext(ctx, "INSERT INTO sign_history(user_id,sign_date) VALUES(?,?)", userIDs[key], date); err != nil {
				return fmt.Errorf("写入签到历史失败: %w", err)
			}
		}
	}
	for _, item := range data.Items {
		if _, err := tx.ExecContext(ctx, "INSERT INTO shop_items(id,name,item_id,amount,points,stock,image,description,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)",
			item.ID, item.Name, item.ItemID, item.Amount, item.Points, item.Stock,
			item.Image, item.Description, item.CreatedAt, item.UpdatedAt); err != nil {
			return fmt.Errorf("写入商品失败: %w", err)
		}
	}
	for _, coupon := range data.Coupons {
		if _, err := tx.ExecContext(ctx, "INSERT INTO coupons(code,type,designated_player,expires_at,one_time_use,used,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)",
			coupon.Code, coupon.Type, coupon.DesignatedPlayer, coupon.ExpiresAt,
			coupon.OneTimeUse, coupon.Used, coupon.CreatedAt, now); err != nil {
			return fmt.Errorf("写入兑换码失败: %w", err)
		}
		for position, item := range coupon.Items {
			if _, err := tx.ExecContext(ctx, "INSERT INTO coupon_items(coupon_code,position,item_id,amount) VALUES(?,?,?,?)",
				coupon.Code, position, item.ItemID, item.Amount); err != nil {
				return fmt.Errorf("写入兑换码物品失败: %w", err)
			}
		}
		for _, username := range coupon.UsedBy {
			if _, err := tx.ExecContext(ctx, "INSERT INTO coupon_usages(coupon_code,username_key,username,used_at) VALUES(?,?,?,NULL)",
				coupon.Code, strings.ToLower(strings.TrimSpace(username)), username); err != nil {
				return fmt.Errorf("写入兑换记录失败: %w", err)
			}
		}
	}
	for key, port := range data.Ports {
		if _, err := tx.ExecContext(ctx, "INSERT INTO ports(key,port,updated_at) VALUES(?,?,?)", key, port, now); err != nil {
			return fmt.Errorf("写入端口映射失败: %w", err)
		}
	}
	return nil
}

func countHistory(accounts []signAccount) int {
	total := 0
	for _, account := range accounts {
		total += len(account.History)
	}
	return total
}

func addIssue(report *Report, severity, key string, index int, code, field string) {
	issue := Issue{Severity: severity, Key: key, Index: index, Code: code, Field: field}
	if severity == "error" {
		report.Errors = append(report.Errors, issue)
	} else {
		report.Warnings = append(report.Warnings, issue)
	}
}
