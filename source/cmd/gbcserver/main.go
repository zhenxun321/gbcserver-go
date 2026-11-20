package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gbcserver/source/internal/httpapi"
	"gbcserver/source/internal/imageutil"
	"gbcserver/source/internal/legacyimport"
	"gbcserver/source/internal/plugin"
	"gbcserver/source/internal/store"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func main() {
	// 双击 Windows 程序进入托盘；Linux 直接运行时进入后台服务。
	if len(os.Args) == 1 {
		switch runtime.GOOS {
		case "windows":
			os.Exit(runCommand([]string{"start", "--tray"}))
		case "linux":
			os.Exit(runCommand([]string{"start", "--background"}))
		}
	}
	os.Exit(runCommand(os.Args[1:]))
}

func runCommand(args []string) int {
	var err error
	args, err = selectCLILanguage(args)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 2
	}
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "help", "--help", "-help", "-h":
		if len(args) == 1 {
			printUsage(os.Stdout)
			return 0
		}
		if args[0] == "help" {
			nextArgs := append([]string{"--lang", string(activeCLILanguage)}, args[1:]...)
			return runCommand(append(nextArgs, "--help"))
		}
		cliFprintln(os.Stderr, "帮助选项后不能附加参数；请使用 gbcserver help <子命令>")
		return 2
	case "migrate":
		return runMigration(args[1:])
	case "plugin-token":
		return runPluginToken(args[1:])
	case "plugin-serve":
		return runLockedService(args[1:], func() int { return runPluginServe(args[1:]) })
	case "web-serve":
		return runLockedService(args[1:], func() int { return runWebServe(args[1:]) })
	case "start", "run":
		return runUnifiedStart(args[1:])
	case "httpport":
		return runPortCommand("httpport", "httpPort", args[1:])
	case "httpsport":
		return runPortCommand("httpsport", "httpsPort", args[1:])
	case "admin-path", "adminpath":
		return runAdminValueCommand(args[0], "adminPath", args[1:])
	case "admin-username", "adminuser":
		return runAdminValueCommand(args[0], "adminUsername", args[1:])
	case "admin-password", "adminpassword":
		return runAdminPasswordCommand(args[1:])
	case "reload":
		return runControlCommand("reload", args[1:])
	case "stop":
		return runControlCommand("stop", args[1:])
	case "install-autostart":
		return runAutostart(args[1:], true)
	case "uninstall-autostart":
		return runAutostart(args[1:], false)
	case "init-db":
		return runInitDB(args[1:])
	case "upgrade-db":
		return runUpgradeDB(args[1:])
	case "compress-images":
		return runCompressImages(args[1:])
	default:
		cliFprintln(os.Stderr, "未知命令；使用 gbcserver --help 查看用法")
		return 2
	}
}

func runUnifiedStart(args []string) int {
	flags := newCommandFlags("start", "[--data-dir <data目录>] [--background|--tray] [--autostart|--no-autostart]")
	dataDir := flags.String("data-dir", defaultDataDir(), cliText("运行数据文件夹本身；默认位于用户主目录或程序目录下的 gbcserver/"))
	background := flags.Bool("background", false, cliText("Linux 后台运行，日志保存在 data 文件夹"))
	tray := flags.Bool("tray", false, cliText("Windows 托盘运行；菜单可打开配置、数据、日志和 Web 页面"))
	autostart := flags.Bool("autostart", false, cliText("安装当前用户的开机自启"))
	noAutostart := flags.Bool("no-autostart", false, cliText("移除当前用户的开机自启"))
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if *autostart && *noAutostart {
		cliFprintln(os.Stderr, "--autostart 与 --no-autostart 不能同时使用")
		return 2
	}
	if *background && runtime.GOOS == "windows" {
		cliFprintln(os.Stderr, "Windows 请直接双击程序或使用 start；--background 仅适用于 Linux")
		return 2
	}
	if *tray && runtime.GOOS != "windows" {
		cliFprintln(os.Stderr, "--tray 仅适用于 Windows")
		return 2
	}
	if *autostart || *noAutostart {
		if err := configureAutostartPlatform(*dataDir, *autostart); err != nil {
			cliFprintln(os.Stderr, err)
			return 1
		}
		if *autostart {
			cliFprintln(os.Stdout, "GBC Server 当前用户开机自启已安装。")
		} else {
			cliFprintln(os.Stdout, "GBC Server 当前用户开机自启已移除。")
		}
	}

	lock, err := acquireInstanceLock()
	if errors.Is(err, errInstanceAlreadyRunning) {
		cliFprintln(os.Stdout, "GBC Server 已在运行；本次 start 未执行。")
		return 0
	}
	if err != nil {
		cliFprintln(os.Stderr, "获取单实例锁失败:", err)
		return 1
	}
	defer lock.Release()
	*dataDir, _, err = bootstrapDeployment(*dataDir, "")
	if err != nil {
		cliFprintln(os.Stderr, "准备部署目录失败:", err)
		return 1
	}
	if *background {
		backgroundArgs := os.Args[1:]
		if len(backgroundArgs) == 0 {
			backgroundArgs = append([]string{"start", "--data-dir", *dataDir}, args...)
		}
		handled, err := launchBackground(backgroundArgs, *dataDir, lock.BackgroundFile())
		if err != nil {
			cliFprintln(os.Stderr, err)
			return 1
		}
		if handled {
			return 0
		}
	}
	if os.Getenv("GBCSERVER_DAEMON") == "1" {
		if err := writeBackgroundPID(*dataDir); err != nil {
			cliFprintln(os.Stderr, err)
			return 1
		}
	}
	defer cleanupBackgroundPID(*dataDir)
	// start 是单一编排入口，同时启动 Web/API、白名单和兑换 listener。
	// Minecraft JAR 仍由 Leaves/Paper 游戏服加载，不在此进程内执行。
	return runManagedStart(*dataDir, "", *tray)
}

// runPortCommand 修改数据库中的 Web 端口。端口命令是顶层命令，不属于 start 的启动参数。
func runPortCommand(commandName, key string, args []string) int {
	flags := newCommandFlags(commandName, "<端口> [--data-dir <data目录>]")
	dataDir := flags.String("data-dir", defaultDataDir(), "运行实例使用的 data 文件夹本身")
	value, ok, code := parseSingleValueCommand(flags, args, "端口号")
	if !ok {
		return code
	}
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		cliFprintln(flags.Output(), "端口必须是 1-65535 之间的整数")
		return 2
	}
	resolvedDataDir, err := filepath.Abs(strings.TrimSpace(*dataDir))
	if err != nil || strings.TrimSpace(*dataDir) == "" {
		cliFprintln(os.Stderr, "--data-dir 无效")
		return 2
	}
	resolvedDataDir, _, err = bootstrapDeployment(resolvedDataDir, "")
	if err != nil {
		cliFprintln(os.Stderr, "准备数据库失败:", err)
		return 1
	}
	db, err := openNormalizedDB(filepath.Join(resolvedDataDir, "gbcserver.db"))
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	if err := (store.ConfigStore{DB: db}).Set(context.Background(), key, strconv.Itoa(port)); err != nil {
		cliFprintln(os.Stderr, "写入数据库失败:", err)
		return 1
	}
	cliFprintf(os.Stdout, "%s 已永久写入数据库：%d\n", commandName, port)
	return 0
}

