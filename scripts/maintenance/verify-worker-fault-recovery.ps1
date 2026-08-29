[CmdletBinding()]
param(
    [ValidateRange(6, 30)]
    [int]$LeaseDurationSeconds = 8,
    [switch]$SkipBuild
)

# This script is intentionally ASCII-only so Windows PowerShell 5.1 parses it
# without relying on a UTF-8 BOM. Runtime evidence still goes to the existing
# chatgpt runtime-artifacts tree.

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

function Invoke-BestEffortDockerCleanup {
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

function Invoke-TestDatabaseScalar {
    param(
        [Parameter(Mandatory)]
        [string]$SQL
    )

    return Get-NativeText -FilePath "docker" -ArgumentList @(
        "compose", "exec", "-T", "postgres",
        "psql", "--username", $script:DatabaseUser,
        "--dbname", $script:TestDatabase,
        "--tuples-only", "--no-align", "--quiet",
        "--set", "ON_ERROR_STOP=1",
        "--command", $SQL
    ) -Description "execute isolated PostgreSQL query"
}

function Wait-TestDatabaseValue {
    param(
        [Parameter(Mandatory)]
        [string]$SQL,

        [Parameter(Mandatory)]
        [string]$ExpectedValue,

        [Parameter(Mandatory)]
        [string]$Description,

        [int]$MaximumAttempts = 240,
        [int]$DelayMilliseconds = 250
    )

    $lastValue = ""
    for ($attempt = 1; $attempt -le $MaximumAttempts; $attempt++) {
        $lastValue = Invoke-TestDatabaseScalar -SQL $SQL
        if ($lastValue -eq $ExpectedValue) {
            return
        }
        Start-Sleep -Milliseconds $DelayMilliseconds
    }
    throw "$Description timed out; last value: '$lastValue'"
}

function Wait-TestDatabaseNonEmpty {
    param(
        [Parameter(Mandatory)]
        [string]$SQL,

        [Parameter(Mandatory)]
        [string]$Description,

        [int]$MaximumAttempts = 240,
        [int]$DelayMilliseconds = 250
    )

    for ($attempt = 1; $attempt -le $MaximumAttempts; $attempt++) {
        $value = Invoke-TestDatabaseScalar -SQL $SQL
        if (-not [string]::IsNullOrWhiteSpace($value)) {
            return $value
        }
        Start-Sleep -Milliseconds $DelayMilliseconds
    }
    throw "$Description timed out"
}

function Wait-WorkerReady {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName,

        [int]$MaximumAttempts = 120
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
        Start-Sleep -Milliseconds 250
    }
    $timeoutLogs = & docker logs $ContainerName 2>&1
    throw "worker $ContainerName did not become ready:`n$timeoutLogs"
}

