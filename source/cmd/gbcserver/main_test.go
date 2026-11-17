package main

import (
	"bytes"
	"context"
	"database/sql"
	"golang.org/x/crypto/bcrypt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gbcserver/source/internal/httpapi"
	"gbcserver/source/internal/imageutil"
	"gbcserver/source/internal/store"
)

func TestParseStatusAPIs(t *testing.T) {
	apis, err := parseStatusAPIs("primary=https://status.example/{hostport},backup=http://127.0.0.1:9000/{host}/{port}")
	if err != nil {
		t.Fatal(err)
	}
	if len(apis) != 2 || apis[0].Name != "primary" || apis[0].URLTemplate != "https://status.example/{hostport}" {
		t.Fatalf("状态 API 配置解析错误: %+v", apis)
	}
	if _, err := parseStatusAPIs("broken=file:///tmp/status"); err == nil {
		t.Fatal("非 HTTP 状态 API URL 应被拒绝")
	}
	apis, err = parseStatusAPIs(`[{"name":"api.mcsrvstat.us","url":"https://api.mcsrvstat.us/3/{hostport}"},{"name":"备用中文","URLTemplate":"http://127.0.0.1:9000/{host}/{port}"}]`)
	if err != nil || len(apis) != 2 || apis[1].Name != "备用中文" {
		t.Fatalf("JSON 状态 API 或中文名称解析错误: %+v %v", apis, err)
	}
}

func TestParseStatusServersSupportsNodeJSONAndChineseNames(t *testing.T) {
	servers, err := parseStatusServers(`[{"name":"电信线路","host":"v4.242774835.xyz","defaultPort":1259},{"name":"移动IPv6","host":"mc.242774835.xyz","port":"25565"}]`)
	if err != nil || len(servers) != 2 || servers[0].Name != "电信线路" || servers[0].DefaultPort != 1259 || servers[1].DefaultPort != 25565 {
		t.Fatalf("旧 Node JSON 状态目标解析错误: %+v %v", servers, err)
	}
	servers, err = parseStatusServers("电信线路=v4.242774835.xyz:1259,备用线路=127.0.0.1:25565")
	if err != nil || len(servers) != 2 || servers[0].Name != "电信线路" {
		t.Fatalf("旧逗号格式或中文名称解析错误: %+v %v", servers, err)
	}
	if _, err := parseStatusServers(`[{"name":"坏\n名称","host":"127.0.0.1","port":25565}]`); err == nil {
		t.Fatal("状态目标名称不应接受换行")
	}
}

func captureCommand(t *testing.T, args []string) (int, string) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "命令输出-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	originalOut, originalErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = output, output
	defer func() { os.Stdout, os.Stderr = originalOut, originalErr; output.Close() }()
	code := runCommand(args)
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(data)
}

func TestHelpDoesNotReadConfigOrRevealSecrets(t *testing.T) {
	t.Setenv("GBC_ADMIN_PASSWORD_HASH", "帮助不能泄露的哈希")
	t.Setenv("GBC_ADMIN_TOTP_SECRET", "帮助不能泄露的TOTP")
	t.Setenv("GBC_SMTP_PASSWORD", "帮助不能泄露的邮件密码")
	t.Setenv("GBC_MOJANG_TIMEOUT", "无效时间也不影响帮助")
	commands := [][]string{
		{"--help"}, {"-h"}, {"help"}, {"help", "web-serve"}, {"help", "plugin-token", "issue"},
		{"start", "--help"},
		{"init-db", "--help"}, {"upgrade-db", "-h"}, {"migrate", "--help"},
		{"plugin-token", "--help"}, {"plugin-token", "issue", "--help"}, {"plugin-token", "revoke", "--help"},
		{"web-serve", "--help"},
		{"plugin-serve", "--help"},
		{"compress-images", "--help"},
		{"reload", "--help"}, {"stop", "--help"}, {"httpport", "--help"}, {"httpsport", "--help"}, {"admin-path", "--help"}, {"admin-username", "--help"}, {"admin-password", "--help"},
	}
	for _, args := range commands {
		code, output := captureCommand(t, args)
		if code != 0 || !strings.Contains(output, "用法") {
			t.Fatalf("帮助 %v 返回 %d: %s", args, code, output)
		}
		if strings.Contains(output, "帮助不能泄露") || strings.Contains(output, "读取配置文件失败") {
			t.Fatalf("帮助读了运行配置或泄露敏感值: %v", args)
		}
		if len(args) == 2 && args[0] == "start" {
			for _, expected := range []string{"--data-dir <data目录>", "--background", "--tray", "--autostart", "--no-autostart"} {
				if !strings.Contains(output, expected) {
					t.Fatalf("start 帮助缺少 %q: %s", expected, output)
				}
			}
			if strings.Contains(output, defaultDataDir()) || strings.Contains(output, "default ") {
				t.Fatalf("start 帮助不应暴露本机路径或冗余默认值: %s", output)
			}
		}
	}
}