func parseSingleValueCommand(flags *flag.FlagSet, args []string, valueName string) (string, bool, int) {
	for _, argument := range args {
		if argument == "--help" || argument == "-h" || argument == "-help" {
			flags.SetOutput(os.Stdout)
			flags.Usage()
			return "", false, 0
		}
	}
	// 允许值写在 --data-dir 前后，例如 command value --data-dir ./data。
	flagArgs := make([]string, 0, len(args))
	positionArgs := make([]string, 0, 1)
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--data-dir" && index+1 < len(args) {
			flagArgs = append(flagArgs, argument, args[index+1])
			index++
			continue
		}
		if strings.HasPrefix(argument, "--data-dir=") {
			flagArgs = append(flagArgs, argument)
			continue
		}
		positionArgs = append(positionArgs, argument)
	}
	if err := parseFlagsWithoutEcho(flags, append(flagArgs, positionArgs...)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", false, 0
		}
		return "", false, 2
	}
	if flags.NArg() != 1 {
		cliFprintln(flags.Output(), cliSprintf("必须提供一个%s；请使用：%s <值> [--data-dir <data目录>]", cliText(valueName), flags.Name()))
		return "", false, 2
	}
	return flags.Arg(0), true, 0
}

func runAdminValueCommand(commandName, key string, args []string) int {
	flags := newCommandFlags(commandName, "<值> [--data-dir <data目录>]")
	dataDir := flags.String("data-dir", defaultDataDir(), "运行实例使用的 data 文件夹本身")
	value, ok, code := parseSingleValueCommand(flags, args, "配置值")
	if !ok {
		return code
	}
	value = strings.TrimSpace(value)
	if key == "adminPath" {
		value = path.Clean("/" + strings.TrimSpace(value))
		if value == "/" || strings.ContainsAny(value, "?#") || strings.Contains(value, " ") {
			cliFprintln(os.Stderr, "管理员入口必须是非根路径，且不能包含空格、? 或 #")
			return 2
		}
	}
	if value == "" {
		cliFprintln(os.Stderr, "配置值不能为空")
		return 2
	}
	if err := writeDatabaseConfigValue(*dataDir, key, value); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	cliFprintf(os.Stdout, "%s 已永久写入数据库。\n", commandName)
	return 0
}

func runAdminPasswordCommand(args []string) int {
	flags := newCommandFlags("admin-password", "<密码> [--data-dir <data目录>]")
	dataDir := flags.String("data-dir", defaultDataDir(), "运行实例使用的 data 文件夹本身")
	password, ok, code := parseSingleValueCommand(flags, args, "管理员密码")
	if !ok {
		return code
	}
	if len([]byte(password)) < 8 || len([]byte(password)) > 72 {
		cliFprintln(os.Stderr, "管理员密码必须为 8-72 字节")
		return 2
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		cliFprintln(os.Stderr, "生成管理员密码哈希失败:", err)
		return 1
	}
	if err := writeDatabaseConfigValue(*dataDir, "adminPasswordHash", string(hash)); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	cliFprintln(os.Stdout, "admin-password 已将 bcrypt 哈希永久写入数据库。")
	return 0
}

