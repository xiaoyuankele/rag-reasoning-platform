[CmdletBinding()]
param(
    [ValidateRange(2, 4)]
    [int]$ReplicasPerRole = 2,
    [switch]$SkipBuild
)

# ReplicasPerRole controls how many independent container processes are started
# for each Worker role. SkipBuild reuses the image already built by Compose.

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

function Invoke-BestEffortCleanup {
    param(
        [Parameter(Mandatory)]
        [string[]]$ArgumentList
    )

    $previousErrorActionPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        & docker @ArgumentList 2>&1 | Out-Null
        return $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorActionPreference
    }
}

function Wait-WorkerReady {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName,

        [int]$MaximumAttempts = 60
    )

    for ($attempt = 1; $attempt -le $MaximumAttempts; $attempt++) {
        $status = & docker inspect --format "{{.State.Status}}" $ContainerName 2>$null
        if ($LASTEXITCODE -eq 0 -and "$status".Trim() -eq "running") {
            & docker exec $ContainerName test -s /tmp/rag-role-ready 2>$null
            if ($LASTEXITCODE -eq 0) {
                return
            }
        }
        elseif ($LASTEXITCODE -eq 0 -and "$status".Trim() -eq "exited") {
            $logs = & docker logs $ContainerName 2>&1
            throw "worker $ContainerName exited before readiness:`n$logs"
        }

        Start-Sleep -Seconds 1
    }

    $timeoutLogs = & docker logs $ContainerName 2>&1
    throw "worker $ContainerName did not become ready:`n$timeoutLogs"
}

function Start-IsolatedWorker {
    param(
        [Parameter(Mandatory)]
        [string]$Role,

        [Parameter(Mandatory)]
        [int]$Replica,

        [Parameter(Mandatory)]
        [string]$DatabaseName,

        [Parameter(Mandatory)]
        [string]$RunMarker
    )

    $containerName = "rag-role-$RunMarker-$Role-$Replica"
    $arguments = @(
        "compose", "run", "-d", "--no-deps",
        "--name", $containerName,
        "-e", "DB_NAME=$DatabaseName",
        "-e", "DB_MAX_CONNECTIONS=2",
        "-e", "APP_READY_FILE=/tmp/rag-role-ready"
    )

    switch ($Role) {
        "document-worker" {
            $arguments += @(
                "-e", "DOCUMENT_WORKER_CONCURRENCY=1",
                "-e", "DOCUMENT_WORKER_ID=$containerName",
                "-e", "PYTHON_PROCESS_MODE=oneshot"
            )
        }
        "embedding-worker" {
            $arguments += @(
                "-e", "EMBEDDING_WORKER_CONCURRENCY=1",
                "-e", "EMBEDDING_WORKER_ID=$containerName",
                "-e", "DASHSCOPE_API_KEY=zero-cost-invalid-key",
                "-e", "DASHSCOPE_EMBEDDING_ENDPOINT=http://127.0.0.1:1/provider-must-not-be-called",
                "-e", "CAPACITY_COORDINATION_ENABLED=false"
            )
        }
        "answer-worker" {
            $arguments += @(
                "-e", "ANSWER_JOB_WORKER_CONCURRENCY=1",
                "-e", "ANSWER_JOB_OWNER_IN_FLIGHT_LIMIT=1",
                "-e", "ANSWER_JOB_OWNER_BORROWED_LIMIT=1",
                "-e", "ANSWER_JOB_WORKER_ID=$containerName",
                "-e", "DASHSCOPE_API_KEY=zero-cost-invalid-key",
                "-e", "DASHSCOPE_EMBEDDING_ENDPOINT=http://127.0.0.1:1/provider-must-not-be-called",
                "-e", "DASHSCOPE_GENERATION_ENDPOINT=http://127.0.0.1:1/provider-must-not-be-called",
                "-e", "CAPACITY_COORDINATION_ENABLED=false",
                "-e", "RAG_CACHE_ENABLED=false"
            )
        }
        default {
            throw "unsupported worker role: $Role"
        }
    }

    $arguments += $Role
    $containerID = Get-NativeText -FilePath "docker" `
        -ArgumentList $arguments `
        -Description "start $containerName"
    if ([string]::IsNullOrWhiteSpace($containerID)) {
        throw "docker did not return a container ID for $containerName"
    }

    try {
        Wait-WorkerReady -ContainerName $containerName
    }
    catch {
        $readinessError = $_
        & docker rm --force $containerName 2>&1 | Out-Null
        throw $readinessError
    }
    return [ordered]@{
        name = $containerName
        id   = $containerID
        role = $Role
    }
}

function Stop-WorkerGracefully {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("stop", "--timeout", "30", $ContainerName) `
        -Description "gracefully stop $ContainerName"

    $stateJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @("inspect", "--format", "{{json .State}}", $ContainerName) `
        -Description "inspect stopped $ContainerName"
    $state = $stateJSON | ConvertFrom-Json
    if ([bool]$state.Running -or [int]$state.ExitCode -ne 0 -or [bool]$state.OOMKilled) {
        throw "$ContainerName did not stop cleanly: exit=$($state.ExitCode), oom=$($state.OOMKilled)"
    }
}

