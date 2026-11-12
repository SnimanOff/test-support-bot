param(
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$')]
    [string] $Version = 'v0.1.0'
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true
$repo = Split-Path -Parent $PSScriptRoot
$previous = @{ CGO_ENABLED = $env:CGO_ENABLED; GOOS = $env:GOOS; GOARCH = $env:GOARCH }
Push-Location -LiteralPath $repo
try {
    $name = "support-bot_${Version}_linux_amd64"
    $stage = Join-Path $repo "dist/$name"
    $archive = Join-Path $repo "dist/$name.tar.gz"
    New-Item -ItemType Directory -Path $stage -Force | Out-Null
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'
    $env:GOARCH = 'amd64'
    go build -trimpath -ldflags '-s -w' -o (Join-Path $stage 'support-bot') .
    if ($LASTEXITCODE -ne 0) { throw 'Сборка Linux не удалась.' }
    Copy-Item -LiteralPath (Join-Path $repo 'tgpanel.yaml') -Destination $stage
    tar -czf $archive -C $stage support-bot tgpanel.yaml
    if ($LASTEXITCODE -ne 0) { throw 'Создание архива не удалось.' }
    $checksum = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
    "$checksum  $name.tar.gz" | Set-Content -Encoding ascii -LiteralPath "$archive.sha256"
    Write-Output $archive
} finally {
    foreach ($key in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process')
    }
    Pop-Location
}
