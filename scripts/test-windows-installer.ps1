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

$testBase = [IO.Path]::GetTempPath()
if ($Integration) { $testBase = $env:ProgramData }
$testDirectory = Join-Path $testBase ('monitor-windows-tests-' + [Guid]::NewGuid().ToString('N'))
$testTask = 'CodexTokenMonitorTest-' + [Guid]::NewGuid().ToString('N')
$originalPath = $env:PATH
$originalCodexHome = [Environment]::GetEnvironmentVariable('CODEX_HOME')
$originalRecord = [Environment]::GetEnvironmentVariable('TOKEN_MONITOR_TEST_REGISTRATION')
$serverInstalled = $false
$clientTaskName = $null
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
    $plan = @(Install-WindowsMonitor -Mode Client -Version 'v0.1.4' -Architecture amd64 -Name 'رایانهٔ تست' -HubUrl 'https://monitor.example.com' -Token $secureKey -InstallRoot $dryRoot -NonInteractive -DryRun)
    Assert-InstallerTest (-not (Test-Path -LiteralPath $dryRoot)) 'Dry run changed the filesystem.'
    Assert-InstallerTest (($plan | Out-String) -notmatch [regex]::Escape($fakeKey)) 'Dry run disclosed a token.'
    Assert-Throws { Assert-HubAddress 'https://name:secret@monitor.example.com' } 'Accepted credentials in server URL.'
    Assert-Throws { Assert-HubAddress 'file:///C:/private' } 'Accepted a non-HTTP server URL.'
    Assert-HubAddress 'http://127.0.0.1:8787'

    $package = Join-Path $testDirectory 'package'
    [void][IO.Directory]::CreateDirectory($package)
    Copy-Item -LiteralPath $BinaryPath -Destination (Join-Path $package 'token-monitor.exe')
    [IO.File]::WriteAllText((Join-Path $package 'unexpected.txt'), 'This file must never be extracted by the installer.')
    $archive = Join-Path $testDirectory 'token-monitor_0.1.4_windows_amd64.zip'
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
        Install-WindowsMonitor -Mode Server -Version 'v0.1.4' -Architecture amd64 -Name 'مرکز تست' -PublicUrl $hubAddress -ListenAddress '127.0.0.1' -Port $testPort -Token $secureKey -InstallRoot $serverRoot -TaskName $testTask -ArchivePath $archive -ChecksumsPath $manifest -NonInteractive
        $serverInstalled = $true
        $tokenBytes = [IO.File]::ReadAllBytes((Join-Path $serverRoot 'config/hub-token'))
        Assert-InstallerTest (([Text.Encoding]::UTF8.GetString($tokenBytes)).Trim() -ceq $fakeKey) 'The token changed in native stdin transmission.'
        $probeConfig = Join-Path $testDirectory 'probe/config.json'
        $probeBinary = Join-Path $serverRoot 'bin/v0.1.4/token-monitor.exe'
        Invoke-MonitorBinary -Executable $probeBinary -Arguments @('init', '--non-interactive', '--name', 'native-probe', '--config', $probeConfig, '--data-dir', (Join-Path $testDirectory 'probe/data'), '--hub-url', $hubAddress, '--token-file', (Join-Path $serverRoot 'config/hub-token'))
        Invoke-MonitorBinary -Executable $probeBinary -Arguments @('collect', '--config', $probeConfig)
        $task = Get-ScheduledTask -TaskName $testTask
        $taskUser = $task.Principal.UserId
        if ($taskUser -ne 'S-1-5-19') { $taskUser = (New-Object Security.Principal.NTAccount($taskUser)).Translate([Security.Principal.SecurityIdentifier]).Value }
        Assert-InstallerTest ($taskUser -eq 'S-1-5-19') 'Server task runs under the wrong account.'
        Assert-PrivateAcl (Join-Path $serverRoot 'config/hub-token') $true
        Add-Type -AssemblyName System.Net.Http
        $httpHandler = New-Object Net.Http.HttpClientHandler
        $httpHandler.UseProxy = $false
        $httpClient = New-Object Net.Http.HttpClient($httpHandler)
        $unauthorized = $httpClient.GetAsync("$hubAddress/health").GetAwaiter().GetResult()
        Assert-InstallerTest ([int]$unauthorized.StatusCode -eq 401) 'Unauthenticated server access succeeded.'
        $unauthorized.Dispose(); $httpClient.Dispose(); $httpHandler.Dispose()

        # An npm-style codex.cmd launcher must receive Unicode/space-containing paths intact.
        $mockDirectory = Join-Path $testDirectory 'mock-codex'
        [void][IO.Directory]::CreateDirectory($mockDirectory)
        & go build -o (Join-Path $mockDirectory 'recorder.exe') (Join-Path $PSScriptRoot 'testdata/record-arguments.go')
        if ($LASTEXITCODE -ne 0) { throw 'Could not build the native argument recorder fixture.' }
        [IO.File]::WriteAllText((Join-Path $mockDirectory 'codex.cmd'), "@echo off`r`n`"%~dp0recorder.exe`" %*`r`nexit /b %ERRORLEVEL%`r`n")
        $env:PATH = $mockDirectory + [IO.Path]::PathSeparator + $originalPath
        $env:CODEX_HOME = Join-Path $testDirectory 'isolated-codex'
        [void][IO.Directory]::CreateDirectory($env:CODEX_HOME)
        $env:TOKEN_MONITOR_TEST_REGISTRATION = Join-Path $testDirectory 'record.json'
        $clientRoot = Join-Path $testDirectory "O'Brien & رایانه"
        Install-WindowsMonitor -Mode Client -Version 'v0.1.4' -Architecture amd64 -Name 'رایانهٔ ویندوز' -HubUrl $hubAddress -Token $secureKey -InstallRoot $clientRoot -ArchivePath $archive -ChecksumsPath $manifest -NonInteractive
        $clientConfig = Join-Path $clientRoot 'config/config.json'
        $profile = Get-Content -LiteralPath $clientConfig -Encoding UTF8 -Raw | ConvertFrom-Json
        Assert-InstallerTest ($profile.machine_name -eq 'رایانهٔ ویندوز') 'Unicode device name changed during installation.'
        $clientTaskName = Get-CollectorTaskName $profile.machine_id
        $clientTask = Get-ScheduledTask -TaskName $clientTaskName
        Assert-InstallerTest ($clientTask.State -eq 'Running') 'Automatic client collector did not start.'
        Assert-InstallerTest ($clientTask.Principal.RunLevel -eq 'Limited') 'Client collector requests administrator privileges.'
        $clientTaskUser = $clientTask.Principal.UserId
        if ($clientTaskUser -notmatch '^S-1-') { $clientTaskUser = (New-Object Security.Principal.NTAccount($clientTaskUser)).Translate([Security.Principal.SecurityIdentifier]).Value }
        Assert-InstallerTest ($clientTaskUser -eq [Security.Principal.WindowsIdentity]::GetCurrent().User.Value) 'Client collector runs as another Windows user.'
        $command = [Text.Encoding]::Unicode.GetString([Convert]::FromBase64String(($clientTask.Actions.Arguments -split ' ')[-1]))
        Assert-InstallerTest (-not $command.Contains($fakeKey)) 'Client task contains a credential.'
        Assert-InstallerTest ($command.Contains('Remove-Item Env:TOKEN_MONITOR_HUB_TOKEN')) 'Client task can inherit a stale token override.'
        Assert-InstallerTest ($command.Contains('--parent-pid $PID')) 'Collector can outlive its task launcher.'
        $registeredSnapshots = @(Get-ChildItem -LiteralPath (Join-Path $serverRoot 'data/hub') -Filter '*.json' | ForEach-Object { Get-Content -LiteralPath $_.FullName -Encoding UTF8 -Raw | ConvertFrom-Json } | Where-Object { $_.machine_id -eq $profile.machine_id })
        Assert-InstallerTest ($registeredSnapshots.Count -eq 1) 'Installation checked health but did not upload the device snapshot.'
        $registered = Get-Content -LiteralPath $env:TOKEN_MONITOR_TEST_REGISTRATION -Encoding UTF8 -Raw | ConvertFrom-Json
        if ($registered -notcontains [IO.Path]::GetFullPath($clientConfig)) { Write-Output ($registered | ConvertTo-Json) }
        Assert-InstallerTest ($registered -contains [IO.Path]::GetFullPath($clientConfig)) 'Codex registration changed the config path.'
        Assert-PrivateAcl $profile.token_file $false
        $oldIdentity = $profile.machine_id
        $profile.interval_seconds = 5
        [IO.File]::WriteAllText($clientConfig, ($profile | ConvertTo-Json -Depth 10), (New-Object Text.UTF8Encoding($false)))
        Install-WindowsMonitor -Mode Client -Version 'v0.1.4' -Architecture amd64 -InstallRoot $clientRoot -ArchivePath $archive -ChecksumsPath $manifest -NonInteractive
        $profile = Get-Content -LiteralPath $clientConfig -Encoding UTF8 -Raw | ConvertFrom-Json
        Assert-InstallerTest ($profile.machine_id -eq $oldIdentity) 'Reinstallation changed device identity.'
        $clientExecutable = Join-Path $clientRoot 'bin/v0.1.4/token-monitor.exe'
        Assert-InstallerTest ((Get-ScheduledTask -TaskName $clientTaskName).State -eq 'Running') 'Reinstallation did not restart the existing collector task.'
        # Codex is only a registration mock here. New logs must reach the hub
        # through the scheduled collector, with no manual collect/tool call.
        $sessionDirectory = Join-Path $profile.codex_home 'sessions'
        [void][IO.Directory]::CreateDirectory($sessionDirectory)
        $timestamp = [DateTime]::UtcNow.ToString('o')
        $usage = @{ input_tokens = 50; cached_input_tokens = 20; output_tokens = 10; total_tokens = 60 }
        $meta = @{ timestamp = $timestamp; type = 'session_meta'; payload = @{ id = 'synthetic-automatic-session'; model_provider = 'openai' } } | ConvertTo-Json -Compress -Depth 10
        $event = @{ timestamp = $timestamp; type = 'event_msg'; payload = @{ type = 'token_count'; info = @{ total_token_usage = $usage; last_token_usage = $usage } } } | ConvertTo-Json -Compress -Depth 10
        [IO.File]::WriteAllText((Join-Path $sessionDirectory 'synthetic.jsonl'), ($meta + "`n" + $event + "`n"), (New-Object Text.UTF8Encoding($false)))
        $automaticUpload = $false
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            $snapshots = @(Get-ChildItem -LiteralPath (Join-Path $serverRoot 'data/hub') -Filter '*.json' | ForEach-Object { Get-Content -LiteralPath $_.FullName -Encoding UTF8 -Raw | ConvertFrom-Json } | Where-Object { $_.machine_id -eq $oldIdentity })
            if ($snapshots.Count -eq 1 -and @($snapshots[0].events | Where-Object { $_.tokens.total_tokens -eq 60 }).Count -eq 1) { $automaticUpload = $true; break }
            Start-Sleep -Seconds 1
        }
        Assert-InstallerTest $automaticUpload 'New session usage was not uploaded automatically while Codex was closed.'
        Invoke-MonitorBinary -Executable $clientExecutable -Arguments @('report', '--html', '--no-refresh', '--config', $clientConfig)
        Assert-InstallerTest (-not (Test-Path -LiteralPath (Join-Path $clientRoot 'data/reports'))) 'Installed client created a local HTML report.'
        Assert-InstallerTest (@(Get-ChildItem -LiteralPath (Join-Path $serverRoot 'data/reports') -Filter '*.html').Count -eq 1) 'Hub did not generate the HTML report.'
    }
    Write-Output ('Windows installer checks passed in PowerShell ' + $PSVersionTable.PSVersion)
} finally {
    if ($clientTaskName -and (Get-ScheduledTask -TaskName $clientTaskName -ErrorAction SilentlyContinue)) {
        Stop-ScheduledTask -TaskName $clientTaskName -ErrorAction SilentlyContinue
        Unregister-ScheduledTask -TaskName $clientTaskName -Confirm:$false
        for ($attempt = 0; $attempt -lt 30; $attempt++) {
            $runningCollectors = @(Get-CimInstance Win32_Process -Filter "Name = 'token-monitor.exe'" | Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($clientRoot, [StringComparison]::OrdinalIgnoreCase) })
            if ($runningCollectors.Count -eq 0) { break }
            Start-Sleep -Milliseconds 200
        }
        Assert-InstallerTest ($runningCollectors.Count -eq 0) 'Stopping the collector task left an orphan native process.'
    }
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
