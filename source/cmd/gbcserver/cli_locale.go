package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

type cliLanguage string

const (
	cliLanguageChinese cliLanguage = "zh-CN"
	cliLanguageEnglish cliLanguage = "en"
)

var activeCLILanguage = cliLanguageChinese

var cliTranslations = map[string]string{
	"GBC Server — Go 服务与迁移工具": "GBC Server — Go service and migration tools",
	"用法：gbcserver <命令> [选项]":  "Usage: gbcserver <command> [options]",
	"全局语言选项须放在命令前：--lang zh-CN|en。未指定时跟随系统语言；未知语言默认中文。": "Place the global language option before the command: --lang zh-CN|en. Without it, the system language is used; unknown languages default to Chinese.",
	"启动网页、API、白名单和兑换接口":                        "Start the web, API, whitelist, and coupon endpoints",
	"重读数据库配置并重启运行中的接口":                         "Reload database settings and restart the running endpoints",
	"优雅停止后台或托盘实例":                              "Gracefully stop a background or tray instance",
	"设置 HTTP 端口并永久写入数据库":                       "Set the HTTP port and save it to the database",
	"设置 HTTPS 端口并永久写入数据库":                      "Set the HTTPS port and save it to the database",
	"设置管理员安全入口并永久写入数据库":                        "Set the administrator path and save it to the database",
	"设置管理员账户并永久写入数据库":                          "Set the administrator username and save it to the database",
	"设置管理员密码并只保存 bcrypt 哈希":                    "Set the administrator password; only its bcrypt hash is stored",
	"安装自启；选项可指定 data 目录":                       "Install autostart; options can select the data directory",
	"移除当前用户安装的自启项":                             "Remove the current user's autostart entry",
	"交互式配置插件令牌；也支持 issue/revoke 参数模式":          "Manage plugin tokens interactively or with issue/revoke arguments",
	"--source 分析旧库；加 --target 与 --apply 后导入新库": "Analyze an old database with --source; import with --target and --apply",
	"使用 --db 创建并初始化空数据库":                       "Create and initialize an empty database with --db",
	"使用 --db 离线升级已有数据库结构":                      "Upgrade an existing database schema offline with --db",
	"压缩已有头像和商品图片；支持 --dry-run 预览":              "Compress existing avatars and shop images; --dry-run previews changes",
	"单独运行网页/API（诊断用途，选项见子帮助）":                  "Run only the web/API service for diagnostics; see command help for options",
	"单独运行内网插件接口（诊断用途，选项见子帮助）":                  "Run only the private plugin endpoints for diagnostics; see command help",
	"查看总帮助或指定命令帮助":                             "Show general help or help for a specific command",
	"启动：gbcserver start --help":                "Start: gbcserver start --help",
	"默认位置：Linux 使用 ~/gbcserver/；Windows 使用程序目录下的 gbcserver/。运行配置保存在 gbcserver/gbcserver.db。":                          "Default locations: Linux uses ~/gbcserver/; Windows uses gbcserver/ beside the executable. Runtime settings are stored in gbcserver/gbcserver.db.",
	"启动选项写在 start 后；端口和三个管理员设置（入口、账户、密码）会永久写入数据库，reload 会重新读取数据库。":                                                    "Place startup options after start. The port and administrator path, username, and password are saved to the database; reload reads them again.",
	"Web 默认同时监听 0.0.0.0:80 和 0.0.0.0:443；只有 gbcserver/server.key 与 gbcserver/server.pem 均为有效证书时才启动 HTTPS，否则仅启动 HTTP。": "Web listens on 0.0.0.0:80 and 0.0.0.0:443 by default. HTTPS starts only when gbcserver/server.key and gbcserver/server.pem are both valid; otherwise only HTTP starts.",
	"已有实例运行时，再次 start 会提示并退出；reload 和 stop 可管理已有实例。自定义 data 目录时，reload/stop 也须传入相同的 --data-dir。":                      "If an instance is already running, another start reports this and exits. Use reload or stop to manage it. When using a custom data directory, pass the same --data-dir to reload and stop.",
	"查看具体选项：gbcserver <命令> --help；直接运行 gbcserver plugin-token 可交互签发或撤销令牌。":                                            "See command options with gbcserver <command> --help. Run gbcserver plugin-token to issue or revoke tokens interactively.",
	"用法：":                       "Usage: ",
	"选项：":                       "Options:",
	"规范化 SQLite 文件路径":           "Normalized SQLite file path",
	"显示帮助并退出":                   "Show help and exit",
	"GBC Server 已转入后台运行。":       "GBC Server started in the background.",
	"预计压缩":                      "Would compress",
	"已压缩":                       "Compressed",
	"端口号":                       "port",
	"配置值":                       "configuration value",
	"管理员密码":                     "administrator password",
	"<值> [--data-dir <data目录>]": "<value> [--data-dir <data directory>]",
	"解析 --data-dir 失败:":         "Failed to resolve --data-dir:",
	"解析数据库路径失败:":                "Failed to resolve the database path:",
	"运行数据文件夹本身；默认位于用户主目录或程序目录下的 gbcserver/": "Runtime data directory; defaults to gbcserver/ in the user home or executable directory",
	"运行实例使用的 data 文件夹本身":                    "The data directory used by the running instance",
	"默认存储根目录":                                           "Default storage root directory",
	"默认存储根目录；未指定时按平台选择":                                 "Default storage root; selected by platform when omitted",
	"规范化 SQLite 文件路径；未指定时为 <data-dir>/gbcserver.db":     "Normalized SQLite file path; defaults to <data-dir>/gbcserver.db",
	"HTTP 监听 IP:端口；默认 0.0.0.0:80":                       "HTTP listen IP:port; defaults to 0.0.0.0:80",
	"HTTPS 监听 IP:端口；仅在 TLS 证书有效时启动":                     "HTTPS listen IP:port; starts only when the TLS certificate is valid",
	"TLS 私钥；默认 <data-dir>/server.key":                   "TLS private key; defaults to <data-dir>/server.key",
	"TLS 证书链；默认 <data-dir>/server.pem":                  "TLS certificate chain; defaults to <data-dir>/server.pem",
	"静态资源目录；未指定时为 <data-dir>/web":                       "Static asset directory; defaults to <data-dir>/web",
	"头像资源目录；未指定时为 <data-dir>/avatars":                   "Avatar asset directory; defaults to <data-dir>/avatars",
	"旧 API JSON/form 请求体最大字节数":                          "Maximum request body size in bytes for legacy API JSON/form requests",
	"网页授权使用的插件服务 ID（白名单与兑换共用）":                          "Plugin server ID used for web authorization (shared by whitelist and coupons)",
	"端口更新 webhook 路径":                                   "Port update webhook path",
	"登录和签到时间相对 UTC 的固定小时偏移；默认 GMT+8":                    "Fixed hour offset from UTC for login and check-in times; defaults to GMT+8",
	"玩家网页授权最长时长":                                        "Maximum duration of player web authorization",
	"注册时是否向 Mojang 查询正版 UUID；默认使用离线 UUID":               "Whether registration checks Mojang for a premium UUID; defaults to offline UUIDs",
	"SMTP 是否使用 SMTPS/STARTTLS；关闭须写 --mail-secure=false": "Use SMTPS/STARTTLS for SMTP; set --mail-secure=false to disable",
	"状态目标 JSON 列表；兼容 name=host:port，名称支持中文":             "Status target JSON list; also accepts name=host:port, including names in Chinese",
	"状态 API JSON 列表；兼容 name=url，空值使用默认三家，none 禁用":       "Status API JSON list; also accepts name=url. Empty uses the three defaults; none disables APIs",
	"Minecraft 状态探测超时":                                  "Minecraft status probe timeout",
	"状态缓存时间":                                            "Status cache duration",
	"旧版 SQLite 文件路径，只读":                                 "Path to the legacy SQLite file (read-only)",
	"新 SQLite 数据库文件路径":                                  "Path for the new SQLite database file",
	"已有规范化 SQLite 数据库文件路径":                              "Path to the existing normalized SQLite database file",
	"规范化 SQLite 的新路径":                                   "Path for the new normalized SQLite database",
	"将数据写入全新的目标数据库；缺省只做 dry-run":                        "Write data to a new target database; defaults to a dry run",
	"data 文件夹本身；未指定时按平台选择":                              "The data directory itself; selected by platform when omitted",
	"白名单插件内网监听地址；跨机器时填写本机私网 IP:端口":                      "Private listen address for the whitelist plugin; use this host's private IP:port for remote machines",
	"兑换插件内网监听地址；跨机器时填写本机私网 IP:端口":                       "Private listen address for the coupon plugin; use this host's private IP:port for remote machines",
	"游戏服稳定标识；必须与数据库 serverID 配置一致":                      "Stable game server ID; must match the database serverID setting",
	"token 权限：whitelist 或 coupon.redeem":                "Token scope: whitelist or coupon.redeem",
	"RFC3339 过期时间；留空表示不设过期时间":                           "RFC3339 expiration time; leave empty for no expiration",
	"要撤销的 token_id":                                     "Token ID to revoke",
	"图片最长边上限，头像使用更小的 256 像素上限":                          "Maximum image dimension; avatars use a smaller 256-pixel limit",
	"只统计将要压缩的文件，不写入磁盘":                                  "Only report files that would be compressed; do not write to disk",
	"JPEG 质量，范围 1-100":                                  "JPEG quality, from 1 to 100",
	"GBC Server 已在运行；本次命令未执行。":                          "GBC Server is already running; this command was not executed.",
	"GBC Server 已在运行；本次 start 未执行。":                     "GBC Server is already running; this start command was not executed.",
	"管理员入口必须是非根路径，且不能包含空格、? 或 #":                        "Administrator path must be a non-root path and cannot contain spaces, ? or #",
	"--data-dir 无效":                                          "Invalid --data-dir",
	"准备数据库失败:":                                               "Failed to prepare the database:",
	"写入数据库失败:":                                               "Failed to write to the database:",
	"端口必须是 1-65535 之间的整数":                                    "Port must be an integer from 1 to 65535",
	"读取 schema 版本失败:":                                        "Failed to read the schema version:",
	"检查 schema_migrations 失败:":                               "Failed to check schema_migrations:",
	"升级 SQLite schema 失败:":                                   "Failed to upgrade SQLite schema:",
	"打开 SQLite 失败:":                                          "Failed to open SQLite database:",
	"规范化 SQLite 初始化完成。":                                      "Normalized SQLite database initialized.",
	"读取运行配置失败:":                                              "Failed to read runtime settings:",
	"读取图片目录配置失败:":                                            "Failed to read image directory settings:",
	"准备数据目录失败:":                                              "Failed to prepare the data directory:",
	"读取数据库 serverID 配置失败:":                                   "Failed to read the database serverID setting:",
	"读取数据库 signTimezoneOffset 配置失败:":                         "Failed to read the database signTimezoneOffset setting:",
	"数据库配置 signTimezoneOffset 必须是 -12 至 14 之间的整数":            "Database setting signTimezoneOffset must be an integer from -12 to 14",
	"必须提供 --source":                                          "--source is required",
	"--apply 时必须提供 --target":                                 "--target is required with --apply",
	"写出 JSON 报告失败:":                                          "Failed to write the JSON report:",
	"未知令牌命令；请使用 gbcserver plugin-token --help":               "Unknown token command; run gbcserver plugin-token --help",
	"--db、--server-id、--scope 均为必填项":                         "--db, --server-id, and --scope are required",
	"--scope 必须是 whitelist 或 coupon.redeem":                  "--scope must be whitelist or coupon.redeem",
	"--expires-at 必须是 RFC3339:":                              "--expires-at must be RFC3339:",
	"--db 和 --token-id 均为必填项":                                "--db and --token-id are required",
	"插件监听地址 %q 无法解析，请先修正运行配置":                                "Cannot resolve plugin listen address %q; correct the runtime settings first",
	"API 主机必须是 loopback、RFC1918、IPv6 ULA 或 link-local 私网 IP": "API host must be loopback, an RFC1918 address, an IPv6 ULA, or a link-local private IP",
	"读取插件服务 ID 失败:":                                          "Failed to read the plugin server ID:",
	"Mojang profile API 地址":                                  "Mojang profile API URL",
	"Mojang 查询超时":                                            "Mojang request timeout",
	"目标路径已存在；为避免意外覆盖，初始化已停止":                                 "Target path already exists; initialization stopped to avoid overwriting it",
	"已有数据库文件不存在或路径无效":                                        "Existing database file does not exist or the path is invalid",
	"数据库路径不能为空":                                              "Database path cannot be empty",
	"数据库缺少 plugin_tokens 表":                                  "Database is missing the plugin_tokens table",
	"创建静态资源目录失败:":                                            "Failed to create the static asset directory:",
	"创建头像目录失败:":                                              "Failed to create the avatar directory:",
	"--listen 无效:":                                           "Invalid --listen:",
	"--https-listen 无效:":                                     "Invalid --https-listen:",
	"--status-servers 无效:":                                   "Invalid --status-servers:",
	"--status-apis 无效:":                                      "Invalid --status-apis:",
	"配置 SMTP 时必须提供 --mail-from-address 或管理员配置中的 mailFromAddress": "When SMTP is configured, provide --mail-from-address or set mailFromAddress in administrator settings",
	"创建 Web handler 失败:":                  "Failed to create the Web handler:",
	"读取数据库配置失败:":                          "Failed to read database settings:",
	"写入数据库配置失败:":                          "Failed to write database settings:",
	"系统单实例锁已由其他实例持有":                      "The system single-instance lock is held by another instance",
	"已初始化数据库：%s\n":                        "Database initialized: %s\n",
	"%s %d/%d 个图片文件，已跳过 %d 个无需改写的文件。\n":   "%s %d/%d image files; skipped %d files that did not need changes.\n",
	"数据库配置 %s 必须是整数":                      "Database setting %s must be an integer",
	"数据库配置 %s 必须是 true 或 false":           "Database setting %s must be true or false",
	"数据库配置 %s 必须是 Go duration，例如 5s":      "Database setting %s must be a Go duration, for example 5s",
	"目标必须是 name=host:port: %s":            "Target must use name=host:port: %s",
	"目标地址无效: %s":                          "Invalid target address: %s",
	"目标端口无效: %s":                          "Invalid target port: %s",
	"接口必须是 name=url: %s":                  "Endpoint must use name=url: %s",
	"接口 URL 无效: %s (%w)":                  "Invalid endpoint URL: %s (%w)",
	"状态目标 JSON 无效: %w":                    "Invalid status target JSON: %w",
	"状态目标 %s 主机无效: %w":                    "Invalid host for status target %s: %w",
	"状态目标 %s 端口无效: %w":                    "Invalid port for status target %s: %w",
	"状态目标 %s defaultPort 无效: %w":          "Invalid defaultPort for status target %s: %w",
	"状态目标 %s 端口必须是 1-65535":               "Port for status target %s must be 1–65535",
	"主机不能为空，且不能包含空白或路径字符":                 "Host cannot be empty or contain whitespace or path characters",
	"请只填写域名或 IP，不要包含端口":                   "Enter only a hostname or IP address, without a port",
	"状态 API JSON 无效: %w":                  "Invalid status API JSON: %w",
	"状态 API %s URL 无效: %w":                "Invalid URL for status API %s: %w",
	"必须使用 HTTP 或 HTTPS":                   "Only HTTP or HTTPS is supported",
	"主机地址无效":                              "Invalid host address",
	"状态目标名称不能为空":                          "Status target name cannot be empty",
	"状态目标名称不能包含换行":                        "Status target name cannot contain a newline",
	"端口必须是整数":                             "Port must be an integer",
	"管理员密码必须为 8-72 字节":                    "Administrator password must be 8 to 72 bytes",
	"生成管理员密码哈希失败:":                        "Failed to hash the administrator password:",
	"admin-password 已将 bcrypt 哈希永久写入数据库。": "admin-password saved the bcrypt hash to the database.",
	"准备部署目录失败:":                           "Failed to prepare the deployment directory:",
	"获取单实例锁失败:":                           "Failed to acquire the single-instance lock:",
	"监听地址必须使用 IP:端口 格式: %w":               "Listen address must use IP:port format: %w",
	"监听地址必须使用 IP 字面值，可指定 0.0.0.0 监听所有网卡":  "Listen address must use an IP literal; use 0.0.0.0 to listen on all interfaces",
	"监听端口必须在 1–65535 之间":                  "Listen port must be between 1 and 65535",
	"Web listener 退出:":                    "Web listener exited:",
	"plugin-serve 退出:":                    "plugin-serve exited:",
	"GBC Server 启动失败:":                    "GBC Server failed to start:",
	"Windows 托盘退出:":                       "Windows tray exited:",
	"数据库配置启动失败，正在恢复当前服务:":                 "Failed to start with the database settings; restoring the current service:",
	"恢复原配置也失败:":                           "Failed to restore the previous settings as well:",
	"GBC Server 已配置为当前用户开机自启；启动项会一体启动 Web/API、白名单和兑换 listener。": "GBC Server autostart is configured for the current user; it starts Web/API, whitelist, and coupon listeners together.",
	"GBC Server 已转入后台运行，但管理通道尚未就绪：%v\n":                         "GBC Server started in the background, but the control channel is not ready: %v\n",
	"PID=%d\n日志：%s\n": "PID=%d\nLog: %s\n",
	"GBC Server 已转入后台运行。PID=%d\n日志：%s\n": "GBC Server started in the background. PID=%d\nLog: %s\n",
	"等待 %s 超时":                                    "Timed out waiting for %s",
	"后台进程在 Web 与插件 listener 就绪前退出":                         "Background process exited before the Web and plugin listeners were ready",
	"%w；后台进程退出：%v":                               "%w; background process exited: %v",
	"%w；PID=%d，日志：%s":                              "%w; PID=%d, log: %s",
	"解析程序路径失败: %w":                                "Failed to resolve the executable path: %w",
	"解析程序绝对路径失败: %w":                              "Failed to resolve the absolute executable path: %w",
	"读取用户主目录失败: %w":                               "Failed to resolve the user home directory: %w",
	"删除 systemd 自启文件失败: %w":                       "Failed to remove the systemd autostart file: %w",
	"创建 systemd 自启目录失败: %w":                       "Failed to create the systemd autostart directory: %w",
	"写入 systemd 自启文件失败: %w":                       "Failed to write the systemd autostart file: %w",
	"未找到 systemctl，已生成 unit 但无法启用: %w":            "systemctl was not found. The unit was created but could not be enabled: %w",
	"systemctl --user %s 失败: %s":                  "systemctl --user %s failed: %s",
	"打开当前用户启动项失败: %w":                             "Failed to open the current user's startup entry: %w",
	"删除 Windows 自启项失败: %w":                        "Failed to remove the Windows autostart entry: %w",
	"写入 Windows 自启项失败: %w":                        "Failed to write the Windows autostart entry: %w",
	"创建本机管理通道失败: %w":                              "Failed to create the local control channel: %w",
	"生成管理通道令牌失败: %w":                              "Failed to generate the control channel token: %w",
	"生成管理通道状态失败: %w":                              "Failed to generate the control channel state: %w",
	"写入管理通道状态失败: %w":                              "Failed to write the control channel state: %w",
	"关闭管理通道状态失败: %w":                              "Failed to close the control channel state: %w",
	"讀取管理通道失敗；服務可能尚未啟動: %w":                       "Failed to read the control channel; the service may not be running: %w",
	"管理通道状态文件不存在；请先启动服务，并确认 --data-dir 与 start 使用的目录一致（%s）": "The control state file is missing. Start the service first and use the same --data-dir as start (%s)",
	"管理通道狀態無效；請確認 data 目錄與運行實例一致":                 "Invalid control channel state; make sure the data directory matches the running instance",
	"無法連接管理通道；服務可能尚未啟動: %w":                       "Could not connect to the control channel; the service may not be running: %w",
	"发送管理请求失败: %w":                                "Failed to send the control request: %w",
	"读取管理响应失败: %w":                                "Failed to read the control response: %w",
	"创建系统单实例事件失败: %w":                             "Failed to create the system single-instance event: %w",
	"设置系统单实例锁权限失败: %w":                            "Failed to set permissions on the system single-instance lock: %w",
	"打开系统单实例锁失败: %w":                              "Failed to open the system single-instance lock: %w",
	"加锁系统单实例文件失败: %w":                             "Failed to lock the system single-instance file: %w",
	"构造系统单实例锁名称失败: %w":                            "Failed to construct the system single-instance lock name: %w",
	"继承的单实例锁文件描述符无效":                              "Inherited single-instance lock file descriptor is invalid",
	"继承的单实例锁文件不可用":                                "Inherited single-instance lock file is unavailable",
	"规范化 SQLite 路径指向目录: %s":                       "Normalized SQLite path points to a directory: %s",
	"初始化 SQLite 连接失败: %w":                         "Failed to initialize the SQLite connection: %w",
	"连接规范化 SQLite 失败: %w":                         "Failed to connect to normalized SQLite: %w",
	"数据库缺少规范化 schema_migrations，未初始化 token 表: %w": "Database lacks normalized schema_migrations; token tables were not initialized: %w",
	"数据库 schema 版本不受支持: %s":                       "Unsupported database schema version: %s",
	"检查 token 表失败: %w":                            "Failed to check token tables: %w",
	"创建数据目录失败: %w":                                "Failed to create the data directory: %w",
	"创建运行目录失败: %w":                                "Failed to create the runtime directory: %w",
	"初始化数据库配置失败: %w":                              "Failed to initialize database settings: %w",
	"检查数据库文件失败: %w":                               "Failed to check the database file: %w",
	"创建数据库目录失败: %w":                               "Failed to create the database directory: %w",
	"创建数据库文件失败: %w":                               "Failed to create the database file: %w",
	"关闭新数据库文件失败: %w":                              "Failed to close the new database file: %w",
	"打开新 SQLite 失败: %w":                           "Failed to open the new SQLite database: %w",
	"初始化新 SQLite 失败: %w":                          "Failed to initialize the new SQLite database: %w",
	"解析数据目录失败: %w":                                "Failed to resolve the data directory: %w",
	"写入数据库配置失败: %w":                               "Failed to write database settings: %w",
	"准备数据库失败: %w":                                 "Failed to prepare the database: %w",
	"创建后台日志目录失败: %w":                              "Failed to create the background log directory: %w",
	"打开后台日志失败: %w":                                "Failed to open the background log: %w",
	"启动后台进程失败: %w":                                "Failed to start the background process: %w",
	"写入后台 PID 失败: %w":                             "Failed to write the background PID: %w",
	"后台启动缺少单实例锁":                                  "Background startup is missing the single-instance lock",
	"Windows 不支持 --background，请使用 start 或开机自启":    "Windows does not support --background; use start or autostart",
	"当前平台不支持 --background":                        "--background is not supported on this platform",
	"当前平台暂不支持开机自启":                                "Autostart is not supported on this platform",
	"当前平台不支持 Windows 托盘":                          "Windows tray is not supported on this platform",
	"解析托盘数据目录失败: %w":                              "Failed to resolve the tray data directory: %w",
	"创建 Windows 托盘窗口失败: %w":                       "Failed to create the Windows tray window: %w",
	"读取托盘消息失败: %w":                                "Failed to read the tray message: %w",
	"创建管理通道状态失败: %w":                              "Failed to generate the control channel state: %w",
	"GBC Server 已有实例运行":                           "A GBC Server instance is already running",
	"服务端私网 IP（回车使用 %s）：":                          "Server private IP (press Enter to use %s): ",
	"游戏服可访问的服务端私网 IP（回车使用 %s）：":                   "Private server IP reachable by the game server (press Enter to use %s): ",
	"为 %s 签发 %s 令牌？[y/N]：":                        "Issue a token for %s with scope %s? [y/N]: ",
	"\n令牌已签发（ID：%s）。明文只显示这一次，请立即保存。\n":            "\nToken issued (ID: %s). The secret is shown only once; save it now.\n",
	"插件接入":                                           "plugin integration",
	"GBC Server 插件接入":                                "GBC Server plugin integration",
	"运行数据目录：%s\n游戏服 ID：%s\n":                         "Data directory: %s\nGame server ID: %s\n",
	"\n1. 签发插件令牌\n2. 撤销插件令牌\n0. 退出":                  "\n1. Issue a plugin token\n2. Revoke a plugin token\n0. Exit",
	"\n1. 白名单插件\n2. 兑换插件":                            "\n1. Whitelist plugin\n2. Coupon plugin",
	"--- 插件配置 ---":                                   "--- Plugin configuration ---",
	"GBC Server 当前用户开机自启已安装。":                        "GBC Server autostart is installed for the current user.",
	"GBC Server 当前用户开机自启已移除。":                        "GBC Server autostart has been removed for the current user.",
	"Web 服务已启动：HTTP://":                              "Web service started: HTTP://",
	"Web 服务已启动：HTTPS://":                             "Web service started: HTTPS://",
	"监听地址不能为空；--db 未提供时默认使用 <data-dir>/gbcserver.db": "Listen addresses cannot be empty; when --db is omitted, <data-dir>/gbcserver.db is used",
	"数据库配置 httpPort 必须是整数":                           "Database setting httpPort must be an integer",
	"数据库配置 httpsPort 必须是整数":                          "Database setting httpsPort must be an integer",
	"SQLite schema 已升级到":                             "SQLite schema upgraded to",
	"未启用 HTTPS：无法识别 TLS 证书或私钥（证书 %q，私钥 %q）：%v；仅启动 HTTP。\n": "HTTPS is disabled: the TLS certificate or private key is invalid (certificate %q, key %q): %v; starting HTTP only.\n",
	"管理员路径":            "Administrator path",
	"必须提供":             "is required",
	"必须提供 --db":        "--db is required",
	"必填项":              "is required",
	"端口必须在 1–65535 之间": "Port must be between 1 and 65535",
	"管理员入口":            "administrator path",
	"状态采样间隔必须在 5s 至 24h 之间，离线告警阈值必须在 5s 至 30d 之间":              "Status sampling interval must be 5 s to 24 h; offline alert threshold must be 5 s to 30 d",
	"配置范围无效：SMTP 端口须为 1–65535，状态超时、缓存和授权时长须大于 0，时区须为 -12 至 14": "Configuration values are out of range: SMTP port must be 1–65535; status timeout, cache, and authorization duration must be positive; timezone must be -12 to 14",
	"--body-limit 必须在 1 KiB 至 256 MiB 之间":                      "--body-limit must be between 1 KiB and 256 MiB",
	"--mojang-timeout 必须在 1ns 至 30s 之间":                        "--mojang-timeout must be between 1 ns and 30 s",
	"--max-dimension 必须在 32-4096 之间，--quality 必须在 1-100 之间":    "--max-dimension must be 32–4096 and --quality must be 1–100",
	"无法打开运行数据库；请先启动一次服务完成初始化，或使用 --data-dir 指定数据目录。":           "Could not open the runtime database. Start the service once to initialize it, or specify a data directory with --data-dir.",
	"未签发令牌：插件选项无效":                                             "Token not issued: invalid plugin selection",
	"过期时间格式无效，请使用 RFC3339，例如 2027-01-01T00:00:00+08:00":        "Invalid expiration time; use RFC3339, for example 2027-01-01T00:00:00+08:00",
	"过期时间必须晚于当前时间":                                             "Expiration time must be in the future",
	"当前没有插件令牌。":                                                "There are no plugin tokens.",
	"请选择：":                                                     "Choose an option: ",
	"选择插件：":                                                    "Select a plugin: ",
	"输入序号撤销，回车返回：":                                             "Enter a number to revoke, or press Enter to return: ",
	"有效":                                                       "Active",
	"已撤销":                                                      "Revoked",
	"已过期":                                                      "Expired",
	"序号无效":                                                     "Invalid selection",
	"撤销令牌 %s？[y/N]：":                                           "Revoke token %s? [y/N]: ",
	"已取消撤销":                                                    "Revocation cancelled",
	"令牌已撤销。":                                                   "Token revoked.",
	"重载请求已发送。":                                                 "Reload request sent.",
	"停止请求已发送。":                                                 "Stop request sent.",
	"GBC Server 正在停止。":                                         "GBC Server is stopping.",
	"GBC Server 配置已重新加载；Web 与插件 listener 已就绪。":                 "GBC Server settings reloaded; Web and plugin listeners are ready.",
	"当前数据库配置已恢复，服务继续运行。":                                       "The database settings were restored; the service is still running.",
	"写出撤销结果失败:":                                                "Failed to write the revocation result:",
	"解析用户主目录失败: %w":                                            "Failed to resolve the user home directory: %w",
	"Windows 请直接双击程序或使用 start；--background 仅适用于 Linux":         "On Windows, double-click the program or use start; --background is for Linux only",
	"--tray 仅适用于 Windows":                                      "--tray is available on Windows only",
	"数据库配置范围无效":                                                "Database settings are out of range",
	"检查目标路径失败:":                                                "Failed to check the target path:",
	"创建数据库目录失败:":                                               "Failed to create the database directory:",
	"以防覆盖模式创建数据库失败:":                                           "Failed to create the database without overwriting an existing file:",
	"关闭新数据库文件失败:":                                              "Failed to close the new database file:",
	"创建 SQLite 失败:":                                            "Failed to create SQLite database:",
	"初始化数据库失败:":                                                "Failed to initialize the database:",
	"数据库 schema 版本不支持升级:":                                      "Database schema version is not supported for upgrade:",
	"写出 token 结果失败:":                                           "Failed to write the token result:",
	"未知命令；使用 gbcserver --help 查看用法":                            "Unknown command; run gbcserver --help for usage",
	"帮助选项后不能附加参数；请使用 gbcserver help <子命令>":                     "No arguments may follow a help option; use gbcserver help <command>",
	"不接受位置参数；选项须使用 --name value 或 --name=value":                "Positional arguments are not accepted; use --name value or --name=value",
	"--autostart 与 --no-autostart 不能同时使用":                      "--autostart and --no-autostart cannot be used together",
	"配置值不能为空":                                                  "Configuration value cannot be empty",
	"选项无效；请使用 --help 查看用法":                                     "Invalid option; run --help to see usage",
	"必须提供一个%s；请使用：%s <值> [--data-dir <data目录>]":                "Provide one %s; use: %s <value> [--data-dir <data directory>]",
	"必须提供一个":                                                   "Provide one ",
	"请使用：":                                                     "use: ",
	"读取输入失败:":                                                  "Failed to read input:",
	"数据库没有 schema_migrations；升级命令不会初始化新库":                      "Database has no schema_migrations table; the upgrade command will not initialize a new database",
	"规范化 SQLite 文件不存在或不可访问: %w":                                "Normalized SQLite file does not exist or is inaccessible: %w",
	"启动 HTTP listener 失败: %w":                                  "Failed to start the HTTP listener: %w",
	"启动 HTTPS listener 失败；HTTP 也已取消: %w":                       "Failed to start the HTTPS listener; HTTP was also cancelled: %w",
	"listener 意外停止":                                            "Listener stopped unexpectedly",
	"准备数据目录失败: %w":                                             "Failed to prepare the data directory: %w",
	"读取数据库 serverID 配置失败: %w":                                  "Failed to read the database serverID setting: %w",
	"Linux 后台运行，日志保存在 data 文件夹":                                "Run in the background on Linux; logs are stored in the data directory",
	"Windows 托盘运行；菜单可打开配置、数据、日志和 Web 页面":                       "Run in the Windows tray; the menu opens settings, data, logs, and the Web page",
	"安装当前用户的开机自启":                                              "Install autostart for the current user",
	"移除当前用户的开机自启":                                              "Remove autostart for the current user",
	"<data目录>":                                                 "<data directory>",
	"<SQLite文件>":                                               "<SQLite file>",
	"<新SQLite>":                                                "<new SQLite file>",
	"<已有规范化SQLite>":                                            "<existing normalized SQLite file>",
	"<旧SQLite>":                                                "<legacy SQLite file>",
	"<标识>":                                                     "<ID>",
	"<像素>":                                                     "<pixels>",
	"选项":                                                       "options",
	"数据目录":                                                     "data directory",
	"服务器配置值不能为空":                                               "Server configuration value cannot be empty",
	"解析 --data-dir 失败":                                         "Failed to resolve --data-dir",
	"HTTP 服务启动失败: %w":                                          "Failed to start the HTTP service: %w",
	"HTTPS 服务启动失败: %w":                                         "Failed to start the HTTPS service: %w",
	"web-serve 退出: %w":                                         "web-serve exited: %w",
	"%s 已永久写入数据库：%d\n":                                         "%s has been saved to the database: %d\n",
	"%s 已永久写入数据库。\n":                                           "%s has been saved to the database.\n",
	"%s 服务意外退出。\n":                                             "%s service exited unexpectedly.\n",
	"%s 服务在 listener 就绪前退出（退出码 %d）":                            "%s service exited before its listener was ready (exit code %d)",
	"管理员账号；诊断模式临时覆盖数据库配置":                                      "Administrator username; temporarily overrides database settings in diagnostic mode",
	"管理员 bcrypt 密码哈希；诊断模式临时覆盖数据库配置":                            "Administrator bcrypt password hash; temporarily overrides database settings in diagnostic mode",
	"管理员 TOTP secret；诊断模式临时覆盖数据库配置":                            "Administrator TOTP secret; temporarily overrides database settings in diagnostic mode",
	"管理员安全入口路径":                                                "Administrator path",
	"image files":                                              "image files",
	"未启用 HTTPS":                                                "HTTPS is disabled",
	"HTTP 服务已启动：":                                              "HTTP service started: ",
	"HTTPS 服务已启动：":                                             "HTTPS service started: ",
	"解析 --data-dir 失败: %w":                                     "Failed to resolve --data-dir: %w",
	"解析数据库路径失败: %w":                                            "Failed to resolve the database path: %w",
	"存储路径解析失败：--data-dir 或 --db 不能为空":                          "Failed to resolve storage paths: --data-dir or --db cannot be empty",
	"请输入 1、2 或 0。":                                             "Enter 1, 2, or 0.",
	"已取消签发":                                                    "Issuance cancelled",
	"解析后台数据目录失败: %w":                                           "Failed to resolve the background data directory: %w",
	"解析系统单实例锁名称失败: %w":                                         "Failed to resolve the system single-instance lock name: %w",
	"--data-dir 不能为空":                                          "--data-dir cannot be empty",
	"SMTP 主机":                                                  "SMTP host",
	"SMTP 端口":                                                  "SMTP port",
	"SMTP 用户名":                                                 "SMTP username",
	"SMTP 密码":                                                  "SMTP password",
	"邮件显示发件人":                                                  "Sender name shown in email",
	"邮件发件地址":                                                   "Sender email address",
	"启动参数：gbcserver plugin-token issue|revoke [选项]\n  issue  --db <SQLite> --server-id <标识> --scope <whitelist|coupon.redeem> [--expires-at <RFC3339>]\n  revoke --db <SQLite> --token-id <token_id>\n使用 issue --help 或 revoke --help 查看具体选项。令牌明文只在签发时输出一次。": "Usage: gbcserver plugin-token issue|revoke [options]\n  issue  --db <SQLite> --server-id <ID> --scope <whitelist|coupon.redeem> [--expires-at <RFC3339>]\n  revoke --db <SQLite> --token-id <token_id>\nRun issue --help or revoke --help for command options. The token secret is printed only once when issued.",
	"用法：gbcserver plugin-token issue|revoke [选项]\n  issue  --db <SQLite> --server-id <标识> --scope <whitelist|coupon.redeem> [--expires-at <RFC3339>]\n  revoke --db <SQLite> --token-id <token_id>\n使用 issue --help 或 revoke --help 查看具体选项。令牌明文只在签发时输出一次。":   "Usage: gbcserver plugin-token issue|revoke [options]\n  issue  --db <SQLite> --server-id <ID> --scope <whitelist|coupon.redeem> [--expires-at <RFC3339>]\n  revoke --db <SQLite> --token-id <token_id>\nRun issue --help or revoke --help for command options. The token secret is printed only once when issued.",
	"全局语言参数需要 zh-CN 或 en":      "--lang requires zh-CN or en",
	"不支持的语言 %q；请使用 zh-CN 或 en": "Unsupported language %q; use zh-CN or en",
	"；请使用：": "; use: ",
	"请使用 gbcserver help plugin-token issue 或 revoke": "Use gbcserver help plugin-token issue or revoke",
}