function Start-TestWorker {
    param(
        [Parameter(Mandatory)]
        [ValidateSet("document-worker", "embedding-worker", "answer-worker")]
        [string]$Role,

        [Parameter(Mandatory)]
        [string]$WorkerID
    )

    $containerName = "rag-fault-$($script:RunMarker)-$WorkerID"
    $arguments = @(
        "compose", "run", "-d", "--no-deps",
        "--name", $containerName,
        "-e", "DB_NAME=$($script:TestDatabase)",
        "-e", "DB_MAX_CONNECTIONS=4",
        "-e", "APP_READY_FILE=/tmp/rag-role-ready"
    )

    switch ($Role) {
        "document-worker" {
            $arguments += @(
                "-e", "WORKER_POLL_INTERVAL=200ms",
                "-e", "WORKER_PROCESSING_TIMEOUT=60s",
                "-e", "DOCUMENT_WORKER_CONCURRENCY=1",
                "-e", "DOCUMENT_WORKER_ID=$WorkerID",
                "-e", "DOCUMENT_JOB_LEASE_DURATION=$($script:LeaseDurationSeconds)s",
                "-e", "DOCUMENT_JOB_HEARTBEAT_INTERVAL=1s",
                "-e", "PROCESSING_MAX_IN_FLIGHT_PER_OWNER=1",
                "-e", "PROCESSING_MAX_BORROWED_IN_FLIGHT_PER_OWNER=1",
                "-e", "PYTHON_PROCESS_MODE=oneshot"
            )
        }
        "embedding-worker" {
            $arguments += @(
                "-e", "EMBEDDING_WORKER_CONCURRENCY=1",
                "-e", "EMBEDDING_WORKER_ID=$WorkerID",
                "-e", "EMBEDDING_JOB_LEASE_DURATION=$($script:LeaseDurationSeconds)s",
                "-e", "EMBEDDING_JOB_HEARTBEAT_INTERVAL=1s",
                "-e", "EMBEDDING_POLL_INTERVAL=200ms",
                "-e", "EMBEDDING_PROCESSING_TIMEOUT=60s",
                "-e", "EMBEDDING_MAX_IN_FLIGHT_PER_OWNER=1",
                "-e", "EMBEDDING_MAX_BORROWED_IN_FLIGHT_PER_OWNER=1",
                "-e", "EMBEDDING_PROVIDER_MAX_CONCURRENCY=2",
                "-e", "EMBEDDING_WORKER_PROVIDER_CONCURRENCY=1",
                "-e", "EMBEDDING_ONLINE_PROVIDER_CONCURRENCY=1",
                "-e", "EMBEDDING_PROVIDER=dashscope",
                "-e", "DASHSCOPE_API_KEY=zero-cost-fake-key",
                "-e", "DASHSCOPE_EMBEDDING_ENDPOINT=$($script:ProviderEndpoint)/v1/embeddings",
                "-e", "EMBEDDING_MODEL=fake-embedding-1536",
                "-e", "EMBEDDING_DIMENSIONS=1536",
                "-e", "EMBEDDING_BATCH_SIZE=8",
                "-e", "EMBEDDING_HTTP_TIMEOUT=60s",
                "-e", "CAPACITY_COORDINATION_ENABLED=false"
            )
        }
        "answer-worker" {
            $arguments += @(
                "-e", "ANSWER_ENABLED=true",
                "-e", "ANSWER_JOBS_ENABLED=true",
                "-e", "ANSWER_JOB_WORKER_CONCURRENCY=1",
                "-e", "ANSWER_JOB_WORKER_ID=$WorkerID",
                "-e", "ANSWER_JOB_LEASE_DURATION=$($script:LeaseDurationSeconds)s",
                "-e", "ANSWER_JOB_HEARTBEAT_INTERVAL=1s",
                "-e", "ANSWER_JOB_POLL_INTERVAL=200ms",
                "-e", "ANSWER_JOB_PROCESSING_TIMEOUT=60s",
                "-e", "ANSWER_JOB_OWNER_IN_FLIGHT_LIMIT=1",
                "-e", "ANSWER_JOB_OWNER_BORROWED_LIMIT=1",
                "-e", "ANSWER_JOB_MAX_ATTEMPTS=3",
                "-e", "ANSWER_MAX_CONCURRENCY=1",
                "-e", "ANSWER_MAX_CONCURRENCY_PER_USER=1",
                "-e", "ANSWER_MAX_WAITERS_GLOBAL=4",
                "-e", "ANSWER_MAX_WAITERS_PER_USER=2",
                "-e", "ANSWER_QUEUE_WAIT_TIMEOUT=5s",
                "-e", "DASHSCOPE_API_KEY=zero-cost-fake-key",
                "-e", "DASHSCOPE_EMBEDDING_ENDPOINT=$($script:ProviderEndpoint)/v1/embeddings",
                "-e", "DASHSCOPE_GENERATION_ENDPOINT=$($script:ProviderEndpoint)/v1/chat/completions",
                "-e", "EMBEDDING_PROVIDER=dashscope",
                "-e", "EMBEDDING_MODEL=fake-embedding-1536",
                "-e", "EMBEDDING_DIMENSIONS=1536",
                "-e", "EMBEDDING_PROVIDER_MAX_CONCURRENCY=2",
                "-e", "EMBEDDING_WORKER_PROVIDER_CONCURRENCY=1",
                "-e", "EMBEDDING_ONLINE_PROVIDER_CONCURRENCY=1",
                "-e", "EMBEDDING_HTTP_TIMEOUT=60s",
                "-e", "GENERATION_MODEL=fake-generation",
                "-e", "GENERATION_HTTP_TIMEOUT=60s",
                "-e", "GENERATION_MAX_OUTPUT_TOKENS=128",
                "-e", "GENERATION_THINKING_ENABLED=false",
                "-e", "CAPACITY_COORDINATION_ENABLED=false",
                "-e", "RAG_CACHE_ENABLED=false"
            )
        }
    }

    $arguments += $Role
    $containerID = Get-NativeText -FilePath "docker" -ArgumentList $arguments -Description "start $containerName"
    if ([string]::IsNullOrWhiteSpace($containerID)) {
        throw "docker did not return a container ID for $containerName"
    }
    [void]$script:Containers.Add($containerName)

    try {
        Wait-WorkerReady -ContainerName $containerName
    }
    catch {
        & docker rm --force $containerName 2>&1 | Out-Null
        throw
    }
    return $containerName
}