func writeDatabaseConfigValue(dataDir, key, value string) error {
	resolvedDataDir, _, err := bootstrapDeployment(dataDir, "")
	if err != nil {
		return fmt.Errorf("准备数据库失败: %w", err)
	}
	db, err := openNormalizedDB(filepath.Join(resolvedDataDir, "gbcserver.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := (store.ConfigStore{DB: db}).Set(context.Background(), key, value); err != nil {
		return fmt.Errorf("写入数据库配置失败: %w", err)
	}
	return nil
}

// runLockedService 让单独诊断 listener 也遵守整机单实例约束。
func runLockedService(args []string, run func() int) int {
	for _, argument := range args {
		if argument == "--help" || argument == "-h" || argument == "-help" {
			return run()
		}
	}
	lock, err := acquireInstanceLock()
	if errors.Is(err, errInstanceAlreadyRunning) {
		cliFprintln(os.Stdout, "GBC Server 已在运行；本次命令未执行。")
		return 0
	}
	if err != nil {
		cliFprintln(os.Stderr, "获取单实例锁失败:", err)
		return 1
	}
	defer lock.Release()
	return run()
}

func runAutostart(args []string, install bool) int {
	flags := newCommandFlags(map[bool]string{true: "install-autostart", false: "uninstall-autostart"}[install], "[--data-dir <目录>]")
	dataDir := flags.String("data-dir", defaultDataDir(), "默认存储根目录")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if err := configureAutostartPlatform(*dataDir, install); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if install {
		cliFprintln(os.Stdout, "GBC Server 已配置为当前用户开机自启；启动项会一体启动 Web/API、白名单和兑换 listener。")
	} else {
		cliFprintln(os.Stdout, "GBC Server 当前用户开机自启已移除。")
	}
	return 0
}

func runWebServe(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWebServeWithContext(args, ctx)
}

func runWebServeWithContext(args []string, ctx context.Context) int {
	return runWebServeWithReady(args, ctx, nil)
}

func runWebServeWithReady(args []string, ctx context.Context, ready chan<- string) int {
	return runWebServeWithAdminControl(args, ctx, ready, nil)
}

func runWebServeWithAdminControl(args []string, ctx context.Context, ready chan<- string, adminControl func(string) error) int {
	flags := newCommandFlags("web-serve", "[--data-dir <目录>] [--db <规范化SQLite>] [选项]")
	dataDir := flags.String("data-dir", defaultDataDir(), "默认存储根目录；未指定时按平台选择")
	dbPath := flags.String("db", "", "规范化 SQLite 文件路径；未指定时为 <data-dir>/gbcserver.db")
	listenAddress := flags.String("listen", "0.0.0.0:80", "HTTP 监听 IP:端口；默认 0.0.0.0:80")
	httpsListenAddress := flags.String("https-listen", "0.0.0.0:443", "HTTPS 监听 IP:端口；仅在 TLS 证书有效时启动")
	tlsKeyFile := flags.String("tls-key-file", "", "TLS 私钥；默认 <data-dir>/server.key")
	tlsCertFile := flags.String("tls-cert-file", "", "TLS 证书链；默认 <data-dir>/server.pem")
	staticDir := flags.String("static-dir", "", "静态资源目录；未指定时为 <data-dir>/web")
	avatarDir := flags.String("avatar-dir", "", "头像资源目录；未指定时为 <data-dir>/avatars")
	bodyLimit := flags.Int64("body-limit", 25_000_000, "旧 API JSON/form 请求体最大字节数")
	serverID := flags.String("server-id", "default", "网页授权使用的插件服务 ID（白名单与兑换共用）")
	webhookPath := flags.String("webhook-path", "/webhook/update-port", "端口更新 webhook 路径")
	signTimezone := flags.Int("sign-timezone-offset", 8, "登录和签到时间相对 UTC 的固定小时偏移；默认 GMT+8")
	maxOnlineTime := flags.Duration("max-online-time", time.Duration(3214080000000)*time.Millisecond, "玩家网页授权最长时长")
	onlineMode := flags.Bool("online-mode", false, "注册时是否向 Mojang 查询正版 UUID；默认使用离线 UUID")
	adminUsername := flags.String("admin-username", "", "管理员账号；诊断模式临时覆盖数据库配置")
	adminPasswordHash := flags.String("admin-password-hash", "", "管理员 bcrypt 密码哈希；诊断模式临时覆盖数据库配置")
	adminTOTPSecret := flags.String("admin-totp-secret", "", "管理员 TOTP secret；诊断模式临时覆盖数据库配置")
	adminPath := flags.String("admin-path", "/admin", "管理员安全入口路径")
	mailHost := flags.String("mail-host", "", "SMTP 主机")
	mailPort := flags.Int("mail-port", 465, "SMTP 端口")
	mailUser := flags.String("mail-username", "", "SMTP 用户名")
	mailPassword := flags.String("mail-password", "", "SMTP 密码")
	mailFromName := flags.String("mail-from-name", "", "邮件显示发件人")
	mailFromAddress := flags.String("mail-from-address", "", "邮件发件地址")
	mailSecure := flags.Bool("mail-secure", true, "SMTP 是否使用 SMTPS/STARTTLS；关闭须写 --mail-secure=false")
	statusServers := flags.String("status-servers", "", "状态目标 JSON 列表；兼容 name=host:port，名称支持中文")
	statusAPIs := flags.String("status-apis", "", "状态 API JSON 列表；兼容 name=url，空值使用默认三家，none 禁用")
	statusTimeout := flags.Duration("status-timeout", 5*time.Second, "Minecraft 状态探测超时")
	statusCacheTTL := flags.Duration("status-cache-ttl", 25*time.Second, "状态缓存时间")
	statusMonitorInterval := time.Minute
	statusAlertAfter := 2 * time.Minute
	statusAlertEmailEnabled := false
	statusAlertEmail := ""
	statusAlertWebhookEnabled := false
	statusAlertWebhookURL := ""
	statusAlertRecoveryEnabled := true
	mojangEndpoint := flags.String("mojang-endpoint", "https://api.mojang.com/users/profiles/minecraft/", "Mojang profile API 地址")
	mojangTimeout := flags.Duration("mojang-timeout", 5*time.Second, "Mojang 查询超时")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if strings.TrimSpace(*dataDir) == "" {
		*dataDir = defaultDataDir()
	}
	if err := resolveDefaultStoragePaths(dataDir, dbPath, staticDir, avatarDir); err != nil {
		cliFprintln(os.Stderr, err)
		return 2
	}
	if strings.TrimSpace(*dbPath) == "" {
		cliFprintln(os.Stderr, "存储路径解析失败：--data-dir 或 --db 不能为空")
		return 2
	}
	if strings.TrimSpace(*tlsKeyFile) == "" {
		*tlsKeyFile = filepath.Join(*dataDir, "server.key")
	}
	if strings.TrimSpace(*tlsCertFile) == "" {
		*tlsCertFile = filepath.Join(*dataDir, "server.pem")
	}
	db, err := openNormalizedDB(*dbPath)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	configStore := store.ConfigStore{DB: db}
	loadConfigValue := func(key, fallback string) string {
		value, ok, loadErr := configStore.Get(ctx, key)
		if loadErr == nil && ok && strings.TrimSpace(value) != "" {
			return value
		}
		return fallback
	}
	isExplicit := func(name string) bool {
		explicit := false
		flags.Visit(func(option *flag.Flag) {
			if option.Name == name {
				explicit = true
			}
		})
		return explicit
	}
	configFlagNames := map[string]string{
		"staticDir": "static-dir", "avatarDir": "avatar-dir", "tlsKeyFile": "tls-key-file", "tlsCertFile": "tls-cert-file",
		"bodyLimitBytes": "body-limit", "serverID": "server-id", "webhookPath": "webhook-path", "signTimezoneOffset": "sign-timezone-offset",
		"maxOnlineTime": "max-online-time", "onlineMode": "online-mode", "adminUsername": "admin-username", "adminPasswordHash": "admin-password-hash",
		"adminTOTPSecret": "admin-totp-secret", "adminPath": "admin-path", "mailHost": "mail-host", "mailPort": "mail-port",
		"mailUsername": "mail-username", "mailPassword": "mail-password", "mailFromName": "mail-from-name", "mailFromAddress": "mail-from-address",
		"mailSecure": "mail-secure", "statusServers": "status-servers", "statusAPIs": "status-apis", "statusTimeout": "status-timeout",
		"statusCacheTTL": "status-cache-ttl", "mojangEndpoint": "mojang-endpoint", "mojangTimeout": "mojang-timeout",
	}
	isConfigExplicit := func(key string) bool {
		if flagName, ok := configFlagNames[key]; ok {
			return isExplicit(flagName)
		}
		return isExplicit(key)
	}
	applyString := func(key string, target *string) {
		if !isConfigExplicit(key) {
			*target = loadConfigValue(key, *target)
		}
	}
	applyInt := func(key string, target *int) error {
		if isConfigExplicit(key) {
			return nil
		}
		value := strings.TrimSpace(loadConfigValue(key, strconv.Itoa(*target)))
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return fmt.Errorf("数据库配置 %s 必须是整数", key)
		}
		*target = parsed
		return nil
	}
	applyInt64 := func(key string, target *int64) error {
		if isConfigExplicit(key) {
			return nil
		}
		value := strings.TrimSpace(loadConfigValue(key, strconv.FormatInt(*target, 10)))
		parsed, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return fmt.Errorf("数据库配置 %s 必须是整数", key)
		}
		*target = parsed
		return nil
	}
	applyBool := func(key string, target *bool) error {
		if isConfigExplicit(key) {
			return nil
		}
		value := strings.TrimSpace(loadConfigValue(key, strconv.FormatBool(*target)))
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return fmt.Errorf("数据库配置 %s 必须是 true 或 false", key)
		}
		*target = parsed
		return nil
	}
	applyDuration := func(key string, target *time.Duration) error {
		if isConfigExplicit(key) {
			return nil
		}
		value := strings.TrimSpace(loadConfigValue(key, target.String()))
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil {
			return fmt.Errorf("数据库配置 %s 必须是 Go duration，例如 5s", key)
		}
		*target = parsed
		return nil
	}
	for _, item := range []struct {
		key    string
		target *string
	}{
		{"staticDir", staticDir}, {"avatarDir", avatarDir}, {"tlsKeyFile", tlsKeyFile}, {"tlsCertFile", tlsCertFile},
		{"serverID", serverID}, {"webhookPath", webhookPath}, {"adminUsername", adminUsername}, {"adminPasswordHash", adminPasswordHash},
		{"adminTOTPSecret", adminTOTPSecret}, {"adminPath", adminPath}, {"mailHost", mailHost}, {"mailUsername", mailUser},
		{"mailPassword", mailPassword}, {"mailFromName", mailFromName}, {"mailFromAddress", mailFromAddress}, {"statusServers", statusServers},
		{"statusAPIs", statusAPIs}, {"statusAlertEmail", &statusAlertEmail}, {"statusAlertWebhookURL", &statusAlertWebhookURL},
		{"mojangEndpoint", mojangEndpoint},
	} {
		applyString(item.key, item.target)
	}
	if configured := loadConfigValue("httpPort", ""); configured != "" && !isExplicit("listen") {
		port, parseErr := strconv.Atoi(configured)
		if parseErr != nil {
			cliFprintln(os.Stderr, "数据库配置 httpPort 必须是整数")
			return 1
		}
		*listenAddress = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	}
	if configured := loadConfigValue("httpsPort", ""); configured != "" && !isExplicit("https-listen") {
		port, parseErr := strconv.Atoi(configured)
		if parseErr != nil {
			cliFprintln(os.Stderr, "数据库配置 httpsPort 必须是整数")
			return 1
		}
		*httpsListenAddress = net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	}
	if err := applyInt64("bodyLimitBytes", bodyLimit); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyInt("mailPort", mailPort); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyInt("signTimezoneOffset", signTimezone); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyBool("onlineMode", onlineMode); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyBool("mailSecure", mailSecure); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyDuration("maxOnlineTime", maxOnlineTime); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyDuration("statusTimeout", statusTimeout); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyDuration("statusCacheTTL", statusCacheTTL); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyDuration("statusMonitorInterval", &statusMonitorInterval); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyDuration("statusAlertAfter", &statusAlertAfter); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyBool("statusAlertEmailEnabled", &statusAlertEmailEnabled); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyBool("statusAlertWebhookEnabled", &statusAlertWebhookEnabled); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyBool("statusAlertRecoveryEnabled", &statusAlertRecoveryEnabled); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := applyDuration("mojangTimeout", mojangTimeout); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	for key, target := range map[string]*string{"staticDir": staticDir, "avatarDir": avatarDir, "tlsKeyFile": tlsKeyFile, "tlsCertFile": tlsCertFile} {
		if !isConfigExplicit(key) && !filepath.IsAbs(strings.TrimSpace(*target)) {
			*target = filepath.Join(*dataDir, strings.TrimSpace(*target))
		}
	}
	if err := os.MkdirAll(*staticDir, 0o750); err != nil {
		cliFprintln(os.Stderr, "创建静态资源目录失败:", err)
		return 1
	}
	if err := os.MkdirAll(*avatarDir, 0o750); err != nil {
		cliFprintln(os.Stderr, "创建头像目录失败:", err)
		return 1
	}
	if err := validateWebListenAddress(*listenAddress); err != nil {
		cliFprintln(os.Stderr, "--listen 无效:", err)
		return 2
	}
	if err := validateWebListenAddress(*httpsListenAddress); err != nil {
		cliFprintln(os.Stderr, "--https-listen 无效:", err)
		return 2
	}
	if *bodyLimit < 1024 || *bodyLimit > 256*1024*1024 {
		cliFprintln(os.Stderr, "--body-limit 必须在 1 KiB 至 256 MiB 之间")
		return 2
	}
	if *mojangTimeout <= 0 || *mojangTimeout > 30*time.Second {
		cliFprintln(os.Stderr, "--mojang-timeout 必须在 1ns 至 30s 之间")
		return 2
	}
	if *mailPort < 1 || *mailPort > 65535 || *statusTimeout <= 0 || *statusCacheTTL <= 0 || *maxOnlineTime <= 0 || *signTimezone < -12 || *signTimezone > 14 {
		cliFprintln(os.Stderr, "配置范围无效：SMTP 端口须为 1–65535，状态超时、缓存和授权时长须大于 0，时区须为 -12 至 14")
		return 2
	}
	if statusMonitorInterval < 5*time.Second || statusMonitorInterval > 24*time.Hour || statusAlertAfter < 5*time.Second || statusAlertAfter > 30*24*time.Hour {
		cliFprintln(os.Stderr, "状态采样间隔必须在 5s 至 24h 之间，离线告警阈值必须在 5s 至 30d 之间")
		return 2
	}
	parsedStatusServers, err := parseStatusServers(*statusServers)
	if err != nil {
		cliFprintln(os.Stderr, "--status-servers 无效:", err)
		return 2
	}
	parsedStatusAPIs, err := parseStatusAPIs(*statusAPIs)
	if err != nil {
		cliFprintln(os.Stderr, "--status-apis 无效:", err)
		return 2
	}
	var mailSender httpapi.MailSender
	if strings.TrimSpace(*mailHost) != "" {
		if strings.TrimSpace(*mailFromAddress) == "" {
			cliFprintln(os.Stderr, "配置 SMTP 时必须提供 --mail-from-address 或管理员配置中的 mailFromAddress")
			return 2
		}
		mailSender = httpapi.SMTPMailSender{Host: strings.TrimSpace(*mailHost), Port: *mailPort, Username: *mailUser, Password: *mailPassword, FromName: *mailFromName, FromAddress: strings.TrimSpace(*mailFromAddress), Secure: *mailSecure}
	}
	var mojangLookup httpapi.MojangLookup
	if *onlineMode {
		mojangLookup = httpapi.NewHTTPMojangLookup(*mojangEndpoint, *mojangTimeout)
	}
	handler, err := httpapi.New(db, *staticDir, *avatarDir, httpapi.Config{
		StaticFS:       embeddedStaticFS(),
		BodyLimitBytes: *bodyLimit, ServerID: *serverID,
		WebhookPath:        *webhookPath,
		SignTimezoneOffset: *signTimezone, MaxOnlineTime: *maxOnlineTime,
		SignTimezoneExplicit: true,
		AdminUsername:        *adminUsername, AdminPasswordHash: *adminPasswordHash, AdminTOTPSecret: *adminTOTPSecret,
		AdminPath: *adminPath, AdminControl: adminControl,
		MailSender: mailSender, StatusServers: parsedStatusServers, StatusAPIs: parsedStatusAPIs, StatusTimeout: *statusTimeout, StatusCacheTTL: *statusCacheTTL,
		StatusMonitorInterval: statusMonitorInterval, StatusAlertAfter: statusAlertAfter,
		StatusAlertEmailEnabled: statusAlertEmailEnabled, StatusAlertEmail: statusAlertEmail,
		StatusAlertWebhookEnabled: statusAlertWebhookEnabled, StatusAlertWebhookURL: statusAlertWebhookURL,
		StatusAlertRecoveryEnabled: statusAlertRecoveryEnabled, StatusAlertRecoveryExplicit: true,
		GitHubRelease: githubReleaseBuild,
		MojangLookup:  mojangLookup,
	})
	if err != nil {
		cliFprintln(os.Stderr, "创建 Web handler 失败:", err)
		return 1
	}
	handler.StartStatusMonitor(ctx)
	var certificate *tls.Certificate
	loadedCertificate, err := tls.LoadX509KeyPair(*tlsCertFile, *tlsKeyFile)
	if err != nil {
		cliFprintf(os.Stderr, "未启用 HTTPS：无法识别 TLS 证书或私钥（证书 %q，私钥 %q）：%v；仅启动 HTTP。\n", *tlsCertFile, *tlsKeyFile, err)
	} else {
		certificate = &loadedCertificate
	}
	if err := serveWebListenersWithReady(ctx, handler, strings.TrimSpace(*listenAddress), strings.TrimSpace(*httpsListenAddress), certificate, ready); err != nil {
		cliFprintln(os.Stderr, "Web listener 退出:", err)
		return 1
	}
	return 0
}