var cliFormattedTranslations = buildCLIFormattedTranslations()

type cliFormattedTranslation struct {
	pattern    *regexp.Regexp
	translated string
	verbs      []string
}

func buildCLIFormattedTranslations() []cliFormattedTranslation {
	keys := make([]string, 0, len(cliTranslations))
	for key := range cliTranslations {
		if strings.Contains(key, "%") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	verbPattern := regexp.MustCompile(`%[a-zA-Z]`)
	translations := make([]cliFormattedTranslation, 0, len(keys))
	for _, key := range keys {
		verbs := verbPattern.FindAllString(key, -1)
		parts := verbPattern.Split(key, -1)
		var expression strings.Builder
		for index, part := range parts {
			expression.WriteString(regexp.QuoteMeta(part))
			if index < len(verbs) {
				expression.WriteString("(.*?)")
			}
		}
		translations = append(translations, cliFormattedTranslation{
			pattern: regexp.MustCompile("^" + expression.String() + "$"), translated: cliTranslations[key], verbs: verbs,
		})
	}
	return translations
}

// selectCLILanguage removes the global language option before command parsing. / 选择 CLI 语言并在命令解析前移除全局语言参数。
func selectCLILanguage(args []string) ([]string, error) {
	activeCLILanguage = detectCLILanguage(os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG"), systemLanguage())
	index := 0
	for index < len(args) {
		argument := args[index]
		if argument == "--lang" {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				return nil, errors.New("--lang requires zh-CN or en / --lang 必须指定 zh-CN 或 en")
			}
			language, ok := normalizeCLILanguage(args[index+1])
			if !ok {
				return nil, unsupportedCLILanguageError(args[index+1])
			}
			activeCLILanguage = language
			index += 2
			continue
		}
		if strings.HasPrefix(argument, "--lang=") {
			value := strings.TrimPrefix(argument, "--lang=")
			language, ok := normalizeCLILanguage(value)
			if !ok {
				return nil, unsupportedCLILanguageError(value)
			}
			activeCLILanguage = language
			index++
			continue
		}
		break
	}
	return append([]string(nil), args[index:]...), nil
}

func unsupportedCLILanguageError(value string) error {
	return fmt.Errorf("Unsupported language %q; use zh-CN or en / 不支持的语言 %q；请使用 zh-CN 或 en", value, value)
}

func normalizeCLILanguage(value string) (cliLanguage, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "en", "en-us", "en-gb", "english":
		return cliLanguageEnglish, true
	case "zh", "zh-cn", "zh-hans", "zh-sg", "chinese":
		return cliLanguageChinese, true
	default:
		return "", false
	}
}

