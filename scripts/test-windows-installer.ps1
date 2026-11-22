#requires -Version 5.1
[CmdletBinding()]
param([Parameter(Mandatory = $true)][string]$BinaryPath, [switch]$Integration)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$installerPath = Join-Path $PSScriptRoot 'install-windows.ps1'
. $installerPath

function Assert-InstallerTest([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}
function Assert-Throws([scriptblock]$Action, [string]$Message) {
    $failed = $false
    try { & $Action } catch { $failed = $true }
    Assert-InstallerTest $failed $Message
}
function Assert-PrivateAcl([string]$Path, [bool]$AllowService) {
    $allowed = @('S-1-5-18', 'S-1-5-32-544', [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
    if ($AllowService) { $allowed += 'S-1-5-19' }
    $acl = Get-Acl -LiteralPath $Path
    foreach ($rule in $acl.Access) {
        if ($rule.AccessControlType -ne 'Allow') { continue }
        $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        Assert-InstallerTest ($sid -in $allowed) "An unexpected account can access $Path"
    }
}

$testDirectory = Join-Path ([IO.Path]::GetTempPath()) ('monitor-windows-tests-' + [Guid]::NewGuid().ToString('N'))
$testTask = 'CodexTokenMonitorTest-' + [Guid]::NewGuid().ToString('N')
$originalPath = $env:PATH
$originalCodexHome = [Environment]::GetEnvironmentVariable('CODEX_HOME')
$originalRecord = [Environment]::GetEnvironmentVariable('TOKEN_MONITOR_TEST_REGISTRATION')
$serverInstalled = $false
try {
    [void][IO.Directory]::CreateDirectory($testDirectory)
    foreach ($scriptFile in Get-ChildItem -LiteralPath $PSScriptRoot -Filter '*.ps1') {
        $parseTokens = $null; $parseErrors = $null
        [void][Management.Automation.Language.Parser]::ParseFile($scriptFile.FullName, [ref]$parseTokens, [ref]$parseErrors)
        Assert-InstallerTest ($parseErrors.Count -eq 0) ("PowerShell syntax errors in " + $scriptFile.Name + ': ' + ($parseErrors | Out-String))
    }
    $fakeKey = 'windows-installer-test-token-0000000000000000000000'
    $secureKey = ConvertTo-SecureString $fakeKey -AsPlainText -Force
    $dryRoot = Join-Path $testDirectory 'dry-run'
    $plan = @(Install-WindowsMonitor -Mode Client -Version 'v0.1.2' -Architecture amd64 -Name 'رایانهٔ تست' -HubUrl 'https://monitor.example.com' -Token $secureKey -InstallRoot $dryRoot -NonInteractive -DryRun)
    Assert-InstallerTest (-not (Test-Path -LiteralPath $dryRoot)) 'Dry run changed the filesystem.'
    Assert-InstallerTest (($plan | Out-String) -notmatch [regex]::Escape($fakeKey)) 'Dry run disclosed a token.'
    Assert-Throws { Assert-HubAddress 'https://name:secret@monitor.example.com' } 'Accepted credentials in server URL.'
    Assert-Throws { Assert-HubAddress 'file:///C:/private' } 'Accepted a non-HTTP server URL.'
    Assert-HubAddress 'http://127.0.0.1:8787'

    $package = Join-Path $testDirectory 'package'
    [void][IO.Directory]::CreateDirectory($package)
    Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $package 'token-monitor.exe')
    [IO.File]::WriteAllText((Join-Path $package 'unexpected.txt'), 'This file must never be extracted by the installer.')
    $archive = Join-Path $testDirectory 'token-monitor_0.1.2_windows_amd64.zip'
    Compress-Archive -Path (Join-Path $package '*') -DestinationPath $archive
    $manifest = Join-Path $testDirectory 'checksums.txt'
    $hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText($manifest, $hash + '  ' + [IO.Path]::GetFileName($archive) + "`n")
    Assert-ArchiveChecksum $archive $manifest ([IO.Path]::GetFileName($archive))
    $invalid = Join-Path $testDirectory 'bad-checksums.txt'
    [IO.File]::WriteAllText($invalid, ('0' * 64) + '  ' + [IO.Path]::GetFileName($archive) + "`n")
    Assert-Throws { Assert-ArchiveChecksum $archive $invalid ([IO.Path]::GetFileName($archive)) } 'Accepted an incorrect archive digest.'
    $extractRoot = Join-Path $testDirectory 'verified'
    Set-PrivateInstallDirectory -Path $extractRoot
    Install-VerifiedExecutable $archive (Join-Path $extractRoot 'token-monitor.exe')
    Assert-InstallerTest (-not (Test-Path -LiteralPath (Join-Path $extractRoot 'unexpected.txt'))) 'Unrequested ZIP contents were extracted.'
    Assert-PrivateAcl $extractRoot $false

    if ($Integration) {
        $listener = New-Object Net.Sockets.TcpListener([Net.IPAddress]::Loopback, 0)
        $listener.Start(); $testPort = $listener.LocalEndpoint.Port; $listener.Stop()
        $hubAddress = 'http://127.0.0.1:' + $testPort
        $serverRoot = Join-Path $testDirectory 'server'
        Install-WindowsMonitor -Mode Server -Version 'v0.1.2' -Architecture amd64 -Name 'مرکز تست' -PublicUrl $hubAddress -ListenAddress '127.0.0.1' -Port $testPort -Token $secureKey -InstallRoot $serverRoot -TaskName $testTask -ArchivePath $archive -ChecksumsPath $manifest -NonInteractive
        $serverInstalled = $true
        $healthy = $false
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            try {
                $response = Invoke-WebRequest -UseBasicParsing -Uri "$hubAddress/health" -Headers @{ Authorization = 'Bearer ' + $fakeKey }
                if ($response.StatusCode -eq 200) { $healthy = $true; break }
            } catch { Start-Sleep -Milliseconds 500 }
        }
        Assert-InstallerTest $healthy 'The native server task did not pass authenticated health checks.'
        $task = Get-ScheduledTask -TaskName $testTask
        $taskUser = $task.Principal.UserId
        if ($taskUser -ne 'S-1-5-19') { $taskUser = (New-Object Security.Principal.NTAccount($taskUser)).Translate([Security.Principal.SecurityIdentifier]).Value }
        Assert-InstallerTest ($taskUser -eq 'S-1-5-19') 'Server task runs under the wrong account.'
        Assert-PrivateAcl (Join-Path $serverRoot 'config/hub-token') $true
        Assert-Throws { Invoke-WebRequest -UseBasicParsing -Uri "$hubAddress/health" | Out-Null } 'Unauthenticated server access succeeded.'

        # An npm-style codex.cmd launcher must receive Unicode/space-containing paths intact.
        $mockDirectory = Join-Path $testDirectory 'mock-codex'
        [void][IO.Directory]::CreateDirectory($mockDirectory)
        $recorder = Join-Path $mockDirectory 'record.ps1'
        [IO.File]::WriteAllText($recorder, '[IO.File]::WriteAllText($env:TOKEN_MONITOR_TEST_REGISTRATION, (ConvertTo-Json -InputObject @($args) -Compress), (New-Object Text.UTF8Encoding($false)))')
        [IO.File]::WriteAllText((Join-Path $mockDirectory 'codex.cmd'), "@echo off`r`npowershell.exe -NoProfile -NonInteractive -File `"%~dp0record.ps1`" %*`r`nexit /b %ERRORLEVEL%`r`n")
        $env:PATH = $mockDirectory + [IO.Path]::PathSeparator + $originalPath
        $env:CODEX_HOME = Join-Path $testDirectory 'isolated-codex'
        [void][IO.Directory]::CreateDirectory($env:CODEX_HOME)
        $env:TOKEN_MONITOR_TEST_REGISTRATION = Join-Path $testDirectory 'record.json'
        $clientRoot = Join-Path $testDirectory "O'Brien & رایانه"
        Install-WindowsMonitor -Mode Client -Version 'v0.1.2' -Architecture amd64 -Name 'رایانهٔ ویندوز' -HubUrl $hubAddress -Token $secureKey -InstallRoot $clientRoot -ArchivePath $archive -ChecksumsPath $manifest -NonInteractive
        $clientConfig = Join-Path $clientRoot 'config/config.json'
        $profile = Get-Content -LiteralPath $clientConfig -Encoding UTF8 -Raw | ConvertFrom-Json
        Assert-InstallerTest ($profile.machine_name -eq 'رایانهٔ ویندوز') 'Unicode device name changed during installation.'
        $registered = @(Get-Content -LiteralPath $env:TOKEN_MONITOR_TEST_REGISTRATION -Encoding UTF8 -Raw | ConvertFrom-Json)
        Assert-InstallerTest ($registered -contains $clientConfig) 'Codex registration changed the config path.'
        Assert-PrivateAcl $profile.token_file $false
        $oldIdentity = $profile.machine_id
        Install-WindowsMonitor -Mode Client -Version 'v0.1.2' -Architecture amd64 -InstallRoot $clientRoot -ArchivePath $archive -ChecksumsPath $manifest -NonInteractive
        $profile = Get-Content -LiteralPath $clientConfig -Encoding UTF8 -Raw | ConvertFrom-Json
        Assert-InstallerTest ($profile.machine_id -eq $oldIdentity) 'Reinstallation changed device identity.'
        $clientExecutable = Join-Path $clientRoot 'bin/v0.1.2/token-monitor.exe'
        Invoke-MonitorBinary -Executable $clientExecutable -Arguments @('collect', '--config', $clientConfig)
        Invoke-MonitorBinary -Executable $clientExecutable -Arguments @('report', '--html', '--no-refresh', '--config', $clientConfig)
        Assert-InstallerTest (@(Get-ChildItem -LiteralPath (Join-Path $clientRoot 'data/reports') -Filter '*.html').Count -eq 1) 'Installed client did not generate an HTML report.'
    }
    Write-Output ('Windows installer checks passed in PowerShell ' + $PSVersionTable.PSVersion)
} finally {
    if ($Integration -and (Get-ScheduledTask -TaskName $testTask -ErrorAction SilentlyContinue)) {
        Stop-ScheduledTask -TaskName $testTask -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $testTask -Confirm:$false
        Start-Sleep -Milliseconds 500
    }
    $env:PATH = $originalPath
    [Environment]::SetEnvironmentVariable('CODEX_HOME', $originalCodexHome, 'Process')
    [Environment]::SetEnvironmentVariable('TOKEN_MONITOR_TEST_REGISTRATION', $originalRecord, 'Process')
    if (Test-Path -LiteralPath $testDirectory) { Remove-Item -LiteralPath $testDirectory -Recurse -Force }
}
