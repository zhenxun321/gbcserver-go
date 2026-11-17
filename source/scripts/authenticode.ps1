function Get-GBCCodeSigningCertificate {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string]$Thumbprint
    )

    if (-not $IsWindows) {
        throw 'Authenticode 签名只能在 Windows 上执行。'
    }

    $normalizedThumbprint = ($Thumbprint -replace '\s', '').ToUpperInvariant()
    if ($normalizedThumbprint -notmatch '^(?:[0-9A-F]{40}|[0-9A-F]{64})$') {
        throw '代码签名证书指纹格式无效。'
    }

    $certificate = $null
    foreach ($storePath in @('Cert:\CurrentUser\My', 'Cert:\LocalMachine\My')) {
        $candidate = Get-ChildItem -LiteralPath $storePath -ErrorAction SilentlyContinue |
            Where-Object { (($_.Thumbprint -replace '\s', '').ToUpperInvariant()) -eq $normalizedThumbprint } |
            Select-Object -First 1
        if ($null -ne $candidate) {
            $certificate = $candidate
            break
        }
    }

    if ($null -eq $certificate) {
        throw '未在当前用户或本机个人证书库找到指定的代码签名证书。'
    }
    if (-not $certificate.HasPrivateKey) {
        throw '指定的代码签名证书没有可用私钥。'
    }

    $codeSigningOID = '1.3.6.1.5.5.7.3.3'
    $hasCodeSigningUsage = @($certificate.EnhancedKeyUsageList | Where-Object { $_.ObjectId -eq $codeSigningOID }).Count -gt 0
    if (-not $hasCodeSigningUsage) {
        throw '指定证书未声明代码签名用途。'
    }

    return $certificate
}

function Set-GBCAuthenticodeSignature {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string]$Path,
        [Parameter(Mandatory)]
        [string]$Thumbprint,
        [string]$TimestampServer = $env:GBC_CODE_SIGN_TIMESTAMP_SERVER
    )

    $certificate = Get-GBCCodeSigningCertificate -Thumbprint $Thumbprint
    $signParameters = @{
        FilePath = $Path
        Certificate = $certificate
        HashAlgorithm = 'SHA256'
    }
    if (-not [string]::IsNullOrWhiteSpace($TimestampServer)) {
        $signParameters.TimestampServer = $TimestampServer
    }

    $null = Set-AuthenticodeSignature @signParameters
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    $signerThumbprint = if ($signature.SignerCertificate) {
        ($signature.SignerCertificate.Thumbprint -replace '\s', '').ToUpperInvariant()
    } else {
        ''
    }
    $expectedThumbprint = ($certificate.Thumbprint -replace '\s', '').ToUpperInvariant()
    $signatureMatchesCertificate = $signerThumbprint -eq $expectedThumbprint
    $trustedSignature = $signature.Status -eq [System.Management.Automation.SignatureStatus]::Valid
    $matchingSelfSignedSignature = $signature.Status -eq [System.Management.Automation.SignatureStatus]::UnknownError -and
        $certificate.Subject -eq $certificate.Issuer -and $signatureMatchesCertificate
    if (-not $signatureMatchesCertificate -or (-not $trustedSignature -and -not $matchingSelfSignedSignature)) {
        throw "Authenticode 签名未通过验证：$($signature.Status)"
    }

    if ($matchingSelfSignedSignature) {
        Write-Warning '自签名的文件签名者与所选证书匹配；证书链仍不受 Windows 信任，360 可能继续拦截。'
    }

    return $signature
}