$scriptDirectory = Split-Path -Parent $PSCommandPath
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory "..\.."))
$originalLocation = Get-Location
$runMarker = [Guid]::NewGuid().ToString("N").Substring(0, 10)
$testDatabase = "rag_role_$runMarker"
$containers = [System.Collections.Generic.List[object]]::new()
$databaseCreated = $false
$operationError = $null
$testedAt = [DateTime]::UtcNow
# Keep the source ASCII-compatible for Windows PowerShell 5.1 while still
# writing runtime evidence into the existing chatgpt/runtime-artifacts/temp tree.
$runtimeArtifactsDirectoryName = -join ([char[]](0x8FD0, 0x884C, 0x4EA7, 0x7269))
$temporaryDirectoryName = -join ([char[]](0x4E34, 0x65F6))
$reportDirectory = Join-Path (Join-Path (Join-Path $projectRoot "chatgpt") $runtimeArtifactsDirectoryName) $temporaryDirectoryName
$reportPath = Join-Path $reportDirectory "worker-multiprocess-$($testedAt.ToString('yyyyMMddTHHmmssZ')).json"
$databaseUser = $null

try {
    Set-Location $projectRoot

    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw "required command 'docker' was not found"
    }

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "--profile", "embedding", "--profile", "answer", "config", "--quiet") `
        -Description "validate all Compose profiles"

    $postgresContainerID = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "ps", "--status", "running", "--quiet", "postgres") `
        -Description "find running PostgreSQL"
    if ([string]::IsNullOrWhiteSpace($postgresContainerID)) {
        throw "PostgreSQL must already be running"
    }
    $postgresHealth = Get-NativeText -FilePath "docker" `
        -ArgumentList @("inspect", "--format", "{{.State.Health.Status}}", $postgresContainerID) `
        -Description "inspect PostgreSQL health"
    if ($postgresHealth -ne "healthy") {
        throw "PostgreSQL must be healthy; current status: $postgresHealth"
    }

    $databaseUser = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_USER") `
        -Description "read PostgreSQL user"

    if (-not $SkipBuild) {
        Invoke-NativeCommand -FilePath "docker" `
            -ArgumentList @("compose", "build", "backend") `
            -Description "build current backend image"
    }

    $createDatabaseSQL = "CREATE DATABASE `"$testDatabase`" OWNER `"$databaseUser`";"
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", "postgres",
            "--set", "ON_ERROR_STOP=1", "--command", $createDatabaseSQL
        ) `
        -Description "create isolated role database"
    $databaseCreated = $true

    # Start one document worker first as the single migration runner. Other
    # processes join only after schema version 28 is ready.
    $firstWorker = Start-IsolatedWorker -Role "document-worker" -Replica 1 -DatabaseName $testDatabase -RunMarker $runMarker
    [void]$containers.Add($firstWorker)

    for ($replica = 2; $replica -le $ReplicasPerRole; $replica++) {
        $worker = Start-IsolatedWorker -Role "document-worker" -Replica $replica -DatabaseName $testDatabase -RunMarker $runMarker
        [void]$containers.Add($worker)
    }
    foreach ($role in @("embedding-worker", "answer-worker")) {
        for ($replica = 1; $replica -le $ReplicasPerRole; $replica++) {
            $worker = Start-IsolatedWorker -Role $role -Replica $replica -DatabaseName $testDatabase -RunMarker $runMarker
            [void]$containers.Add($worker)
        }
    }

    $schemaVersion = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", $testDatabase,
            "--tuples-only", "--no-align",
            "--command", "SELECT MAX(version) FROM schema_migrations;"
        ) `
        -Description "read isolated schema version"
    if ($schemaVersion -ne "28") {
        throw "isolated schema version is $schemaVersion, want 28"
    }

    foreach ($container in $containers) {
        $actualRole = Get-NativeText -FilePath "docker" `
            -ArgumentList @("exec", $container.name, "printenv", "APP_ROLE") `
            -Description "read role from $($container.name)"
        if ($actualRole -ne $container.role) {
            throw "$($container.name) role is $actualRole, want $($container.role)"
        }

        $logs = Get-NativeText -FilePath "docker" `
            -ArgumentList @("logs", $container.name) `
            -Description "read logs from $($container.name)"
        if ($logs.Contains('"event":"embedding_provider_request_started"') -or
            $logs.Contains('"event":"generation_request_started"')) {
            throw "$($container.name) unexpectedly attempted a remote Provider call"
        }
    }

    foreach ($container in $containers) {
        Stop-WorkerGracefully -ContainerName $container.name
    }

    $result = [ordered]@{
        status              = "passed"
        tested_at_utc       = $testedAt.ToString("o")
        git_revision        = Get-NativeText -FilePath "git" -ArgumentList @("rev-parse", "HEAD") -Description "read git revision"
        replicas_per_role   = $ReplicasPerRole
        total_processes     = $containers.Count
        schema_version      = [int]$schemaVersion
        provider_calls      = 0
        isolated_database   = $testDatabase
        containers          = $containers
    }

    New-Item -ItemType Directory -Force -Path $reportDirectory | Out-Null
    $result | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $reportPath -Encoding UTF8
    Write-Host "Multi-process worker verification passed."
    Write-Host "Report: $reportPath"
}
catch {
    $operationError = $_
    throw
}
finally {
    foreach ($container in $containers) {
        [void](Invoke-BestEffortCleanup -ArgumentList @("rm", "--force", $container.name))
    }

    if ($databaseCreated -and -not [string]::IsNullOrWhiteSpace($databaseUser)) {
        $dropDatabaseSQL = "DROP DATABASE IF EXISTS `"$testDatabase`" WITH (FORCE);"
        [void](Invoke-BestEffortCleanup -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", "postgres",
            "--command", $dropDatabaseSQL
        ))
    }

    Set-Location $originalLocation
    if ($null -ne $operationError) {
        Write-Host "Multi-process verification failed: $($operationError.Exception.Message)"
    }
}
