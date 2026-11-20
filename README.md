<div align="center">
  <img src="source/web/images/logo-readme.png" alt="Gbcserver-Go logo" width="180">
  <h1>Gbcserver-Go</h1>
  <p>面向 Minecraft Java 服务器的玩家网页、管理和插件服务。</p>
  <p><a href="README.en.md">English</a> | <a href="https://github.com/zhenxun321/gbcserver-go/releases">下载与更新日志</a></p>
</div>

Gbcserver-Go 为 Minecraft Java 服务器提供玩家登录、签到、商城、状态查看和管理员配置功能，并支持白名单与兑换码插件。

![玩家登录页](source/web/images/readme-login.png)

## 能做什么

- 玩家登录、账户资料、每日签到和积分商城
- 查看游戏服务器在线状态与历史记录
- 管理站点参数、管理员账户和插件接入
- 通过独立的 Paper/Leaves 插件提供白名单和兑换码功能
- 使用 SQLite 保存服务配置和玩家数据

## 安装

从 [GitHub Releases](https://github.com/zhenxun321/gbcserver-go/releases) 下载对应平台的 ZIP，解压后在程序目录启动：

```text
gbcserver-windows-amd64.exe start --tray
./gbcserver-linux-amd64 start --background
```

首次管理员初始化仅允许本机连接。远程部署时请先用 SSH 端口转发访问管理页，再设置账户和密码。双重验证可暂不配置，之后可在管理员设置中启用。启用时请将二维码添加到身份验证器，并输入一次验证码确认；关闭时需要管理员密码和当前验证码。

默认 HTTP 端口为 80。端口不可用时，先运行 `gbcserver-<平台> httpport 8080`。自定义数据目录时，为启动和设置命令传入相同的 `--data-dir <目录>`。

游戏插件需单独部署。请查看仓库中的插件说明和部署指南。

## Windows 安全提示

Windows 程序使用自签名代码签名证书。Windows 默认不信任该证书，可能显示未知发布者或安全警告。

## 获取帮助

请查看 [发布说明](source/deploy/gbcserver-go发布说明.md)、[部署指南](source/deploy/部署说明.md) 和 [API 对接说明](source/deploy/API接口对接.md)。