func detectCLILanguage(values ...string) cliLanguage {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if language, ok := normalizeCLILanguage(value); ok {
			return language
		}
		if strings.HasPrefix(value, "zh_") || strings.HasPrefix(value, "zh-") {
			return cliLanguageChinese
		}
		if strings.HasPrefix(value, "en_") || strings.HasPrefix(value, "en-") {
			return cliLanguageEnglish
		}
		return cliLanguageChinese
	}
	return cliLanguageChinese
}

func cliText(chinese string) string {
	if activeCLILanguage == cliLanguageEnglish {
		if english, ok := cliTranslations[chinese]; ok {
			return english
		}
		for _, translation := range cliFormattedTranslations {
			matches := translation.pattern.FindStringSubmatch(chinese)
			if matches == nil {
				continue
			}
			translated := translation.translated
			for index, verb := range translation.verbs {
				translated = strings.Replace(translated, verb, matches[index+1], 1)
			}
			return translated
		}
		longestPrefix := ""
		for key := range cliTranslations {
			if len(key) > len(longestPrefix) && strings.HasSuffix(key, "://") && strings.HasPrefix(chinese, key) {
				longestPrefix = key
			}
		}
		if longestPrefix != "" {
			return cliTranslations[longestPrefix] + strings.TrimPrefix(chinese, longestPrefix)
		}
	}
	return chinese
}

