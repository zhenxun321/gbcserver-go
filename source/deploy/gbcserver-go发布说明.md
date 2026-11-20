# Gbcserver-Go v1.0.2

Gbcserver-Go 为 Minecraft Java 服务器提供玩家登录、签到、商城、管理页面和插件接口。

Gbcserver-Go provides player login, daily check-ins, a shop, an administration panel, and plugin APIs for Minecraft Java servers.

## 更新日志

- [修复] 统一玩家、管理员和状态页面的颜色、间距、表单、面板与弹窗样式，采用 Material 3 视觉规范。
- [修复] 状态页标题图标对齐到标题区域，避免漂移和遮挡。
- [修复] 手机底部导航提供滚动提示，避免主题和退出入口被误认为不可用。
- [修复] 玩家登录后仍可切换网页语言。
- [修复] 窄屏表单、配置列表、状态信息和按钮支持换行与单列布局。
- [更新] 状态页默认页脚显示软件版本 v1.0.2。
- [更新] 管理员、玩家和状态页面使用随程序打包的本地样式资源。

## Changelog

- [Fixed] Unified player, administrator, and status page colors, spacing, forms, panels, and dialogs with a Material 3 visual style.
- [Fixed] Aligned the status page heading icon to prevent it from drifting or overlapping content.
- [Fixed] Added a visible scroll indicator to mobile navigation so theme and sign-out actions remain discoverable.
- [Fixed] Kept the language switch available after player login.
- [Fixed] Improved narrow-screen wrapping and single-column layouts for forms, configuration lists, status details, and actions.
- [Updated] The default status page footer shows software version v1.0.2.
- [Updated] Administrator, player, and status pages use local styles bundled with the program.

## 安装与启动

下载对应平台的 ZIP，解压后在程序目录启动：

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

首次打开管理员页面时，设置管理员账户和密码。双重验证可暂不配置，登录后可在管理员设置中启用。默认 HTTP 端口为 80；端口受限时先运行 `gbcserver-<平台> httpport 8080`。自定义数据目录时，为启动和设置命令传入相同的 `--data-dir <目录>`。

下载包不包含游戏插件。请从项目仓库分别构建 WhiteListSync 和 CouponPlugin，并按部署说明配置连接地址和令牌。

Windows 程序使用自签名代码签名证书。Windows 默认不信任该证书，可能显示未知发布者或安全警告。签名不代表 Windows 或安全软件已信任程序。

## Install and start

Download and extract the ZIP for your platform, then start it from the program directory:

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

On first use, set the administrator username and password. Two-factor authentication is optional during setup and can be enabled later in the administration settings. The default HTTP port is 80. If it is unavailable, run `gbcserver-<platform> httpport 8080` first. Pass the same `--data-dir <directory>` to startup and configuration commands when using a custom data directory.

The download does not include game plugins. Build WhiteListSync and CouponPlugin from the project repository, then configure their connections and tokens using the deployment guide.

Windows binaries are signed with a self-signed code-signing certificate. Windows does not trust it by default and may show an unknown publisher or security warning. The signature does not mean Windows or security software trusts the program.