func embeddedStaticFS() fs.FS {
	static, err := fs.Sub(embeddedWebFiles, "web")
	if err != nil {
		return nil
	}
	return static
}

func parseStatusServers(value string) ([]httpapi.StatusServerConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") {
		if servers, ok, err := parseStatusServersJSON(value); ok {
			return servers, err
		}
	}
	servers := make([]httpapi.StatusServerConfig, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		name := strings.TrimSpace(parts[0])
		if len(parts) != 2 || name == "" {
			return nil, fmt.Errorf("目标必须是 name=host:port: %s", item)
		}
		if err := validateStatusName(name); err != nil {
			return nil, err
		}
		host, portText, err := net.SplitHostPort(strings.TrimSpace(parts[1]))
		if err != nil || strings.TrimSpace(host) == "" {
			return nil, fmt.Errorf("目标地址无效: %s", item)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("目标端口无效: %s", item)
		}
		servers = append(servers, httpapi.StatusServerConfig{Name: name, Host: host, DefaultPort: port})
	}
	return servers, nil
}

func parseStatusAPIs(value string) ([]httpapi.StatusAPIConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.EqualFold(value, "none") {
		return []httpapi.StatusAPIConfig{}, nil
	}
	if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") {
		if apis, ok, err := parseStatusAPIsJSON(value); ok {
			return apis, err
		}
	}
	apis := make([]httpapi.StatusAPIConfig, 0)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.SplitN(item, "=", 2)
		name := strings.TrimSpace(parts[0])
		if len(parts) != 2 || name == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("接口必须是 name=url: %s", item)
		}
		if err := validateStatusName(name); err != nil {
			return nil, err
		}
		endpoint := strings.TrimSpace(parts[1])
		if err := validateStatusURL(endpoint); err != nil {
			return nil, fmt.Errorf("接口 URL 无效: %s (%w)", item, err)
		}
		apis = append(apis, httpapi.StatusAPIConfig{Name: name, URLTemplate: endpoint})
	}
	return apis, nil
}

