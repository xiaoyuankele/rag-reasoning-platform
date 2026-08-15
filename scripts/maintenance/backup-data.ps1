[CmdletBinding()]
param(
    # 备份产物默认进入已被 Git 忽略的本机运行产物目录。
    # 传入相对路径时，始终相对于项目根目录解析。
    [string]$OutputRoot = "",

    # 默认会在备份结束后恢复原本正在运行的 backend 容器。
    # 维护窗口需要继续停机时才显式使用该开关。
    [switch]$KeepBackendStopped
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# Invoke-NativeCommand 统一检查外部程序退出码。
# PowerShell 只会自动处理 PowerShell 异常，不会因为 docker/tar 返回非零就主动停止。
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

# Get-NativeText 用于取得 docker/git 等命令的文本结果，同时保留退出码检查。
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

function Get-AbsoluteProjectPath {
    param(
        [Parameter(Mandatory)]
        [string]$Path,

        [Parameter(Mandatory)]
        [string]$ProjectRoot
    )

    if ([System.IO.Path]::IsPathRooted($Path)) {
        return [System.IO.Path]::GetFullPath($Path)
    }

    return [System.IO.Path]::GetFullPath((Join-Path $ProjectRoot $Path))
}

function Write-Utf8Json {
    param(
        [Parameter(Mandatory)]
        [object]$Value,

        [Parameter(Mandatory)]
        [string]$LiteralPath
    )

    $json = $Value | ConvertTo-Json -Depth 8
    $utf8WithoutBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($LiteralPath, $json + [Environment]::NewLine, $utf8WithoutBom)
}

$scriptDirectory = Split-Path -Parent $PSCommandPath
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory "..\.."))
$originalLocation = Get-Location

if ([string]::IsNullOrWhiteSpace($OutputRoot)) {
    $OutputRoot = "chatgpt\运行产物\备份\backend-data"
}

$outputRootPath = Get-AbsoluteProjectPath -Path $OutputRoot -ProjectRoot $projectRoot
$storagePath = Join-Path $projectRoot "storage"
$storagePrefix = $storagePath.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar

if ($outputRootPath -eq $storagePath -or
    $outputRootPath.StartsWith($storagePrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "OutputRoot must not be inside storage, otherwise the archive could include itself"
}

$backendWasRunning = $false
$backendStoppedByScript = $false
$containerDumpPath = ""
$postgresContainerID = ""
$operationError = $null
$restartError = $null
$backupDirectory = ""

try {
    Set-Location $projectRoot

    foreach ($requiredCommand in @("docker", "tar", "git")) {
        if (-not (Get-Command $requiredCommand -ErrorAction SilentlyContinue)) {
            throw "required command '$requiredCommand' was not found"
        }
    }

    if (-not (Test-Path -LiteralPath $storagePath -PathType Container)) {
        throw "storage directory does not exist: $storagePath"
    }

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("compose", "config", "--quiet") `
        -Description "validate Compose configuration"

    $postgresContainerID = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "ps", "--status", "running", "--quiet", "postgres") `
        -Description "find the running PostgreSQL container"

    if ([string]::IsNullOrWhiteSpace($postgresContainerID)) {
        throw "PostgreSQL is not running; start it with 'docker compose up -d postgres'"
    }

    $postgresHealth = Get-NativeText -FilePath "docker" `
        -ArgumentList @("inspect", "--format", "{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}", $postgresContainerID) `
        -Description "inspect PostgreSQL health"

    if ($postgresHealth -ne "healthy") {
        throw "PostgreSQL must be healthy before backup; current status: $postgresHealth"
    }

    $runningBackendID = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "ps", "--status", "running", "--quiet", "backend") `
        -Description "inspect backend state"
    $backendWasRunning = -not [string]::IsNullOrWhiteSpace($runningBackendID)

    if ($backendWasRunning) {
        Write-Host "Stopping backend to freeze database/file writes..."
        Invoke-NativeCommand -FilePath "docker" `
            -ArgumentList @("compose", "stop", "backend") `
            -Description "stop backend"
        $backendStoppedByScript = $true
    }

    $databaseName = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_DB") `
        -Description "read PostgreSQL database name"
    $databaseUser = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_USER") `
        -Description "read PostgreSQL user"

    if ([string]::IsNullOrWhiteSpace($databaseName) -or [string]::IsNullOrWhiteSpace($databaseUser)) {
        throw "POSTGRES_DB and POSTGRES_USER must be available inside the container"
    }

    New-Item -ItemType Directory -Force -Path $outputRootPath | Out-Null

    $createdAt = [DateTime]::UtcNow
    $backupID = "rag-platform-{0}" -f $createdAt.ToString("yyyyMMddTHHmmssfffZ")
    $backupDirectory = Join-Path $outputRootPath $backupID
    if (Test-Path -LiteralPath $backupDirectory) {
        throw "backup directory already exists: $backupDirectory"
    }
    New-Item -ItemType Directory -Path $backupDirectory | Out-Null

    # 数据库转储先写入容器 /tmp，再通过 docker cp 复制出来。
    # 这样不会让旧版 Windows PowerShell 的二进制重定向损坏 custom-format dump。
    $containerDumpPath = "/tmp/rag-backup-$([Guid]::NewGuid().ToString('N')).dump"
    $databaseDumpPath = Join-Path $backupDirectory "database.dump"
    $storageArchivePath = Join-Path $backupDirectory "storage.tar.gz"
    $manifestPath = Join-Path $backupDirectory "manifest.json"

    Write-Host "Creating PostgreSQL logical backup..."
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "pg_dump",
            "--username", $databaseUser,
            "--dbname", $databaseName,
            "--format", "custom",
            "--compress", "6",
            "--no-owner",
            "--no-privileges",
            "--file", $containerDumpPath
        ) `
        -Description "create PostgreSQL dump"

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("cp", "${postgresContainerID}:$containerDumpPath", $databaseDumpPath) `
        -Description "copy PostgreSQL dump to the host"

    $null = Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "pg_restore", "--list", $containerDumpPath
        ) `
        -Description "validate PostgreSQL dump"

    $tableCountSQL = @"
SELECT json_build_object(
    'documents', (SELECT COUNT(*) FROM documents),
    'document_jobs', (SELECT COUNT(*) FROM document_jobs),
    'text_chunks', (SELECT COUNT(*) FROM text_chunks),
    'embedding_jobs', (SELECT COUNT(*) FROM embedding_jobs),
    'chunk_embeddings', (SELECT COUNT(*) FROM chunk_embeddings)
)::text;
"@
    $tableCountsJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql",
            "--username", $databaseUser,
            "--dbname", $databaseName,
            "--tuples-only",
            "--no-align",
            "--command", $tableCountSQL
        ) `
        -Description "read source table counts"
    $tableCounts = $tableCountsJSON | ConvertFrom-Json

    Write-Host "Archiving storage directory..."
    Invoke-NativeCommand -FilePath "tar" `
        -ArgumentList @("-czf", $storageArchivePath, "-C", $projectRoot, "storage") `
        -Description "archive storage directory"
    $null = Invoke-NativeCommand -FilePath "tar" `
        -ArgumentList @("-tzf", $storageArchivePath) `
        -Description "validate storage archive"

    $storageFiles = @(Get-ChildItem -LiteralPath $storagePath -File -Recurse -Force)
    $storageBytesMeasurement = $storageFiles | Measure-Object -Property Length -Sum
    $storageBytes = if ($null -eq $storageBytesMeasurement.Sum) { 0L } else { [int64]$storageBytesMeasurement.Sum }

    $databaseDump = Get-Item -LiteralPath $databaseDumpPath
    $storageArchive = Get-Item -LiteralPath $storageArchivePath
    $gitCommit = Get-NativeText -FilePath "git" `
        -ArgumentList @("rev-parse", "HEAD") `
        -Description "read Git commit"

    # manifest.json 最后写入。没有 manifest 的目录一律视为未完成备份。
    $manifest = [ordered]@{
        format_version = 1
        status = "complete"
        backup_id = $backupID
        created_at_utc = $createdAt.ToString("o")
        git_commit = $gitCommit
        database = [ordered]@{
            name = $databaseName
            user = $databaseUser
            dump_file = $databaseDump.Name
            size_bytes = [int64]$databaseDump.Length
            sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $databaseDumpPath).Hash.ToLowerInvariant()
            table_counts = $tableCounts
        }
        storage = [ordered]@{
            source_directory = "storage"
            archive_file = $storageArchive.Name
            archive_size_bytes = [int64]$storageArchive.Length
            sha256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $storageArchivePath).Hash.ToLowerInvariant()
            file_count = [int64]$storageFiles.Count
            source_size_bytes = $storageBytes
        }
    }

    Write-Utf8Json -Value $manifest -LiteralPath $manifestPath
}
catch {
    $operationError = $_
}
finally {
    if (-not [string]::IsNullOrWhiteSpace($containerDumpPath) -and
        -not [string]::IsNullOrWhiteSpace($postgresContainerID)) {
        & docker compose exec -T postgres rm -f $containerDumpPath *> $null
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "could not remove temporary dump from PostgreSQL container: $containerDumpPath"
        }
    }

    if ($backendStoppedByScript -and -not $KeepBackendStopped) {
        Write-Host "Restarting backend because it was running before backup..."
        & docker compose start backend
        if ($LASTEXITCODE -ne 0) {
            $restartError = "backup finished, but backend could not be restarted"
        }
    }

    Set-Location $originalLocation
}

if ($null -ne $operationError) {
    throw $operationError
}
if ($null -ne $restartError) {
    throw $restartError
}

Write-Host "Backup completed: $backupDirectory"
Write-Output $backupDirectory
