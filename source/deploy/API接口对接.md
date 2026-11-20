# GBC Server API 对接说明

本文档描述 Go 服务当前提供的网页接口、兼容 `/api` 分发器以及 WhiteListSync、CouponPlugin 使用的插件接口。Web 默认绑定 `0.0.0.0:80`，证书有效时还会并行绑定 `0.0.0.0:443`；白名单和兑换插件 listener 默认绑定 loopback。`0.0.0.0` 只用于服务端监听，客户端应连接服务器实际 IP 或域名。Web 可从所有网卡访问，请用防火墙控制来源，并优先通过 HTTPS 访问。

## 服务地址与通用规则

| 服务 | 默认监听地址 | 用途 |
| --- | --- | --- |
| Web/API HTTP | `0.0.0.0:80` | 网页静态资源、账户、商城、管理接口和健康检查。 |
| Web/API HTTPS | `0.0.0.0:443`（证书有效时） | 与 HTTP 同时启动，使用 data 中的 TLS 证书。 |
| 白名单 listener | `127.0.0.1:18094` | Minecraft 插件内网登录校验、入服确认、在线心跳与登出。 |
| 兑换 listener | `127.0.0.1:18095` | 游戏服通过内网 HTTP 直接调用 CouponPlugin 的 `/api` 兑换接口。 |

实际端口和插件 listener 地址保存在 `gbcserver/gbcserver.db` 的 `app_config` 表中，由顶层设置命令和管理员页面修改。Web listener 接受 `0.0.0.0` 绑定全部 IPv4 网卡；白名单与兑换 listener 只允许 loopback、RFC1918/IPv6 ULA 或 link-local IP 字面地址，且会检查 TCP 对端是否来自内网，不信任 `X-Forwarded-For`。插件配置只需填写监听地址，不需要配置来源网段。跨机器部署时将插件 listener 绑定到 Go 服务端私网 IP，并用主机防火墙限制游戏服访问。插件 listener 使用内网 HTTP，不经公网反向代理。

默认 TLS 证书链位于 `<data-dir>/server.pem`，私钥位于 `<data-dir>/server.key`。证书和私钥均有效时，HTTP 与 HTTPS 同时启动；缺少或无法识别证书时只启动 HTTP。替换证书后运行 `reload` 或重启服务使其重新加载。Linux 直接绑定 80/443 需要 root 或 `CAP_NET_BIND_SERVICE`；无该权限时可使用 `httpsport` 设置高位端口。

JSON API 返回 UTF-8 JSON。网页用户 session 通过 `POST /api/webLogin` 获取，管理员 session 通过 `POST /api/adminLogin` 获取；两种 session 权限隔离并保存在内存，重启后必须重新登录。不要把 token、session、口令或 TOTP 写入公开日志、URL 或前端静态文件。生产站点可使用内置 HTTPS，也可由 HTTPS 反向代理对外提供服务。

## Web 基础接口

以下 `{base}` 指 Web/API 地址。

English: `{base}` refers to the Web/API base address.