function Stop-WorkerGracefully {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    Invoke-NativeCommand -FilePath "docker" -ArgumentList @(
        "stop", "--timeout", "20", $ContainerName
    ) -Description "gracefully stop $ContainerName"

    $state = (Get-NativeText -FilePath "docker" -ArgumentList @(
        "inspect", "--format", "{{json .State}}", $ContainerName
    ) -Description "inspect stopped $ContainerName") | ConvertFrom-Json
    if ([bool]$state.Running -or [int]$state.ExitCode -ne 0 -or [bool]$state.OOMKilled) {
        throw "$ContainerName did not stop cleanly"
    }
}

function Kill-Worker {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    Invoke-NativeCommand -FilePath "docker" -ArgumentList @(
        "kill", "--signal", "KILL", $ContainerName
    ) -Description "force kill $ContainerName"

    $state = (Get-NativeText -FilePath "docker" -ArgumentList @(
        "inspect", "--format", "{{json .State}}", $ContainerName
    ) -Description "inspect killed $ContainerName") | ConvertFrom-Json
    if ([int]$state.ExitCode -ne 137 -or [bool]$state.OOMKilled) {
        throw "$ContainerName forced exit was not SIGKILL exit 137"
    }
}

function Wait-ProviderReady {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    $probe = "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:18080/health', timeout=1).read().decode())"
    for ($attempt = 1; $attempt -le 120; $attempt++) {
        $output = & docker exec $ContainerName python3 -c $probe 2>$null
        if ($LASTEXITCODE -eq 0 -and "$output".Contains('"status":"ok"')) {
            return
        }
        Start-Sleep -Milliseconds 250
    }
    $logs = & docker logs $ContainerName 2>&1
    throw "fake Provider did not become ready:`n$logs"
}

function Get-ProviderStats {
    $probe = "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:18080/stats', timeout=1).read().decode())"
    $json = Get-NativeText -FilePath "docker" -ArgumentList @(
        "exec", $script:ProviderContainer, "python3", "-c", $probe
    ) -Description "read fake Provider stats"
    return $json | ConvertFrom-Json
}

function Wait-ProviderCount {
    param(
        [Parameter(Mandatory)]
        [ValidateSet("embedding", "generation")]
        [string]$Kind,

        [Parameter(Mandatory)]
        [int]$ExpectedCount
    )

    $lastCount = 0
    for ($attempt = 1; $attempt -le 240; $attempt++) {
        $stats = Get-ProviderStats
        $lastCount = [int]$stats.$Kind
        if ($lastCount -ge $ExpectedCount) {
            return
        }
        Start-Sleep -Milliseconds 250
    }
    throw "fake Provider $Kind count is $lastCount, want at least $ExpectedCount"
}

function Start-TableLock {
    param(
        [Parameter(Mandatory)]
        [string]$TableName
    )

    if ($TableName -notmatch '^[a-z_]+$') {
        throw "unsafe table name"
    }
    $containerName = "rag-fault-$($script:RunMarker)-lock-$TableName"
    $sql = "BEGIN; LOCK TABLE $TableName IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(120);"
    $containerID = Get-NativeText -FilePath "docker" -ArgumentList @(
        "run", "-d", "--name", $containerName,
        "--network", $script:NetworkName,
        "-e", "PGPASSWORD=$($script:DatabasePassword)",
        $script:PostgresImage,
        "psql", "--host", "postgres",
        "--username", $script:DatabaseUser,
        "--dbname", $script:TestDatabase,
        "--set", "ON_ERROR_STOP=1",
        "--command", $sql
    ) -Description "lock $TableName"
    if ([string]::IsNullOrWhiteSpace($containerID)) {
        throw "docker did not return the $TableName lock container ID"
    }
    [void]$script:Containers.Add($containerName)

    $lockSQL = "SELECT CASE WHEN EXISTS (SELECT 1 FROM pg_locks AS lock JOIN pg_class AS relation ON relation.oid = lock.relation WHERE relation.relname = '$TableName' AND lock.mode = 'AccessExclusiveLock' AND lock.granted) THEN 't' ELSE 'f' END;"
    Wait-TestDatabaseValue -SQL $lockSQL -ExpectedValue "t" -Description "wait for $TableName lock"
    $backendPID = Invoke-TestDatabaseScalar -SQL "SELECT pid::TEXT FROM pg_stat_activity WHERE datname = current_database() AND query LIKE 'BEGIN; LOCK TABLE $TableName IN ACCESS EXCLUSIVE MODE;%';"
    if ([string]::IsNullOrWhiteSpace($backendPID)) {
        throw "could not find the PostgreSQL backend holding $TableName"
    }
    $script:LockBackendPIDs[$containerName] = [int]$backendPID
    return $containerName
}