func TestCompressImagesCommandRewritesExistingImages(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if _, _, err := bootstrapDeployment(dataDir, ""); err != nil {
		t.Fatal(err)
	}
	sample := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			sample.SetRGBA(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 20, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, sample); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "web", "items"), 0o700); err != nil {
		t.Fatal(err)
	}
	imagePath := filepath.Join(dataDir, "web", "items", "large.png")
	if err := os.WriteFile(imagePath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := runCompressImages([]string{"--data-dir", dataDir, "--max-dimension", "64"}); code != 0 {
		t.Fatalf("compress-images 失败: %d", code)
	}
	compressed, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	width, height, err := imageutil.DecodeDimensions(compressed)
	if err != nil || width > 64 || height > 32 || width*2 != height*4 {
		t.Fatalf("历史商品图片未按命令压缩: %dx%d err=%v", width, height, err)
	}
}

func TestRootHelpListsPersistentAdminSettings(t *testing.T) {
	code, output := captureCommand(t, []string{"--help"})
	if code != 0 {
		t.Fatalf("总帮助应成功，code=%d output=%s", code, output)
	}
	for _, command := range []string{"admin-path", "admin-username", "admin-password"} {
		if !strings.Contains(output, command) {
			t.Fatalf("总帮助缺少管理员命令 %q: %s", command, output)
		}
	}
	if !strings.Contains(output, "三个管理员设置") {
		t.Fatalf("总帮助没有说明三个管理员设置永久写入数据库: %s", output)
	}
	if strings.Contains(output, "\t") {
		t.Fatalf("总帮助不应使用制表符导致排版错位: %q", output)
	}
}