| 方法与路径 | 认证 | 说明 |
| --- | --- | --- |
| `GET /healthz` | 无 | SQLite 可用时返回 `200 {"status":"ok"}`；数据库不可用时返回 `503`。 |
| `GET /api/publicConfig` | 无 | 返回网页可使用的上传大小、商品数量和请求体限制，不包含密钥。 |
| `POST /api/webLogin` | 无 | 玩家网页登录。JSON `{ "username":"玩家名", "password":"口令" }`；成功返回 `success/session/username/message`。 |
| `GET /api/userInfo` | Web session | 读取当前用户资料、积分、在线状态。Session 放在 `X-Web-Session`，也可用 query `session`；可附 `username`，但必须和 session 用户相同。 |
| `GET` 或 `POST /api/sign` | Web session | 执行每日签到；用 `X-Web-Session` 传 session，可选 `username`。重复签到以 JSON `success:false` 返回。 |
| `GET /api/signHistory` | Web session | 查询签到状态和历史。 |
| `GET /api/leaderboard` | Web 或管理员 session | 查询积分/在线榜单。Session 可放 `X-Web-Session`、`X-Admin-Session` 或 query `session`。 |
| `POST /api/adminLogin` | 无 | 管理员登录，输入字段见下方示例。 |
| `GET /api/admin/status` | 无 | 查询是否已完成管理员初始化；不会返回自定义管理员安全入口。 |
| `POST /api/admin/setup` | 未初始化且请求来自本机 | 仅接受 loopback TCP 连接；首次设置管理员账户和密码，`totpSecret` 可选。远程管理员请使用 SSH 端口转发。密码服务端生成 bcrypt 哈希后写入数据库。 |
| `POST /api/admin/totp/begin` | 管理员 session | 本地生成待绑定 TOTP 密钥和二维码；换绑已有密钥时需要当前验证码。 |
| `POST /api/admin/totp/confirm` | 管理员 session | 使用新验证码确认启用或换绑；换绑时当前验证码已在 begin 阶段验证。 |
| `POST /api/admin/totp/disable` | 管理员 session | 使用管理员密码和当前验证码关闭双重验证。 |
| `GET /api/admin/config` | 管理员 session | 读取数据库中的可编辑运行配置；密码哈希、TOTP secret 和 SMTP 密码只返回是否已配置。 |
| `PUT /api/admin/config` | 管理员 session | 写入可编辑运行配置；`adminPassword` 会立即转换为 bcrypt 哈希，不能直接写哈希。修改端口、入口或 listener 后从网页或命令行重新加载服务。 |
| `GET /api/admin/plugin-tokens` | 管理员 session | 列出插件令牌元数据和当前服务 ID；不返回 secret 或摘要。 |
| `POST /api/admin/plugin-tokens` | 管理员 session | 请求提供 `scope`、游戏服可访问的私网 IP `apiHost` 和可选 `expiresAt`；按当前 `serverID` 签发令牌，响应返回生成的 `apiURL` 和一次性 secret。 |
| `DELETE /api/admin/plugin-tokens/{tokenID}` | 管理员 session | 撤销指定令牌。 |
| `POST /api/admin/control` | 管理员 session | JSON action 可为 `reload`、`stop`、`install-autostart` 或 `uninstall-autostart`；管理式 `start` 支持网页重载、停止及当前用户自启管理。 |
| `POST /api/uploadAvatar` | Web session | JSON `{ "username":"玩家名", "avatar":"data:image/png;base64,..." }`；Session 通过 `X-Web-Session` 或 JSON `session` 传入。仅接受有效 PNG，服务端会把头像缩放到最长边 256 像素并重新编码。 |
| `POST /api/uploadItemImage` | 管理员 session | 使用 `multipart/form-data` 的 `file` 字段上传商品图片；仅接受 PNG/JPG/JPEG，受配置上限约束并按文件签名校验。图片会缩放到最长边 512 像素并重新编码；旧版 JSON PNG/JPG/JPEG data URL 仍兼容。成功返回本地 `imageUrl`，商品记录不得填写外部图片链接。 |
| `GET /api/serverStatus` | 无 | 探测配置中的服务器状态，含 TCP 与配置的第三方 API。 |
| `GET /ports` | 无 | 返回端口映射对象。 |
| `GET <webhookPath>/health` | 无 | 端口更新 webhook 健康探测。`webhookPath` 默认 `/webhook/update-port`。 |
| `POST <webhookPath>` | 无 | 更新已有服务名对应的 host/port；body 或 query 提供 `name`、`host`、`port`。name 与 host 必须和服务配置匹配。建议在反向代理/防火墙限制来源。 |

English: The administrator setup endpoint accepts requests only over loopback TCP and supports an optional `totpSecret`. Use SSH port forwarding for remote setup. The TOTP begin and confirm endpoints generate and verify QR-based enrollment locally; changing an existing secret requires the current code at the begin step. Each TOTP time step can be used once. Disabling TOTP requires the administrator password and current code. Administrator login accepts `totp` only when two-factor authentication is enabled.

玩家登录示例：

```sh
curl -sS -X POST 'https://gbc.example.com/api/webLogin' \
  -H 'Content-Type: application/json' \
  --data '{"username":"Steve","password":"玩家密码"}'
```

返回的 `session` 应由调用方安全保存，然后以 `X-Web-Session` 请求用户资料：

```sh
curl -sS 'https://gbc.example.com/api/userInfo' \
  -H 'X-Web-Session: <web-session>'
```

