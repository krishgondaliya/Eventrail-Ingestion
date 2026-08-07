Set-StrictMode -Version Latest

function Get-RepoRoot {
    $scriptDir = Split-Path -Parent $PSCommandPath
    return (Resolve-Path (Join-Path $scriptDir "..")).Path
}

function Get-DemoPaths {
    param([string]$Root)

    $demoDir = Join-Path $Root ".demo"
    $logDir = Join-Path $demoDir "logs"
    $processFile = Join-Path $demoDir "processes.json"

    [pscustomobject]@{
        DemoDir = $demoDir
        LogDir = $logDir
        ProcessFile = $processFile
    }
}

function Ensure-DemoDirectories {
    param([string]$Root)

    $paths = Get-DemoPaths -Root $Root
    New-Item -ItemType Directory -Force -Path $paths.LogDir | Out-Null
    return $paths
}

function Test-CommandAvailable {
    param([string]$Name)

    return [bool](Get-Command $Name -ErrorAction SilentlyContinue)
}

function Assert-CommandAvailable {
    param([string]$Name)

    if (-not (Test-CommandAvailable -Name $Name)) {
        throw "Required command '$Name' was not found on PATH."
    }
}

function Invoke-LoggedProcess {
    param(
        [string]$Name,
        [string]$Command,
        [string]$WorkingDirectory,
        [string]$StdoutPath,
        [string]$StderrPath
    )

    return Start-Process `
        -FilePath "powershell" `
        -ArgumentList "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", $Command `
        -WorkingDirectory $WorkingDirectory `
        -RedirectStandardOutput $StdoutPath `
        -RedirectStandardError $StderrPath `
        -WindowStyle Hidden `
        -PassThru
}

function Wait-Until {
    param(
        [string]$Name,
        [scriptblock]$Check,
        [int]$TimeoutSeconds = 60,
        [int]$DelayMilliseconds = 1000
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        try {
            if (& $Check) {
                return $true
            }
        } catch {
            # Retry until the deadline.
        }
        Start-Sleep -Milliseconds $DelayMilliseconds
    } while ((Get-Date) -lt $deadline)

    throw "$Name did not become ready within $TimeoutSeconds seconds."
}

function Test-HttpHealthy {
    param([string]$Url)

    try {
        Invoke-WebRequest -Method Get -Uri $Url -TimeoutSec 3 -UseBasicParsing | Out-Null
        return $true
    } catch {
        return $false
    }
}

function Test-ProcessRunning {
    param([Nullable[int]]$ProcessId)

    if ($null -eq $ProcessId) {
        return $false
    }
    return [bool](Get-Process -Id $ProcessId -ErrorAction SilentlyContinue)
}

function Read-ProcessState {
    param([string]$ProcessFile)

    if (-not (Test-Path -LiteralPath $ProcessFile)) {
        return $null
    }
    return Get-Content -Raw -LiteralPath $ProcessFile | ConvertFrom-Json
}

function Get-ObjectProperty {
    param(
        [object]$Object,
        [string]$Name
    )

    if ($null -eq $Object) {
        return $null
    }
    $property = $Object.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $null
    }
    return $property.Value
}

function Write-ProcessState {
    param(
        [string]$ProcessFile,
        [object]$State
    )

    $State | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $ProcessFile -Encoding UTF8
}

function Stop-ProcessTree {
    param([Nullable[int]]$ProcessId)

    if ($null -eq $ProcessId) {
        return
    }
    if (-not (Test-ProcessRunning -ProcessId $ProcessId)) {
        return
    }

    & taskkill.exe /PID $ProcessId /T /F | Out-Null
}

function Get-AppStatus {
    param(
        [object]$State,
        [string]$Key,
        [string]$Url
    )

    if ($null -eq $State) {
        return "Not started"
    }

    $entry = Get-ObjectProperty -Object $State.processes -Name $Key
    if ($null -eq $entry) {
        return "Not started"
    }
    if ((Get-ObjectProperty -Object $entry -Name "skipped") -eq $true) {
        return "Skipped"
    }

    $pidValue = [int](Get-ObjectProperty -Object $entry -Name "pid")
    if (-not (Test-ProcessRunning -ProcessId $pidValue)) {
        return "Process stopped"
    }
    if (Test-HttpHealthy -Url $Url) {
        return "Healthy"
    }
    return "Unavailable"
}

function Get-DockerServiceStatus {
    param(
        [string]$Root,
        [string]$Service,
        [scriptblock]$Check
    )

    Push-Location $Root
    try {
        $ids = docker compose ps -q $Service 2>$null
        if (-not $ids) {
            return "Not started"
        }
        if (& $Check) {
            return "Healthy"
        }
        return "Unavailable"
    } finally {
        Pop-Location
    }
}
