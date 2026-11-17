package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path"
	"strings"
)

const defaultShopImage = "images/default_item.png"

// normalizeShopImageForWrite 只接受内置默认图或本服务上传后生成的商品图片路径。
func normalizeShopImageForWrite(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return defaultShopImage, true
	}
	if value == defaultShopImage || value == "/"+defaultShopImage {
		return defaultShopImage, true
	}
	if strings.HasPrefix(value, "items/") {
		value = "/" + value
	}
	if !strings.HasPrefix(value, "/items/") {
		return "", false
	}
	name := path.Base(value)
	if !safeShopImageFilename(name) {
		return "", false
	}
	return "/items/" + name, true
}

// normalizeLegacyShopImage 把旧 Node 版本的商品外链转换为本地 /items 路径。
func normalizeLegacyShopImage(value string) (string, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return defaultShopImage, true
	}
	if value == defaultShopImage || value == "/"+defaultShopImage {
		return defaultShopImage, true
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
	if !safeShopImageFilename(name) {
		return "", false
	}
	return "/items/" + name, true
}

func safeShopImageFilename(name string) bool {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") {
		return false
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif":
		return true
	default:
		return false
	}
}

// repairLegacyShopImages 启动时修复仍保留旧外链的商品记录，避免升级后的网页继续依赖旧站点。
func repairLegacyShopImages(ctx context.Context, db *sql.DB) error {
	var tableExists int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='shop_items'").Scan(&tableExists); err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx, "SELECT id,image FROM shop_items")
	if err != nil {
		return err
	}
	type repair struct{ id, image string }
	var repairs []repair
	for rows.Next() {
		var item repair
		if err := rows.Scan(&item.id, &item.image); err != nil {
			rows.Close()
			return err
		}
		if normalized, ok := normalizeLegacyShopImage(item.image); ok && normalized != item.image {
			repairs = append(repairs, repair{id: item.id, image: normalized})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(repairs) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range repairs {
		if _, err := tx.ExecContext(ctx, "UPDATE shop_items SET image=? WHERE id=?", item.image, item.id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

var errInvalidShopImageReference = errors.New("商品图片必须先上传到本服务")
