#requires -Version 7.0
# 与 Web 进程共用规范化数据库，白名单和兑换使用不同监听端口。
$ErrorActionPreference = 'Stop'
$executable = Join-Path $PSScriptRoot 'gbcserver.exe'
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
    $executable = Join-Path $PSScriptRoot 'bin/gbcserver.exe'
}
$dataDirectory = Join-Path $PSScriptRoot 'data'
& $executable plugin-serve --data-dir $dataDirectory @args
exit $LASTEXITCODE
