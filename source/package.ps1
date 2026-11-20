#requires -Version 7.0
param(
    [string]$InputDirectory = '目标文件/v1.0.1-build',
    [string]$OutputDirectory = '目标文件/部署包/v1.0.1',
    [switch]$NoArchive,
    [switch]$AllowUntrustedWindowsSignature,
    [switch]$SkipWindowsSignatureCheck
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.IO.Compression.FileSystem
$workspaceRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
function Resolve-PackagePath([string]$Path) {
    if ([IO.Path]::IsPathRooted($Path)) { return [IO.Path]::GetFullPath($Path) }
    return [IO.Path]::GetFullPath((Join-Path $workspaceRoot $Path))
}
$inputRoot = Resolve-PackagePath $InputDirectory
$outputRoot = Resolve-PackagePath $OutputDirectory
$batchName = (Get-Date -Format 'yyyyMMdd-HHmmss') + '-release-' + [Guid]::NewGuid().ToString('N').Substring(0, 6)
$batchRoot = Join-Path $outputRoot $batchName
$targets = @('windows-386', 'windows-amd64', 'windows-arm64', 'linux-386', 'linux-amd64', 'linux-arm64-aarch64', 'linux-armv5', 'linux-armv6', 'linux-armv7')

# Go 程序不内嵌数据库、头像、商品图片或插件 JAR。
$releaseGuide = Join-Path $PSScriptRoot 'deploy/gbcserver-go发布说明.md'
if (-not (Test-Path -LiteralPath $releaseGuide -PathType Leaf)) {
    throw "gbcserver-go 发布说明不存在：$releaseGuide"
}
$buildBinaryPrefix = 'gbcserver'
$requiredFiles = @()
foreach ($target in $targets) {
    $extension = if ($target.StartsWith('windows-')) { '.exe' } else { '' }
    $requiredFiles += Join-Path $inputRoot "$buildBinaryPrefix-$target$extension"
}
foreach ($file in $requiredFiles) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) { throw "打包输入不存在：$file" }
}
function Assert-WindowsArtifactSignature([string]$Path) {
    if ($SkipWindowsSignatureCheck) {
        Write-Warning "已跳过 Windows 签名检查：$Path"
        return
    }
    if (-not $IsWindows) {
        throw '当前系统无法读取 Authenticode；请在 Windows 上打包，或显式使用 -SkipWindowsSignatureCheck。'
    }
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.Status -eq [System.Management.Automation.SignatureStatus]::Valid) {
        return
    }
    $isSelfSigned = $null -ne $signature.SignerCertificate -and
        $signature.SignerCertificate.Subject -eq $signature.SignerCertificate.Issuer
    if ($signature.Status -eq [System.Management.Automation.SignatureStatus]::UnknownError -and
        $AllowUntrustedWindowsSignature -and $isSelfSigned) {
        Write-Warning "允许自签名的 Windows 程序进入发布包：$([IO.Path]::GetFileName($Path))；Windows 默认不信任此证书，可能显示未知发布者警告。 / Self-signed Windows binary included in release package; Windows does not trust this certificate by default and may show an unknown publisher warning."
        return
    }
    if ($signature.Status -eq [System.Management.Automation.SignatureStatus]::NotSigned) {
        throw "Windows 程序未签名，禁止进入分发包：$Path"
    }
    throw "Windows 程序签名未通过信任检查（$($signature.Status)），请使用受信任的组织代码签名证书：$Path"
}

foreach ($file in $requiredFiles) {
    if ([IO.Path]::GetExtension($file) -eq '.exe') {
        Assert-WindowsArtifactSignature $file
    }
}
if (Test-Path -LiteralPath $batchRoot) { throw '本批次输出目录已存在，请重新执行以创建新批次。' }
New-Item -ItemType Directory -Path $batchRoot -Force | Out-Null

foreach ($target in $targets) {
    $windows = $target.StartsWith('windows-')
    $extension = if ($windows) { '.exe' } else { '' }
    $sourceBinary = Join-Path $inputRoot "$buildBinaryPrefix-$target$extension"
    $artifactName = "gbcserver-$target$extension"
    $packageRoot = Join-Path $batchRoot ".package-$target"
    New-Item -ItemType Directory -Path $packageRoot -Force | Out-Null
    Copy-Item -LiteralPath $sourceBinary -Destination (Join-Path $packageRoot $artifactName) -Force
    Copy-Item -LiteralPath $releaseGuide -Destination $packageRoot -Force
    if ($NoArchive) {
        Move-Item -LiteralPath $packageRoot -Destination (Join-Path $batchRoot $target)
    } else {
        $archivePath = Join-Path $batchRoot "gbcserver-$target.zip"
        [IO.Compression.ZipFile]::CreateFromDirectory($packageRoot, $archivePath, [IO.Compression.CompressionLevel]::Optimal, $false)
        Remove-Item -LiteralPath $packageRoot -Recurse -Force
    }
    Write-Host "部署包已生成：$target"
}
Write-Host "本批次：$batchRoot"
