package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Scope 是插件 token 的最小权限范围。
type Scope string

const (
	ScopeWhitelist    Scope = "whitelist"
	ScopeCouponRedeem Scope = "coupon.redeem"
)

var (
	ErrMissingToken   = errors.New("缺少插件 token")
	ErrInvalidToken   = errors.New("插件 token 无效")
	ErrRevokedToken   = errors.New("插件 token 已撤销")
	ErrExpiredToken   = errors.New("插件 token 已过期")
	ErrScopeForbidden = errors.New("插件 token 权限不足")
)

// Principal 是认证成功后可传给业务层的服务端身份。
type Principal struct {
	TokenID  string
	ServerID string
	Scope    Scope
}

// IssuedToken 只在签发时返回明文 Secret，数据库只保存 Secret 的摘要。
type IssuedToken struct {
	TokenID   string     `json:"token_id"`
	ServerID  string     `json:"server_id"`
	Scope     Scope      `json:"scope"`
	Secret    string     `json:"secret"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// TokenRecord 是不含明文或摘要的插件令牌管理信息。
type TokenRecord struct {
	TokenID   string `json:"token_id"`
	ServerID  string `json:"server_id"`
	Scope     Scope  `json:"scope"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	RevokedAt *int64 `json:"revoked_at,omitempty"`
}

// TokenStore 负责插件 token 的签发、校验和撤销。
type TokenStore struct {
	DB  *sql.DB
	Now func() time.Time
}

// NewTokenStore 创建 token 存储；Now 主要用于确定性的测试和迁移演练。
func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{DB: db, Now: time.Now}
}

// Issue 为指定游戏服签发一枚最小权限 token。
func (s *TokenStore) Issue(ctx context.Context, serverID string, scope Scope, expiresAt *time.Time) (IssuedToken, error) {
	if s == nil || s.DB == nil {
		return IssuedToken{}, errors.New("token store 未初始化")
	}
	serverID = strings.TrimSpace(serverID)
	if serverID == "" || strings.ContainsAny(serverID, "\r\n") {
		return IssuedToken{}, errors.New("server_id 不合法")
	}
	if !validScope(scope) {
		return IssuedToken{}, ErrScopeForbidden
	}
	if expiresAt != nil && !expiresAt.After(s.now()) {
		return IssuedToken{}, errors.New("过期时间必须晚于当前时间")
	}

	tokenID, err := randomIdentifier("pt_")
	if err != nil {
		return IssuedToken{}, fmt.Errorf("生成 token_id 失败: %w", err)
	}
	secret, err := randomSecret()
	if err != nil {
		return IssuedToken{}, fmt.Errorf("生成 token 失败: %w", err)
	}
	createdAt := s.now().UnixMilli()
	var expiresMillis any
	if expiresAt != nil {
		expiresMillis = expiresAt.UnixMilli()
	}
	if _, err := s.DB.ExecContext(ctx,
		"INSERT INTO plugin_tokens(token_id,server_id,scope,token_hash,created_at,expires_at,revoked_at) VALUES(?,?,?,?,?,?,NULL)",
		tokenID, serverID, string(scope), hashToken(secret), createdAt, expiresMillis); err != nil {
		return IssuedToken{}, fmt.Errorf("保存插件 token 失败: %w", err)
	}
	return IssuedToken{TokenID: tokenID, ServerID: serverID, Scope: scope, Secret: secret, ExpiresAt: expiresAt}, nil
}

// Authenticate 校验明文 Bearer token，并返回其服务器身份和权限。
func (s *TokenStore) Authenticate(ctx context.Context, rawToken string, required Scope) (Principal, error) {
	if s == nil || s.DB == nil {
		return Principal{}, errors.New("token store 未初始化")
	}
	if !validScope(required) {
		return Principal{}, ErrScopeForbidden
	}
	secret := strings.TrimSpace(rawToken)
	if secret == "" {
		return Principal{}, ErrMissingToken
	}

	var principal Principal
	var scope string
	var expiresAt, revokedAt sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `
		SELECT token_id, server_id, scope, expires_at, revoked_at
		FROM plugin_tokens
		WHERE token_hash = ? AND scope = ?
		LIMIT 1`, hashToken(secret), string(required)).
		Scan(&principal.TokenID, &principal.ServerID, &scope, &expiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrInvalidToken
	}
	if err != nil {
		return Principal{}, fmt.Errorf("查询插件 token 失败: %w", err)
	}
	if revokedAt.Valid {
		return Principal{}, ErrRevokedToken
	}
	if expiresAt.Valid && expiresAt.Int64 <= s.now().UnixMilli() {
		return Principal{}, ErrExpiredToken
	}
	principal.Scope = Scope(scope)
	return principal, nil
}

// AuthenticateRequest 从 Authorization header 读取 Bearer token。
// 不读取 query、Cookie 或 X-Forwarded-*，避免凭据进入 URL 和不可信转发头。
func (s *TokenStore) AuthenticateRequest(ctx context.Context, r *http.Request, required Scope) (Principal, error) {
	if r == nil {
		return Principal{}, ErrMissingToken
	}
	token, err := ParseBearer(r.Header.Get("Authorization"))
	if err != nil {
		return Principal{}, err
	}
	return s.Authenticate(ctx, token, required)
}

// Revoke 撤销指定 token；幂等处理不存在的 token。
func (s *TokenStore) Revoke(ctx context.Context, tokenID string) error {
	if s == nil || s.DB == nil {
		return errors.New("token store 未初始化")
	}
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return errors.New("token_id 不能为空")
	}
	if _, err := s.DB.ExecContext(ctx,
		"UPDATE plugin_tokens SET revoked_at = COALESCE(revoked_at, ?) WHERE token_id = ?",
		s.now().UnixMilli(), tokenID); err != nil {
		return fmt.Errorf("撤销插件 token 失败: %w", err)
	}
	return nil
}

// List 返回令牌管理信息，不读取数据库中的令牌摘要。
func (s *TokenStore) List(ctx context.Context) ([]TokenRecord, error) {
	if s == nil || s.DB == nil {
		return nil, errors.New("token store 未初始化")
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT token_id, server_id, scope, created_at, expires_at, revoked_at
		FROM plugin_tokens
		ORDER BY created_at DESC, token_id`)
	if err != nil {
		return nil, fmt.Errorf("读取插件 token 列表失败: %w", err)
	}
	defer rows.Close()

	items := make([]TokenRecord, 0)
	for rows.Next() {
		var item TokenRecord
		var expiresAt, revokedAt sql.NullInt64
		if err := rows.Scan(&item.TokenID, &item.ServerID, &item.Scope, &item.CreatedAt, &expiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("读取插件 token 信息失败: %w", err)
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Int64
		}
		if revokedAt.Valid {
			item.RevokedAt = &revokedAt.Int64
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历插件 token 列表失败: %w", err)
	}
	return items, nil
}

// ParseBearer 严格解析 Authorization: Bearer <token>。
func ParseBearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) == 0 {
		return "", ErrMissingToken
	}
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", ErrInvalidToken
	}
	return parts[1], nil
}

func (s *TokenStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func validScope(scope Scope) bool {
	return scope == ScopeWhitelist || scope == ScopeCouponRedeem
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomIdentifier(prefix string) (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
