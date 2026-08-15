[CmdletBinding()]
param(
    # 指向同时包含 manifest.json、database.dump 和 storage.tar.gz 的完整备份目录。
    [Parameter(Mandatory)]
    [string]$BackupDirectory,

    # 恢复脚本只允许安全的 PostgreSQL 标识符，并且拒绝覆盖已经存在的数据库。
    [Parameter(Mandatory)]
    [string]$TargetDatabase,

    # 恢复后的文件会放在 TargetRoot/storage。默认进入本机运行产物目录。
    [string]$TargetRoot = ""
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

function Assert-ManifestFileName {
    param(
        [Parameter(Mandatory)]
        [string]$FileName,

        [Parameter(Mandatory)]
        [string]$FieldName
    )

    # manifest 中只允许单个文件名，不能出现 ../ 或绝对路径，避免恢复到备份目录之外。
    if ([string]::IsNullOrWhiteSpace($FileName) -or
        [System.IO.Path]::GetFileName($FileName) -ne $FileName) {
        throw "manifest field '$FieldName' must contain a safe file name"
    }
}

function Assert-FileHash {
    param(
        [Parameter(Mandatory)]
        [string]$LiteralPath,

        [Parameter(Mandatory)]
        [string]$ExpectedSHA256
    )

    if ($ExpectedSHA256 -notmatch "^[0-9a-fA-F]{64}$") {
        throw "manifest contains an invalid SHA-256 value for $LiteralPath"
    }

    $actualSHA256 = (Get-FileHash -Algorithm SHA256 -LiteralPath $LiteralPath).Hash
    if ($actualSHA256 -ne $ExpectedSHA256) {
        throw "SHA-256 mismatch for $LiteralPath"
    }
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
$backupDirectoryPath = Get-AbsoluteProjectPath -Path $BackupDirectory -ProjectRoot $projectRoot

if ($TargetDatabase -notmatch "^[A-Za-z_][A-Za-z0-9_]{0,62}$") {
    throw "TargetDatabase must start with a letter/underscore and contain at most 63 letters, digits, or underscores"
}

if ([string]::IsNullOrWhiteSpace($TargetRoot)) {
    $TargetRoot = "chatgpt\运行产物\临时\restore-$TargetDatabase"
}
$targetRootPath = Get-AbsoluteProjectPath -Path $TargetRoot -ProjectRoot $projectRoot
$backupPrefix = $backupDirectoryPath.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar

if (-not (Test-Path -LiteralPath $backupDirectoryPath -PathType Container)) {
    throw "backup directory does not exist: $backupDirectoryPath"
}
if ($targetRootPath -eq $backupDirectoryPath -or
    $targetRootPath.StartsWith($backupPrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "TargetRoot must not be inside the backup directory"
}
if (Test-Path -LiteralPath $targetRootPath) {
    throw "TargetRoot already exists; restore refuses to overwrite it: $targetRootPath"
}

$manifestPath = Join-Path $backupDirectoryPath "manifest.json"
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
    throw "manifest.json is missing; incomplete backups cannot be restored"
}

$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding utf8 | ConvertFrom-Json
if ($manifest.format_version -ne 1 -or $manifest.status -ne "complete") {
    throw "unsupported or incomplete backup manifest"
}

$databaseDumpFile = [string]$manifest.database.dump_file
$storageArchiveFile = [string]$manifest.storage.archive_file
Assert-ManifestFileName -FileName $databaseDumpFile -FieldName "database.dump_file"
Assert-ManifestFileName -FileName $storageArchiveFile -FieldName "storage.archive_file"

$databaseDumpPath = Join-Path $backupDirectoryPath $databaseDumpFile
$storageArchivePath = Join-Path $backupDirectoryPath $storageArchiveFile
if (-not (Test-Path -LiteralPath $databaseDumpPath -PathType Leaf)) {
    throw "database dump is missing: $databaseDumpPath"
}
if (-not (Test-Path -LiteralPath $storageArchivePath -PathType Leaf)) {
    throw "storage archive is missing: $storageArchivePath"
}

# 在创建数据库或目录之前先验证两个大文件，校验失败时不会改变外部状态。
Assert-FileHash -LiteralPath $databaseDumpPath -ExpectedSHA256 ([string]$manifest.database.sha256)
Assert-FileHash -LiteralPath $storageArchivePath -ExpectedSHA256 ([string]$manifest.storage.sha256)

$postgresContainerID = ""
$containerDumpPath = ""
$databaseCreated = $false
$operationError = $null

try {
    Set-Location $projectRoot

    foreach ($requiredCommand in @("docker", "tar")) {
        if (-not (Get-Command $requiredCommand -ErrorAction SilentlyContinue)) {
            throw "required command '$requiredCommand' was not found"
        }
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
        throw "PostgreSQL must be healthy before restore; current status: $postgresHealth"
    }

    $databaseUser = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_USER") `
        -Description "read PostgreSQL user"
    if ([string]::IsNullOrWhiteSpace($databaseUser)) {
        throw "POSTGRES_USER must be available inside the container"
    }

    # TargetDatabase 已经过严格正则校验，因此可以安全地用于以下只读存在性查询。
    $databaseExistsSQL = "SELECT 1 FROM pg_database WHERE datname = '$TargetDatabase';"
    $databaseExists = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql",
            "--username", $databaseUser,
            "--dbname", "postgres",
            "--tuples-only",
            "--no-align",
            "--command", $databaseExistsSQL
        ) `
        -Description "check target database"
    if ($databaseExists -eq "1") {
        throw "target database already exists; restore refuses to overwrite it: $TargetDatabase"
    }

    $archiveEntries = @(& tar -tzf $storageArchivePath)
    if ($LASTEXITCODE -ne 0) {
        throw "validate storage archive failed with exit code $LASTEXITCODE"
    }
    if ($archiveEntries.Count -eq 0) {
        throw "storage archive is empty"
    }
    foreach ($entry in $archiveEntries) {
        $normalizedEntry = ([string]$entry).Replace("\", "/")
        if (($normalizedEntry -ne "storage/") -and
            (-not $normalizedEntry.StartsWith("storage/", [System.StringComparison]::Ordinal))) {
            throw "storage archive contains an unexpected path: $normalizedEntry"
        }
        if ($normalizedEntry.Contains("../") -or $normalizedEntry.StartsWith("/")) {
            throw "storage archive contains an unsafe path: $normalizedEntry"
        }
    }

    # TargetRoot 必须不存在，所以这里不会覆盖任何已有文件。
    New-Item -ItemType Directory -Path $targetRootPath | Out-Null
    Invoke-NativeCommand -FilePath "tar" `
        -ArgumentList @("-xzf", $storageArchivePath, "-C", $targetRootPath) `
        -Description "extract storage archive"

    $restoredStoragePath = Join-Path $targetRootPath "storage"
    if (-not (Test-Path -LiteralPath $restoredStoragePath -PathType Container)) {
        throw "restored archive did not create the expected storage directory"
    }

    $restoredStorageFiles = @(Get-ChildItem -LiteralPath $restoredStoragePath -File -Recurse -Force)
    $restoredBytesMeasurement = $restoredStorageFiles | Measure-Object -Property Length -Sum
    $restoredStorageBytes = if ($null -eq $restoredBytesMeasurement.Sum) { 0L } else { [int64]$restoredBytesMeasurement.Sum }
    if ([int64]$restoredStorageFiles.Count -ne [int64]$manifest.storage.file_count) {
        throw "restored storage file count does not match manifest"
    }
    if ($restoredStorageBytes -ne [int64]$manifest.storage.source_size_bytes) {
        throw "restored storage byte count does not match manifest"
    }

    $containerDumpPath = "/tmp/rag-restore-$([Guid]::NewGuid().ToString('N')).dump"
    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @("cp", $databaseDumpPath, "${postgresContainerID}:$containerDumpPath") `
        -Description "copy PostgreSQL dump into the container"

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "createdb",
            "--username", $databaseUser,
            "--encoding", "UTF8",
            $TargetDatabase
        ) `
        -Description "create target database"
    $databaseCreated = $true

    Invoke-NativeCommand -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "pg_restore",
            "--username", $databaseUser,
            "--dbname", $TargetDatabase,
            "--exit-on-error",
            "--no-owner",
            "--no-privileges",
            $containerDumpPath
        ) `
        -Description "restore PostgreSQL dump"

    $tableCountSQL = @"
SELECT json_build_object(
    'documents', (SELECT COUNT(*) FROM documents),
    'document_jobs', (SELECT COUNT(*) FROM document_jobs),
    'text_chunks', (SELECT COUNT(*) FROM text_chunks),
    'embedding_jobs', (SELECT COUNT(*) FROM embedding_jobs),
    'chunk_embeddings', (SELECT COUNT(*) FROM chunk_embeddings)
)::text;
"@
    $restoredCountsJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql",
            "--username", $databaseUser,
            "--dbname", $TargetDatabase,
            "--tuples-only",
            "--no-align",
            "--command", $tableCountSQL
        ) `
        -Description "read restored table counts"
    $restoredCounts = $restoredCountsJSON | ConvertFrom-Json

    foreach ($tableName in @("documents", "document_jobs", "text_chunks", "embedding_jobs", "chunk_embeddings")) {
        $expectedCount = [int64]$manifest.database.table_counts.PSObject.Properties[$tableName].Value
        $actualCount = [int64]$restoredCounts.PSObject.Properties[$tableName].Value
        if ($actualCount -ne $expectedCount) {
            throw "restored row count for $tableName is $actualCount; expected $expectedCount"
        }
    }

    $result = [ordered]@{
        status = "verified"
        restored_at_utc = [DateTime]::UtcNow.ToString("o")
        source_backup = $backupDirectoryPath
        target_database = $TargetDatabase
        target_storage = $restoredStoragePath
        database_table_counts = $restoredCounts
        storage_file_count = [int64]$restoredStorageFiles.Count
        storage_size_bytes = $restoredStorageBytes
    }
    Write-Utf8Json -Value $result -LiteralPath (Join-Path $targetRootPath "restore-result.json")
}
catch {
    $operationError = $_
}
finally {
    if (-not [string]::IsNullOrWhiteSpace($containerDumpPath) -and
        -not [string]::IsNullOrWhiteSpace($postgresContainerID)) {
        & docker compose exec -T postgres rm -f $containerDumpPath *> $null
        if ($LASTEXITCODE -ne 0) {
            Write-Warning "could not remove temporary restore dump from PostgreSQL container: $containerDumpPath"
        }
    }

    Set-Location $originalLocation
}

if ($null -ne $operationError) {
    if ($databaseCreated) {
        Write-Warning "restore failed after creating database '$TargetDatabase'; it was preserved for diagnosis"
    }
    if (Test-Path -LiteralPath $targetRootPath) {
        Write-Warning "restore target was preserved for diagnosis: $targetRootPath"
    }
    throw $operationError
}

Write-Host "Restore verified successfully."
Write-Host "Database: $TargetDatabase"
Write-Host "Files: $(Join-Path $targetRootPath 'storage')"
Write-Output $targetRootPath
