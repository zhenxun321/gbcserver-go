package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gbcserver/source/internal/store"
)

// bootstrapDeployment 创建首次启动需要的数据目录和空数据库，不生成配置文件。
func bootstrapDeployment(dataDir, _ string) (string, string, error) {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = defaultDataDir()
	}
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", fmt.Errorf("解析数据目录失败: %w", err)
	}
	if err := os.MkdirAll(absDataDir, 0o750); err != nil {
		return "", "", fmt.Errorf("创建数据目录失败: %w", err)
	}
	for _, directory := range []string{filepath.Join(absDataDir, "web"), filepath.Join(absDataDir, "avatars")} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			return "", "", fmt.Errorf("创建运行目录失败: %w", err)
		}
	}
	databasePath := filepath.Join(absDataDir, "gbcserver.db")
	if err := initializeDatabaseIfMissing(databasePath); err != nil {
		return "", "", err
	}
	db, err := openNormalizedDB(databasePath)
	if err != nil {
		return "", "", err
	}
	defer db.Close()
	if err := (store.ConfigStore{DB: db}).SetDefaults(context.Background(), map[string]string{
		"httpPort": "80", "httpsPort": "443", "adminPath": "/admin",
		"whitelistListen": "127.0.0.1:18094", "couponListen": "127.0.0.1:18095",
		"staticDir": filepath.Join(absDataDir, "web"), "avatarDir": filepath.Join(absDataDir, "avatars"),
		"tlsKeyFile": filepath.Join(absDataDir, "server.key"), "tlsCertFile": filepath.Join(absDataDir, "server.pem"),
		"bodyLimitBytes": "25000000", "serverID": "default", "webhookPath": "/webhook/update-port",
		"signTimezoneOffset": "8", "maxOnlineTime": "3214080000000ms", "onlineMode": "false",
		"mailHost": "", "mailPort": "465", "mailUsername": "", "mailPassword": "", "mailFromName": "", "mailFromAddress": "", "mailSecure": "true",
		"statusServers": "", "statusAPIs": "", "statusTimeout": "5s", "statusCacheTTL": "25s",
		"statusMonitorInterval": "1m", "statusAlertAfter": "2m", "statusAlertEmailEnabled": "false",
		"statusAlertEmail": "", "statusAlertWebhookEnabled": "false", "statusAlertWebhookURL": "", "statusAlertRecoveryEnabled": "true",
		"mojangEndpoint": "https://api.mojang.com/users/profiles/minecraft/", "mojangTimeout": "5s",
	}); err != nil {
		return "", "", fmt.Errorf("初始化数据库配置失败: %w", err)
	}
	return absDataDir, "", nil
}

// initializeDatabaseIfMissing 只初始化确实不存在的文件，不改动已有数据库。
func initializeDatabaseIfMissing(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查数据库文件失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建数据库目录失败: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("创建数据库文件失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭新数据库文件失败: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("打开新 SQLite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;"); err == nil {
		err = store.Migrate(ctx, db)
	}
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("初始化新 SQLite 失败: %w", err)
	}
	fmt.Fprintf(os.Stdout, "已初始化数据库：%s\n", path)
	return nil
}