func runCompressImages(args []string) int {
	flags := newCommandFlags("compress-images", "[--data-dir <data目录>] [--max-dimension <像素>] [--quality <1-100>] [--dry-run]")
	dataDir := flags.String("data-dir", defaultDataDir(), "运行数据文件夹本身")
	maxDimension := flags.Int("max-dimension", 512, "图片最长边上限，头像使用更小的 256 像素上限")
	quality := flags.Int("quality", 82, "JPEG 质量，范围 1-100")
	dryRun := flags.Bool("dry-run", false, "只统计将要压缩的文件，不写入磁盘")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if *maxDimension < 32 || *maxDimension > 4096 || *quality < 1 || *quality > 100 {
		cliFprintln(os.Stderr, "--max-dimension 必须在 32-4096 之间，--quality 必须在 1-100 之间")
		return 2
	}
	resolvedDataDir, _, err := bootstrapDeployment(*dataDir, "")
	if err != nil {
		cliFprintln(os.Stderr, "准备数据目录失败:", err)
		return 1
	}
	db, err := openNormalizedDB(filepath.Join(resolvedDataDir, "gbcserver.db"))
	if err != nil {
		cliFprintln(os.Stderr, "读取运行配置失败:", err)
		return 1
	}
	configStore := store.ConfigStore{DB: db}
	avatarDir, _, avatarErr := configStore.Get(context.Background(), "avatarDir")
	staticDir, _, staticErr := configStore.Get(context.Background(), "staticDir")
	closeErr := db.Close()
	if avatarErr != nil || staticErr != nil || closeErr != nil {
		cliFprintln(os.Stderr, "读取图片目录配置失败:", errors.Join(avatarErr, staticErr, closeErr))
		return 1
	}
	if strings.TrimSpace(avatarDir) == "" {
		avatarDir = filepath.Join(resolvedDataDir, "avatars")
	} else if !filepath.IsAbs(avatarDir) {
		avatarDir = filepath.Join(resolvedDataDir, avatarDir)
	}
	if strings.TrimSpace(staticDir) == "" {
		staticDir = filepath.Join(resolvedDataDir, "web")
	} else if !filepath.IsAbs(staticDir) {
		staticDir = filepath.Join(resolvedDataDir, staticDir)
	}
	roots := []struct {
		path         string
		maxDimension int
	}{{avatarDir, minInt(*maxDimension, 256)}, {filepath.Join(staticDir, "items"), *maxDimension}}
	var scanned, changed, skipped int
	for _, root := range roots {
		if _, err := os.Stat(root.path); os.IsNotExist(err) {
			continue
		}
		if err := filepath.WalkDir(root.path, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" && ext != ".webp" {
				return nil
			}
			scanned++
			data, readErr := os.ReadFile(filePath)
			if readErr != nil {
				return fmt.Errorf("读取 %s 失败: %w", filePath, readErr)
			}
			compressed, didChange, compressErr := imageutil.Compress(data, ext, imageutil.Options{MaxDimension: root.maxDimension, JPEGQuality: *quality})
			if compressErr != nil {
				return fmt.Errorf("处理 %s 失败: %w", filePath, compressErr)
			}
			if !didChange {
				skipped++
				return nil
			}
			changed++
			if !*dryRun {
				info, infoErr := entry.Info()
				if infoErr != nil {
					return infoErr
				}
				if writeErr := os.WriteFile(filePath, compressed, info.Mode().Perm()); writeErr != nil {
					return fmt.Errorf("写入 %s 失败: %w", filePath, writeErr)
				}
			}
			return nil
		}); err != nil {
			cliFprintln(os.Stderr, err)
			return 1
		}
	}
	prefix := "已压缩"
	if *dryRun {
		prefix = "预计压缩"
	}
	cliFprintf(os.Stdout, "%s %d/%d 个图片文件，已跳过 %d 个无需改写的文件。\n", prefix, changed, scanned, skipped)
	return 0
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// statusServerJSON 同时兼容 Go 管理页保存的 port 和旧 Node 配置的 defaultPort。
type statusServerJSON struct {
	Name        string          `json:"name"`
	Host        string          `json:"host"`
	Port        json.RawMessage `json:"port"`
	DefaultPort json.RawMessage `json:"defaultPort"`
}

