package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gbcserver/source/internal/plugin"
	"gbcserver/source/internal/store"
)

func runInteractivePluginToken(args []string) int {
	flags := newCommandFlags("plugin-token", "[--data-dir <运行数据目录>]")
	dataDir := flags.String("data-dir", defaultDataDir(), "运行实例使用的 data 文件夹本身")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	dbPath := filepath.Join(strings.TrimSpace(*dataDir), "gbcserver.db")
	db, err := openNormalizedDB(dbPath)
	if err != nil {
		cliFprintln(os.Stderr, "无法打开运行数据库；请先启动一次服务完成初始化，或使用 --data-dir 指定数据目录。", err)
		return 1
	}
	defer db.Close()

	ctx := context.Background()
	config := store.ConfigStore{DB: db}
	serverID := "default"
	if value, exists, err := config.Get(ctx, "serverID"); err != nil {
		cliFprintln(os.Stderr, "读取插件服务 ID 失败:", err)
		return 1
	} else if exists && strings.TrimSpace(value) != "" {
		serverID = strings.TrimSpace(value)
	}
	whitelistListen := readInteractiveConfig(ctx, config, "whitelistListen", "127.0.0.1:18094")
	couponListen := readInteractiveConfig(ctx, config, "couponListen", "127.0.0.1:18095")
	tokens := plugin.NewTokenStore(db)
	reader := bufio.NewReader(os.Stdin)

	cliPrintln("GBC Server 插件接入")
	cliPrintf("运行数据目录：%s\n游戏服 ID：%s\n", *dataDir, serverID)
	for {
		cliPrintln("\n1. 签发插件令牌\n2. 撤销插件令牌\n0. 退出")
		choice, err := readInteractiveLine(reader, "请选择：")
		if err != nil {
			cliFprintln(os.Stderr, "读取输入失败:", err)
			return 1
		}
		switch choice {
		case "0", "q", "Q":
			return 0
		case "1":
			if err := issueInteractivePluginToken(reader, ctx, tokens, serverID, whitelistListen, couponListen); err != nil {
				cliFprintln(os.Stderr, err)
			}
		case "2":
			if err := revokeInteractivePluginToken(reader, ctx, tokens); err != nil {
				cliFprintln(os.Stderr, err)
			}
		default:
			cliPrintln("请输入 1、2 或 0。")
		}
	}
}

func issueInteractivePluginToken(reader *bufio.Reader, ctx context.Context, tokens *plugin.TokenStore, serverID, whitelistListen, couponListen string) error {
	cliPrintln("\n1. 白名单插件\n2. 兑换插件")
	choice, err := readInteractiveLine(reader, "选择插件：")
	if err != nil {
		return err
	}
	var scope plugin.Scope
	listen := whitelistListen
	if choice == "1" {
		scope = plugin.ScopeWhitelist
	} else if choice == "2" {
		scope = plugin.ScopeCouponRedeem
		listen = couponListen
	} else {
		return fmt.Errorf("未签发令牌：插件选项无效")
	}
	defaultHost, err := interactivePluginHost(listen)
	if err != nil {
		return err
	}
	apiHost, err := readInteractiveLine(reader, fmt.Sprintf("游戏服可访问的服务端私网 IP（回车使用 %s）：", defaultHost))
	if err != nil {
		return err
	}
	if apiHost == "" {
		apiHost = defaultHost
	}
	apiURL, err := interactivePluginURL(scope, listen, apiHost)
	if err != nil {
		return err
	}
	expiresText, err := readInteractiveLine(reader, "过期时间 RFC3339（留空表示不设过期时间）：")
	if err != nil {
		return err
	}
	var expiresAt *time.Time
	if expiresText != "" {
		parsed, err := time.Parse(time.RFC3339, expiresText)
		if err != nil {
			return fmt.Errorf("过期时间格式无效，请使用 RFC3339，例如 2027-01-01T00:00:00+08:00")
		}
		if !parsed.After(time.Now()) {
			return fmt.Errorf("过期时间必须晚于当前时间")
		}
		expiresAt = &parsed
	}
	confirm, err := readInteractiveLine(reader, fmt.Sprintf("为 %s 签发 %s 令牌？[y/N]：", serverID, scope))
	if err != nil {
		return err
	}
	if !strings.EqualFold(confirm, "y") && !strings.EqualFold(confirm, "yes") {
		return fmt.Errorf("已取消签发")
	}
	issued, err := tokens.Issue(ctx, serverID, scope, expiresAt)
	if err != nil {
		return err
	}
	key := "whitelist-token"
	if scope == plugin.ScopeCouponRedeem {
		key = "coupon-token"
	}
	cliPrintf("\n令牌已签发（ID：%s）。明文只显示这一次，请立即保存。\n", issued.TokenID)
	cliPrintf("%s\n%s: %s\n", "--- 插件配置 ---", "api-url", strconv.Quote(apiURL))
	cliPrintf("%s: %s\n", key, strconv.Quote(issued.Secret))
	return nil
}

