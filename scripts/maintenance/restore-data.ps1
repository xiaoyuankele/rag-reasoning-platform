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
$manifestFormatVersion = [int]$manifest.format_version
if ($manifestFormatVersion -notin @(1, 2, 3) -or $manifest.status -ne "complete") {
    throw "unsupported or incomplete backup manifest"
}

$manifestSchemaMigrationVersion = $null
$manifestGitWorktreeDirty = $null
if ($manifestFormatVersion -eq 3) {
    $migrationVersionProperty = $manifest.database.PSObject.Properties["schema_migration_version"]
    if ($null -eq $migrationVersionProperty) {
        throw "manifest format v3 is missing database.schema_migration_version"
    }
    try {
        $manifestSchemaMigrationVersion = [int64]$migrationVersionProperty.Value
    }
    catch {
        throw "manifest database.schema_migration_version must be an integer"
    }
    if ($manifestSchemaMigrationVersion -lt 0) {
        throw "manifest database.schema_migration_version must not be negative"
    }

    $dirtyProperty = $manifest.PSObject.Properties["git_worktree_dirty"]
    if ($null -eq $dirtyProperty -or -not ($dirtyProperty.Value -is [bool])) {
        throw "manifest format v3 requires boolean git_worktree_dirty"
    }
    $manifestGitWorktreeDirty = [bool]$dirtyProperty.Value
}

$legacyTableNames = @(
    "documents",
    "document_jobs",
    "text_chunks",
    "embedding_jobs",
    "chunk_embeddings"
)
$longLivedConversationTableNames = @(
    "users",
    "user_sessions",
    "conversations",
    "conversation_messages",
    "answer_jobs"
)
$tableNamesToVerify = if ($manifestFormatVersion -eq 1) {
    $legacyTableNames
}
else {
    @($legacyTableNames + $longLivedConversationTableNames)
}

