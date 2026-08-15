[CmdletBinding()]
param(
    # 使用独立宿主机端口，避免影响本机直接运行在 8080 的 Go 服务。
    [ValidateRange(1, 65535)]
    [int]$HostPort = 18080
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory)]
        [string]$FilePath,

        [Parameter(Mandatory)]
        [string[]]$ArgumentList,

        [Parameter(Mandatory)]
        [string]$Description
    )

    & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE"
    }
}

function Get-NativeText {
    param(
        [Parameter(Mandatory)]
        [string]$FilePath,

        [Parameter(Mandatory)]
        [string[]]$ArgumentList,

        [Parameter(Mandatory)]
        [string]$Description
    )

    $output = & $FilePath @ArgumentList
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE"
    }

    return (($output | ForEach-Object { [string]$_ }) -join "`n").Trim()
}

function Invoke-BestEffortNativeCleanup {
    param(
        [Parameter(Mandatory)]
        [string]$FilePath,

        [Parameter(Mandatory)]
        [string[]]$ArgumentList
    )

    # Windows PowerShell 5.1 会把 Docker 写到 stderr 的正常进度提示包装成
    # NativeCommandError。清理阶段只依据进程退出码判断，避免掩盖真正的验收结果。
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        & $FilePath @ArgumentList 2>&1 | Out-Null
        return $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
}

function Wait-BackendHealthy {
    param(
        [Parameter(Mandatory)]
        [int]$Port,

        [int]$MaximumAttempts = 60
    )

    for ($attempt = 1; $attempt -le $MaximumAttempts; $attempt++) {
        $containerOutput = & docker compose ps --status running --quiet backend
        if ($LASTEXITCODE -ne 0) {
            throw "inspect running backend failed with exit code $LASTEXITCODE"
        }
        $containerID = (($containerOutput | ForEach-Object { [string]$_ }) -join "`n").Trim()

        if (-not [string]::IsNullOrWhiteSpace($containerID)) {
            $healthOutput = & docker inspect --format "{{.State.Health.Status}}" $containerID 2>$null
            $health = (($healthOutput | ForEach-Object { [string]$_ }) -join "`n").Trim()
            if ($LASTEXITCODE -eq 0 -and $health -eq "healthy") {
                & curl.exe --fail --silent --show-error "http://127.0.0.1:$Port/health" *> $null
                if ($LASTEXITCODE -eq 0) {
                    return $containerID
                }
            }
        }

        Start-Sleep -Seconds 1
    }

    throw "backend did not become healthy on port $Port"
}

function Get-ContainerState {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerID
    )

    $json = Get-NativeText -FilePath "docker" `
        -ArgumentList @("inspect", $ContainerID) `
        -Description "inspect backend container"
    return ($json | ConvertFrom-Json)[0].State
}

function Write-Utf8Text {
    param(
        [Parameter(Mandatory)]
        [string]$LiteralPath,

        [Parameter(Mandatory)]
        [string]$Value
    )

    $utf8WithoutBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($LiteralPath, $Value, $utf8WithoutBom)
}

$scriptDirectory = Split-Path -Parent $PSCommandPath
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory "..\.."))
$originalLocation = Get-Location
$marker = "lifecycle-$([Guid]::NewGuid().ToString('N'))"
$documentStoragePath = "lifecycle-verification/$marker-document.md"
$embeddingStoragePath = "lifecycle-verification/$marker-embedding.md"
$fixturesCreated = $false
$backendCreated = $false
$operationError = $null
$logs = ""
$gracefulStopMilliseconds = 0L
$gracefulExitCode = $null
$forcedExitCode = $null
$recoveryState = $null
$testedAt = [DateTime]::UtcNow
$resultDirectory = Join-Path $projectRoot "chatgpt\运行产物\临时"
$logDirectory = Join-Path $projectRoot "chatgpt\运行产物\日志"
$resultPath = Join-Path $resultDirectory "container-lifecycle-$($testedAt.ToString('yyyyMMddTHHmmssZ')).json"
$logPath = Join-Path $logDirectory "container-lifecycle-$($testedAt.ToString('yyyyMMddTHHmmssZ')).log"