管理员登录使用数据库中的管理员账户和 bcrypt 密码哈希；如果已启用双重验证，还需提供 TOTP 验证码。请求字段为 `name`、`passwd`、`totp`：

```json
{"name":"admin","passwd":"管理员密码","totp":"当前六位验证码"}
```

成功返回 `{ "success":true, "session":"...", "message":"管理员登录成功" }`。后续管理请求使用 `X-Admin-Session`；兼容 `/api` 也支持 `session` 参数。失败请求按来源 IP 限流。

商品图片使用管理员 session 直接上传文件，服务端按文件魔数识别格式并返回本地路径；不要把公网 URL 写入商品记录：

```sh
curl -sS -X POST 'https://gbc.example.com/api/uploadItemImage' \
  -H 'X-Admin-Session: <admin-session>' \
  -F 'file=@./product.png'
```

成功响应中的 `imageUrl` 形如 `/items/<随机文件名>.png`，随后把它作为 `addShopItem` 或 `updateShopItem` JSON 的 `image` 字段。省略 `image` 时使用内置默认图；旧客户端提交 PNG/JPG/JPEG data URL 仍可兼容。历史生产目录中的 WebP/GIF 文件可以继续被引用，并可使用 `compress-images` 保留格式处理，但新上传会拒绝这两种格式。

## 兼容式 `/api` 分发器

旧版网页动作仍使用 `POST` 或 `GET /api`，通过 `method` 字段选择操作。例如 `POST /api` 的 JSON body 可为 `{"method":"getShopItems"}`；查询参数和 body 也可混用，body 同名字段优先。旧式业务失败大多仍以 HTTP 200 和 `{success:false,message}` 表示；登录、注册等部分动作返回纯文本。客户端应检查业务字段，不能只判断 HTTP 200。

| method | 权限 | 必需字段或用途 |
| --- | --- | --- |
| `getShopItems` | 无 | 读取商城商品；兼容 GET/POST。 |
| `purchaseItem` | 玩家 session | `username`、`itemId`；用户名必须属于当前 Web session。 |
| `changePassword` | 玩家 session | `name`、`oldpasswd`、`newpasswd`；本人修改密码。 |
| `login` / `logout` | 玩家 session | `name`；旧版 Minecraft 网页授权与登出兼容入口。新插件必须改用专用白名单 listener。 |
| `regist` | 无 | `name`、`passwd`、`email`；触发注册验证码邮件。 |
| `verifyRegist` | 无 | `email`、`code`；验证注册邮件验证码。 |
| `repasswd` | 无 | `name`、`newpasswd`；申请密码重置邮件。 |
| `verifyCode` | 无 | `email`、`code`；验证重置验证码。 |
| `getAllUsers` | 管理员 session | 查询用户列表。 |
| `activateUser` / `banUser` / `unbanUser` / `deleteUser` | 管理员 session | `name`；启用、封禁、解封或删除用户。 |
| `updateEmail` | 管理员 session | `name`、`newEmail`；更新邮箱。 |
| `addShopItem` | 管理员 session | JSON body 中提供 `name`、`itemId`、`amount`、`points`、`stock`、`image`、`description` 等商品字段；`image` 必须是上传接口返回的本地路径，省略时使用默认图。 |
| `updateShopItem` | 管理员 session | JSON body 中提供商品 `id` 和要更新的字段；图片只能使用上传接口返回的本地路径。 |
| `deleteShopItem` | 管理员 session | `itemId`；删除商品。 |
| `generateCoupon` | 管理员 session | JSON body `{ "type":"item|bundle", "items":[{"itemId":"...","amount":1}], "expiresAt":"RFC3339时间" }`；可选 `designatedPlayer`、`oneTimeUse`。 |
| `getCoupons` / `deleteCoupon` | 管理员 session | 查询兑换码；删除时提供 `code`。 |
| `useCoupon` | 不可通过 Web listener 使用 | Web `/api` 返回 401。必须通过下方独立兑换 listener 和 `coupon.redeem` token。 |

管理员 session 兼容参数可以放在 `session` 字段或 `X-Admin-Session` header。`addShopItem`、`updateShopItem` 和 `generateCoupon` 的业务字段必须放在 JSON body。`uuid`、`loginTime`、`onlineTime` 不允许由客户端指定；关键参数不能包含空格。

## WhiteListSync 白名单接口

