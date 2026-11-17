#requires -Version 7.0
# 附加参数原样交给程序，重复选项由程序按最后一次生效处理。
$ErrorActionPreference = 'Stop'
$executable = Join-Path $PSScriptRoot 'gbcserver.exe'
if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) {
    $executable = Join-Path $PSScriptRoot 'bin/gbcserver.exe'
}
$dataDirectory = Join-Path $PSScriptRoot 'data'
& $executable web-serve --data-dir $dataDirectory @args
exit $LASTEXITCODE
