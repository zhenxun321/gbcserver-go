#requires -Version 7.0
# 一体启动 Web/API、白名单 listener 和兑换 listener；插件 JAR 仍由 Leaves/Paper 加载。
$ErrorActionPreference = 'Stop'
$executable = Join-Path $PSScriptRoot 'gbcserver.exe'
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
    $executable = Join-Path $PSScriptRoot 'bin/gbcserver.exe'
}
$dataDirectory = Join-Path $PSScriptRoot 'data'
$arguments = @('start', '--tray', '--data-dir', $dataDirectory)
& $executable @arguments @args
exit $LASTEXITCODE