每个 Go 服务实例使用独立的 `whitelist` scope Bearer token。token 使用 `plugin-token issue --server-id <稳定服务ID> --scope whitelist` 签发；该服务 ID 必须与数据库配置 `serverID` 相同（默认 `default`）。同一实例的兑换 token 也使用这个 `server_id`，但 token scope、`token_id` 和 secret 分开签发。所有接口均为 `POST`，调用头：

```http
Authorization: Bearer <whitelist-token>
Content-Type: application/json
```

实际 `api-url` 指向白名单 listener 的内网地址，例如 `http://10.20.0.5:18094`。路径中的用户名需进行 URL path 编码。

| 路径 | 请求 | 成功响应 |
| --- | --- | --- |
| `/check/{username}` | 空 body | `{"allowed":true,"reason":"authorized","loginAttemptId":"...","expiresAt":<毫秒时间戳>}`。业务拒绝返回 `403`、`allowed:false` 与稳定 `reason`。 |
| `/join/{username}` | Header `X-Login-Attempt-ID: <loginAttemptId>` | `{"ok":true,"alreadyOpen":false}`。仅确认 `/check` 创建的有效登录尝试。 |
| `/heartbeat` | JSON `{"players":["Steve","Alex"]}` | `{"ok":true,"rejectedPlayers":[]}`；最多 5000 名玩家。 |
| `/logout/{username}` | 空 body | `{"ok":true,"alreadyClosed":false}`。 |

鉴权失败返回 401，scope 不匹配或来源不允许返回 403，方法不支持返回 405，服务/数据库暂不可用返回 503。插件应按 fail-closed 策略处理白名单错误。令牌只放在插件配置和受限的服务端密钥存储中，不能放进网页代码。

## CouponPlugin 兑换接口

CouponPlugin 直接连接 Go 程序的内网兑换 listener，例如 `http://10.20.0.5:18095/api`；游戏服和 Go 服务同机时使用 `http://127.0.0.1:18095/api`。地址只允许 loopback、RFC1918、IPv6 ULA 或 link-local IP 字面地址，协议使用 HTTP。白名单与兑换插件都通过内网直连，不配置来源网段，也不需要公网反向代理。跨机器部署时只需配置服务端私网监听地址，并在主机防火墙中限制游戏服来源。

请求必须是 `POST`，并携带 `coupon.redeem` scope Bearer token。token 用 `plugin-token issue --server-id <与serverID配置相同的服务ID> --scope coupon.redeem` 签发；它与白名单 token 共用 `server_id`，但必须分开保存。JSON 示例：

```http
POST /api HTTP/1.1
Authorization: Bearer <coupon.redeem-token>
Content-Type: application/json
X-Request-ID: <稳定且唯一的请求ID>
```

```json
{
  "method": "useCoupon",
  "username": "Steve",
  "code": "ABCD-EFGH",
  "requestId": "<与X-Request-ID相同的请求ID>"
}
```

成功响应为 `{"success":true,"message":"兑换成功","items":[{"itemId":"minecraft:diamond","amount":1}]}`；业务拒绝为 `success:false`。网络超时、408、425、429、5xx 等可重试响应时，客户端必须使用**同一个** `requestId` 重试。Go 在 SQLite 中以请求 ID 保存兑换结果，防止重试重复扣码；更换 request ID 会被视作新请求。调用方应核对响应成功后再发放物品。

接口错误包括 400 参数不完整、401 token 缺失或无效、403 来源或权限不符、405 非 POST、415 非 JSON、429 限流、413 body 超限、503 服务不可用。不要将 coupon token 用于白名单接口，也不要把兑换源站端口直接暴露公网。

## 令牌与安全检查

```sh
# 交互选择签发或撤销，并输出插件配置片段
./gbcserver-linux-amd64 plugin-token

# 自动化脚本仍可使用显式参数模式
./gbcserver-linux-amd64 plugin-token issue \
  --db /var/lib/gbcserver/gbcserver.db \
  --server-id default --scope whitelist
```

令牌明文只在签发时显示一次。发生泄露时立即撤销旧 `token_id` 并重新签发。白名单和兑换的数据库路径必须与正在运行的服务所用数据库一致。首次部署时先生成配置/数据库并启动一次，再签发 token；已有数据库不应通过删除文件来重置。