func revokeInteractivePluginToken(reader *bufio.Reader, ctx context.Context, tokens *plugin.TokenStore) error {
	items, err := tokens.List(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		cliPrintln("当前没有插件令牌。")
		return nil
	}
	for index, item := range items {
		state := "有效"
		if item.RevokedAt != nil {
			state = "已撤销"
		} else if item.ExpiresAt != nil && *item.ExpiresAt <= time.Now().UnixMilli() {
			state = "已过期"
		}
		cliPrintf("%d. %s  %s  %s  %s\n", index+1, item.TokenID, item.ServerID, item.Scope, state)
	}
	choice, err := readInteractiveLine(reader, "输入序号撤销，回车返回：")
	if err != nil {
		return err
	}
	if choice == "" {
		return nil
	}
	index, err := strconv.Atoi(choice)
	if err != nil || index < 1 || index > len(items) {
		return fmt.Errorf("序号无效")
	}
	selected := items[index-1]
	confirm, err := readInteractiveLine(reader, fmt.Sprintf("撤销令牌 %s？[y/N]：", selected.TokenID))
	if err != nil {
		return err
	}
	if !strings.EqualFold(confirm, "y") && !strings.EqualFold(confirm, "yes") {
		return fmt.Errorf("已取消撤销")
	}
	if err := tokens.Revoke(ctx, selected.TokenID); err != nil {
		return err
	}
	cliPrintln("令牌已撤销。")
	return nil
}

func interactivePluginHost(listen string) (string, error) {
	host, _, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", fmt.Errorf("插件监听地址 %q 无法解析，请先修正运行配置", listen)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if _, err := interactivePluginURL(plugin.ScopeWhitelist, listen, host); err != nil {
		return "", err
	}
	return strings.Trim(host, "[]"), nil
}

func interactivePluginURL(scope plugin.Scope, listen, host string) (string, error) {
	_, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return "", fmt.Errorf("插件监听地址 %q 无法解析，请先修正运行配置", listen)
	}
	ip := net.ParseIP(strings.Trim(strings.TrimSpace(host), "[]"))
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast()) {
		return "", fmt.Errorf("API 主机必须是 loopback、RFC1918、IPv6 ULA 或 link-local 私网 IP")
	}
	apiURL := "http://" + net.JoinHostPort(ip.String(), port)
	if scope == plugin.ScopeCouponRedeem {
		apiURL += "/api"
	}
	return apiURL, nil
}

func readInteractiveConfig(ctx context.Context, config store.ConfigStore, key, fallback string) string {
	value, exists, err := config.Get(ctx, key)
	if err != nil || !exists || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func readInteractiveLine(reader *bufio.Reader, prompt string) (string, error) {
	cliFprint(os.Stdout, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