try {
    Set-Location $projectRoot

    foreach ($requiredCommand in @("docker", "curl.exe")) {
        if (-not (Get-Command $requiredCommand -ErrorAction SilentlyContinue)) {
            throw "required command '$requiredCommand' was not found"
        }
    }

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "config", "--quiet") `
        -Description "validate Compose configuration"

    $postgresContainerID = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "ps", "--status", "running", "--quiet", "postgres") `
        -Description "find running PostgreSQL"
    if ([string]::IsNullOrWhiteSpace($postgresContainerID)) {
        throw "PostgreSQL is not running"
    }

    $postgresHealth = Get-NativeText -FilePath "docker" `
        -ArgumentList @("inspect", "--format", "{{.State.Health.Status}}", $postgresContainerID) `
        -Description "inspect PostgreSQL health"
    if ($postgresHealth -ne "healthy") {
        throw "PostgreSQL must be healthy; current status: $postgresHealth"
    }

    # 为避免改变开发者已经创建的容器配置，验收要求开始前不存在 backend 容器。
    $existingBackendID = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "ps", "--all", "--quiet", "backend") `
        -Description "inspect existing backend container"
    if (-not [string]::IsNullOrWhiteSpace($existingBackendID)) {
        throw "backend container already exists; stop and remove it before lifecycle verification"
    }

    $databaseName = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_DB") `
        -Description "read PostgreSQL database name"
    $databaseUser = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_USER") `
        -Description "read PostgreSQL user"

    $activeJobsSQL = @"
SELECT json_build_object(
    'document_jobs', (SELECT COUNT(*) FROM document_jobs WHERE status = 'processing'),
    'embedding_jobs', (SELECT COUNT(*) FROM embedding_jobs WHERE status = 'processing')
)::text;
"@
    $activeJobsJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", $databaseName,
            "--tuples-only", "--no-align", "--command", $activeJobsSQL
        ) `
        -Description "inspect active jobs"
    $activeJobs = $activeJobsJSON | ConvertFrom-Json
    if ([int64]$activeJobs.document_jobs -ne 0 -or [int64]$activeJobs.embedding_jobs -ne 0) {
        throw "existing processing jobs must be preserved; lifecycle verification was not started"
    }

    # 独立 PowerShell 进程内覆盖开关，保证验收不会产生远程 API 费用。
    $env:BACKEND_HOST_PORT = [string]$HostPort
    $env:EMBEDDING_WORKER_ENABLED = "false"
    $env:SEMANTIC_SEARCH_ENABLED = "false"
    $env:ANSWER_ENABLED = "false"

    # 验收的正是当前工作区代码和 Dockerfile，不能复用可能过期的本地镜像。
    Write-Host "Starting isolated backend container..."
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "up", "-d", "--build", "backend") `
        -Description "start backend"
    $backendCreated = $true
    $backendContainerID = Wait-BackendHealthy -Port $HostPort

    Write-Host "Verifying SIGTERM graceful shutdown..."
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "stop", "backend") `
        -Description "gracefully stop backend"
    $stopwatch.Stop()
    $gracefulStopMilliseconds = $stopwatch.ElapsedMilliseconds

    $gracefulState = Get-ContainerState -ContainerID $backendContainerID
    $gracefulExitCode = [int]$gracefulState.ExitCode
    if ([bool]$gracefulState.Running) {
        throw "backend is still running after docker compose stop"
    }
    if ($gracefulExitCode -ne 0 -or [bool]$gracefulState.OOMKilled) {
        throw "graceful stop ended with exit code $gracefulExitCode (OOMKilled=$($gracefulState.OOMKilled))"
    }
    if ($gracefulStopMilliseconds -gt 30000) {
        throw "graceful stop exceeded the configured 30 second grace period"
    }

    $gracefulLogs = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "logs", "--no-color", "backend") `
        -Description "read graceful shutdown logs"
    if (-not $gracefulLogs.Contains('"event":"application_shutdown_started"') -or
        -not $gracefulLogs.Contains('"event":"application_stopped"')) {
        throw "graceful shutdown lifecycle events were not found in logs"
    }

    Write-Host "Restarting backend before abnormal-exit verification..."
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "start", "backend") `
        -Description "restart backend"
    $backendContainerID = Wait-BackendHealthy -Port $HostPort

    # 创建两条只用于恢复验收的 processing 任务。Worker 不会领取 processing，
    # 因此它们会一直保留到强制终止后的下一次启动恢复。
    $fixtureSQL = @"
WITH document_fixture AS (
    INSERT INTO documents (
        original_name, storage_path, mime_type, size_bytes, sha256, status
    )
    VALUES (
        '$marker-document.md',
        '$documentStoragePath',
        'text/markdown',
        0,
        repeat('d', 64),
        'processing'
    )
    RETURNING id
),
document_job_fixture AS (
    INSERT INTO document_jobs (
        document_id, status, attempt_count, started_at
    )
    SELECT id, 'processing', 1, CURRENT_TIMESTAMP
    FROM document_fixture
    RETURNING id, document_id
),
embedding_document_fixture AS (
    INSERT INTO documents (
        original_name, storage_path, mime_type, size_bytes, sha256, status
    )
    VALUES (
        '$marker-embedding.md',
        '$embeddingStoragePath',
        'text/markdown',
        0,
        repeat('e', 64),
        'ready'
    )
    RETURNING id
),
embedding_job_fixture AS (
    INSERT INTO embedding_jobs (
        document_id, model_name, dimensions, status, attempt_count, started_at
    )
    SELECT id, 'lifecycle-verification', 1536, 'processing', 1, CURRENT_TIMESTAMP
    FROM embedding_document_fixture
    RETURNING id, document_id
)
SELECT json_build_object(
    'document_job_id', document_job_fixture.id,
    'document_id', document_job_fixture.document_id,
    'embedding_job_id', embedding_job_fixture.id,
    'embedding_document_id', embedding_job_fixture.document_id
)::text
FROM document_job_fixture
CROSS JOIN embedding_job_fixture;
"@
    $fixtureJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", $databaseName,
            "--tuples-only", "--no-align", "--command", $fixtureSQL
        ) `
        -Description "create lifecycle verification fixtures"
    $fixture = $fixtureJSON | ConvertFrom-Json
    $fixturesCreated = $true

    Write-Host "Forcing SIGKILL to simulate an abnormal process death..."
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("kill", "--signal", "KILL", "rag_reasoning_backend") `
        -Description "force kill backend"

    $forcedState = Get-ContainerState -ContainerID $backendContainerID
    $forcedExitCode = [int]$forcedState.ExitCode
    if ($forcedExitCode -ne 137) {
        throw "forced stop exit code is $forcedExitCode; expected 137"
    }

    Write-Host "Starting the same container to trigger startup recovery..."
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "start", "backend") `
        -Description "start backend after forced termination"
    $backendContainerID = Wait-BackendHealthy -Port $HostPort

    $recoverySQL = @"
