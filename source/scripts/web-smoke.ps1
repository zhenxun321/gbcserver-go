param(
    [string]$SigningCertificateThumbprint = ''
)

$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot 'authenticode.ps1')
if ([string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
    $SigningCertificateThumbprint = $env:GBC_CODE_SIGN_CERT_THUMBPRINT
}
if (-not [string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
    $null = Get-GBCCodeSigningCertificate -Thumbprint $SigningCertificateThumbprint
}
chcp 65001 > $null
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

$moduleRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$workspaceRoot = [System.IO.Path]::GetFullPath((Join-Path $moduleRoot ".."))
$smokeRoot = Join-Path $moduleRoot ".smoke"
$workDirectory = Join-Path $smokeRoot ("带空格的 smoke " + [Guid]::NewGuid().ToString("N"))
$temporaryBinary = Join-Path $workDirectory "gbcserver.exe"
$binary = $temporaryBinary
$process = $null
$passed = $false
$previousGoEnvironment = @{
    GOPATH = $env:GOPATH
    GOMODCACHE = $env:GOMODCACHE
    GOCACHE = $env:GOCACHE
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) {
        throw "smoke 验收失败：$Message"
    }
}

function Assert-ResponseContains {
    param([string]$Url, [string]$Text, [string]$Name)
    $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 3
    Assert-True ($response.StatusCode -eq 200) "$Name 返回 HTTP $($response.StatusCode)"
    Assert-True ($response.Content.Contains($Text)) "$Name 未包含预期内容"
}

try {
    New-Item -ItemType Directory -Path $workDirectory -Force | Out-Null

    $localGo = Join-Path $workspaceRoot ".codex-tools\go1.24.13\go\bin\go.exe"
    if (Test-Path -LiteralPath $localGo -PathType Leaf) {
        $go = $localGo
        $env:GOPATH = Join-Path $workspaceRoot ".codex-tools\gopath"
        $env:GOMODCACHE = Join-Path $env:GOPATH "pkg\mod"
        $env:GOCACHE = Join-Path $workspaceRoot ".codex-tools\gocache"
    } else {
        $go = (Get-Command go -ErrorAction Stop).Source
    }

    $database = Join-Path $workDirectory "smoke.db"
    $stdoutPath = Join-Path $workDirectory "web.stdout.log"
    $stderrPath = Join-Path $workDirectory "web.stderr.log"
    $staticDirectory = Join-Path $moduleRoot "web"

    Push-Location $moduleRoot
    try {
        & $go build -trimpath -ldflags "-s -w" -o $temporaryBinary ./cmd/gbcserver
        Assert-True ($LASTEXITCODE -eq 0) "Go smoke 二进制构建失败"
        if (-not [string]::IsNullOrWhiteSpace($SigningCertificateThumbprint)) {
            $signature = Set-GBCAuthenticodeSignature -Path $temporaryBinary -Thumbprint $SigningCertificateThumbprint
            Write-Output ("smoke 测试程序签名验证通过：{0}" -f $signature.Status)
        }
        $signature = Get-AuthenticodeSignature -LiteralPath $temporaryBinary
        $signatureAccepted = $signature.Status -eq [System.Management.Automation.SignatureStatus]::Valid
        if (-not [string]::IsNullOrWhiteSpace($SigningCertificateThumbprint) -and
            $signature.Status -eq [System.Management.Automation.SignatureStatus]::UnknownError) {
            $signingCertificate = Get-GBCCodeSigningCertificate -Thumbprint $SigningCertificateThumbprint
            $actualThumbprint = if ($signature.SignerCertificate) {
                ($signature.SignerCertificate.Thumbprint -replace '\s', '').ToUpperInvariant()
            } else {
                ''
            }
            $expectedThumbprint = ($signingCertificate.Thumbprint -replace '\s', '').ToUpperInvariant()
            $signatureAccepted = $signingCertificate.Subject -eq $signingCertificate.Issuer -and
                $actualThumbprint -eq $expectedThumbprint
            if ($signatureAccepted) {
                Write-Warning 'smoke 使用匹配的自签名证书；Windows 信任状态仍为 UnknownError，360 可能继续拦截。'
            }
        }
        if (-not $signatureAccepted) {
            throw "smoke 未启动：Windows 测试二进制的 Authenticode 状态为 $($signature.Status)，且不满足可信签名或匹配的自签名条件。请使用受信任的组织代码签名证书。脚本不会关闭或绕过防护。"
        }
        foreach ($helpArguments in @(
            ,@('--help'), ,@('-h'), ,@('help'), ,@('help', 'web-serve'),
            ,@('init-db', '--help'), ,@('upgrade-db', '--help'), ,@('migrate', '--help'),
            ,@('web-serve', '--help'),
            ,@('plugin-serve', '--help'), ,@('plugin-token', '--help'),
            ,@('plugin-token', 'issue', '--help'), ,@('plugin-token', 'revoke', '--help')
        )) {
            $helpText = & $binary @helpArguments | Out-String
            Assert-True ($LASTEXITCODE -eq 0 -and $helpText.Contains('用法')) "帮助选项未成功：$helpArguments"
        }
        $invalidOutput = & $binary init-db --db $database extra 2>&1 | Out-String
        Assert-True ($LASTEXITCODE -eq 2 -and -not (Test-Path -LiteralPath $database)) '位置参数应被拒绝且不应创建数据库'
        & $binary init-db --db $database
        Assert-True ($LASTEXITCODE -eq 0) "规范化 SQLite 初始化失败"

        $probe = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
        $probe.Start()
        $port = ([System.Net.IPEndPoint]$probe.LocalEndpoint).Port
        $probe.Stop()

        $dataDirectory = Join-Path $workDirectory "data"

        # ArgumentList 由 .NET 逐项编码，路径含空格时不会被 Start-Process 拼接拆开。
        $startInfo = [System.Diagnostics.ProcessStartInfo]::new($binary)
        $startInfo.UseShellExecute = $false
        $startInfo.CreateNoWindow = $true
        $startInfo.RedirectStandardOutput = $true
        $startInfo.RedirectStandardError = $true
        foreach ($argument in @('web-serve', '--data-dir', $dataDirectory, '--db', $database, '--listen', "127.0.0.1:$port", '--static-dir', $staticDirectory, '--body-limit', '1048576', '--status-apis', 'none')) {
            $startInfo.ArgumentList.Add($argument)
        }
        $process = [System.Diagnostics.Process]::Start($startInfo)
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $baseURL = "http://127.0.0.1:$port"
        $ready = $false
        for ($attempt = 0; $attempt -lt 40; $attempt++) {
            Start-Sleep -Milliseconds 100
            if ($process.HasExited) {
                $details = $stderrTask.GetAwaiter().GetResult()
                throw "Web 进程提前退出：$details"
            }
            try {
                $health = Invoke-WebRequest -Uri "$baseURL/healthz" -UseBasicParsing -TimeoutSec 1
                if ($health.StatusCode -eq 200) {
                    $ready = $true
                    break
                }
            } catch {
                # listener 尚未绑定完成，继续等待。
            }
        }
        Assert-True $ready "Web listener 未在 4 秒内就绪"

        Assert-ResponseContains "$baseURL/healthz" '"status":"ok"' "健康检查"
        Assert-ResponseContains "$baseURL/api/publicConfig" '"bodyLimitBytes":1048576' "公开配置"
        Assert-ResponseContains "$baseURL/" "<!DOCTYPE html>" "静态首页"
        Assert-ResponseContains "$baseURL/ports" "{}" "端口读视图"
        Assert-ResponseContains "$baseURL/api?method=missing" "传入参数错误" "旧 method 未知分发"
        Assert-ResponseContains "$baseURL/api?method=activateUser%26name=Player" "传入参数错误" "旧 method 查询解析"

        $unauthorized = Invoke-WebRequest -Uri "$baseURL/api?method=activateUser&name=Player&session=invalid" -UseBasicParsing -TimeoutSec 3
        Assert-True ($unauthorized.StatusCode -eq 200) "旧 method 业务失败应保留 HTTP 200"
        Assert-True ($unauthorized.Content.Contains("管理员会话无效")) "旧 method 管理员权限边界响应错误：$($unauthorized.Content)"

        $couponRequest = [System.Net.HttpWebRequest]::Create("$baseURL/api")
        $couponRequest.Method = "POST"
        $couponRequest.ContentType = "application/json"
        $body = [System.Text.Encoding]::UTF8.GetBytes('{"method":"useCoupon","username":"Player","code":"SMOKE"}')
        $couponRequest.ContentLength = $body.Length
        $stream = $couponRequest.GetRequestStream()
        try { $stream.Write($body, 0, $body.Length) } finally { $stream.Dispose() }
        try {
            $couponResponse = [System.Net.HttpWebResponse]$couponRequest.GetResponse()
        } catch [System.Net.WebException] {
            $couponResponse = [System.Net.HttpWebResponse]$_.Exception.Response
        }
        try {
            $couponText = [System.IO.StreamReader]::new($couponResponse.GetResponseStream()).ReadToEnd()
            Assert-True ([int]$couponResponse.StatusCode -eq 401) "Web listener 必须拒绝兑换请求"
            Assert-True ($couponText.Contains("兑换接口需要独立令牌")) "兑换隔离拒绝消息错误"
        } finally {
            $couponResponse.Dispose()
        }
    } finally {
        Pop-Location
    }

    $passed = $true
    Write-Output "Web smoke 验收通过：根/子命令帮助、错误参数、带空格路径、重复配置与 CLI 覆盖、静态首页、健康检查、旧 /api 分发和兑换 listener 隔离。"
} finally {
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force
        $null = $process.WaitForExit(5000)
    }
    if ($null -ne $process -and $process.HasExited) {
        if ($null -ne $stdoutTask) { Set-Content -LiteralPath $stdoutPath -Value $stdoutTask.GetAwaiter().GetResult() -Encoding utf8NoBOM }
        if ($null -ne $stderrTask) { Set-Content -LiteralPath $stderrPath -Value $stderrTask.GetAwaiter().GetResult() -Encoding utf8NoBOM }
        $process.Dispose()
    }
    $resolvedWorkDirectory = [System.IO.Path]::GetFullPath($workDirectory)
    $smokePrefix = [System.IO.Path]::GetFullPath($smokeRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if ($resolvedWorkDirectory.StartsWith($smokePrefix, [System.StringComparison]::OrdinalIgnoreCase) -and
        (Test-Path -LiteralPath $resolvedWorkDirectory)) {
        $resolvedTemporaryBinary = [System.IO.Path]::GetFullPath($temporaryBinary)
        if ($resolvedTemporaryBinary.StartsWith($resolvedWorkDirectory.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase) -and
            (Test-Path -LiteralPath $resolvedTemporaryBinary)) {
            Remove-Item -LiteralPath $resolvedTemporaryBinary -Force
        }
        if ($passed) {
            Remove-Item -LiteralPath $resolvedWorkDirectory -Recurse -Force
        } else {
            $remainingFiles = Get-ChildItem -LiteralPath $resolvedWorkDirectory -Force
            if ($remainingFiles.Count -eq 0) {
                Remove-Item -LiteralPath $resolvedWorkDirectory -Force
            } else {
                Write-Warning "smoke 失败，保留可复查日志：$resolvedWorkDirectory"
            }
        }
    }
    foreach ($name in @("GOPATH", "GOMODCACHE", "GOCACHE")) {
        $value = $previousGoEnvironment[$name]
        if ($null -eq $value) {
            Remove-Item ("Env:{0}" -f $name) -ErrorAction SilentlyContinue
        } else {
            Set-Item ("Env:{0}" -f $name) $value
        }
    }
}

