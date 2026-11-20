package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// executableDir 返回程序所在目录，便携部署时不依赖启动进程的当前目录。
func executableDir() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		resolved = executable
	}
	return filepath.Dir(resolved)
}

func defaultDataDir() string {
	return filepath.Join(defaultDeploymentRoot(), "gbcserver")
}

func defaultDeploymentRoot() string {
	home, _ := os.UserHomeDir()
	programDir := executableDir()
	workingDir, _ := os.Getwd()
	return deploymentRoot(runtime.GOOS, home, programDir, workingDir)
}

func deploymentRoot(goos, home, programDir, workingDir string) string {
	if goos == "linux" && strings.TrimSpace(home) != "" {
		return home
	}
	if strings.TrimSpace(programDir) != "" {
		// Windows 交付目录可把程序放在 bin/，此时配置和 gbcserver/ 放在包根目录。
		if goos == "windows" && strings.EqualFold(filepath.Base(programDir), "bin") {
			return filepath.Dir(programDir)
		}
		return programDir
	}
	if strings.TrimSpace(workingDir) != "" {
		return workingDir
	}
	return "."
}

func resolveDefaultStoragePaths(dataDir, dbPath, staticDir, avatarDir *string) error {
	base := strings.TrimSpace(*dataDir)
	if base == "" {
		return errors.New("--data-dir 不能为空")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return fmt.Errorf("解析 --data-dir 失败: %w", err)
	}
	*dataDir = abs
	if strings.TrimSpace(*dbPath) == "" {
		*dbPath = filepath.Join(abs, "gbcserver.db")
	}
	if strings.TrimSpace(*staticDir) == "" {
		*staticDir = filepath.Join(abs, "web")
	}
	if strings.TrimSpace(*avatarDir) == "" {
		*avatarDir = filepath.Join(abs, "avatars")
	}
	return nil
}

func printUsage(out io.Writer) {
	if activeCLILanguage == cliLanguageEnglish {
		cliFprintln(out, `GBC Server — Go service and migration tools

Usage: gbcserver <command> [options]
  start               Start web, API, whitelist, and coupon endpoints
  reload              Reload database settings and restart running endpoints
  stop                Gracefully stop a background or tray instance
  httpport            Set and save the HTTP port
  httpsport           Set and save the HTTPS port
  admin-path          Set and save the administrator path
  admin-username      Set and save the administrator username
  admin-password      Set the administrator password; only the bcrypt hash is stored
  install-autostart   Install autostart for the current user
  uninstall-autostart Remove the current user's autostart entry
  plugin-token        Manage plugin tokens interactively or with issue/revoke arguments
  migrate             Analyze a legacy database; use --target and --apply to import
  init-db             Create and initialize an empty database with --db
  upgrade-db          Upgrade an existing database schema offline with --db
  compress-images     Compress existing avatars and shop images; --dry-run previews changes
  web-serve           Run only the web/API service for diagnostics; see command help
  plugin-serve        Run only the private plugin endpoints for diagnostics; see command help
  help                Show general help or help for a specific command

Start: gbcserver start --help
Default locations: Linux uses ~/gbcserver/; Windows uses gbcserver/ beside the executable. Runtime settings are stored in gbcserver/gbcserver.db.
Place startup options after start. The port and administrator path, username, and password are saved to the database; reload reads them again.
Web listens on 0.0.0.0:80 and 0.0.0.0:443 by default. HTTPS starts only when gbcserver/server.key and gbcserver/server.pem are both valid; otherwise only HTTP starts.
If an instance is already running, another start reports this and exits. Use reload or stop to manage it. With a custom data directory, pass the same --data-dir to reload and stop.
See command options with gbcserver <command> --help. Run gbcserver plugin-token to issue or revoke tokens interactively.
Global language option before the command: --lang zh-CN|en. Without it, the system language is used; unknown languages default to Chinese.`)
		return
	}
	cliFprintln(out, `GBC Server — Go 服务与迁移工具

用法：gbcserver <命令> [选项]
  start               启动网页、API、白名单和兑换接口
  reload              重读数据库配置并重启运行中的接口
  stop                优雅停止后台或托盘实例
  httpport            设置 HTTP 端口并永久写入数据库
  httpsport           设置 HTTPS 端口并永久写入数据库
  admin-path          设置管理员安全入口并永久写入数据库
  admin-username      设置管理员账户并永久写入数据库
  admin-password      设置管理员密码并只保存 bcrypt 哈希
  install-autostart   安装自启；选项可指定 data 目录
  uninstall-autostart 移除当前用户安装的自启项
  plugin-token        交互式配置插件令牌；也支持 issue/revoke 参数模式
  migrate             --source 分析旧库；加 --target 与 --apply 后导入新库
  init-db             使用 --db 创建并初始化空数据库
  upgrade-db          使用 --db 离线升级已有数据库结构
  compress-images     压缩已有头像和商品图片；支持 --dry-run 预览
  web-serve           单独运行网页/API（诊断用途，选项见子帮助）
  plugin-serve        单独运行内网插件接口（诊断用途，选项见子帮助）
  help                查看总帮助或指定命令帮助

启动：gbcserver start --help
默认位置：Linux 使用 ~/gbcserver/；Windows 使用程序目录下的 gbcserver/。运行配置保存在 gbcserver/gbcserver.db。
启动选项写在 start 后；端口和三个管理员设置（入口、账户、密码）会永久写入数据库，reload 会重新读取数据库。
Web 默认同时监听 0.0.0.0:80 和 0.0.0.0:443；只有 gbcserver/server.key 与 gbcserver/server.pem 均为有效证书时才启动 HTTPS，否则仅启动 HTTP。
已有实例运行时，再次 start 会提示并退出；reload 和 stop 可管理已有实例。自定义 data 目录时，reload/stop 也须传入相同的 --data-dir。
查看具体选项：gbcserver <命令> --help；直接运行 gbcserver plugin-token 可交互签发或撤销令牌。
全局语言选项须放在命令前：--lang zh-CN|en。未指定时跟随系统语言；未知语言默认中文。`)
}

