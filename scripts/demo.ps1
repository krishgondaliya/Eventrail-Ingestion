param(
    [Parameter(Position = 0)]
    [ValidateSet("start", "status", "stop", "reset")]
    [string]$Command = "status",

    [switch]$NoBrowser,
    [switch]$UseOpenAI,
    [switch]$SkipAI,
    [switch]$Force
)

Set-StrictMode -Version Latest
. (Join-Path $PSScriptRoot "demo-common.ps1")

$Root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$Paths = Get-DemoPaths -Root $Root

function Get-OpenAIDemoModel {
    $configured = [string]$env:OPENAI_MODEL
    if ([string]::IsNullOrWhiteSpace($configured)) {
        return "gpt-5.4-mini"
    }
    return $configured.Trim()
}

function Get-OpenAIDemoTimeoutSeconds {
    $configured = [string]$env:OPENAI_TIMEOUT_SECONDS
    if ([string]::IsNullOrWhiteSpace($configured)) {
        return "30"
    }
    return $configured.Trim()
}

function ConvertTo-PowerShellSingleQuotedValue {
    param([string]$Value)

    return "'" + ($Value -replace "'", "''") + "'"
}

function Start-Demo {
    param([switch]$Fresh)

    if ($UseOpenAI -and $SkipAI) {
        throw "Use either -UseOpenAI or -SkipAI, not both."
    }
    if ($UseOpenAI -and [string]::IsNullOrWhiteSpace([string]$env:OPENAI_API_KEY)) {
        throw "OPENAI_API_KEY is required for OpenAI Event Intelligence."
    }

    $paths = Ensure-DemoDirectories -Root $Root

    foreach ($commandName in @("docker", "go", "python", "npm")) {
        Assert-CommandAvailable -Name $commandName
    }

    Push-Location $Root
    try {
        docker info *> $null
        if ($LASTEXITCODE -ne 0) {
            throw "Docker does not appear to be running."
        }
    } finally {
        Pop-Location
    }

    $existing = Read-ProcessState -ProcessFile $paths.ProcessFile
    if ($null -ne $existing) {
        Write-Host "EventRail demo already appears to be started."
        Show-Status
        return
    }
    if (Test-DemoPortsInUse) {
        Write-Host "EventRail demo services already appear to be running."
        Show-Status
        return
    }

    Ensure-PythonDependencies
    Ensure-DashboardDependencies

    $started = @{}
    $state = [ordered]@{
        started_at = (Get-Date).ToString("o")
        ai_mode = if ($SkipAI) { "skipped" } elseif ($UseOpenAI) { "openai" } else { "deterministic" }
        event_intelligence_model = if ($UseOpenAI) { Get-OpenAIDemoModel } else { $null }
        processes = [ordered]@{}
    }

    try {
        Push-Location $Root
        docker compose up -d postgres redis | Out-Host
        Pop-Location

        Wait-Until -Name "PostgreSQL" -Check {
            Push-Location $Root
            try {
                $output = docker compose exec -T postgres pg_isready -U eventrail 2>$null
                return $LASTEXITCODE -eq 0 -and ($output -match "accepting connections")
            } finally {
                Pop-Location
            }
        }
        Write-Host "[1/6] PostgreSQL ready"

        Wait-Until -Name "Redis" -Check {
            Push-Location $Root
            try {
                $output = docker compose exec -T redis redis-cli ping 2>$null
                return $LASTEXITCODE -eq 0 -and ($output -match "PONG")
            } finally {
                Pop-Location
            }
        }
        Write-Host "[2/6] Redis ready"

        $mock = Start-AppProcess `
            -Name "mock" `
            -Command "go run ./cmd/mock-destination" `
            -WorkingDirectory $Root `
            -LogDir $paths.LogDir
        $started["mock_destination"] = $mock.Id
        $state.processes["mock_destination"] = @{ pid = $mock.Id }

        Wait-Until -Name "Mock Receipt Service" -Check { Test-HttpHealthy -Url "http://127.0.0.1:8081/stats" }
        Write-Host "[3/6] Mock Receipt Service ready"

        if ($SkipAI) {
            $state.processes["ai_service"] = @{ skipped = $true }
            Write-Host "[4/6] AI Triage skipped"
        } else {
            $aiModeCommand = if ($UseOpenAI) {
                $openAIModel = ConvertTo-PowerShellSingleQuotedValue -Value (Get-OpenAIDemoModel)
                $openAITimeout = ConvertTo-PowerShellSingleQuotedValue -Value (Get-OpenAIDemoTimeoutSeconds)
                "`$env:TRIAGE_PROVIDER='deterministic'; `$env:EXPLAIN_PROVIDER='openai'; `$env:OPENAI_MODEL=$openAIModel; `$env:OPENAI_TIMEOUT_SECONDS=$openAITimeout; python -m uvicorn eventrail_ai.api:app --host 127.0.0.1 --port 8090"
            } else {
                "`$env:TRIAGE_PROVIDER='deterministic'; `$env:EXPLAIN_PROVIDER='deterministic'; python -m uvicorn eventrail_ai.api:app --host 127.0.0.1 --port 8090"
            }
            $ai = Start-AppProcess `
                -Name "ai" `
                -Command $aiModeCommand `
                -WorkingDirectory (Join-Path $Root "ai-service") `
                -LogDir $paths.LogDir
            $started["ai_service"] = $ai.Id
            $state.processes["ai_service"] = @{ pid = $ai.Id }

            $aiWaitSeconds = 60
            Wait-Until -Name "AI Triage" -TimeoutSeconds $aiWaitSeconds -Check { Test-HttpHealthy -Url "http://127.0.0.1:8090/health/live" }
            Write-Host "[4/6] AI Triage ready"
        }

        $apiCommand = "`$env:POSTGRES_DSN='postgres://eventrail:eventrail@127.0.0.1:5432/eventrail'; `$env:REDIS_ADDR='127.0.0.1:6379'; `$env:AI_SERVICE_URL='http://127.0.0.1:8090'; `$env:AI_SERVICE_TIMEOUT_MS='60000'; go run ./cmd/api"
        $api = Start-AppProcess `
            -Name "api" `
            -Command $apiCommand `
            -WorkingDirectory $Root `
            -LogDir $paths.LogDir
        $started["eventrail_api"] = $api.Id
        $state.processes["eventrail_api"] = @{ pid = $api.Id }

        Wait-Until -Name "EventRail API" -Check { Test-HttpHealthy -Url "http://127.0.0.1:8080/health/ready" }
        Write-Host "[5/6] EventRail API ready"

        $dashboardCommand = "`$env:VITE_EVENTRAIL_API_URL='http://127.0.0.1:8080'; `$env:VITE_MOCK_DESTINATION_URL='http://127.0.0.1:8081'; & '.\node_modules\.bin\vite.cmd' --host 127.0.0.1"
        $dashboard = Start-AppProcess `
            -Name "dashboard" `
            -Command $dashboardCommand `
            -WorkingDirectory (Join-Path $Root "dashboard") `
            -LogDir $paths.LogDir
        $started["dashboard"] = $dashboard.Id
        $state.processes["dashboard"] = @{ pid = $dashboard.Id }

        Wait-Until -Name "Dashboard" -Check { Test-HttpHealthy -Url "http://127.0.0.1:5173/" }
        Write-Host "[6/6] Dashboard ready"

        Write-ProcessState -ProcessFile $paths.ProcessFile -State $state
        Show-StartSummary -Paths $paths -Fresh:$Fresh

        if (-not $NoBrowser) {
            Start-Process "http://127.0.0.1:5173"
        }
    } catch {
        Write-Host "Startup failed: $($_.Exception.Message)" -ForegroundColor Red
        Write-Host "Logs: $($paths.LogDir)"
        foreach ($pidValue in $started.Values) {
            Stop-ProcessTree -ProcessId ([int]$pidValue)
        }
        exit 1
    }
}