function Release-TableLock {
    param(
        [Parameter(Mandatory)]
        [string]$ContainerName
    )

    if (-not $script:LockBackendPIDs.ContainsKey($ContainerName)) {
        throw "PostgreSQL lock backend was not recorded for $ContainerName"
    }
    $backendPID = [int]$script:LockBackendPIDs[$ContainerName]
    $terminated = Invoke-TestDatabaseScalar -SQL "SELECT CASE WHEN pg_terminate_backend($backendPID) THEN 't' ELSE 'f' END;"
    if ($terminated -ne "t") {
        throw "could not terminate PostgreSQL lock backend $backendPID"
    }
    Invoke-NativeCommand -FilePath "docker" -ArgumentList @(
        "rm", "--force", $ContainerName
    ) -Description "remove the released table lock container"
}

function Assert-DistinctLeaseTokens {
    param(
        [Parameter(Mandatory)]
        [string]$TableName,

        [Parameter(Mandatory)]
        [int64]$JobID
    )

    $value = Invoke-TestDatabaseScalar -SQL "SELECT COUNT(*)::TEXT || '|' || COUNT(DISTINCT lease_token)::TEXT FROM fault_lease_audit WHERE table_name = '$TableName' AND job_id = $JobID;"
    if ($value -ne "2|2") {
        throw "$TableName job $JobID lease audit is '$value', want two distinct tokens"
    }
}

function Assert-ReplacementDidNotStealEarly {
    param(
        [Parameter(Mandatory)]
        [string]$TableName,

        [Parameter(Mandatory)]
        [int64]$JobID,

        [Parameter(Mandatory)]
        [string]$OriginalWorkerID
    )

    Start-Sleep -Milliseconds 500
    $value = Invoke-TestDatabaseScalar -SQL "SELECT COALESCE(worker_id, '') || '|' || attempt_count::TEXT FROM $TableName WHERE id = $JobID AND status = 'processing';"
    if ($value -ne "$OriginalWorkerID|1") {
        throw "$TableName job $JobID was replaced before the original lease expired: '$value'"
    }
}

$scriptDirectory = Split-Path -Parent $PSCommandPath
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory "..\.."))
$originalLocation = Get-Location
$script:RunMarker = [Guid]::NewGuid().ToString("N").Substring(0, 10)
$script:TestDatabase = "rag_fault_$($script:RunMarker)"
$script:LeaseDurationSeconds = $LeaseDurationSeconds
$script:DatabaseUser = ""
$script:DatabasePassword = ""
$script:NetworkName = ""
$script:PostgresImage = ""
$script:ProviderContainer = "rag-fault-$($script:RunMarker)-provider"
$script:ProviderEndpoint = "http://$($script:ProviderContainer):18080"
$script:Containers = [System.Collections.Generic.List[string]]::new()
$script:LockBackendPIDs = @{}
$databaseCreated = $false
$operationError = $null
$testedAt = [DateTime]::UtcNow
$previousStorageHostPath = [Environment]::GetEnvironmentVariable("STORAGE_HOST_PATH", "Process")
$runtimeArtifactsDirectoryName = -join ([char[]](0x8FD0, 0x884C, 0x4EA7, 0x7269))
$temporaryDirectoryName = -join ([char[]](0x4E34, 0x65F6))
$runtimeTemporaryRoot = Join-Path (Join-Path (Join-Path $projectRoot "chatgpt") $runtimeArtifactsDirectoryName) $temporaryDirectoryName
$storageRoot = [System.IO.Path]::GetFullPath((Join-Path $runtimeTemporaryRoot "worker-fault-$($script:RunMarker)"))
$reportPath = Join-Path $runtimeTemporaryRoot "worker-fault-recovery-$($testedAt.ToString('yyyyMMddTHHmmssZ')).json"
$documentResult = $null
$embeddingResult = $null
$answerResult = $null

