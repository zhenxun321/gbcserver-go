# Gbcserver-Go

Gbcserver-Go 正式版不包含玩家官网首页。访问根路径会进入玩家登录页；管理员入口默认为 `/admin/`，首次启动后可在数据库中更改。

Windows 程序使用自签名代码签名证书。Windows 默认不信任该证书，可能显示未知发布者或安全警告；该签名不代表 Windows 或安全软件已信任程序。

Windows binaries are signed with a self-signed code-signing certificate. Windows does not trust it by default and may show an unknown publisher or security warning. The signature does not mean Windows or security software trusts the program.

## 启动

解压对应平台 ZIP，在程序所在目录运行：

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

默认 HTTP 端口为 80。端口受限时，先运行 `gbcserver-<平台> httpport 8080`。默认数据目录为 Windows 程序旁的 `gbcserver/` 或 Linux 当前用户目录下的 `gbcserver/`；自定义目录时为启动和设置命令都传入 `--data-dir <目录>`。

首次访问 `http://服务器地址:端口/admin/`，设置管理员账户、密码和 TOTP。运行配置、插件令牌、重载和停止操作都可从管理页面完成。

## 游戏插件

ZIP 不含插件 JAR。请从[项目仓库](https://github.com/zhenxun321/gbcserver-go)的 `plugins/` 工程构建，并按[部署说明](https://github.com/zhenxun321/gbcserver-go/blob/main/source/deploy/部署说明.md)配置白名单和兑换 listener。插件令牌可在管理页面签发；两个插件使用不同令牌。

## English

Gbcserver-Go release builds do not include a public landing page. The root path opens the player login page. The administrator entry is `/admin/` by default and can be changed in the database after the first start.

Windows binaries are signed with a self-signed code-signing certificate. Windows does not trust it by default and may show an unknown publisher or security warning. The signature does not mean Windows or security software trusts the program.

## Start

Extract the ZIP for your platform and run the program from its directory:

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

The default HTTP port is 80. If that port is restricted, run `gbcserver-<platform> httpport 8080` first. The default data directory is `gbcserver/` beside the Windows executable or under the current Linux user's home directory. Pass `--data-dir <directory>` to both startup and configuration commands to use a custom directory.

Open `http://server-address:port/admin/` on first use to set the administrator account, password, and TOTP. Runtime configuration, plugin tokens, reload, and stop operations are available in the administration page.

## Game plugins

ZIP files do not contain plugin JARs. Build the `plugins/` projects in the [repository](https://github.com/zhenxun321/gbcserver-go) and follow the [deployment guide](https://github.com/zhenxun321/gbcserver-go/blob/main/source/deploy/部署说明.md) to configure the whitelist and coupon listeners. Issue separate tokens for the two plugins in the administration page.
