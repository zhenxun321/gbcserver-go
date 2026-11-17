# GBC Server Go 目录说明

项目根目录按源码、构建产物、插件和文档分类。此分支只构建 GitHub 管理版，移除玩家官网首页及专用资源，保留玩家登录、状态页和管理系统。系统名称、登录页标题、状态页标题和页脚，以及状态目标与状态 API 均可在管理页修改。

| 目录 | 内容 |
| --- | --- |
| `source/` | Go module、Web/API、插件 listener、前端源文件和构建脚本 |
| `目标文件/` | 本地生成的 GitHub 管理版程序、部署 ZIP 和插件 JAR，不提交到 Git |
| `plugins/` | WhiteListSync 与 CouponPlugin Java 源工程 |
| `source/deploy/` | 部署、API 对接和 GitHub 管理版说明 |

在项目根目录使用 PowerShell 7：

```powershell
Push-Location .\source
go test ./...
go vet ./...
Pop-Location
.\source\build.ps1
.\source\package.ps1
mvn -o -f .\plugins\whitelist\pom.xml clean package
mvn -o -f .\plugins\coupon\pom.xml clean package
```

插件 JAR 分别输出到 `目标文件/plugins/whitelist/` 和 `目标文件/plugins/coupon/`；它们作为独立产物保留，不放入 GitHub 管理版 ZIP。

## 部署行为

单文件首次启动在部署根目录创建 `gbcserver/`，并在其中初始化 SQLite 与运行子目录；不会生成或读取 JSON 配置。Linux 默认数据根为当前用户 `$HOME/gbcserver/`；Windows 默认数据根为程序目录下的 `gbcserver/`，程序位于 `bin/` 时使用其上级目录。运行配置全部保存在 SQLite 的 `app_config` 表。

```text
gbcserver start [--data-dir <data目录>] [--background|--tray] [--autostart|--no-autostart]
gbcserver httpport <端口> [--data-dir <data目录>]
gbcserver httpsport <端口> [--data-dir <data目录>]
gbcserver admin-path <入口> [--data-dir <data目录>]
gbcserver admin-username <账户> [--data-dir <data目录>]
gbcserver admin-password <密码> [--data-dir <data目录>]
gbcserver reload [--data-dir <data目录>]
gbcserver stop [--data-dir <data目录>]
```

端口和三个 admin 设置是顶层命令，不是 `start` 参数；设置命令完成后永久写入数据库。`admin-password` 只写 bcrypt 哈希。首次访问默认 `/admin/` 会进入管理员初始化页面，初始化账户、密码和 TOTP。管理员 session 与玩家 session 隔离。

直接运行 `gbcserver plugin-token` 可交互签发或撤销令牌；填写游戏服可访问的私网 IP 后，程序会自动生成插件 API 地址。管理员网页的“插件接入”页也能管理令牌；“运行配置”页可修改运行配置、重新加载或停止服务，并安装或移除当前用户的开机自启。启动和离线数据库维护仍需在主机本地执行。

Web 默认监听 `0.0.0.0:80`。`gbcserver/server.pem` 与 `gbcserver/server.key` 有效时同时监听 `0.0.0.0:443`，否则只启动 HTTP。白名单和兑换 listener 默认使用 `127.0.0.1:18094`、`127.0.0.1:18095`，跨机器时使用服务端私网 IP，并用主机防火墙限制游戏服访问；两个插件均经内网直连，不使用公网反向代理或来源网段配置。

Windows 双击进入托盘，Linux 无参数启动等价于 `start --background`。同一台机器只允许一个实例；已有实例运行时再次 `start` 会提示并退出。`reload` 和 `stop` 使用 data 目录中的本机管理通道。玩家页面不含管理员入口和管理代码；管理员页面由数据库中的自定义安全入口单独提供，管理员与玩家使用独立会话。

GitHub ZIP 只含不带玩家首页的管理版程序和说明，不含插件 JAR 或运行数据。所有 ZIP 都不包含数据库、头像、商品图片、WAL/SHM、源码、管理员口令、TOTP、插件令牌、SMTP 凭据或 TLS 私钥。

当前工作区没有 `目标文件/gbcserver/` 生产数据目录。生产数据库、头像和商品图片须从受限备份单独恢复，不能用构建产物代替。

管理员入口默认使用深色模式；首次访问管理员页面会加载 `theme.js`，主题按钮可切换并记住浏览器选择。网页可管理运行配置、插件令牌并执行重载和停止。启动、自启、数据库初始化与迁移仍通过本机命令执行。运行配置字段和示例见 [`source/deploy/部署说明.md`](source/deploy/部署说明.md)。

详细命令、数据库配置、TLS、内网插件 listener 和 API 示例见 [`source/deploy/部署说明.md`](source/deploy/部署说明.md)、[`source/deploy/API接口对接.md`](source/deploy/API接口对接.md) 与根目录 [`openapi.yaml`](openapi.yaml)。