func TestValidatePluginTokenServerIDMatchesDatabaseConfig(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if _, _, err := bootstrapDeployment(dataDir, ""); err != nil {
		t.Fatal(err)
	}
	db, err := openNormalizedDB(filepath.Join(dataDir, "gbcserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := validatePluginTokenServerID(ctx, db, " default "); err != nil {
		t.Fatalf("相同服务 ID 被拒绝：%v", err)
	}
	if err := validatePluginTokenServerID(ctx, db, "survival-1"); err == nil || !strings.Contains(err.Error(), "serverID") {
		t.Fatalf("不同服务 ID 应被拒绝并提示数据库配置，得到：%v", err)
	}
}

func TestStartAutostartSwitchesAreMutuallyExclusive(t *testing.T) {
	code, output := captureCommand(t, []string{"start", "--autostart", "--no-autostart"})
	if code != 2 || !strings.Contains(output, "不能同时使用") {
		t.Fatalf("互斥的自启开关应返回用法错误，code=%d output=%s", code, output)
	}
}

func TestPortCommandsPersistToDatabase(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if code, output := captureCommand(t, []string{"httpport", "8088", "--data-dir", dataDir}); code != 0 {
		t.Fatalf("httpport 应成功，code=%d output=%s", code, output)
	}
	if code, output := captureCommand(t, []string{"httpsport", "8443", "--data-dir", dataDir}); code != 0 {
		t.Fatalf("httpsport 应成功，code=%d output=%s", code, output)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "gbcserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, expected := range []struct{ key, value string }{{"httpPort", "8088"}, {"httpsPort", "8443"}} {
		var value string
		if err := db.QueryRow("SELECT value FROM app_config WHERE key=?", expected.key).Scan(&value); err != nil || value != expected.value {
			t.Fatalf("%s 未永久写入数据库: value=%q err=%v", expected.key, value, err)
		}
	}
}

func TestAdminCommandsPersistAndHashPassword(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	for _, args := range [][]string{
		{"admin-path", "secure-admin", "--data-dir", dataDir},
		{"admin-username", "root-admin", "--data-dir", dataDir},
		{"admin-password", "correct-horse-battery", "--data-dir", dataDir},
	} {
		if code, output := captureCommand(t, args); code != 0 {
			t.Fatalf("管理员命令失败 args=%v code=%d output=%s", args, code, output)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "gbcserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	values := map[string]string{}
	rows, err := db.Query("SELECT key,value FROM app_config WHERE key IN ('adminPath','adminUsername','adminPasswordHash')")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		values[key] = value
	}
	rows.Close()
	if values["adminPath"] != "/secure-admin" || values["adminUsername"] != "root-admin" {
		t.Fatalf("管理员配置未写入数据库: %#v", values)
	}
	if bcrypt.CompareHashAndPassword([]byte(values["adminPasswordHash"]), []byte("correct-horse-battery")) != nil {
		t.Fatal("管理员密码没有保存为可验证的 bcrypt 哈希")
	}
}

func TestInvalidArgumentsDoNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "不应创建.db")
	commands := [][]string{
		nil, {"unknown"}, {"--help", "extra"}, {"plugin-token", "missing"},
		{"init-db", "--db", path, "extra"}, {"init-db", "--db", path, "--typo"},
		{"init-db", "--db"}, {"migrate", "unexpected", "--apply"},
		{"web-serve", "unexpected"}, {"plugin-serve", "extra"},
		{"plugin-token", "issue", "extra"}, {"plugin-token", "revoke", "extra"},
	}
	for _, args := range commands {
		if code, output := captureCommand(t, args); code != 2 {
			t.Fatalf("无效参数 %v 退出码=%d 输出=%s", args, code, output)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("错误参数不应创建数据库")
	}
}

func TestInitDBCommandCreatesNormalizedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "normalized.db")
	if code := runInitDB([]string{"--db", path}); code != 0 {
		t.Fatalf("init-db 退出码为 %d", code)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("SELECT version FROM schema_migrations ORDER BY applied_at DESC LIMIT 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != store.SchemaVersion {
		t.Fatalf("初始化 schema 版本不正确: %s", version)
	}
}

func TestDefaultStoragePathsUseDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "程序目录")
	db, static, avatars := "", "", ""
	if err := resolveDefaultStoragePaths(&dataDir, &db, &static, &avatars); err != nil {
		t.Fatal(err)
	}
	// 使用真实 dataDir 重新计算，避免测试依赖当前工作目录。
	base, err := filepath.Abs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if dataDir != base || db != filepath.Join(base, "gbcserver.db") || static != filepath.Join(base, "web") || avatars != filepath.Join(base, "avatars") {
		t.Fatalf("默认存储路径错误: data=%q db=%q static=%q avatars=%q", dataDir, db, static, avatars)
	}
}

func TestDefaultDataDirUsesGBCServerDirectory(t *testing.T) {
	if filepath.Base(defaultDataDir()) != "gbcserver" {
		t.Fatalf("默认运行目录应为 gbcserver: %q", defaultDataDir())
	}
}

func TestExplicitStoragePathsArePreserved(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "程序目录")
	db, static, avatars := "自定义.db", "自定义-web", "自定义-avatars"
	if err := resolveDefaultStoragePaths(&dataDir, &db, &static, &avatars); err != nil {
		t.Fatal(err)
	}
	if db != "自定义.db" || static != "自定义-web" || avatars != "自定义-avatars" {
		t.Fatalf("显式路径被覆盖: db=%q static=%q avatars=%q", db, static, avatars)
	}
}

