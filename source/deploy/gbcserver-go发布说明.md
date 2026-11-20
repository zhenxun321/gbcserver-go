# Gbcserver-Go v1.0.1

Gbcserver-Go 为 Minecraft Java 服务器提供玩家登录、签到、商城、管理页面和插件接口。

Gbcserver-Go provides player login, daily check-ins, a shop, an administration panel, and plugin APIs for Minecraft Java servers.

## 更新日志

- [新增] 网页和命令行支持简体中文与英文。
- [新增] 管理员初始化时可跳过双重验证；之后可在管理页面启用。
- [新增] 启用或更换双重验证时显示本地生成的二维码；换绑时需先验证当前验证码，关闭时需管理员密码和当前验证码。
- [修复] UTC+0 签到时区被错误当成默认 UTC+8。
- [修复] 双重验证换绑流程阻止旧会话覆盖已变更的密钥。
- [修复] 管理员初始化仅接受本机连接，避免远程请求抢先创建账户。
- [修复] 同一 TOTP 验证码不能重复用于登录或双重验证设置。
- [修复] 管理员名等动态内容不会被网页翻译改写，补齐令牌管理页英文提示。
- [修复] 无效初始化密钥不会留下半初始化账户；CLI 错误提示保持所选语言。
- [更新] 网页图标与二维码使用本地资源。

## 安装与启动

下载对应平台的 ZIP，解压后在程序目录启动：

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

首次打开管理员页面时，设置管理员账户和密码。双重验证可暂不配置，登录后可在管理员设置中启用。默认 HTTP 端口为 80；端口受限时先运行 `gbcserver-<平台> httpport 8080`。自定义数据目录时，为启动和设置命令传入相同的 `--data-dir <目录>`。

下载包不包含游戏插件。请从项目仓库分别构建 WhiteListSync 和 CouponPlugin，并按部署说明配置连接地址和令牌。

Windows 程序使用自签名代码签名证书。Windows 默认不信任该证书，可能显示未知发布者或安全警告。签名不代表 Windows 或安全软件已信任程序。

## Changelog

- [Added] Simplified Chinese and English support for the web interface and CLI.
- [Added] Administrator setup can skip two-factor authentication; it can be enabled later in the administration settings.
- [Added] Two-factor setup displays a locally generated QR code. Rebinding requires the current code before setup; disabling requires the administrator password and current code.
- [Fixed] UTC+0 check-in timezone was incorrectly treated as the UTC+8 default.
- [Fixed] Initial administrator setup accepts only loopback connections, preventing remote account takeover during first startup. Use SSH port forwarding for remote administration.
- [Fixed] TOTP codes cannot be reused for login or two-factor settings.
- [Fixed] Dynamic account names are excluded from web translation, and token management prompts are translated.
- [Fixed] Invalid setup secrets no longer leave a partially configured administrator; CLI errors follow the selected language.
- [Fixed] Prevented an older session from overwriting a two-factor secret changed by another session.
- [Updated] Web icons and QR codes use local resources.

## Install and start

Download and extract the ZIP for your platform, then start it from the program directory:

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

On first use, set the administrator username and password. Two-factor authentication is optional during setup and can be enabled later in the administration settings. The default HTTP port is 80. If it is unavailable, run `gbcserver-<platform> httpport 8080` first. Pass the same `--data-dir <directory>` to startup and configuration commands when using a custom data directory.

The download does not include game plugins. Build WhiteListSync and CouponPlugin from the project repository, then configure their connections and tokens using the deployment guide.

Windows binaries are signed with a self-signed code-signing certificate. Windows does not trust it by default and may show an unknown publisher or security warning. The signature does not mean Windows or security software trusts the program.