type statusAPIJSON struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLUpper    string `json:"URL"`
	URLTemplate string `json:"URLTemplate"`
	Template    string `json:"template"`
}

func parseStatusServersJSON(value string) ([]httpapi.StatusServerConfig, bool, error) {
	var entries []statusServerJSON
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, true, fmt.Errorf("状态目标 JSON 无效: %w", err)
		}
	} else {
		var envelope struct {
			Servers []statusServerJSON `json:"servers"`
		}
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.Servers == nil {
			return nil, false, nil
		}
		entries = envelope.Servers
	}
	servers := make([]httpapi.StatusServerConfig, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		host, hostErr := normalizeStatusHost(entry.Host)
		if err := validateStatusName(name); err != nil {
			return nil, true, err
		}
		if hostErr != nil {
			return nil, true, fmt.Errorf("状态目标 %s 主机无效: %w", name, hostErr)
		}
		port, err := statusJSONPort(entry.Port)
		if err != nil {
			return nil, true, fmt.Errorf("状态目标 %s 端口无效: %w", name, err)
		}
		if port == 0 {
			port, err = statusJSONPort(entry.DefaultPort)
			if err != nil {
				return nil, true, fmt.Errorf("状态目标 %s defaultPort 无效: %w", name, err)
			}
		}
		if port == 0 {
			if parsedHost, parsedPort, splitErr := net.SplitHostPort(host); splitErr == nil {
				host = parsedHost
				port, _ = strconv.Atoi(parsedPort)
			}
		}
		if port < 1 || port > 65535 {
			return nil, true, fmt.Errorf("状态目标 %s 端口必须是 1-65535", name)
		}
		servers = append(servers, httpapi.StatusServerConfig{Name: name, Host: host, DefaultPort: port})
	}
	return servers, true, nil
}

func normalizeStatusHost(value string) (string, error) {
	host := strings.TrimSpace(value)
	if host == "" || strings.ContainsAny(host, " \t\r\n/?#@\\") {
		return "", errors.New("主机不能为空，且不能包含空白或路径字符")
	}
	ipText := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if ip := net.ParseIP(ipText); ip != nil {
		return ip.String(), nil
	}
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.Host == "" || parsed.Hostname() != host || parsed.Port() != "" {
		return "", errors.New("请只填写域名或 IP，不要包含端口")
	}
	return host, nil
}

func parseStatusAPIsJSON(value string) ([]httpapi.StatusAPIConfig, bool, error) {
	var entries []statusAPIJSON
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			return nil, true, fmt.Errorf("状态 API JSON 无效: %w", err)
		}
	} else {
		var envelope struct {
			APIs []statusAPIJSON `json:"apis"`
		}
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.APIs == nil {
			return nil, false, nil
		}
		entries = envelope.APIs
	}
	apis := make([]httpapi.StatusAPIConfig, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name)
		endpoint := firstNonemptyStatusValue(entry.URL, entry.URLUpper, entry.URLTemplate, entry.Template)
		if err := validateStatusName(name); err != nil {
			return nil, true, err
		}
		if err := validateStatusURL(endpoint); err != nil {
			return nil, true, fmt.Errorf("状态 API %s URL 无效: %w", name, err)
		}
		apis = append(apis, httpapi.StatusAPIConfig{Name: name, URLTemplate: endpoint})
	}
	return apis, true, nil
}

func firstNonemptyStatusValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func statusJSONPort(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var port int
	if json.Unmarshal(raw, &port) == nil {
		return port, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return 0, nil
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(text))
		return parsed, err
	}
	return 0, errors.New("端口必须是整数")
}

func validateStatusName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("状态目标名称不能为空")
	}
	if strings.ContainsAny(name, "\r\n") {
		return errors.New("状态目标名称不能包含换行")
	}
	return nil
}

func validateStatusURL(endpoint string) error {
	endpoint = strings.TrimSpace(endpoint)
	lower := strings.ToLower(endpoint)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return errors.New("必须使用 HTTP 或 HTTPS")
	}
	probe := strings.NewReplacer("{hostport}", "example.com:25565", "{host}", "example.com", "{port}", "25565").Replace(endpoint)
	parsed, err := url.Parse(probe)
	if err != nil || parsed.Host == "" {
		return errors.New("主机地址无效")
	}
	return nil
}

