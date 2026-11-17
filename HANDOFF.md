# 交接说明

- Go 源码在 `source/`，网页源文件在 `source/web/`。
- 网页变更需同步到 `source/cmd/gbcserver/web/`；此分支只构建 GitHub 管理版。
- GitHub 网页资源清单在 `source/cmd/gbcserver/embedded_web_github.go`；管理员静态资源只通过安全入口访问，例如 `/admin/admin-tokens.js`。
- `source/build.ps1` 会先递归清空嵌入网页目录，再从网页源目录复制 GitHub 发布资源。目录内若有额外文件，运行前需先确认用途。
- 两个 Java 插件需分别使用 Maven 离线构建；JAR 输出到 `目标文件/plugins/whitelist/` 和 `目标文件/plugins/coupon/`，不进入 GitHub 管理版 ZIP。Windows Maven 启动器可能在日志前打印 `Access is denied.`；确认命令退出码为 0 且输出 `BUILD SUCCESS` 后再检查 JAR。
- Windows 下 Go 测试若在 `%TEMP%` 启动测试程序时返回 `Access is denied`，将 `GOTMPDIR` 指向工作区内目录，例如 `.codex-tools/gotmp`。
- 状态历史存于 SQLite 的 `status_samples`，保留七天；相关 schema 迁移版本定义在 `source/internal/store/schema.go`。
