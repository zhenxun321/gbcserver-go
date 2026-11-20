<div align="center">
  <img src="source/web/images/logo-readme.png" alt="Gbcserver-Go logo" width="180">
  <h1>Gbcserver-Go</h1>
  <p>Player pages, administration, and plugin services for Minecraft Java servers.</p>
  <p><a href="README.md">简体中文</a> | <a href="https://github.com/zhenxun321/gbcserver-go/releases">Downloads and changelogs</a></p>
</div>

Gbcserver-Go provides player login, check-ins, a points shop, server status, and administrator settings for Minecraft Java servers. Separate Paper/Leaves plugins provide whitelist synchronization and coupon redemption.

![Player login page](source/web/images/readme-login.png)

## Features

- Player login, account details, daily check-ins, and a points shop
- Current Minecraft server status and history
- Site settings, administrator accounts, and plugin access management
- Whitelist and coupon features through separate Paper/Leaves plugins
- SQLite storage for service settings and player data

## Install

Download the ZIP for your platform from [GitHub Releases](https://github.com/zhenxun321/gbcserver-go/releases), extract it, and start the program from its directory:

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

Initial administrator setup is available only from the local machine. For remote deployment, use SSH port forwarding to open the administration page, then set the username and password. Two-factor authentication is optional during setup and can be enabled later in the administration settings. To enable it, add the displayed QR code to an authenticator and enter a verification code. Disabling it requires the administrator password and current verification code.

The default HTTP port is 80. If it is unavailable, run `gbcserver-<platform> httpport 8080` first. Pass the same `--data-dir <directory>` to startup and configuration commands when using a custom data directory.

Game plugins are installed separately. See the plugin guides and deployment documentation in the repository.

## Windows security notice

Windows binaries use a self-signed code-signing certificate. Windows does not trust it by default and may show an unknown publisher or security warning.

## Help

See the [release notes](source/deploy/gbcserver-go发布说明.md), [deployment guide](source/deploy/部署说明.md), and [API integration guide](source/deploy/API接口对接.md).