try {
    Set-Location $projectRoot
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw "required command 'docker' was not found"
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $storageRoot "documents") | Out-Null
    $documentFile = Join-Path $storageRoot "documents\fault-recovery.md"
    $documentContent = "# Fault recovery`n`nThis document proves lease recovery and fencing.`n"
    [System.IO.File]::WriteAllText(
        $documentFile,
        $documentContent,
        (New-Object System.Text.UTF8Encoding($false))
    )
    $env:STORAGE_HOST_PATH = $storageRoot

    Invoke-NativeCommand -FilePath "docker" -ArgumentList @(
        "compose", "--profile", "embedding", "--profile", "answer", "config", "--quiet"
    ) -Description "validate all Compose profiles"

    $postgresContainerID = Get-NativeText -FilePath "docker" -ArgumentList @(
        "compose", "ps", "--status", "running", "--quiet", "postgres"
    ) -Description "find running PostgreSQL"
    if ([string]::IsNullOrWhiteSpace($postgresContainerID)) {
        throw "PostgreSQL must already be running"
    }
    $postgresHealth = Get-NativeText -FilePath "docker" -ArgumentList @(
        "inspect", "--format", "{{.State.Health.Status}}", $postgresContainerID
    ) -Description "inspect PostgreSQL health"
    if ($postgresHealth -ne "healthy") {
        throw "PostgreSQL must be healthy; current status: $postgresHealth"
    }

    $script:DatabaseUser = Get-NativeText -FilePath "docker" -ArgumentList @(
        "compose", "exec", "-T", "postgres", "printenv", "POSTGRES_USER"
    ) -Description "read PostgreSQL user"
    $script:DatabasePassword = Get-NativeText -FilePath "docker" -ArgumentList @(
        "compose", "exec", "-T", "postgres", "printenv", "POSTGRES_PASSWORD"
    ) -Description "read PostgreSQL password"
    $script:PostgresImage = Get-NativeText -FilePath "docker" -ArgumentList @(
        "inspect", "--format", "{{.Config.Image}}", $postgresContainerID
    ) -Description "read PostgreSQL image"
    $networkJSON = Get-NativeText -FilePath "docker" -ArgumentList @(
        "inspect", "--format", "{{json .NetworkSettings.Networks}}", $postgresContainerID
    ) -Description "read PostgreSQL network"
    $networkObject = $networkJSON | ConvertFrom-Json
    $networkNames = @($networkObject.PSObject.Properties.Name)
    if ($networkNames.Count -ne 1) {
        throw "PostgreSQL must belong to exactly one test network"
    }
    $script:NetworkName = $networkNames[0]

    if (-not $SkipBuild) {
        Invoke-NativeCommand -FilePath "docker" -ArgumentList @(
            "compose", "build", "backend"
        ) -Description "build current backend image"
    }

    $createDatabaseSQL = "CREATE DATABASE `"$($script:TestDatabase)`" OWNER `"$($script:DatabaseUser)`";"
    Invoke-NativeCommand -FilePath "docker" -ArgumentList @(
        "compose", "exec", "-T", "postgres",
        "psql", "--username", $script:DatabaseUser,
        "--dbname", "postgres", "--set", "ON_ERROR_STOP=1",
        "--command", $createDatabaseSQL
    ) -Description "create isolated fault database"
    $databaseCreated = $true

    $migrationWorker = Start-TestWorker -Role "document-worker" -WorkerID "migration"
    Wait-TestDatabaseValue -SQL "SELECT MAX(version)::TEXT FROM schema_migrations;" -ExpectedValue "28" -Description "wait for schema 28"
    Stop-WorkerGracefully -ContainerName $migrationWorker
    [void](Invoke-BestEffortDockerCleanup -ArgumentList @("rm", "--force", $migrationWorker))

    $auditSQL = @'