func TestDeploymentRootDefaultsByPlatform(t *testing.T) {
	home := t.TempDir()
	linuxRoot := deploymentRoot("linux", home, filepath.Join("usr", "bin"), "working")
	if linuxRoot != home || filepath.Join(linuxRoot, "gbcserver") != filepath.Join(home, "gbcserver") {
		t.Fatalf("Linux 默认路径应位于用户 home: root=%q", linuxRoot)
	}
	programRoot := filepath.Join(t.TempDir(), "程序")
	windowsRoot := deploymentRoot("windows", home, filepath.Join(programRoot, "bin"), "working")
	if windowsRoot != programRoot || filepath.Join(windowsRoot, "gbcserver") != filepath.Join(programRoot, "gbcserver") {
		t.Fatalf("Windows 默认路径应位于程序根目录: root=%q", windowsRoot)
	}
}

func TestWebURLFromDataDirUsesDatabasePort(t *testing.T) {
	dataDir := t.TempDir()
	if _, _, err := bootstrapDeployment(dataDir, ""); err != nil {
		t.Fatal(err)
	}
	db, err := openNormalizedDB(filepath.Join(dataDir, "gbcserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := (store.ConfigStore{DB: db}).Set(context.Background(), "httpPort", "8088"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	if got := webURLFromDataDir(dataDir); got != "http://127.0.0.1:8088/" {
		t.Fatalf("数据库端口应用于托盘 Web 地址，实际 %q", got)
	}
}

func TestBootstrapCreatesDataOnlyAndDatabaseWithoutOverwriting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "部署根目录")
	dataDir := filepath.Join(root, "gbcserver")
	resolvedDataDir, resolvedConfig, err := bootstrapDeployment(dataDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedDataDir != dataDir || resolvedConfig != "" {
		t.Fatalf("启动路径解析错误: data=%q config=%q", resolvedDataDir, resolvedConfig)
	}
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(rootEntries) != 1 || rootEntries[0].Name() != "gbcserver" || !rootEntries[0].IsDir() {
		t.Fatalf("首次启动根目录只能包含 gbcserver/: %+v", rootEntries)
	}
	for _, path := range []string{filepath.Join(dataDir, "web"), filepath.Join(dataDir, "avatars"), filepath.Join(dataDir, "gbcserver.db")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("首次启动未创建 %q: %v", path, err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "gbcserver.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var port string
	if err := db.QueryRow("SELECT value FROM app_config WHERE key='httpPort'").Scan(&port); err != nil || port != "80" {
		t.Fatalf("默认 HTTP 端口未写入数据库: %q %v", port, err)
	}
	if _, err := db.Exec("CREATE TABLE bootstrap_sentinel (value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bootstrapDeployment(dataDir, ""); err != nil {
		t.Fatal(err)
	}
	var tableCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bootstrap_sentinel'").Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatal("已有数据库被重建")
	}
}

func TestEmbeddedWebsiteOmitsProductAndAvatarFiles(t *testing.T) {
	static := embeddedStaticFS()
	if err := fs.WalkDir(static, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(path, "items/") || strings.HasPrefix(path, "avatars/") {
			t.Fatalf("不应把运行时资源嵌入程序: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"items/yapcb.jpg", "avatars/player.png"} {
		if _, err := static.Open(path); err == nil {
			t.Fatalf("不应把用户或商品图片嵌入程序: %s", path)
		}
	}
	for _, path := range []string{"images/logo.png", "images/default_item.png", "admin.html", "admin.css"} {
		if _, err := static.Open(path); err != nil {
			t.Fatalf("网站显示必需的通用资源应保留 (%s): %v", path, err)
		}
	}
}

func TestEmbeddedWebsiteAssets(t *testing.T) {
	static := embeddedStaticFS()
	for _, path := range []string{"admin.html", "login.html", "ping.html", "site-config.js", "admin.css", "admin-tokens.js", "status-page.js", "status-redesign.css"} {
		file, err := static.Open(path)
		if err != nil {
			t.Fatalf("管理页面资源应嵌入 (%s): %v", path, err)
		}
		file.Close()
	}
	for _, path := range []string{"index.html", "welcome-redesign.css", "images/introduction1.webp"} {
		if file, err := static.Open(path); err == nil {
			file.Close()
			t.Errorf("GitHub 版不应嵌入官网资源: %s", path)
		}
	}
}

func TestEmbeddedStatusPageIncludesRecentAvailabilityTimeline(t *testing.T) {
	static := embeddedStaticFS()
	page, err := fs.ReadFile(static, "ping.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(static, "status-page.js")
	if err != nil {
		t.Fatal(err)
	}
	style, err := fs.ReadFile(static, "status-redesign.css")
	if err != nil {
		t.Fatal(err)
	}
	player, err := fs.ReadFile(static, "player.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(page), `id="nextSample"`) {
		t.Error("状态页不应在总览和线路卡片中重复显示采样倒计时")
	}
	for _, expected := range []string{"近 7 天可用率", "近 60 次记录", "status-history-scale", "status-next-sample"} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("嵌入状态页脚本缺少历史状态内容 %q", expected)
		}
	}
	if !strings.Contains(string(style), ".status-history-scale") || !strings.Contains(string(style), "--status-page: #111827") {
		t.Error("嵌入状态页样式未包含新版时间线和配色")
	}
	if !strings.Contains(string(player), "const enterActions") {
		t.Error("玩家账户操作输入框缺少 Enter 提交处理")
	}
}

func TestEmbeddedWebsiteRoutesMatchProfile(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.New(db, "", "", httpapi.Config{
		AdminPath: "/admin", StaticFS: embeddedStaticFS(), GitHubRelease: githubReleaseBuild,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/login.html", "/ping.html", "/status-page.js", "/site-config.js", "/admin/", "/admin/admin-tokens.js"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s 应可访问，得到 %d", path, response.Code)
		}
	}
	publicTokenScript := httptest.NewRecorder()
	handler.ServeHTTP(publicTokenScript, httptest.NewRequest(http.MethodGet, "/admin-tokens.js", nil))
	if publicTokenScript.Code != http.StatusNotFound {
		t.Fatalf("插件令牌脚本只能通过管理员入口访问，公开路径得到 %d", publicTokenScript.Code)
	}
	publicConfig := httptest.NewRecorder()
	handler.ServeHTTP(publicConfig, httptest.NewRequest(http.MethodGet, "/api/publicConfig", nil))
	wantReleaseProfile := `"githubRelease":true`
	if publicConfig.Code != http.StatusOK || !strings.Contains(publicConfig.Body.String(), wantReleaseProfile) {
		t.Fatalf("构建配置没有暴露正确的发布版标记: %d %s", publicConfig.Code, publicConfig.Body.String())
	}
	root := httptest.NewRecorder()
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusFound || root.Header().Get("Location") != "/login.html" {
		t.Fatalf("GitHub 版根路径应进入玩家登录页: %d %q", root.Code, root.Header().Get("Location"))
	}
	for _, path := range []string{"/index.html", "/welcome-redesign.css"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GitHub 版不应提供官网资源 %s: %d", path, response.Code)
		}
	}
}