func runInitDB(args []string) int {
	flags := newCommandFlags("init-db", "--db <新SQLite>")
	dbPath := flags.String("db", "", "新 SQLite 数据库文件路径")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if strings.TrimSpace(*dbPath) == "" {
		cliFprintln(os.Stderr, "必须提供 --db")
		return 2
	}
	abs, err := filepath.Abs(*dbPath)
	if err != nil {
		cliFprintln(os.Stderr, "解析数据库路径失败:", err)
		return 1
	}
	if _, err := os.Stat(abs); err == nil {
		cliFprintln(os.Stderr, "目标路径已存在；为避免意外覆盖，初始化已停止")
		return 1
	} else if !os.IsNotExist(err) {
		cliFprintln(os.Stderr, "检查目标路径失败:", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		cliFprintln(os.Stderr, "创建数据库目录失败:", err)
		return 1
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		cliFprintln(os.Stderr, "以防覆盖模式创建数据库失败:", err)
		return 1
	}
	if err := file.Close(); err != nil {
		cliFprintln(os.Stderr, "关闭新数据库文件失败:", err)
		return 1
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		cliFprintln(os.Stderr, "创建 SQLite 失败:", err)
		return 1
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;")
	if err == nil {
		err = store.Migrate(ctx, db)
	}
	if err == nil {
		err = db.Close()
	} else {
		db.Close()
	}
	if err != nil {
		cliFprintln(os.Stderr, "初始化数据库失败:", err)
		return 1
	}
	cliFprintln(os.Stdout, "规范化 SQLite 初始化完成。")
	return 0
}

func runUpgradeDB(args []string) int {
	flags := newCommandFlags("upgrade-db", "--db <已有规范化SQLite>")
	dbPath := flags.String("db", "", "已有规范化 SQLite 数据库文件路径")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if strings.TrimSpace(*dbPath) == "" {
		cliFprintln(os.Stderr, "必须提供 --db")
		return 2
	}
	abs, err := filepath.Abs(*dbPath)
	if err != nil {
		cliFprintln(os.Stderr, "解析数据库路径失败:", err)
		return 1
	}
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		cliFprintln(os.Stderr, "已有数据库文件不存在或路径无效")
		return 1
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		cliFprintln(os.Stderr, "打开 SQLite 失败:", err)
		return 1
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var migrationTable int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&migrationTable); err != nil {
		db.Close()
		cliFprintln(os.Stderr, "检查 schema_migrations 失败:", err)
		return 1
	}
	if migrationTable != 1 {
		db.Close()
		cliFprintln(os.Stderr, "数据库没有 schema_migrations；升级命令不会初始化新库")
		return 1
	}
	var currentVersion string
	if err := db.QueryRowContext(ctx,
		"SELECT version FROM schema_migrations ORDER BY applied_at DESC, rowid DESC LIMIT 1").Scan(&currentVersion); err != nil {
		db.Close()
		cliFprintln(os.Stderr, "读取 schema 版本失败:", err)
		return 1
	}
	if currentVersion != "0001_normalized" && currentVersion != "0002_plugin_login_attempts_leases" && currentVersion != "0003_app_config" && currentVersion != store.SchemaVersion {
		db.Close()
		cliFprintln(os.Stderr, "数据库 schema 版本不支持升级:", currentVersion)
		return 1
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err == nil {
		err = store.Migrate(ctx, db)
	}
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cliFprintln(os.Stderr, "升级 SQLite schema 失败:", err)
		return 1
	}
	cliFprintln(os.Stdout, "SQLite schema 已升级到", store.SchemaVersion)
	return 0
}

func runMigration(args []string) int {
	flags := newCommandFlags("migrate", "--source <旧SQLite> [--target <新SQLite>] [--apply]")
	source := flags.String("source", "", "旧版 SQLite 文件路径，只读")
	target := flags.String("target", "", "规范化 SQLite 的新路径")
	apply := flags.Bool("apply", false, "将数据写入全新的目标数据库；缺省只做 dry-run")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if *source == "" {
		cliFprintln(os.Stderr, "必须提供 --source")
		return 2
	}
	if *apply && *target == "" {
		cliFprintln(os.Stderr, "--apply 时必须提供 --target")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var report legacyimport.Report
	var err error
	if *apply {
		report, err = legacyimport.Import(ctx, *source, *target)
	} else {
		report, err = legacyimport.Analyze(ctx, *source)
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		cliFprintln(os.Stderr, "写出 JSON 报告失败:", encodeErr)
		return 1
	}
	if err != nil {
		cliFprintln(os.Stderr, err.Error())
		return 1
	}
	if !report.CanApply {
		return 1
	}
	return 0
}

func runPluginToken(args []string) int {
	if len(args) == 0 {
		return runInteractivePluginToken(nil)
	}
	switch args[0] {
	case "help", "--help", "-help", "-h":
		if len(args) != 1 {
			cliFprintln(os.Stderr, "请使用 gbcserver help plugin-token issue 或 revoke")
			return 2
		}
		cliFprintln(os.Stdout, "用法：gbcserver plugin-token issue|revoke [选项]\n  issue  --db <SQLite> --server-id <标识> --scope <whitelist|coupon.redeem> [--expires-at <RFC3339>]\n  revoke --db <SQLite> --token-id <token_id>\n使用 issue --help 或 revoke --help 查看具体选项。令牌明文只在签发时输出一次。")
		return 0
	case "issue":
		return issuePluginToken(args[1:])
	case "revoke":
		return revokePluginToken(args[1:])
	case "--data-dir":
		return runInteractivePluginToken(args)
	default:
		cliFprintln(os.Stderr, "未知令牌命令；请使用 gbcserver plugin-token --help")
		return 2
	}
}

func runPluginServe(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runPluginServeWithContext(args, ctx)
}

func runPluginServeWithContext(args []string, ctx context.Context) int {
	return runPluginServeWithReady(args, ctx, nil)
}

func runPluginServeWithReady(args []string, ctx context.Context, ready chan<- string) int {
	flags := newCommandFlags("plugin-serve", "[--data-dir <data目录>] [--db <SQLite>] [--whitelist-listen <IP:端口>] [--coupon-listen <IP:端口>]")
	dataDir := flags.String("data-dir", defaultDataDir(), "data 文件夹本身；未指定时按平台选择")
	dbPath := flags.String("db", "", "规范化 SQLite 文件路径；未指定时为 <data-dir>/gbcserver.db")
	whitelistAddress := flags.String("whitelist-listen", "127.0.0.1:18094", "白名单插件内网监听地址；跨机器时填写本机私网 IP:端口")
	couponAddress := flags.String("coupon-listen", "127.0.0.1:18095", "兑换插件内网监听地址；跨机器时填写本机私网 IP:端口")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if strings.TrimSpace(*dataDir) == "" {
		*dataDir = defaultDataDir()
	}
	if strings.TrimSpace(*dbPath) == "" {
		*dbPath = filepath.Join(*dataDir, "gbcserver.db")
	}
	if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*whitelistAddress) == "" || strings.TrimSpace(*couponAddress) == "" {
		cliFprintln(os.Stderr, "监听地址不能为空；--db 未提供时默认使用 <data-dir>/gbcserver.db")
		return 2
	}
	db, err := openNormalizedDB(*dbPath)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	pluginConfigStore := store.ConfigStore{DB: db}
	expectedServerID := "default"
	if configured, ok, readErr := pluginConfigStore.Get(ctx, "serverID"); readErr != nil {
		cliFprintln(os.Stderr, "读取数据库 serverID 配置失败:", readErr)
		return 1
	} else if ok && strings.TrimSpace(configured) != "" {
		expectedServerID = strings.TrimSpace(configured)
	}
	signTimezoneOffset := 8
	signTimezoneExplicit := false
	if configured, ok, readErr := pluginConfigStore.Get(ctx, "signTimezoneOffset"); readErr != nil {
		cliFprintln(os.Stderr, "读取数据库 signTimezoneOffset 配置失败:", readErr)
		return 1
	} else if ok && strings.TrimSpace(configured) != "" {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(configured))
		if parseErr != nil || parsed < -12 || parsed > 14 {
			cliFprintln(os.Stderr, "数据库配置 signTimezoneOffset 必须是 -12 至 14 之间的整数")
			return 1
		}
		signTimezoneOffset = parsed
		signTimezoneExplicit = true
	}
	whitelistExplicit, couponExplicit := false, false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "whitelist-listen" {
			whitelistExplicit = true
		}
		if option.Name == "coupon-listen" {
			couponExplicit = true
		}
	})
	if configured, ok, readErr := pluginConfigStore.Get(ctx, "whitelistListen"); readErr == nil && ok && !whitelistExplicit && strings.TrimSpace(configured) != "" {
		*whitelistAddress = configured
	}
	if configured, ok, readErr := pluginConfigStore.Get(ctx, "couponListen"); readErr == nil && ok && !couponExplicit && strings.TrimSpace(configured) != "" {
		*couponAddress = configured
	}
	err = plugin.ServeWithReady(ctx, db, plugin.ListenerConfig{
		WhitelistAddress: strings.TrimSpace(*whitelistAddress),
		CouponAddress:    strings.TrimSpace(*couponAddress),
		ExpectedServerID: expectedServerID, SignTimezoneOffsetHours: signTimezoneOffset, SignTimezoneExplicit: signTimezoneExplicit,
	}, ready)
	if err != nil {
		cliFprintln(os.Stderr, "plugin-serve 退出:", err)
		return 1
	}
	return 0
}

