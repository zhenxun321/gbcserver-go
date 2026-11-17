package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultMojangProfileEndpoint = "https://api.mojang.com/users/profiles/minecraft/"

// MojangLookupFunc 便于测试或部署方替换 Mojang 查询实现。
type MojangLookupFunc func(ctx context.Context, username string) (string, error)

func (f MojangLookupFunc) Lookup(ctx context.Context, username string) (string, error) {
	return f(ctx, username)
}

// HTTPMojangLookup 使用 Mojang 公共 profile API 查询正版 UUID。
type HTTPMojangLookup struct {
	Client   *http.Client
	Endpoint string
}

// NewHTTPMojangLookup 创建带超时的 Mojang 查询器。
func NewHTTPMojangLookup(endpoint string, timeout time.Duration) HTTPMojangLookup {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultMojangProfileEndpoint
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return HTTPMojangLookup{
		Client:   &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }},
		Endpoint: strings.TrimRight(strings.TrimSpace(endpoint), "/") + "/",
	}
}

func (lookup HTTPMojangLookup) Lookup(ctx context.Context, username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", errors.New("Mojang 用户名为空")
	}
	endpoint := strings.TrimSpace(lookup.Endpoint)
	if endpoint == "" {
		endpoint = defaultMojangProfileEndpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("Mojang endpoint 无效")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/"+url.PathEscape(username), nil)
	if err != nil {
		return "", err
	}
	client := lookup.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Mojang 返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil {
		return "", err
	}
	if len(body) > 64*1024 {
		return "", errors.New("Mojang 响应过大")
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	return normalizeMojangUUID(payload.ID)
}

func normalizeMojangUUID(value string) (string, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "-", "")
	if len(value) != 32 {
		return "", errors.New("Mojang UUID 格式无效")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("Mojang UUID 格式无效")
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", value[:8], value[8:12], value[12:16], value[16:20], value[20:]), nil
}
