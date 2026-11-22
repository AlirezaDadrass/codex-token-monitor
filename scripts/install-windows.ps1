#requires -Version 5.1
[CmdletBinding()]
param(
    [ValidateSet('Client', 'Server')][string]$Mode = 'Client',
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')][string]$Version = 'v0.1.4',
    [ValidateSet('Auto', 'amd64', 'arm64')][string]$Architecture = 'Auto',
    [string]$Name,
    [string]$HubUrl,
    [Security.SecureString]$Token,
    [string]$TokenFile,
    [string]$InstallRoot,
    [string]$ListenAddress = '127.0.0.1',
    [ValidateRange(1024, 65535)][int]$Port = 8787,
    [string]$PublicUrl,
    [string]$TlsCertificate,
    [string]$TlsKey,
    [string]$TaskName = 'CodexTokenMonitorHub',
    [string]$ArchivePath,
    [string]$ChecksumsPath,
    [switch]$NonInteractive,
    [switch]$DryRun
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Test-WindowsPlatform {
    return [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
}

function Get-WindowsArchitecture {
    $value = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITEW6432')
    if (-not $value) { $value = [Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE') }
    switch -Regex ($value) {
        '^(AMD64|x86_64)$' { return 'amd64' }
        '^(ARM64|aarch64)$' { return 'arm64' }
        default { throw 'Only 64-bit Windows amd64 and arm64 are supported.' }
    }
}

function Assert-HubAddress([string]$Address) {
    $parsed = $null
    if (-not [Uri]::TryCreate($Address, [UriKind]::Absolute, [ref]$parsed) -or
        $parsed.Scheme -notin @('http', 'https') -or -not $parsed.Host -or
        $parsed.UserInfo -or $parsed.Query -or $parsed.Fragment) {
        throw 'The server address must be an http(s) URL without credentials, query or fragment.'
    }
}

function Read-RequiredValue([string]$Value, [string]$Label, [bool]$Unattended) {
    if ([string]::IsNullOrWhiteSpace($Value)) {
        if ($Unattended) { throw "$Label is required in non-interactive mode." }
        $Value = Read-Host $Label
    }
    if ([string]::IsNullOrWhiteSpace($Value)) { throw "$Label is required." }
    return $Value.Trim()
}

function ConvertTo-WindowsArgument([AllowEmptyString()][string]$Value) {
    # Windows CommandLineToArgvW quoting; never concatenate a shell command.
    $escaped = [regex]::Replace($Value, '(\\*)"', '$1$1\"')
    $escaped = [regex]::Replace($escaped, '(\\+)$', '$1$1')
    return '"' + $escaped + '"'
}

function Invoke-MonitorBinary {
    param([string]$Executable, [string[]]$Arguments, [Security.SecureString]$Secret)
    $info = New-Object Diagnostics.ProcessStartInfo
    $info.FileName = $Executable
    $info.Arguments = (($Arguments | ForEach-Object { ConvertTo-WindowsArgument $_ }) -join ' ')
    $info.UseShellExecute = $false
    $info.RedirectStandardInput = $null -ne $Secret
    if ($info.RedirectStandardInput -and $info.PSObject.Properties['StandardInputEncoding']) {
        $info.StandardInputEncoding = New-Object Text.UTF8Encoding($false)
    }
    # The chosen token source must take precedence over a stale parent environment.
    $info.EnvironmentVariables.Remove('TOKEN_MONITOR_HUB_TOKEN')
    $process = New-Object Diagnostics.Process
    $process.StartInfo = $info
    $pointer = [IntPtr]::Zero
    try {
        if (-not $process.Start()) { throw 'Could not start token-monitor.exe.' }
        if ($null -ne $Secret) {
            $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($Secret)
            $plain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
            $payload = (New-Object Text.UTF8Encoding($false)).GetBytes($plain + "`n")
            $process.StandardInput.BaseStream.Write($payload, 0, $payload.Length)
            $process.StandardInput.BaseStream.Flush()
            [Array]::Clear($payload, 0, $payload.Length)
            $process.StandardInput.Close()
            $plain = $null
        }
        $process.WaitForExit()
        if ($process.ExitCode -ne 0) { throw "token-monitor exited with code $($process.ExitCode). Installation did not complete." }
    } finally {
        if ($pointer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
        $process.Dispose()
    }
}

function Set-PrivateInstallDirectory {
    param([string]$Path, [switch]$Server, [switch]$ServiceWrite)
    [void][IO.Directory]::CreateDirectory($Path)
    $acl = New-Object Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    $propagation = [Security.AccessControl.PropagationFlags]::None
    $allow = [Security.AccessControl.AccessControlType]::Allow
    $identities = @('S-1-5-18', 'S-1-5-32-544') # SYSTEM and Administrators
    if (-not $Server) { $identities += [Security.Principal.WindowsIdentity]::GetCurrent().User.Value }
    foreach ($identity in $identities) {
        $sid = New-Object Security.Principal.SecurityIdentifier($identity)
        $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, 'FullControl', $inheritance, $propagation, $allow)
        [void]$acl.AddAccessRule($rule)
    }
    if ($Server) {
        $rights = 'ReadAndExecute'
        if ($ServiceWrite) { $rights = 'Modify' }
        $sid = New-Object Security.Principal.SecurityIdentifier('S-1-5-19') # LOCAL SERVICE
        $rule = New-Object Security.AccessControl.FileSystemAccessRule($sid, $rights, $inheritance, $propagation, $allow)
        [void]$acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Assert-ArchiveChecksum([string]$Archive, [string]$Manifest, [string]$AssetName) {
    $matchesFound = @([IO.File]::ReadAllLines($Manifest) | Where-Object {
        $_ -match ('^[a-fA-F0-9]{64}\s+\*?' + [regex]::Escape($AssetName) + '$')
    })
    if ($matchesFound.Count -ne 1) { throw 'The release checksum is missing or ambiguous.' }
    $expected = ($matchesFound[0] -split '\s+')[0]
    $actual = (Get-FileHash -LiteralPath $Archive -Algorithm SHA256).Hash
    if (-not [string]::Equals($actual, $expected, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Release checksum verification failed. The executable was not installed.'
    }
}

function Get-CollectorTaskName([string]$MachineId) {
    if ($MachineId -notmatch '^[a-f0-9]{32}$') { throw 'Invalid collector machine identity.' }
    return 'CodexTokenMonitorCollector-' + $MachineId
}

function Register-ClientCollectorTask([string]$Executable, [string]$ConfigPath, [string]$InstallRoot, [string]$MachineId) {
    $name = Get-CollectorTaskName $MachineId
    $description = 'Codex Token Monitor collector | ' + [IO.Path]::GetFullPath($ConfigPath)
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $old = Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
    $oldXml = $null
    if ($old) {
        $owner = $old.Principal.UserId
        if ($owner -notmatch '^S-1-') { $owner = (New-Object Security.Principal.NTAccount($owner)).Translate([Security.Principal.SecurityIdentifier]).Value }
        if ($old.Description -ne $description -or $owner -ne $identity.User.Value) {
            throw 'The collector task belongs to another profile or user; it was not modified.'
        }
        $oldXml = Export-ScheduledTask -TaskName $name
    }
    # The native collector runs as this user while logged in, independently of
    # Codex. A hidden PowerShell parent avoids opening a console and removes a
    # stale inherited token override. No credential is in the task arguments.
    $quotedExe = "'" + $Executable.Replace("'", "''") + "'"
    $quotedConfig = "'" + $ConfigPath.Replace("'", "''") + "'"
    $launch = '$ErrorActionPreference = ''Stop''; Remove-Item Env:TOKEN_MONITOR_HUB_TOKEN -ErrorAction SilentlyContinue; & ' + $quotedExe + ' watch --parent-pid $PID --config ' + $quotedConfig + '; exit $LASTEXITCODE'
    $encoded = [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($launch))
    $powershell = Join-Path $env:SystemRoot 'System32/WindowsPowerShell/v1.0/powershell.exe'
    $action = New-ScheduledTaskAction -Execute $powershell -Argument ('-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand ' + $encoded) -WorkingDirectory $InstallRoot
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
    $principal = New-ScheduledTaskPrincipal -UserId $identity.User.Value -LogonType Interactive -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -MultipleInstances IgnoreNew -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    try {
        if ($old) { Stop-ScheduledTask -TaskName $name }
        [void](Register-ScheduledTask -TaskName $name -Description $description -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force)
        Start-ScheduledTask -TaskName $name
        $running = $false
        for ($attempt = 0; $attempt -lt 15; $attempt++) {
            if ((Get-ScheduledTask -TaskName $name).State -eq 'Running') { $running = $true; break }
            Start-Sleep -Seconds 1
        }
        if (-not $running) { throw "Automatic collector $name did not stay running." }
    } catch {
        $failure = $_
        if ($oldXml) {
            [void](Register-ScheduledTask -TaskName $name -Xml $oldXml -Force)
            Start-ScheduledTask -TaskName $name
        } else {
            Stop-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
            Unregister-ScheduledTask -TaskName $name -Confirm:$false -ErrorAction SilentlyContinue
        }
        throw $failure
    }
    Write-Output "Automatic collector is running as the current user: $name"
    Write-Output 'Usage sync continues while this Windows user is logged in, even when Codex is closed.'
}

function Install-VerifiedExecutable([string]$Archive, [string]$Destination) {
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    $temporary = Join-Path (Split-Path -Parent $Destination) ('.binary-' + [Guid]::NewGuid().ToString('N'))
    try {
        $entry = $zip.GetEntry('token-monitor.exe')
        if ($null -eq $entry -or $entry.Length -le 0 -or $entry.Length -gt 128MB) { throw 'The release archive has no valid token-monitor.exe.' }
        # Extract only the fixed executable name; archive paths never choose a destination.
        [IO.Compression.ZipFileExtensions]::ExtractToFile($entry, $temporary, $false)
        if (Test-Path -LiteralPath $Destination) {
            if ((Get-FileHash -LiteralPath $Destination).Hash -ne (Get-FileHash -LiteralPath $temporary).Hash) {
                throw 'This version is already installed with different contents. It was not overwritten.'
            }
        } else { [IO.File]::Move($temporary, $Destination) }
    } finally {
        $zip.Dispose()
        if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force }
    }
}

function Install-WindowsMonitor {
    [CmdletBinding()]
    param(
        [string]$Mode = 'Client', [string]$Version = 'v0.1.4', [string]$Architecture = 'Auto', [string]$Name, [string]$HubUrl,
        [Security.SecureString]$Token, [string]$TokenFile, [string]$InstallRoot,
        [string]$ListenAddress = '127.0.0.1', [int]$Port = 8787, [string]$PublicUrl, [string]$TlsCertificate,
        [string]$TlsKey, [string]$TaskName = 'CodexTokenMonitorHub', [string]$ArchivePath, [string]$ChecksumsPath,
        [switch]$NonInteractive, [switch]$DryRun
    )
    if (-not (Test-WindowsPlatform) -and -not $DryRun) { throw 'Run this installer on Windows.' }
    if ($Architecture -eq 'Auto') { $Architecture = Get-WindowsArchitecture }
    if (-not $InstallRoot) {
        if ($Mode -eq 'Server') { $InstallRoot = Join-Path $env:ProgramData 'CodexTokenMonitor' }
        else { $InstallRoot = Join-Path $env:LOCALAPPDATA 'CodexTokenMonitor' }
    }
    $InstallRoot = [IO.Path]::GetFullPath($InstallRoot)
    $configDirectory = Join-Path $InstallRoot 'config'
    $configPath = Join-Path $configDirectory 'config.json'
    $binaryDirectory = Join-Path (Join-Path $InstallRoot 'bin') $Version
    $executable = Join-Path $binaryDirectory 'token-monitor.exe'
    $dataDirectory = Join-Path $InstallRoot 'data'
    $serverMode = $Mode -eq 'Server'
    if ($Token -and $TokenFile) { throw 'Choose either -Token or -TokenFile.' }
    if ($TokenFile -and -not (Test-Path -LiteralPath $TokenFile -PathType Leaf)) { throw 'The selected token file was not found.' }
    if ([bool]$ArchivePath -ne [bool]$ChecksumsPath) { throw 'Provide both -ArchivePath and -ChecksumsPath for offline installation.' }
    $existing = $null
    if (Test-Path -LiteralPath $configPath) {
        if ($serverMode) { throw 'Server configuration already exists. Follow the upgrade guide; it will not be overwritten.' }
        if ($Token -or $TokenFile) { throw 'Existing credentials are preserved. Edit the profile before changing credentials.' }
        $existing = Get-Content -LiteralPath $configPath -Encoding UTF8 -Raw | ConvertFrom-Json
        if (-not $Name) { $Name = $existing.machine_name }
        if (-not $HubUrl) { $HubUrl = $existing.hub_url }
    }
    $Name = Read-RequiredValue $Name 'نام دستگاه یا مرکز در گزارش' $NonInteractive
    if ($Name.Length -gt 200) { throw 'The display name must be at most 200 characters.' }
    if ($serverMode) {
        if (-not $NonInteractive -and -not $PSBoundParameters.ContainsKey('ListenAddress')) {
            $entered = Read-Host "آدرس گوش‌دادن [$ListenAddress]"
            if ($entered) { $ListenAddress = $entered }
        }
        if (-not $NonInteractive -and -not $PSBoundParameters.ContainsKey('Port')) {
            $entered = Read-Host "پورت سرور [$Port]"
            if ($entered -and -not [int]::TryParse($entered, [ref]$Port)) { throw 'Invalid port.' }
        }
        if ($ListenAddress -notmatch '^[a-zA-Z0-9.:_-]+$' -or $Port -lt 1024 -or $Port -gt 65535) { throw 'Invalid listen address or port.' }
        if (-not $PublicUrl -and -not $NonInteractive) { $PublicUrl = Read-Host 'آدرس بیرونی سرور، مانند https://monitor.example.com' }
        $PublicUrl = Read-RequiredValue $PublicUrl 'PublicUrl' $true
        Assert-HubAddress $PublicUrl
        if (-not $TlsCertificate -and -not $NonInteractive) { $TlsCertificate = Read-Host 'مسیر گواهی TLS مستقیم؛ برای تونل یا reverse proxy خالی بگذارید' }
        if ($TlsCertificate) {
            $TlsKey = Read-RequiredValue $TlsKey 'مسیر کلید خصوصی TLS' $NonInteractive
            if (-not (Test-Path -LiteralPath $TlsCertificate -PathType Leaf) -or -not (Test-Path -LiteralPath $TlsKey -PathType Leaf)) { throw 'TLS files were not found.' }
        } elseif ($TlsKey) { throw 'A TLS key requires a certificate.' }
    } else {
        $HubUrl = Read-RequiredValue $HubUrl 'آدرس سرور مشترک، بدون مسیر /mcp' $NonInteractive
        Assert-HubAddress $HubUrl
    }
    $asset = 'token-monitor_' + $Version.Substring(1) + '_windows_' + $Architecture + '.zip'
    $releaseUrl = 'https://github.com/AlirezaDadrass/codex-token-monitor/releases/download/' + $Version
    if ($DryRun) {
        Write-Output ('Dry run: no downloads, files, credentials, tasks or firewall rules were changed.')
        return [pscustomobject]@{ Mode = $Mode; Name = $Name; Version = $Version; Architecture = $Architecture; Asset = "$releaseUrl/$asset"; Executable = $executable; Configuration = $configPath }
    }
    if ($Architecture -ne (Get-WindowsArchitecture)) { throw 'The selected release architecture does not match this Windows system.' }
    if ($serverMode) {
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = New-Object Security.Principal.WindowsPrincipal($identity)
        if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'Open PowerShell as Administrator for server installation.' }
        if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) { throw 'The server task already exists; it was not modified.' }
    } elseif (-not (Get-Command codex -ErrorAction SilentlyContinue)) {
        throw 'Codex CLI was not found. Install Codex CLI first, then run this installer again.'
    }
    if ($null -eq $existing -and -not $Token -and -not $TokenFile) {
        if ($NonInteractive) {
            if (-not $serverMode) { throw 'Provide -Token as a SecureString or -TokenFile in non-interactive mode.' }
        } else { $Token = Read-Host 'توکن اتصال (مخفی)؛ در حالت سرور خالی = ساخت کلید جدید' -AsSecureString }
        if (-not $serverMode -and ($null -eq $Token -or $Token.Length -lt 32)) { throw 'A client connection token of at least 32 characters is required.' }
    }
    $stage = Join-Path ([IO.Path]::GetTempPath()) ('codex-token-monitor-' + [Guid]::NewGuid().ToString('N'))
    try {
        Set-PrivateInstallDirectory -Path $stage
        if (-not $ArchivePath) {
            [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
            $ArchivePath = Join-Path $stage $asset
            $ChecksumsPath = Join-Path $stage 'checksums.txt'
            Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/$asset" -OutFile $ArchivePath
            Invoke-WebRequest -UseBasicParsing -Uri "$releaseUrl/checksums.txt" -OutFile $ChecksumsPath
        }
        Assert-ArchiveChecksum $ArchivePath $ChecksumsPath $asset
        Set-PrivateInstallDirectory -Path $InstallRoot -Server:$serverMode
        Set-PrivateInstallDirectory -Path $configDirectory -Server:$serverMode
        Set-PrivateInstallDirectory -Path $binaryDirectory -Server:$serverMode
        Set-PrivateInstallDirectory -Path $dataDirectory -Server:$serverMode -ServiceWrite:$serverMode
        Install-VerifiedExecutable $ArchivePath $executable
        if ($serverMode) {
            $arguments = @('init', '--non-interactive', '--name', $Name, '--config', $configPath, '--data-dir', $dataDirectory, '--codex-home', (Join-Path $InstallRoot 'unused-codex-home'), '--public-url', $PublicUrl)
        } else {
            $arguments = @('install', '--non-interactive', '--name', $Name, '--hub-url', $HubUrl, '--config', $configPath, '--data-dir', $dataDirectory)
        }
        if ($null -eq $existing) {
            if ($TokenFile) {
                $storedToken = Join-Path $configDirectory 'connection-token'
                Copy-Item -LiteralPath $TokenFile -Destination $storedToken
                $arguments += @('--token-file', $storedToken)
            }
            elseif ($Token -and $Token.Length -gt 0) { $arguments += '--token-stdin' }
            elseif ($serverMode) { $arguments += '--create-token' }
        }
        $stdinSecret = $null
        if ($arguments -contains '--token-stdin') { $stdinSecret = $Token }
        Invoke-MonitorBinary -Executable $executable -Arguments $arguments -Secret $stdinSecret
        if ($serverMode) {
            $listen = $ListenAddress + ':' + $Port
            if ($ListenAddress.Contains(':')) { $listen = '[' + $ListenAddress + ']:' + $Port }
            $serverProfile = Get-Content -LiteralPath $configPath -Encoding UTF8 -Raw | ConvertFrom-Json
            $hubArguments = @('hub', '--config', $configPath, '--token-file', $serverProfile.token_file, '--listen', $listen)
            if ($TlsCertificate) {
                $certTarget = Join-Path $configDirectory 'server.crt'; $keyTarget = Join-Path $configDirectory 'server.key'
                Copy-Item -LiteralPath $TlsCertificate -Destination $certTarget
                Copy-Item -LiteralPath $TlsKey -Destination $keyTarget
                $hubArguments += @('--tls-cert', $certTarget, '--tls-key', $keyTarget)
            }
            $action = New-ScheduledTaskAction -Execute $executable -Argument (($hubArguments | ForEach-Object { ConvertTo-WindowsArgument $_ }) -join ' ') -WorkingDirectory $InstallRoot
            $trigger = New-ScheduledTaskTrigger -AtStartup
            $principal = New-ScheduledTaskPrincipal -UserId 'S-1-5-19' -LogonType ServiceAccount -RunLevel Limited
            $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -StartWhenAvailable -MultipleInstances IgnoreNew
            [void](Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings)
            Start-ScheduledTask -TaskName $TaskName
            $running = $false
            for ($attempt = 0; $attempt -lt 15; $attempt++) {
                if ((Get-ScheduledTask -TaskName $TaskName).State -eq 'Running') { $running = $true; break }
                Start-Sleep -Seconds 1
            }
            if (-not $running) { throw "Task $TaskName did not stay running. Inspect Task Scheduler before considering installation complete." }
            Write-Output "Server task registered as LOCAL SERVICE: $TaskName"
            Write-Output 'No firewall, execution-policy, or reverse-proxy settings were changed.'
        } else {
            $profile = Get-Content -LiteralPath $configPath -Encoding UTF8 -Raw | ConvertFrom-Json
            Register-ClientCollectorTask -Executable $executable -ConfigPath $configPath -InstallRoot $InstallRoot -MachineId $profile.machine_id
        }
        Write-Output "Installed executable: $executable"
        Write-Output "Configuration: $configPath"
        Write-Output 'Token values were not displayed. No Go toolchain or build was required.'
    } finally {
        if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
    }
}

# Dot-sourcing loads helpers for tests; it never starts an installation.
if ($MyInvocation.InvocationName -ne '.') { Install-WindowsMonitor @PSBoundParameters }