# 在改变数据库或文件系统之前验证 manifest 包含当前格式要求的全部非负行数。
foreach ($tableName in $tableNamesToVerify) {
    $countProperty = $manifest.database.table_counts.PSObject.Properties[$tableName]
    if ($null -eq $countProperty) {
        throw "manifest format v$manifestFormatVersion is missing database.table_counts.$tableName"
    }

    try {
        $expectedCount = [int64]$countProperty.Value
    }
    catch {
        throw "manifest table count for $tableName must be an integer"
    }
    if ($expectedCount -lt 0) {
        throw "manifest table count for $tableName must not be negative"
    }
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

    $restoredSchemaMetadataSQL = @"
SELECT json_build_object(
    'schema_migration_version', (
        SELECT COALESCE(MAX(version), 0)
        FROM schema_migrations
    ),
    'requested_document_id_available', EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'answer_jobs'
          AND column_name = 'requested_document_id'
    )
)::text;
"@
    $restoredSchemaMetadataJSON = Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql",
            "--username", $databaseUser,
            "--dbname", $TargetDatabase,
            "--tuples-only",
            "--no-align",
            "--command", $restoredSchemaMetadataSQL
        ) `
        -Description "read restored schema metadata"
    $restoredSchemaMetadata = $restoredSchemaMetadataJSON | ConvertFrom-Json
    $restoredSchemaMigrationVersion = [int64]$restoredSchemaMetadata.schema_migration_version
    $requestedDocumentIDAvailable = [bool]$restoredSchemaMetadata.requested_document_id_available

    if ($manifestFormatVersion -eq 3 -and
        $restoredSchemaMigrationVersion -ne $manifestSchemaMigrationVersion) {
        throw "restored schema migration version is $restoredSchemaMigrationVersion; expected $manifestSchemaMigrationVersion"
    }

    $tableCountPairs = @(
        $tableNamesToVerify |
            ForEach-Object { "    '$_', (SELECT COUNT(*) FROM $_)" }
    )
    $tableCountSQL = "SELECT json_build_object(`n{0}`n)::text;" -f ($tableCountPairs -join ",`n")
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

    foreach ($tableName in $tableNamesToVerify) {
        $expectedCount = [int64]$manifest.database.table_counts.PSObject.Properties[$tableName].Value
        $actualCount = [int64]$restoredCounts.PSObject.Properties[$tableName].Value
        if ($actualCount -ne $expectedCount) {
            throw "restored row count for $tableName is $actualCount; expected $expectedCount"
        }
    }

    $conversationIntegrity = [ordered]@{
        status = "not_checked_manifest_v1"
        standalone_source_privacy = [ordered]@{
            status = "not_checked_manifest_v1"
        }
    }
    if ($manifestFormatVersion -ge 2) {
        # 只返回计数和索引门禁，不读取或输出消息正文、查询、答案、来源内容或 Owner 标识。
        $conversationIntegritySQL = @"
SELECT json_build_object(
    'linked_answer_jobs', (
        SELECT COUNT(*)
        FROM answer_jobs
        WHERE conversation_id IS NOT NULL
          AND user_message_id IS NOT NULL
          AND assistant_message_id IS NOT NULL
    ),
    'partially_linked_answer_jobs', (
        SELECT COUNT(*)
        FROM answer_jobs
        WHERE num_nonnulls(conversation_id, user_message_id, assistant_message_id) NOT IN (0, 3)
    ),
    'invalid_linked_message_relations', (
        SELECT COUNT(*)
        FROM answer_jobs AS job
        LEFT JOIN conversations AS conversation
          ON conversation.id = job.conversation_id
        LEFT JOIN conversation_messages AS user_message
          ON user_message.id = job.user_message_id
        LEFT JOIN conversation_messages AS assistant_message
          ON assistant_message.id = job.assistant_message_id
        WHERE num_nonnulls(job.conversation_id, job.user_message_id, job.assistant_message_id) = 3
          AND (
              conversation.id IS NULL
              OR conversation.owner_user_id IS DISTINCT FROM job.owner_user_id
              OR user_message.id IS NULL
              OR user_message.conversation_id IS DISTINCT FROM job.conversation_id
              OR user_message.role IS DISTINCT FROM 'user'
              OR assistant_message.id IS NULL
              OR assistant_message.conversation_id IS DISTINCT FROM job.conversation_id
              OR assistant_message.role IS DISTINCT FROM 'assistant'
          )
    ),
    'orphan_conversation_messages', (
        SELECT COUNT(*)
        FROM conversation_messages AS message
        LEFT JOIN conversations AS conversation
          ON conversation.id = message.conversation_id
        WHERE conversation.id IS NULL
    ),
    'incomplete_succeeded_results', (
        SELECT COUNT(*)
        FROM answer_jobs
        WHERE status = 'succeeded'
          AND (
              answer_text IS NULL
              OR LENGTH(BTRIM(answer_text)) = 0
              OR resolved_response_language IS NULL
              OR sources IS NULL
              OR jsonb_typeof(sources) IS DISTINCT FROM 'array'
              OR prompt_tokens IS NULL
              OR completion_tokens IS NULL
              OR total_tokens IS NULL
              OR prompt_tokens < 0
              OR completion_tokens < 0
              OR total_tokens < 0
              OR prompt_tokens + completion_tokens IS DISTINCT FROM total_tokens
          )
    ),
    'unique_user_message_index_count', (
        SELECT COUNT(*)
        FROM pg_catalog.pg_index AS index_metadata
        JOIN pg_catalog.pg_class AS index_relation
          ON index_relation.oid = index_metadata.indexrelid
        JOIN pg_catalog.pg_class AS table_relation
          ON table_relation.oid = index_metadata.indrelid
        JOIN pg_catalog.pg_namespace AS table_namespace
          ON table_namespace.oid = table_relation.relnamespace
        WHERE table_namespace.nspname = 'public'
          AND table_relation.relname = 'answer_jobs'
          AND index_relation.relname = 'uq_answer_jobs_user_message'
          AND index_metadata.indisunique
          AND pg_get_indexdef(index_metadata.indexrelid) LIKE '%(user_message_id)%'
    )
)::text;
"@
        $conversationIntegrityJSON = Get-NativeText -FilePath "docker" `
            -ArgumentList @(
                "compose", "exec", "-T", "postgres",
                "psql",
                "--username", $databaseUser,
                "--dbname", $TargetDatabase,
                "--tuples-only",
                "--no-align",
                "--command", $conversationIntegritySQL
            ) `
            -Description "verify restored conversation relationships"
        $conversationIntegrityData = $conversationIntegrityJSON | ConvertFrom-Json
        $requestedDocumentSnapshot = [ordered]@{
            status = "not_available"
            mismatch_count = $null
        }
        if ($requestedDocumentIDAvailable) {
            $requestedDocumentMismatchText = Get-NativeText -FilePath "docker" `
                -ArgumentList @(
                    "compose", "exec", "-T", "postgres",
                    "psql",
                    "--username", $databaseUser,
                    "--dbname", $TargetDatabase,
                    "--tuples-only",
                    "--no-align",
                    "--command", "SELECT COUNT(*) FROM answer_jobs WHERE document_id IS NOT NULL AND requested_document_id IS DISTINCT FROM document_id;"
                ) `
                -Description "verify requested document snapshots"
            [int64]$requestedDocumentMismatchCount = 0
            if (-not [int64]::TryParse($requestedDocumentMismatchText, [ref]$requestedDocumentMismatchCount) -or
                $requestedDocumentMismatchCount -lt 0) {
                throw "requested document snapshot mismatch count must be a non-negative integer"
            }
            $requestedDocumentSnapshot = [ordered]@{
                status = "verified"
                mismatch_count = $requestedDocumentMismatchCount
            }
        }
        elseif ($restoredSchemaMigrationVersion -ge 35) {
            throw "restored schema version $restoredSchemaMigrationVersion is missing answer_jobs.requested_document_id"
        }

        # standalone succeeded Job 的 sources 属于用户私有恢复边界。先按 JSON 类型、正整数格式和
        # BIGINT 上界分类，再用 documents.id::text 关联；不对不可信 JSON 文本执行 BIGINT cast。
        $standaloneSourcePrivacySQL = @"
WITH standalone_source_entries AS MATERIALIZED (
    SELECT
        job.id AS job_id,
        job.owner_user_id,
        source_entry.value -> 'document_id' AS snake_document_id,
        source_entry.value -> 'DocumentID' AS camel_document_id
    FROM answer_jobs AS job
    CROSS JOIN LATERAL jsonb_array_elements(
        CASE
            WHEN jsonb_typeof(job.sources) = 'array' THEN job.sources
            ELSE '[]'::jsonb
        END
    ) WITH ORDINALITY AS source_entry(value, ordinal)
    WHERE job.conversation_id IS NULL
      AND job.status = 'succeeded'
),
source_document_values AS MATERIALIZED (
    SELECT
        job_id,
        owner_user_id,
        snake_document_id,
        camel_document_id,
        CASE
            WHEN snake_document_id IS NULL THEN NULL
            ELSE snake_document_id #>> '{}'
        END AS snake_document_id_text,
        CASE
            WHEN camel_document_id IS NULL THEN NULL
            ELSE camel_document_id #>> '{}'
        END AS camel_document_id_text
    FROM standalone_source_entries
),
classified_sources AS MATERIALIZED (
    SELECT
        job_id,
        owner_user_id,
        COALESCE(snake_document_id_text, camel_document_id_text) AS document_id_text,
        (
            (snake_document_id IS NULL AND camel_document_id IS NULL)
            OR (
                snake_document_id IS NOT NULL
                AND (
                    jsonb_typeof(snake_document_id) <> 'number'
                    OR snake_document_id_text !~ '^[1-9][0-9]*$'
                    OR LENGTH(snake_document_id_text) > 19
                    OR (
                        LENGTH(snake_document_id_text) = 19
                        AND snake_document_id_text > '9223372036854775807'
                    )
                )
            )
            OR (
                camel_document_id IS NOT NULL
                AND (
                    jsonb_typeof(camel_document_id) <> 'number'
                    OR camel_document_id_text !~ '^[1-9][0-9]*$'
                    OR LENGTH(camel_document_id_text) > 19
                    OR (
                        LENGTH(camel_document_id_text) = 19
                        AND camel_document_id_text > '9223372036854775807'
                    )
                )
            )
            OR (
                snake_document_id IS NOT NULL
                AND camel_document_id IS NOT NULL
                AND snake_document_id_text IS DISTINCT FROM camel_document_id_text
            )
        ) AS invalid_document_id
    FROM source_document_values
)
SELECT json_build_object(
    'source_entry_count', COUNT(*),
    'invalid_document_id_count', COUNT(*) FILTER (
        WHERE source.invalid_document_id
    ),
    'missing_document_count', COUNT(*) FILTER (
        WHERE NOT source.invalid_document_id
          AND document.id IS NULL
    ),
    'owner_mismatch_count', COUNT(*) FILTER (
        WHERE NOT source.invalid_document_id
          AND document.id IS NOT NULL
          AND document.owner_user_id IS DISTINCT FROM source.owner_user_id
    )
)::text
FROM classified_sources AS source
LEFT JOIN documents AS document
  ON NOT source.invalid_document_id
 AND document.id::text = source.document_id_text;
"@
        $standaloneSourcePrivacyJSON = Get-NativeText -FilePath "docker" `
            -ArgumentList @(
                "compose", "exec", "-T", "postgres",
                "psql",
                "--username", $databaseUser,
                "--dbname", $TargetDatabase,
                "--tuples-only",
                "--no-align",
                "--command", $standaloneSourcePrivacySQL
            ) `
            -Description "verify restored standalone answer source privacy"
        $standaloneSourcePrivacyData = $standaloneSourcePrivacyJSON | ConvertFrom-Json
        $standaloneSourcePrivacy = [ordered]@{
            status = "verified"
            source_entry_count = [int64]$standaloneSourcePrivacyData.source_entry_count
            invalid_document_id_count = [int64]$standaloneSourcePrivacyData.invalid_document_id_count
            missing_document_count = [int64]$standaloneSourcePrivacyData.missing_document_count
            owner_mismatch_count = [int64]$standaloneSourcePrivacyData.owner_mismatch_count
        }

        $conversationIntegrity = [ordered]@{
            status = "verified"
            linked_answer_jobs = [int64]$conversationIntegrityData.linked_answer_jobs
            partially_linked_answer_jobs = [int64]$conversationIntegrityData.partially_linked_answer_jobs
            invalid_linked_message_relations = [int64]$conversationIntegrityData.invalid_linked_message_relations
            orphan_conversation_messages = [int64]$conversationIntegrityData.orphan_conversation_messages
            incomplete_succeeded_results = [int64]$conversationIntegrityData.incomplete_succeeded_results
            unique_user_message_index_count = [int64]$conversationIntegrityData.unique_user_message_index_count
            requested_document_snapshot = $requestedDocumentSnapshot
            standalone_source_privacy = $standaloneSourcePrivacy
        }

        if ($conversationIntegrity.partially_linked_answer_jobs -ne 0) {
            throw "restored answer jobs contain incomplete conversation link IDs"
        }
        if ($conversationIntegrity.invalid_linked_message_relations -ne 0) {
            throw "restored answer jobs contain invalid conversation message ownership or roles"
        }
        if ($conversationIntegrity.orphan_conversation_messages -ne 0) {
            throw "restored conversation messages contain missing conversation parents"
        }
        if ($conversationIntegrity.incomplete_succeeded_results -ne 0) {
            throw "restored succeeded answer jobs contain incomplete sources or token fields"
        }
        if ($conversationIntegrity.unique_user_message_index_count -ne 1) {
            throw "restored database is missing the unique answer-job user-message index"
        }
        if ($requestedDocumentSnapshot.status -eq "verified" -and
            $requestedDocumentSnapshot.mismatch_count -ne 0) {
            throw "restored answer jobs contain mismatched requested document snapshots"
        }
        if ($standaloneSourcePrivacy.invalid_document_id_count -ne 0) {
            throw "restored standalone answer sources contain invalid document IDs"
        }
        if ($standaloneSourcePrivacy.missing_document_count -ne 0) {
            throw "restored standalone answer sources reference missing documents"
        }
        if ($standaloneSourcePrivacy.owner_mismatch_count -ne 0) {
            throw "restored standalone answer sources reference documents owned by another user"
        }

        Write-Host (
            "Conversation integrity counts: linked_jobs={0}, partial_links={1}, invalid_message_links={2}, orphan_messages={3}, incomplete_succeeded={4}, unique_user_message_indexes={5}, requested_document_snapshot={6}, requested_document_mismatches={7}, standalone_source_entries={8}, invalid_source_document_ids={9}, missing_source_documents={10}, source_owner_mismatches={11}" -f
                $conversationIntegrity.linked_answer_jobs,
                $conversationIntegrity.partially_linked_answer_jobs,
                $conversationIntegrity.invalid_linked_message_relations,
                $conversationIntegrity.orphan_conversation_messages,
                $conversationIntegrity.incomplete_succeeded_results,
                $conversationIntegrity.unique_user_message_index_count,
                $requestedDocumentSnapshot.status,
                $requestedDocumentSnapshot.mismatch_count,
                $standaloneSourcePrivacy.source_entry_count,
                $standaloneSourcePrivacy.invalid_document_id_count,
                $standaloneSourcePrivacy.missing_document_count,
                $standaloneSourcePrivacy.owner_mismatch_count
        )
    }

    $result = [ordered]@{
        status = "verified"
        manifest_format_version = $manifestFormatVersion
        source_git_worktree_dirty = $manifestGitWorktreeDirty
        restored_schema_migration_version = $restoredSchemaMigrationVersion
        restored_at_utc = [DateTime]::UtcNow.ToString("o")
        source_backup = $backupDirectoryPath
        target_database = $TargetDatabase
        target_storage = $restoredStoragePath
        database_table_counts = $restoredCounts
        conversation_integrity = $conversationIntegrity
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
