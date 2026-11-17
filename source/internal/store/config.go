package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// ConfigStore 保存运行时配置；所有值都以字符串存储，类型转换由调用方完成。
type ConfigStore struct{ DB *sql.DB }

func (s ConfigStore) Get(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.DB.QueryRowContext(ctx, "SELECT value FROM app_config WHERE key=?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return value, err == nil, err
}

func (s ConfigStore) Set(ctx context.Context, key, value string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO app_config(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, value, time.Now().UnixMilli())
	return err
}

func (s ConfigStore) SetDefaults(ctx context.Context, values map[string]string) error {
	for key, value := range values {
		if current, exists, err := s.Get(ctx, key); err != nil {
			return err
		} else if !exists || strings.TrimSpace(current) == "" {
			if err := s.Set(ctx, key, value); err != nil {
				return err
			}
		}
	}
	return nil
}