func newCommandFlags(name, synopsis string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {
		cliFprintf(flags.Output(), "%sgbcserver %s %s\n%s\n", cliText("用法："), name, localizeCLISyntax(synopsis), cliText("选项："))
		flags.VisitAll(func(option *flag.Flag) {
			argument := "--" + option.Name
			boolOption, isBoolean := option.Value.(interface{ IsBoolFlag() bool })
			if !isBoolean || !boolOption.IsBoolFlag() {
				argument += " " + flagValueHint(option.Name)
			}
			cliFprintf(flags.Output(), "  %s\t%s\n", argument, cliText(option.Usage))
		})
		cliFprintf(flags.Output(), "  --help\t%s\n", cliText("显示帮助并退出"))
	}
	return flags
}

func flagValueHint(name string) string {
	switch name {
	case "data-dir":
		return "<data目录>"
	case "db", "source", "target":
		return "<SQLite文件>"
	case "listen", "https-listen", "whitelist-listen", "coupon-listen":
		return "<IP:端口>"
	case "static-dir", "avatar-dir", "tls-key-file", "tls-cert-file":
		return "<目录>"
	case "expires-at":
		return "<RFC3339时间>"
	default:
		return "<值>"
	}
}

func localizeCLISyntax(value string) string {
	if activeCLILanguage != cliLanguageEnglish {
		return value
	}
	for _, replacement := range [][2]string{
		{"<data目录>", "<data directory>"}, {"<运行数据目录>", "<runtime data directory>"},
		{"<端口>", "<port>"}, {"<值>", "<value>"}, {"<密码>", "<password>"},
		{"<目录>", "<directory>"}, {"<SQLite文件>", "<SQLite file>"}, {"<新SQLite>", "<new SQLite file>"},
		{"<已有规范化SQLite>", "<existing normalized SQLite file>"}, {"<旧SQLite>", "<legacy SQLite file>"},
		{"<标识>", "<ID>"}, {"<像素>", "<pixels>"}, {"选项", "options"}, {"数据目录", "data directory"},
	} {
		value = strings.ReplaceAll(value, replacement[0], replacement[1])
	}
	return value
}

func parseCommandFlags(flags *flag.FlagSet, args []string) (code int, stop bool) {
	// 帮助独立于运行配置和外部服务；不把配置或环境中的密钥显示为默认值。
	for _, argument := range args {
		if argument == "--help" || argument == "-h" || argument == "-help" {
			flags.SetOutput(os.Stdout)
			flags.Usage()
			return 0, true
		}
	}
	if err := parseFlagsWithoutEcho(flags, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 2, true
	}
	if flags.NArg() != 0 {
		// 不回显额外参数，避免把误放的令牌或密码写进日志。
		cliFprintln(flags.Output(), cliText("不接受位置参数；选项须使用 --name value 或 --name=value"))
		return 2, true
	}
	return 0, false
}

func parseFlagsWithoutEcho(flags *flag.FlagSet, args []string) error {
	output := flags.Output()
	flags.SetOutput(io.Discard)
	err := flags.Parse(args)
	flags.SetOutput(output)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		cliFprintln(output, cliText("选项无效；请使用 --help 查看用法"))
	}
	return err
}