SELECT json_build_object(
    'document_status', (
        SELECT status FROM documents WHERE storage_path = '$documentStoragePath'
    ),
    'document_error', (
        SELECT error_message FROM documents WHERE storage_path = '$documentStoragePath'
    ),
    'document_job_status', (
        SELECT j.status
        FROM document_jobs AS j
        JOIN documents AS d ON d.id = j.document_id
        WHERE d.storage_path = '$documentStoragePath'
    ),
    'document_job_error', (
        SELECT j.error_message
        FROM document_jobs AS j
        JOIN documents AS d ON d.id = j.document_id
        WHERE d.storage_path = '$documentStoragePath'
    ),
    'embedding_document_status', (
        SELECT status FROM documents WHERE storage_path = '$embeddingStoragePath'
    ),
    'embedding_job_status', (
        SELECT j.status
        FROM embedding_jobs AS j
        JOIN documents AS d ON d.id = j.document_id
        WHERE d.storage_path = '$embeddingStoragePath'
    ),
    'embedding_job_error', (
        SELECT j.error_message
        FROM embedding_jobs AS j
        JOIN documents AS d ON d.id = j.document_id
        WHERE d.storage_path = '$embeddingStoragePath'
    ),
    'embedding_started_at_is_null', (
        SELECT j.started_at IS NULL
        FROM embedding_jobs AS j
        JOIN documents AS d ON d.id = j.document_id
        WHERE d.storage_path = '$embeddingStoragePath'
    )
)::text;
"@
    $recoveryJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", $databaseName,
            "--tuples-only", "--no-align", "--command", $recoverySQL
        ) `
        -Description "read recovered lifecycle state"
    $recoveryState = $recoveryJSON | ConvertFrom-Json

    if ($recoveryState.document_status -ne "failed" -or
        $recoveryState.document_job_status -ne "failed" -or
        $recoveryState.document_error -ne "document processing was interrupted" -or
        $recoveryState.document_job_error -ne "document processing was interrupted") {
        throw "document processing recovery did not produce the expected failed state"
    }
    if ($recoveryState.embedding_document_status -ne "ready" -or
        $recoveryState.embedding_job_status -ne "queued" -or
        $recoveryState.embedding_job_error -ne "embedding generation was interrupted" -or
        -not [bool]$recoveryState.embedding_started_at_is_null) {
        throw "embedding recovery did not produce the expected queued state"
    }

    $logs = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "logs", "--no-color", "backend") `
        -Description "read lifecycle verification logs"
    if (-not $logs.Contains('"event":"processing_jobs_recovered"') -or
        -not $logs.Contains('"event":"embedding_jobs_requeued"')) {
        throw "startup recovery lifecycle events were not found in logs"
    }

    $result = [ordered]@{
        status = "verified"
        tested_at_utc = $testedAt.ToString("o")
        host_port = $HostPort
        remote_ai_enabled = $false
        graceful_shutdown = [ordered]@{
            exit_code = $gracefulExitCode
            duration_ms = $gracefulStopMilliseconds
            shutdown_event_found = $true
            stopped_event_found = $true
        }
        abnormal_shutdown = [ordered]@{
            signal = "SIGKILL"
            exit_code = $forcedExitCode
        }
        recovery = [ordered]@{
            document_job_id = [int64]$fixture.document_job_id
            embedding_job_id = [int64]$fixture.embedding_job_id
            document_status = [string]$recoveryState.document_status
            document_job_status = [string]$recoveryState.document_job_status
            embedding_document_status = [string]$recoveryState.embedding_document_status
            embedding_job_status = [string]$recoveryState.embedding_job_status
        }
    }

    New-Item -ItemType Directory -Force -Path $resultDirectory | Out-Null
    $resultJSON = $result | ConvertTo-Json -Depth 8
    Write-Utf8Text -LiteralPath $resultPath -Value ($resultJSON + [Environment]::NewLine)
}
catch {
    $operationError = $_
}
finally {
    if ($backendCreated) {
        if ([string]::IsNullOrWhiteSpace($logs)) {
            $logs = ((& docker compose logs --no-color backend 2>&1) | ForEach-Object { [string]$_ }) -join "`n"
        }
        New-Item -ItemType Directory -Force -Path $logDirectory | Out-Null
        Write-Utf8Text -LiteralPath $logPath -Value ($logs + [Environment]::NewLine)
    }

    if ($fixturesCreated) {
        $cleanupSQL = @"
WITH deleted AS (
    DELETE FROM documents
    WHERE storage_path IN ('$documentStoragePath', '$embeddingStoragePath')
    RETURNING id
)
SELECT COUNT(*) FROM deleted;
"@
        $deletedCount = (& docker compose exec -T postgres psql `
            --username $databaseUser `
            --dbname $databaseName `
            --tuples-only `
            --no-align `
            --command $cleanupSQL 2>$null).Trim()
        if ($LASTEXITCODE -ne 0 -or $deletedCount -ne "2") {
            Write-Warning "could not fully remove lifecycle fixtures; marker: $marker"
        }
    }

    if ($backendCreated) {
        $stopExitCode = Invoke-BestEffortNativeCleanup -FilePath "docker" `
            -ArgumentList @("compose", "stop", "backend")
        if ($stopExitCode -ne 0) {
            Write-Warning "could not stop lifecycle verification backend container"
        }

        $removeExitCode = Invoke-BestEffortNativeCleanup -FilePath "docker" `
            -ArgumentList @("compose", "rm", "-f", "backend")
        if ($removeExitCode -ne 0) {
            Write-Warning "could not remove lifecycle verification backend container"
        }
    }

    Set-Location $originalLocation
}

if ($null -ne $operationError) {
    Write-Warning "lifecycle verification log: $logPath"
    throw $operationError
}

Write-Host "Container lifecycle verification passed."
Write-Host "Graceful exit code: $gracefulExitCode ($gracefulStopMilliseconds ms)"
Write-Host "Forced exit code: $forcedExitCode"
Write-Host "Document recovery: processing -> failed"
Write-Host "Embedding recovery: processing -> queued"
Write-Host "Result: $resultPath"
Write-Host "Log: $logPath"
Write-Output $resultPath