function Start-AppProcess {
    param(
        [string]$Name,
        [string]$Command,
        [string]$WorkingDirectory,
        [string]$LogDir
    )

    $stdout = Join-Path $LogDir "$Name.stdout.log"
    $stderr = Join-Path $LogDir "$Name.stderr.log"
    return Invoke-LoggedProcess `
        -Name $Name `
        -Command $Command `
        -WorkingDirectory $WorkingDirectory `
        -StdoutPath $stdout `
        -StderrPath $stderr
}

function Ensure-PythonDependencies {
    Push-Location (Join-Path $Root "ai-service")
    try {
        python -c "import eventrail_ai" 2>$null
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Installing Python dependencies..."
            python -m pip install -e ".[dev]"
            if ($LASTEXITCODE -ne 0) {
                throw "Python dependency installation failed."
            }
        }
    } finally {
        Pop-Location
    }
}

function Ensure-DashboardDependencies {
    $nodeModules = Join-Path $Root "dashboard\node_modules"
    if (-not (Test-Path -LiteralPath $nodeModules)) {
        Write-Host "Installing dashboard dependencies..."
        Push-Location (Join-Path $Root "dashboard")
        try {
            npm install
            if ($LASTEXITCODE -ne 0) {
                throw "Dashboard dependency installation failed."
            }
        } finally {
            Pop-Location
        }
    }
}

function Test-DemoPortsInUse {
    return (
        (Test-HttpHealthy -Url "http://127.0.0.1:8081/stats") -or
        (Test-HttpHealthy -Url "http://127.0.0.1:8090/health/live") -or
        (Test-HttpHealthy -Url "http://127.0.0.1:8080/health/ready") -or
        (Test-HttpHealthy -Url "http://127.0.0.1:5173/")
    )
}

function Show-StartSummary {
    param(
        [object]$Paths,
        [switch]$Fresh
    )

    $exceptionGuidance = if ($SkipAI) { "Skipped" } else { "Deterministic" }
    $eventIntelligence = if ($SkipAI) { "Skipped" } elseif ($UseOpenAI) { "OpenAI ($(Get-OpenAIDemoModel))" } else { "Deterministic" }
    Write-Host ""
    if ($Fresh) {
        Write-Host "Fresh EventRail demo is ready."
    } else {
        Write-Host "EventRail demo is ready."
    }
    Write-Host ""
    Write-Host "Dashboard:"
    Write-Host "http://127.0.0.1:5173"
    Write-Host ""
    Write-Host "EventRail API:"
    Write-Host "http://127.0.0.1:8080"
    Write-Host ""
    Write-Host "Exception Guidance:"
    Write-Host $exceptionGuidance
    Write-Host ""
    Write-Host "Event Intelligence:"
    Write-Host $eventIntelligence
    Write-Host ""
    Write-Host "Logs:"
    Write-Host $Paths.LogDir
    Write-Host ""
    Write-Host "Stop command:"
    Write-Host ".\scripts\demo.ps1 stop"
}

function Show-Status {
    $paths = Get-DemoPaths -Root $Root
    $state = Read-ProcessState -ProcessFile $paths.ProcessFile

    $rows = @(
        [pscustomobject]@{
            Service = "PostgreSQL"
            Status = Get-DockerServiceStatus -Root $Root -Service "postgres" -Check {
                $output = docker compose exec -T postgres pg_isready -U eventrail 2>$null
                return $LASTEXITCODE -eq 0 -and ($output -match "accepting connections")
            }
            URL = "localhost:5432"
        },
        [pscustomobject]@{
            Service = "Redis"
            Status = Get-DockerServiceStatus -Root $Root -Service "redis" -Check {
                $output = docker compose exec -T redis redis-cli ping 2>$null
                return $LASTEXITCODE -eq 0 -and ($output -match "PONG")
            }
            URL = "localhost:6379"
        },
        [pscustomobject]@{
            Service = "Mock Receipt Service"
            Status = Get-AppStatus -State $state -Key "mock_destination" -Url "http://127.0.0.1:8081/stats"
            URL = "http://127.0.0.1:8081"
        },
        [pscustomobject]@{
            Service = "AI Triage"
            Status = Get-AppStatus -State $state -Key "ai_service" -Url "http://127.0.0.1:8090/health/live"
            URL = "http://127.0.0.1:8090"
        },
        [pscustomobject]@{
            Service = "EventRail API"
            Status = Get-AppStatus -State $state -Key "eventrail_api" -Url "http://127.0.0.1:8080/health/ready"
            URL = "http://127.0.0.1:8080"
        },
        [pscustomobject]@{
            Service = "Dashboard"
            Status = Get-AppStatus -State $state -Key "dashboard" -Url "http://127.0.0.1:5173/"
            URL = "http://127.0.0.1:5173"
        }
    )

    $rows | Format-Table -AutoSize
}

function Stop-Demo {
    $paths = Get-DemoPaths -Root $Root
    Stop-RecordedProcesses -Paths $paths

    Push-Location $Root
    try {
        docker compose down 2>$null | Out-Host
        if ($LASTEXITCODE -ne 0) {
            Write-Host "Docker Compose down did not complete; Docker may be unavailable."
        }
    } finally {
        Pop-Location
    }

    if (Test-Path -LiteralPath $paths.ProcessFile) {
        Remove-Item -LiteralPath $paths.ProcessFile -Force
    }
    Write-Host "EventRail demo stopped. Logs preserved at $($paths.LogDir)"
}

function Stop-RecordedProcesses {
    param([object]$Paths)

    $state = Read-ProcessState -ProcessFile $Paths.ProcessFile

    if ($null -eq $state) {
        Write-Host "No recorded EventRail demo processes found."
        return
    }

    foreach ($key in @("dashboard", "eventrail_api", "ai_service", "mock_destination")) {
        $entry = Get-ObjectProperty -Object $state.processes -Name $key
        if ($null -eq $entry -or (Get-ObjectProperty -Object $entry -Name "skipped") -eq $true) {
            continue
        }
        Stop-ProcessTree -ProcessId ([int](Get-ObjectProperty -Object $entry -Name "pid"))
        Write-Host "Stopped $key"
    }
}

function Reset-Demo {
    if (-not $Force) {
        Write-Host "Reset deletes local EventRail PostgreSQL and Redis demo data."
        Write-Host "Run again with -Force to continue."
        exit 1
    }

    $paths = Ensure-DemoDirectories -Root $Root
    Stop-RecordedProcesses -Paths $paths

    Push-Location $Root
    try {
        docker compose down -v | Out-Host
        if ($LASTEXITCODE -ne 0) {
            throw "Docker Compose reset failed."
        }
    } finally {
        Pop-Location
    }

    if (Test-Path -LiteralPath $paths.ProcessFile) {
        Remove-Item -LiteralPath $paths.ProcessFile -Force
    }
    if (Test-Path -LiteralPath $paths.LogDir) {
        Get-ChildItem -LiteralPath $paths.LogDir -File | Remove-Item -Force
    }
    New-Item -ItemType Directory -Force -Path $paths.LogDir | Out-Null

    Start-Demo -Fresh
    Show-Status
}

try {
    switch ($Command) {
        "start" { Start-Demo }
        "status" { Show-Status }
        "stop" { Stop-Demo }
        "reset" { Reset-Demo }
    }
} catch {
    Write-Host $_.Exception.Message -ForegroundColor Red
    exit 1
}