func TestEmbeddedAdminStatusSettingsUseEditableLists(t *testing.T) {
	static := embeddedStaticFS()
	page, err := fs.ReadFile(static, "admin.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(static, "admin-config.js")
	if err != nil {
		t.Fatal(err)
	}
	style, err := fs.ReadFile(static, "admin.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`id="statusServers-list"`, `id="statusAPIs-list"`, `id="addStatusServerButton"`, `id="addStatusAPIButton"`} {
		if !strings.Contains(string(page), expected) {
			t.Errorf("嵌入管理员页面缺少状态列表控件 %q", expected)
		}
	}
	for _, expected := range []string{"api.mcsrvstat.us", "mcapi.us", "api.minetools.eu", "function renderStatusList", "function addStatusListRow"} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("嵌入管理员脚本缺少状态列表处理 %q", expected)
		}
	}
	for _, expected := range []string{"function applyReleaseProfile(enabled)", "applyReleaseProfile(Boolean(result.githubRelease))"} {
		if !strings.Contains(string(script), expected) {
			t.Errorf("嵌入管理员脚本缺少发布版配置处理 %q", expected)
		}
	}
	if strings.Contains(string(script), "plugin-tokens')?.setAttribute('hidden'") {
		t.Error("发布版管理员脚本不应隐藏插件令牌入口")
	}
	if !strings.Contains(string(style), ".status-list-row") {
		t.Error("嵌入管理员样式缺少状态列表布局")
	}
}
