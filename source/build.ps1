param(
    [string]$OutputDirectory = "目标文件/v1.0.1-build",
    [string]$SigningCertificateThumbprint = '',
    [switch]$RequireWindowsSignature
)

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot 'scripts\authenticode.ps1')
if ([string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
    $SigningCertificateThumbprint = $env:GBC_CODE_SIGN_CERT_THUMBPRINT
}
if (-not [string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
    $null = Get-GBCCodeSigningCertificate -Thumbprint $SigningCertificateThumbprint
} elseif ($RequireWindowsSignature) {
    throw "已要求 Windows 签名，但没有提供 -SigningCertificateThumbprint 或 GBC_CODE_SIGN_CERT_THUMBPRINT。"
}

$targets = @(
    @{ Name = "windows-386"; GOOS = "windows"; GOARCH = "386"; GOARM = $null },
    @{ Name = "windows-amd64"; GOOS = "windows"; GOARCH = "amd64"; GOARM = $null },
    @{ Name = "windows-arm64"; GOOS = "windows"; GOARCH = "arm64"; GOARM = $null },
    @{ Name = "linux-386"; GOOS = "linux"; GOARCH = "386"; GOARM = $null },
    @{ Name = "linux-amd64"; GOOS = "linux"; GOARCH = "amd64"; GOARM = $null },
    @{ Name = "linux-arm64-aarch64"; GOOS = "linux"; GOARCH = "arm64"; GOARM = $null },
    @{ Name = "linux-armv5"; GOOS = "linux"; GOARCH = "arm"; GOARM = "5" },
    @{ Name = "linux-armv6"; GOOS = "linux"; GOARCH = "arm"; GOARM = "6" },
    @{ Name = "linux-armv7"; GOOS = "linux"; GOARCH = "arm"; GOARM = "7" }
)

$workspaceRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$moduleRoot = $PSScriptRoot
$canonicalWeb = Join-Path $moduleRoot "web"
$embeddedWeb = Join-Path $moduleRoot "cmd\gbcserver\web"
if (-not (Test-Path -LiteralPath $canonicalWeb -PathType Container)) {
    throw "网页源目录不存在：$canonicalWeb"
}
New-Item -ItemType Directory -Path $embeddedWeb -Force | Out-Null
# 只把 go:embed 声明需要的网页文件复制到生成目录，避免把运行时商品图片等资源带入构建上下文。
Get-ChildItem -LiteralPath $embeddedWeb -Force | Remove-Item -Recurse -Force
$embeddedRootFiles = @(
    'admin.html', 'login.html', 'ping.html', 'admin.css', 'admin-config.js', 'admin-legacy.js',
    'admin-tokens.js', 'player.js', 'status-page.js', 'theme.js', 'site-config.js',
    'interaction-effects.js', 'i18n.js', 'i18n-player.js', 'i18n-admin.js', 'minecraft-items.json', 'system-redesign.css',
    'interaction-effects.css', 'theme.css', 'visual-polish.css', 'status-redesign.css'
)
foreach ($fileName in $embeddedRootFiles) {
    $sourcePath = Join-Path $canonicalWeb $fileName
    if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
        throw "GitHub 网页资源不存在：$sourcePath"
    }
    Copy-Item -LiteralPath $sourcePath -Destination (Join-Path $embeddedWeb $fileName) -Force
}
$embeddedFontAwesome = Join-Path $embeddedWeb 'vendor\fontawesome'
New-Item -ItemType Directory -Path (Join-Path $embeddedFontAwesome 'css'), (Join-Path $embeddedFontAwesome 'webfonts') -Force | Out-Null
foreach ($relativePath in @('css/fontawesome.min.css', 'css/solid.min.css', 'webfonts/fa-solid-900.woff2', 'webfonts/fa-solid-900.ttf', 'LICENSE.txt')) {
    $sourcePath = Join-Path $canonicalWeb "vendor/fontawesome/$relativePath"
    if (-not (Test-Path -LiteralPath $sourcePath -PathType Leaf)) {
        throw "Font Awesome 本地资源不存在：$sourcePath"
    }
    $destinationPath = Join-Path $embeddedFontAwesome $relativePath
    New-Item -ItemType Directory -Path (Split-Path -Parent $destinationPath) -Force | Out-Null
    Copy-Item -LiteralPath $sourcePath -Destination $destinationPath -Force
}
$embeddedImages = @('logo.png', 'default_item.png')
$embeddedImageDirectory = Join-Path $embeddedWeb 'images'
New-Item -ItemType Directory -Path $embeddedImageDirectory -Force | Out-Null
foreach ($imageName in $embeddedImages) {
    $imagePath = Join-Path $canonicalWeb "images/$imageName"
    if (-not (Test-Path -LiteralPath $imagePath -PathType Leaf)) {
        throw "网页通用资源不存在：$imagePath"
    }
    Copy-Item -LiteralPath $imagePath -Destination (Join-Path $embeddedImageDirectory $imageName) -Force
}
$goCommand = Get-Command go -ErrorAction SilentlyContinue
$localGo = Join-Path $workspaceRoot ".codex-tools\go1.24.13\go\bin\go.exe"
$goExecutable = $null
$usingWorkspaceToolchain = $false
if ($goCommand) {
    $goExecutable = $goCommand.Source
} elseif (Test-Path -LiteralPath $localGo -PathType Leaf) {
    $goExecutable = $localGo
    $usingWorkspaceToolchain = $true
} else {
    throw "未找到 Go 工具链，请安装 Go 1.24 或更高版本后重试。"
}

$versionOutput = & $goExecutable version
if ($LASTEXITCODE -ne 0 -or $versionOutput -notmatch "go version go(?<major>\d+)\.(?<minor>\d+)") {
    throw "无法读取 Go 工具链版本：$versionOutput"
}
if ([int]$Matches.major -lt 1 -or ([int]$Matches.major -eq 1 -and [int]$Matches.minor -lt 24)) {
    throw "本项目依赖要求 Go 1.24 或更高版本，当前为：$versionOutput"
}

if ([System.IO.Path]::IsPathRooted($OutputDirectory)) {
    $resolvedOutput = [System.IO.Path]::GetFullPath($OutputDirectory)
} else {
    $resolvedOutput = [System.IO.Path]::GetFullPath((Join-Path $workspaceRoot $OutputDirectory))
}
New-Item -ItemType Directory -Path $resolvedOutput -Force | Out-Null
$previous = @{
    GOOS = $env:GOOS
    GOARCH = $env:GOARCH
    GOARM = $env:GOARM
    CGO_ENABLED = $env:CGO_ENABLED
    GOPATH = $env:GOPATH
    GOMODCACHE = $env:GOMODCACHE
    GOCACHE = $env:GOCACHE
}

try {
    if ($usingWorkspaceToolchain) {
        if (-not $env:GOPATH) { $env:GOPATH = Join-Path $workspaceRoot ".codex-tools\gopath" }
        if (-not $env:GOMODCACHE) { $env:GOMODCACHE = Join-Path $env:GOPATH "pkg\mod" }
        if (-not $env:GOCACHE) { $env:GOCACHE = Join-Path $workspaceRoot ".codex-tools\gocache" }
    }
    $env:CGO_ENABLED = "0"
    foreach ($target in $targets) {
        $env:GOOS = $target.GOOS
        $env:GOARCH = $target.GOARCH
        if ($null -eq $target.GOARM) {
            Remove-Item Env:GOARM -ErrorAction SilentlyContinue
        } else {
            $env:GOARM = $target.GOARM
        }

        $extension = if ($target.GOOS -eq "windows") { ".exe" } else { "" }
        $output = Join-Path $resolvedOutput ("gbcserver-{0}{1}" -f $target.Name, $extension)
        Write-Host ("构建 gbcserver-go {0}/{1} -> {2}" -f $target.GOOS, $target.GOARCH, $output)
        Push-Location $moduleRoot
        try {
            $buildArguments = @('build', '-trimpath', '-ldflags', '-s -w', '-o', $output)
            $buildArguments += './cmd/gbcserver'
            & $goExecutable @buildArguments
        } finally {
            Pop-Location
        }
        if ($LASTEXITCODE -ne 0) {
            throw ("目标 {0} 构建失败" -f $target.Name)
        }
        if ($target.GOOS -eq 'windows' -and -not [string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
            $signature = Set-GBCAuthenticodeSignature -Path $output -Thumbprint $SigningCertificateThumbprint
            Write-Host ("Authenticode 验证通过：{0} ({1})" -f $target.Name, $signature.Status)
        } elseif ($target.GOOS -eq 'windows' -and $RequireWindowsSignature) {
            throw ("目标 {0} 未签名；已停止构建。" -f $target.Name)
        }
    }
} finally {
    foreach ($name in @("GOOS", "GOARCH", "GOARM", "CGO_ENABLED", "GOPATH", "GOMODCACHE", "GOCACHE")) {
        $value = $previous[$name]
        if ($null -eq $value) {
            Remove-Item ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        } else {
            Set-Item ("Env:{0}" -f $name) $value
        }
    }
}