CREATE TABLE fault_lease_audit (
    table_name TEXT NOT NULL,
    job_id BIGINT NOT NULL,
    worker_id TEXT NOT NULL,
    lease_token TEXT NOT NULL,
    captured_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE OR REPLACE FUNCTION capture_fault_lease()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.lease_token IS NOT NULL
       AND NEW.lease_token IS DISTINCT FROM OLD.lease_token THEN
        INSERT INTO fault_lease_audit (
            table_name, job_id, worker_id, lease_token
        ) VALUES (
            TG_TABLE_NAME, NEW.id, NEW.worker_id, NEW.lease_token
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER capture_document_job_lease
AFTER UPDATE OF lease_token ON document_jobs
FOR EACH ROW EXECUTE FUNCTION capture_fault_lease();

CREATE TRIGGER capture_embedding_job_lease
AFTER UPDATE OF lease_token ON embedding_jobs
FOR EACH ROW EXECUTE FUNCTION capture_fault_lease();

CREATE TRIGGER capture_answer_job_lease
AFTER UPDATE OF lease_token ON answer_jobs
FOR EACH ROW EXECUTE FUNCTION capture_fault_lease();
'@
    [void](Invoke-TestDatabaseScalar -SQL $auditSQL)

    $providerScriptPath = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory "fake-openai-provider.py"))
    $providerMount = "${providerScriptPath}:/tmp/fake-openai-provider.py:ro"
    $providerID = Get-NativeText -FilePath "docker" -ArgumentList @(
        "run", "-d", "--name", $script:ProviderContainer,
        "--network", $script:NetworkName,
        "--read-only",
        "-e", "FAKE_EMBEDDING_FIRST_DELAY_SECONDS=20",
        "-e", "FAKE_GENERATION_FIRST_DELAY_SECONDS=20",
        "-e", "FAKE_EMBEDDING_DIMENSIONS=1536",
        "--volume", $providerMount,
        "--entrypoint", "python3",
        "rag-reasoning-platform-backend:local",
        "/tmp/fake-openai-provider.py"
    ) -Description "start zero-cost fake Provider"
    if ([string]::IsNullOrWhiteSpace($providerID)) {
        throw "docker did not return the fake Provider container ID"
    }
    [void]$script:Containers.Add($script:ProviderContainer)
    Wait-ProviderReady -ContainerName $script:ProviderContainer

    $ownerID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO users (email, email_verified_at, display_name, password_hash) VALUES ('fault-$($script:RunMarker)@example.com', CURRENT_TIMESTAMP, 'Fault Recovery', 'fault-recovery-hash') RETURNING id;")

    # Document Worker: block chunk writes, kill holder A, then let B recover.
    $documentHash = (Get-FileHash -LiteralPath $documentFile -Algorithm SHA256).Hash.ToLowerInvariant()
    $documentSize = (Get-Item -LiteralPath $documentFile).Length
    $documentID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO documents (original_name, storage_path, mime_type, size_bytes, sha256, status, owner_user_id) VALUES ('fault-recovery.md', 'documents/fault-recovery.md', 'text/markdown', $documentSize, '$documentHash', 'uploaded', $ownerID) RETURNING id;")
    [void](Invoke-TestDatabaseScalar -SQL "INSERT INTO document_processing_owner_schedules (owner_user_id) VALUES ($ownerID);")
    $documentJobID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO document_jobs (document_id) VALUES ($documentID) RETURNING id;")

    $chunkLock = Start-TableLock -TableName "text_chunks"
    $documentWorkerAID = "document-a"
    $documentWorkerA = Start-TestWorker -Role "document-worker" -WorkerID $documentWorkerAID
    [void](Wait-TestDatabaseNonEmpty -SQL "SELECT worker_id || '|' || lease_token || '|' || attempt_count::TEXT FROM document_jobs WHERE id = $documentJobID AND status = 'processing' AND worker_id = '$documentWorkerAID';" -Description "wait for Document Worker A claim")
    Kill-Worker -ContainerName $documentWorkerA
    Release-TableLock -ContainerName $chunkLock
    $documentWorkerB = Start-TestWorker -Role "document-worker" -WorkerID "document-b"
    Assert-ReplacementDidNotStealEarly -TableName "document_jobs" -JobID $documentJobID -OriginalWorkerID $documentWorkerAID
    Wait-TestDatabaseValue -SQL "SELECT status FROM document_jobs WHERE id = $documentJobID;" -ExpectedValue "succeeded" -Description "wait for Document Worker B success"
    Stop-WorkerGracefully -ContainerName $documentWorkerB
    Assert-DistinctLeaseTokens -TableName "document_jobs" -JobID $documentJobID
    Wait-TestDatabaseValue -SQL "SELECT status FROM documents WHERE id = $documentID;" -ExpectedValue "ready" -Description "verify recovered document state"
    Wait-TestDatabaseValue -SQL "SELECT COUNT(*)::TEXT FROM text_chunks WHERE document_id = $documentID;" -ExpectedValue "1" -Description "verify recovered chunks"
    $documentAttempts = [int](Invoke-TestDatabaseScalar -SQL "SELECT attempt_count FROM document_jobs WHERE id = $documentJobID;")
    if ($documentAttempts -ne 2) {
        throw "Document job attempt count is $documentAttempts, want 2"
    }
    $documentResult = [ordered]@{
        job_id = $documentJobID
        first_exit_code = 137
        attempt_count = $documentAttempts
        distinct_lease_tokens = 2
        status = "succeeded"
        chunk_count = 1
    }

    # Embedding Worker: delay only the first local Provider request.
    $embeddingDocumentID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO documents (original_name, storage_path, mime_type, size_bytes, sha256, status, owner_user_id) VALUES ('embedding-source.md', 'documents/embedding-source.md', 'text/markdown', 10, repeat('e', 64), 'ready', $ownerID) RETURNING id;")
    $embeddingChunkID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO text_chunks (document_id, chunk_index, content) VALUES ($embeddingDocumentID, 0, 'fault recovery semantic evidence') RETURNING id;")
    [void](Invoke-TestDatabaseScalar -SQL "INSERT INTO embedding_owner_schedules (owner_user_id) VALUES ($ownerID);")
    $embeddingJobID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO embedding_jobs (document_id, model_name, dimensions) VALUES ($embeddingDocumentID, 'fake-embedding-1536', 1536) RETURNING id;")

    $embeddingWorkerAID = "embedding-a"
    $embeddingWorkerA = Start-TestWorker -Role "embedding-worker" -WorkerID $embeddingWorkerAID
    [void](Wait-TestDatabaseNonEmpty -SQL "SELECT worker_id || '|' || lease_token || '|' || attempt_count::TEXT FROM embedding_jobs WHERE id = $embeddingJobID AND status = 'processing' AND worker_id = '$embeddingWorkerAID';" -Description "wait for Embedding Worker A claim")
    Wait-ProviderCount -Kind "embedding" -ExpectedCount 1
    Kill-Worker -ContainerName $embeddingWorkerA
    $embeddingWorkerB = Start-TestWorker -Role "embedding-worker" -WorkerID "embedding-b"
    Assert-ReplacementDidNotStealEarly -TableName "embedding_jobs" -JobID $embeddingJobID -OriginalWorkerID $embeddingWorkerAID
    Wait-TestDatabaseValue -SQL "SELECT status FROM embedding_jobs WHERE id = $embeddingJobID;" -ExpectedValue "succeeded" -Description "wait for Embedding Worker B success"
    Wait-ProviderCount -Kind "embedding" -ExpectedCount 2
    Stop-WorkerGracefully -ContainerName $embeddingWorkerB
    Assert-DistinctLeaseTokens -TableName "embedding_jobs" -JobID $embeddingJobID
    Wait-TestDatabaseValue -SQL "SELECT COUNT(*)::TEXT FROM chunk_embeddings WHERE chunk_id = $embeddingChunkID AND embedding_job_id = $embeddingJobID;" -ExpectedValue "1" -Description "verify recovered vector"
    $embeddingAttempts = [int](Invoke-TestDatabaseScalar -SQL "SELECT attempt_count FROM embedding_jobs WHERE id = $embeddingJobID;")
    if ($embeddingAttempts -ne 2) {
        throw "Embedding job attempt count is $embeddingAttempts, want 2"
    }
    $embeddingResult = [ordered]@{
        job_id = $embeddingJobID
        first_exit_code = 137
        attempt_count = $embeddingAttempts
        distinct_lease_tokens = 2
        provider_requests = 2
        status = "succeeded"
        vector_count = 1
    }

    # Answer Worker: reuse the recovered vector and delay the first generation.
    [void](Invoke-TestDatabaseScalar -SQL "INSERT INTO answer_owner_schedules (owner_user_id) VALUES ($ownerID);")
    $answerJobID = [int64](Invoke-TestDatabaseScalar -SQL "INSERT INTO answer_jobs (owner_user_id, document_id, query, top_k, requested_response_language) VALUES ($ownerID, $embeddingDocumentID, 'What proves recovery?', 1, 'en') RETURNING id;")

    $answerWorkerAID = "answer-a"
    $answerWorkerA = Start-TestWorker -Role "answer-worker" -WorkerID $answerWorkerAID
    [void](Wait-TestDatabaseNonEmpty -SQL "SELECT worker_id || '|' || lease_token || '|' || attempt_count::TEXT FROM answer_jobs WHERE id = $answerJobID AND status = 'processing' AND worker_id = '$answerWorkerAID';" -Description "wait for Answer Worker A claim")
    Wait-ProviderCount -Kind "generation" -ExpectedCount 1
    Kill-Worker -ContainerName $answerWorkerA
    $answerWorkerB = Start-TestWorker -Role "answer-worker" -WorkerID "answer-b"
    Assert-ReplacementDidNotStealEarly -TableName "answer_jobs" -JobID $answerJobID -OriginalWorkerID $answerWorkerAID
    Wait-TestDatabaseValue -SQL "SELECT status FROM answer_jobs WHERE id = $answerJobID;" -ExpectedValue "succeeded" -Description "wait for Answer Worker B success"
    Wait-ProviderCount -Kind "generation" -ExpectedCount 2
    Stop-WorkerGracefully -ContainerName $answerWorkerB
    Assert-DistinctLeaseTokens -TableName "answer_jobs" -JobID $answerJobID
    Wait-TestDatabaseValue -SQL "SELECT CASE WHEN answer_text = 'fault recovery answer [1]' AND jsonb_array_length(sources) = 1 THEN 't' ELSE 'f' END FROM answer_jobs WHERE id = $answerJobID;" -ExpectedValue "t" -Description "verify recovered answer snapshot"
    $answerAttempts = [int](Invoke-TestDatabaseScalar -SQL "SELECT attempt_count FROM answer_jobs WHERE id = $answerJobID;")
    if ($answerAttempts -ne 2) {
        throw "Answer job attempt count is $answerAttempts, want 2"
    }
    $answerResult = [ordered]@{
        job_id = $answerJobID
        first_exit_code = 137
        attempt_count = $answerAttempts
        distinct_lease_tokens = 2
        generation_requests = 2
        status = "succeeded"
        source_count = 1
    }

    $providerStats = Get-ProviderStats
    if ([int]$providerStats.embedding -ne 4 -or [int]$providerStats.generation -ne 2) {
        throw "Fake Provider request counts are embedding=$($providerStats.embedding), generation=$($providerStats.generation); want 4 and 2"
    }
    $worktreeStatus = Get-NativeText -FilePath "git" -ArgumentList @("status", "--porcelain") -Description "read git worktree status"
    $result = [ordered]@{
        status = "passed"
        tested_at_utc = $testedAt.ToString("o")
        git_revision = Get-NativeText -FilePath "git" -ArgumentList @("rev-parse", "HEAD") -Description "read git revision"
        worktree_dirty = -not [string]::IsNullOrWhiteSpace($worktreeStatus)
        verification_script_sha256 = (Get-FileHash -LiteralPath $PSCommandPath -Algorithm SHA256).Hash.ToLowerInvariant()
        fake_provider_script_sha256 = (Get-FileHash -LiteralPath $providerScriptPath -Algorithm SHA256).Hash.ToLowerInvariant()
        schema_version = 28
        lease_duration_seconds = $LeaseDurationSeconds
        remote_provider_calls = 0
        local_fake_provider = [ordered]@{
            embedding_requests = [int]$providerStats.embedding
            generation_requests = [int]$providerStats.generation
        }
        document = $documentResult
        embedding = $embeddingResult
        answer = $answerResult
    }
    New-Item -ItemType Directory -Force -Path $runtimeTemporaryRoot | Out-Null
    $resultJSON = $result | ConvertTo-Json -Depth 10
    [System.IO.File]::WriteAllText(
        $reportPath,
        $resultJSON + [Environment]::NewLine,
        (New-Object System.Text.UTF8Encoding($false))
    )
}
catch {
    $operationError = $_
}
finally {
    foreach ($containerName in $script:Containers) {
        [void](Invoke-BestEffortDockerCleanup -ArgumentList @("rm", "--force", $containerName))
    }

    if ($databaseCreated -and -not [string]::IsNullOrWhiteSpace($script:DatabaseUser)) {
        $dropDatabaseSQL = "DROP DATABASE IF EXISTS `"$($script:TestDatabase)`" WITH (FORCE);"
        [void](Invoke-BestEffortDockerCleanup -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $script:DatabaseUser,
            "--dbname", "postgres", "--command", $dropDatabaseSQL
        ))
    }

    if ($null -eq $previousStorageHostPath) {
        Remove-Item Env:STORAGE_HOST_PATH -ErrorAction SilentlyContinue
    }
    else {
        $env:STORAGE_HOST_PATH = $previousStorageHostPath
    }

    $safeRuntimeRoot = [System.IO.Path]::GetFullPath($runtimeTemporaryRoot)
    $safeStorageRoot = [System.IO.Path]::GetFullPath($storageRoot)
    if (Test-Path -LiteralPath $safeStorageRoot) {
        if (-not $safeStorageRoot.StartsWith(
            $safeRuntimeRoot + [System.IO.Path]::DirectorySeparatorChar,
            [System.StringComparison]::OrdinalIgnoreCase
        )) {
            throw "refusing to remove a storage path outside runtime temp"
        }
        Remove-Item -LiteralPath $safeStorageRoot -Recurse -Force
    }
    Set-Location $originalLocation
}

if ($null -ne $operationError) {
    throw $operationError
}

Write-Host "Worker fault recovery verification passed."
Write-Host "Document: SIGKILL -> lease expiry -> succeeded on attempt 2"
Write-Host "Embedding: SIGKILL -> lease expiry -> succeeded on attempt 2"
Write-Host "Answer: SIGKILL -> lease expiry -> succeeded on attempt 2"
Write-Host "Remote Provider calls: 0"
Write-Host "Report: $reportPath"
Write-Output $reportPath