func cliError(message string) error {
	return errors.New(cliText(message))
}

func cliErrorf(format string, arguments ...any) error {
	return fmt.Errorf(cliText(format), arguments...)
}

func cliSprintf(format string, arguments ...any) string {
	return fmt.Sprintf(cliText(format), arguments...)
}

func cliFprint(writer interface{ Write([]byte) (int, error) }, arguments ...any) {
	_, _ = fmt.Fprint(writer, localizeCLIArguments(arguments)...)
}

func cliFprintln(writer interface{ Write([]byte) (int, error) }, arguments ...any) {
	_, _ = fmt.Fprintln(writer, localizeCLIArguments(arguments)...)
}

func cliFprintf(writer interface{ Write([]byte) (int, error) }, format string, arguments ...any) {
	_, _ = fmt.Fprintf(writer, cliText(format), localizeCLIArguments(arguments)...)
}

func cliPrintln(arguments ...any) {
	_, _ = fmt.Fprintln(os.Stdout, localizeCLIArguments(arguments)...)
}

func cliPrintf(format string, arguments ...any) {
	_, _ = fmt.Fprintf(os.Stdout, cliText(format), localizeCLIArguments(arguments)...)
}

func localizeCLIArguments(arguments []any) []any {
	localized := make([]any, len(arguments))
	for index, argument := range arguments {
		switch value := argument.(type) {
		case string:
			localized[index] = cliText(value)
		case error:
			localized[index] = cliText(value.Error())
		default:
			localized[index] = argument
		}
	}
	return localized
}

func runCLILanguageDetectionForTest(env ...string) string {
	return string(detectCLILanguage(env...))
}
