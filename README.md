# gbcserver-go

gbcserver-go 为 Minecraft Java 服务器提供玩家网页、管理页面、白名单接口和兑换码接口。服务端使用 Go；白名单与兑换功能由独立的 Paper/Leaves 插件连接。

此仓库用于构建和发布 gbcserver-go 正式版。程序不包含玩家官网首页。部署 ZIP、九个平台的 Go 程序和插件 JAR 均由源码构建，不提交到 Git。

## 环境要求

- Go 1.24 或更高版本
- 构建插件需要 JDK 17 或更高版本和 Maven
- 插件运行于 Paper/Leaves 1.21 系列服务器

## 检查和构建

在仓库根目录使用 PowerShell 7：

```powershell
Push-Location .\source
try {
    go test ./...
    go vet ./...
} finally {
    Pop-Location
}

.\source\build.ps1
# 本地未签名测试包；正式 Windows 发布需要签名文件
.\source\package.ps1 -SkipWindowsSignatureCheck
mvn -f .\plugins\whitelist\pom.xml clean package
mvn -f .\plugins\coupon\pom.xml clean package
```

Go 程序和部署 ZIP 输出到 `目标文件/`。WhiteListSync 与 CouponPlugin 的 JAR 分别输出到 `目标文件/plugins/whitelist/` 和 `目标文件/plugins/coupon/`。插件 JAR 单独部署，不包含在发布 ZIP 中。

## 文档

- [运行与部署](source/deploy/部署说明.md)
- [gbcserver-go 发布说明](source/deploy/gbcserver-go发布说明.md)
- [API 对接说明](source/deploy/API接口对接.md)
- [WhiteListSync 插件说明](plugins/whitelist/插件说明.md)
- [CouponPlugin 插件说明](plugins/coupon/插件说明.md)
- [OpenAPI 定义](openapi.yaml)