func issuePluginToken(args []string) int {
	flags := newCommandFlags("plugin-token issue", "--db <SQLite> --server-id <标识> --scope <whitelist|coupon.redeem> [选项]")
	dbPath := flags.String("db", "", "规范化 SQLite 文件路径")
	serverID := flags.String("server-id", "", "游戏服稳定标识；必须与数据库 serverID 配置一致")
	scopeValue := flags.String("scope", "", "token 权限：whitelist 或 coupon.redeem")
	expiresAtValue := flags.String("expires-at", "", "RFC3339 过期时间；留空表示不设过期时间")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*serverID) == "" || strings.TrimSpace(*scopeValue) == "" {
		cliFprintln(os.Stderr, "--db、--server-id、--scope 均为必填项")
		return 2
	}
	scope := plugin.Scope(strings.TrimSpace(*scopeValue))
	if scope != plugin.ScopeWhitelist && scope != plugin.ScopeCouponRedeem {
		cliFprintln(os.Stderr, "--scope 必须是 whitelist 或 coupon.redeem")
		return 2
	}
	var expiresAt *time.Time
	if strings.TrimSpace(*expiresAtValue) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*expiresAtValue))
		if err != nil {
			cliFprintln(os.Stderr, "--expires-at 必须是 RFC3339:", err)
			return 2
		}
		expiresAt = &parsed
	}
	db, err := openNormalizedDB(*dbPath)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := validatePluginTokenServerID(ctx, db, *serverID); err != nil {
		cliFprintln(os.Stderr, err)
		return 2
	}
	issued, err := plugin.NewTokenStore(db).Issue(ctx, *serverID, scope, expiresAt)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	// Secret 只在签发命令的标准输出出现一次；日志和数据库不保存明文。
	if err := json.NewEncoder(os.Stdout).Encode(issued); err != nil {
		cliFprintln(os.Stderr, "写出 token 结果失败:", err)
		return 1
	}
	return 0
}

func validatePluginTokenServerID(ctx context.Context, db *sql.DB, requested string) error {
	configured, exists, err := (store.ConfigStore{DB: db}).Get(ctx, "serverID")
	if err != nil {
		return fmt.Errorf("读取数据库 serverID 配置失败: %w", err)
	}
	requested = strings.TrimSpace(requested)
	configured = strings.TrimSpace(configured)
	if !exists || configured == "" {
		configured = "default"
	}
	if requested != configured {
		return fmt.Errorf("--server-id %q 必须与数据库 serverID 配置 %q 一致；白名单和兑换令牌共用服务 ID，但必须分别签发", requested, configured)
	}
	return nil
}

func revokePluginToken(args []string) int {
	flags := newCommandFlags("plugin-token revoke", "--db <SQLite> --token-id <token_id>")
	dbPath := flags.String("db", "", "规范化 SQLite 文件路径")
	tokenID := flags.String("token-id", "", "要撤销的 token_id")
	if code, stop := parseCommandFlags(flags, args); stop {
		return code
	}
	if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*tokenID) == "" {
		cliFprintln(os.Stderr, "--db 和 --token-id 均为必填项")
		return 2
	}
	db, err := openNormalizedDB(*dbPath)
	if err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := plugin.NewTokenStore(db).Revoke(ctx, *tokenID); err != nil {
		cliFprintln(os.Stderr, err)
		return 1
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"revoked": true, "token_id": strings.TrimSpace(*tokenID)}); err != nil {
		cliFprintln(os.Stderr, "写出撤销结果失败:", err)
		return 1
	}
	return 0
}

func openNormalizedDB(path string) (*sql.DB, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("数据库路径不能为空")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("解析数据库路径失败: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("规范化 SQLite 文件不存在或不可访问: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("规范化 SQLite 路径指向目录: %s", path)
	}
	db, err := sql.Open("sqlite", abs)
	if err != nil {
		return nil, fmt.Errorf("打开规范化 SQLite 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化 SQLite 连接失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接规范化 SQLite 失败: %w", err)
	}
	var schemaVersion string
	if err := db.QueryRow("SELECT version FROM schema_migrations ORDER BY applied_at DESC, rowid DESC LIMIT 1").Scan(&schemaVersion); err != nil || schemaVersion != store.SchemaVersion {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("数据库缺少规范化 schema_migrations，未初始化 token 表: %w", err)
		}
		return nil, fmt.Errorf("数据库 schema 版本不受支持: %s", schemaVersion)
	}
	var tokenTableCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='plugin_tokens'").Scan(&tokenTableCount); err != nil || tokenTableCount != 1 {
		db.Close()
		if err != nil {
			return nil, fmt.Errorf("检查 token 表失败: %w", err)
		}
		return nil, fmt.Errorf("数据库缺少 plugin_tokens 表")
	}
	return db, nil
}
