# Gbcserver-Go v1.0.3

Gbcserver-Go 为 Minecraft Java 服务器提供玩家登录、签到、商城、管理页面和插件接口。

Gbcserver-Go provides player login, daily check-ins, a shop, an administration panel, and plugin APIs for Minecraft Java servers.

## 更新日志

- [重构] 重建管理端、玩家端和登录页的页面外壳与响应式布局。
- [修复] 限制宽屏内容宽度，修复插件接入页面错位、令牌表单对齐和状态页品牌图标裁切。
- [修复] 登录页主题与语言按钮聚集在工具栏右侧，侧栏显示项目图标。
- [新增] 用户列表无数据时显示空状态。
- [新增] 管理员可手动创建并立即激活玩家账户。
- [修复] 调整管理端商城留白、统计卡片、筛选控件和侧栏分隔线。
- [修复] 修正兑换码过期日期格式，按配置时区有效至所选日期结束；恢复生成结果弹窗并收紧表格宽度。
- [修复] 修复玩家内容外框和管理员双重验证布局，状态查询失败时停止绑定，并标记移除开机自启为危险操作。
- [修复] 完整显示发布版服务器配置，恢复 SMTP、插件接口、目录证书和 Mojang 参数；保存管理员及 SMTP 新密码。
- [新增] 管理员可输入自定义 TOTP 密钥；只有验证码校验成功后才启用。
- [修复] 兑换码筛选区与表格跟随商城宽度对齐，修复宽屏缩放与筛选间距。
- [移除] 站点文案自定义入口及其公开读取和保存接口；旧数据库配置保留但不再使用。
- [更新] 状态页显示软件版本 v1.0.3。
- [更新] 九个平台 ZIP 统一命名为 `gbcserver-<系统>-<架构>.zip`，压缩包内主程序统一为 `gbcserver`，并附带 Minecraft 插件及部署/API 文档。

## Changelog

- [Refactored] Rebuilt the admin, player, and login page shells with responsive layouts.
- [Fixed] Capped wide-screen content, corrected the misplaced plugin access page and token form alignment, and prevented status-page logo clipping.
- [Fixed] Grouped login theme and language controls on the right and added the project icon to the sidebar.
- [Added] Displayed an empty state when the user list has no entries.
- [Added] Allowed administrators to create and immediately activate player accounts.
- [Fixed] Adjusted admin shop spacing, summary cards, filters and sidebar dividers.
- [Fixed] Accepted coupon expiry dates through the selected day in the configured timezone, restored the result dialog and constrained the coupon table width.
- [Fixed] Restored frameless player content, corrected the admin two-factor layout, stopped enrollment when status lookup fails, and marked startup removal as a dangerous action.
- [Fixed] Showed all release server settings, including SMTP, plugin listeners, paths, certificates and Mojang options, and enabled saving new admin and SMTP passwords.
- [Added] Allowed administrators to enter a custom TOTP secret. Authentication remains disabled until its verification code succeeds.
- [Fixed] Aligned the coupon filters and table to the shop width and restored spacing on wide screens.
- [Removed] Site copy customization and its public read and save paths. Existing database values are retained but ignored.
- [Updated] The status page shows software version v1.0.3.
- [Updated] Standardized all nine ZIP names as `gbcserver-<system>-<architecture>.zip`. Each archive names its main executable `gbcserver` and includes Minecraft plugins plus deployment and API documentation.

## 安装与启动

下载 `gbcserver-<系统>-<架构>.zip`，解压后在程序目录启动：

```text
gbcserver.exe start --tray
./gbcserver start --background
```

首次打开管理员页面时，设置管理员账户和密码。双重验证可暂不配置，登录后可在管理员设置中启用。默认 HTTP 端口为 80；端口受限时先运行 `gbcserver-<平台> httpport 8080`。自定义数据目录时，为启动和设置命令传入相同的 `--data-dir <目录>`。

每个 ZIP 都包含 `plugins/whitelist/` 和 `plugins/coupon/` 中的 Minecraft 插件 JAR，并附带服务部署、API 对接和插件配置说明。将所需插件 JAR 复制到 Paper/Leaves 服务端的 `plugins/` 目录，按包内说明配置 listener 和令牌。

Windows 程序使用自签名代码签名证书。Windows 默认不信任该证书，可能显示未知发布者或安全警告。签名不代表 Windows 或安全软件已信任程序。

## Install and start

Download `gbcserver-<system>-<architecture>.zip` and extract it. Start the main program from the package directory:

```text
gbcserver.exe start --tray
./gbcserver start --background
```

On first use, set the administrator username and password. Two-factor authentication is optional during setup and can be enabled later in the administration settings. The default HTTP port is 80. If it is unavailable, run `gbcserver-<platform> httpport 8080` first. Pass the same `--data-dir <directory>` to startup and configuration commands when using a custom data directory.

Each ZIP includes the WhiteListSync and CouponPlugin JARs under `plugins/whitelist/` and `plugins/coupon/`, together with deployment, API and plugin setup guides. Copy the required JARs to the Paper/Leaves server's `plugins/` directory, then configure listeners and tokens using the included documentation.

Windows binaries are signed with a self-signed code-signing certificate. Windows does not trust it by default and may show an unknown publisher or security warning. The signature does not mean Windows or security software trusts the program.
