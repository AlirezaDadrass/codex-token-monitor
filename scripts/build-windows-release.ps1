#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')][string]$Version,
    [ValidateSet('amd64', 'arm64')][string]$Architecture = 'amd64'
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$repositoryDirectory = Split-Path -Parent $PSScriptRoot
$stage = Join-Path $repositoryDirectory ('.local/release-windows-' + [Guid]::NewGuid().ToString('N'))
$destination = Join-Path $repositoryDirectory 'dist'
$previous = @{}
foreach ($key in @('GOOS', 'GOARCH', 'CGO_ENABLED')) { $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process') }
try {
    [void][IO.Directory]::CreateDirectory($stage)
    [void][IO.Directory]::CreateDirectory($destination)
    $env:GOOS = 'windows'; $env:GOARCH = $Architecture; $env:CGO_ENABLED = '0'
    Push-Location $repositoryDirectory
    try {
        & go build -trimpath '-ldflags=-s -w' -o (Join-Path $stage 'token-monitor.exe') ./cmd/token-monitor
        if ($LASTEXITCODE -ne 0) { throw 'Windows build failed.' }
    } finally { Pop-Location }
    foreach ($name in @('README.md', 'THIRD_PARTY_NOTICES.md', 'THIRD_PARTY_LICENSES.txt')) {
        Copy-Item -LiteralPath (Join-Path $repositoryDirectory $name) -Destination $stage
    }
    Copy-Item -LiteralPath (Join-Path $repositoryDirectory 'internal/monitor/templates/OFL.txt') -Destination $stage
    Copy-Item -LiteralPath (Join-Path $repositoryDirectory 'scripts/install-windows.ps1') -Destination $stage
    Copy-Item -LiteralPath (Join-Path $repositoryDirectory 'docs') -Destination $stage -Recurse
    $archive = Join-Path $destination ('token-monitor_' + $Version.Substring(1) + '_windows_' + $Architecture + '.zip')
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive -Force
    Write-Output $archive
} finally {
    foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
    if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
}
