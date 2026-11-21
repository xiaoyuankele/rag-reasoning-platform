[CmdletBinding()]
param(
    # 默认使用当前命令行中的 Python 3.11。
    [string]$PythonExecutable = "python",

    # 显式加入真实 OSS 文档纵向门禁。默认关闭，避免普通回归产生云请求。
    [switch]$IncludeOSSVertical,

    # 默认报告写入 Git 已忽略的 chatgpt/运行产物。
    [string]$ReportDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-RegressionStep {
    param(
        [Parameter(Mandatory)]
        [string]$Name,

        [Parameter(Mandatory)]
        [string]$Description,

        [Parameter(Mandatory)]
        [scriptblock]$Action,

        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [System.Collections.Generic.List[object]]$Results
    )

    Write-Host ""
    Write-Host "[$Name] $Description"
    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()

    try {
        & $Action
        $stopwatch.Stop()
        [void]$Results.Add([ordered]@{
            name        = $Name
            description = $Description
            status      = "passed"
            duration_ms = $stopwatch.ElapsedMilliseconds
        })
        Write-Host "[$Name] PASSED ($($stopwatch.ElapsedMilliseconds) ms)"
    }
    catch {
        $stopwatch.Stop()
        [void]$Results.Add([ordered]@{
            name        = $Name
            description = $Description
            status      = "failed"
            duration_ms = $stopwatch.ElapsedMilliseconds
            error       = $_.Exception.Message
        })
        Write-Host "[$Name] FAILED ($($stopwatch.ElapsedMilliseconds) ms)"
        throw
    }
}

function Assert-LastExitCode {
    param(
        [Parameter(Mandatory)]
        [string]$Description
    )

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

function Invoke-GoTest {
    param(
        [Parameter(Mandatory)]
        [string]$BackendDirectory,

        [Parameter(Mandatory)]
        [string[]]$Packages,

        [string]$RunPattern
    )

    $arguments = @("test", "-p", "1", "-count=1", "-v")
    if (-not [string]::IsNullOrWhiteSpace($RunPattern)) {
        $arguments += @("-run", $RunPattern)
    }
    $arguments += $Packages

    Push-Location $BackendDirectory
    try {
        & go @arguments
        Assert-LastExitCode -Description "go test"
    }
    finally {
        Pop-Location
    }
}

function ConvertTo-PostgresIdentifier {
    param(
        [Parameter(Mandatory)]
        [string]$Value
    )

    # PostgreSQL 标识符使用双引号；内部双引号必须重复一次。
    return '"' + $Value.Replace('"', '""') + '"'
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

function Save-ProcessEnvironment {
    param(
        [Parameter(Mandatory)]
        [string[]]$Names
    )

    $saved = @{}
    foreach ($name in $Names) {
        $item = Get-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
        if ($null -eq $item) {
            $saved[$name] = [ordered]@{
                existed = $false
                value   = $null
            }
            continue
        }

        $saved[$name] = [ordered]@{
            existed = $true
            value   = [string]$item.Value
        }
    }

    return $saved
}

function Restore-ProcessEnvironment {
    param(
        [Parameter(Mandatory)]
        [hashtable]$Saved
    )

    foreach ($name in $Saved.Keys) {
        $entry = $Saved[$name]
        if ([bool]$entry.existed) {
            Set-Item -LiteralPath "Env:$name" -Value ([string]$entry.value)
            continue
        }

        Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
    }
}

$scriptDirectory = Split-Path -Parent $PSCommandPath
$projectRoot = [System.IO.Path]::GetFullPath((Join-Path $scriptDirectory "..\.."))
$backendDirectory = Join-Path $projectRoot "backend"
$originalLocation = Get-Location
$startedAt = [DateTime]::UtcNow
$timestamp = $startedAt.ToString("yyyyMMddTHHmmssZ")
$uniqueSuffix = [Guid]::NewGuid().ToString("N").Substring(0, 8)
$testDatabaseName = "rag_integration_$($startedAt.ToString('yyyyMMddHHmmss'))_$uniqueSuffix"
$stepResults = New-Object 'System.Collections.Generic.List[object]'
$operationError = $null
$databaseCreated = $false
$cleanupStatus = "not_needed"
$cleanupError = $null

if ($testDatabaseName -notmatch '^rag_integration_[a-zA-Z0-9_]+$') {
    throw "generated integration database name is unsafe"
}

if ([string]::IsNullOrWhiteSpace($ReportDirectory)) {
    $ReportDirectory = Join-Path $projectRoot "chatgpt\运行产物\回归"
}
else {
    $ReportDirectory = [System.IO.Path]::GetFullPath(
        (Join-Path $originalLocation $ReportDirectory)
    )
}
$reportPath = Join-Path $ReportDirectory "backend-local-integration-$timestamp.json"

$isolatedEnvironment = @(
    "RUN_DATABASE_TESTS",
    "RUN_PYTHON_TESTS",
    "RUN_OSS_INTEGRATION_TESTS",
    "RUN_OSS_VERTICAL_INTEGRATION_TESTS",
    "DB_HOST",
    "DB_PORT",
    "DB_NAME",
    "DB_USER",
    "DB_PASSWORD",
    "DB_SSLMODE",
    "PYTHON_EXECUTABLE",
    "FILE_STORAGE_DRIVER",
    "OSS_VERTICAL_REPOSITORY_ROOT",
    "EMBEDDING_WORKER_ENABLED",
    "SEMANTIC_SEARCH_ENABLED",
    "ANSWER_ENABLED",
    "ANSWER_JOBS_ENABLED",
    "OPENAI_API_KEY",
    "DASHSCOPE_API_KEY"
)
$savedEnvironment = Save-ProcessEnvironment -Names $isolatedEnvironment

try {
    Set-Location $projectRoot

    foreach ($requiredCommand in @("go", $PythonExecutable, "docker")) {
        if (-not (Get-Command $requiredCommand -ErrorAction SilentlyContinue)) {
            throw "required command '$requiredCommand' was not found"
        }
    }

    if ($IncludeOSSVertical) {
        foreach ($requiredOSSVariable in @(
            "OSS_BUCKET",
            "OSS_REGION",
            "OSS_ENDPOINT",
            "OSS_CREDENTIAL_MODE"
        )) {
            $requiredOSSValue = Get-Item `
                -LiteralPath "Env:$requiredOSSVariable" `
                -ErrorAction SilentlyContinue
            if ($null -eq $requiredOSSValue -or
                [string]::IsNullOrWhiteSpace([string]$requiredOSSValue.Value)) {
                throw "$requiredOSSVariable must be configured when -IncludeOSSVertical is used"
            }
        }

        switch ([string]$env:OSS_CREDENTIAL_MODE) {
            "environment" {
                foreach ($credentialVariable in @(
                    "OSS_ACCESS_KEY_ID",
                    "OSS_ACCESS_KEY_SECRET"
                )) {
                    $credentialValue = Get-Item `
                        -LiteralPath "Env:$credentialVariable" `
                        -ErrorAction SilentlyContinue
                    if ($null -eq $credentialValue -or
                        [string]::IsNullOrWhiteSpace([string]$credentialValue.Value)) {
                        throw "$credentialVariable must be configured for OSS environment credentials"
                    }
                }
            }
            "ecs_ram_role" {
                if ([string]::IsNullOrWhiteSpace([string]$env:OSS_ECS_RAM_ROLE)) {
                    throw "OSS_ECS_RAM_ROLE must be configured for ECS RAM Role credentials"
                }
            }
            default {
                throw "OSS_CREDENTIAL_MODE must be environment or ecs_ram_role"
            }
        }
    }

    [void](Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "config", "--quiet") `
        -Description "validate Compose configuration")

    $postgresContainerID = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "ps", "--status", "running", "--quiet", "postgres") `
        -Description "find running PostgreSQL container"
    if ([string]::IsNullOrWhiteSpace($postgresContainerID)) {
        throw "PostgreSQL Compose service is not running"
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
    $databasePassword = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "exec", "-T", "postgres", "printenv", "POSTGRES_PASSWORD") `
        -Description "read PostgreSQL password"
    if ([string]::IsNullOrWhiteSpace($databaseUser) -or
        [string]::IsNullOrWhiteSpace($databasePassword)) {
        throw "PostgreSQL container user or password is empty"
    }

    $publishedAddress = Get-NativeText -FilePath "docker" `
        -ArgumentList @("compose", "port", "postgres", "5432") `
        -Description "read PostgreSQL published port"
    $publishedPort = $null
    foreach ($addressLine in ($publishedAddress -split "`n")) {
        if ($addressLine -match ':(\d+)$') {
            $publishedPort = $Matches[1]
            break
        }
    }
    if ([string]::IsNullOrWhiteSpace($publishedPort)) {
        throw "could not parse PostgreSQL published port from '$publishedAddress'"
    }

    # 全部测试只连接这个一次性数据库，不接触正式 POSTGRES_DB 中的业务表。
    $quotedDatabaseName = ConvertTo-PostgresIdentifier -Value $testDatabaseName
    $quotedDatabaseUser = ConvertTo-PostgresIdentifier -Value $databaseUser
    $createDatabaseSQL = "CREATE DATABASE $quotedDatabaseName OWNER $quotedDatabaseUser TEMPLATE template0;"
    [void](Get-NativeText -FilePath "docker" `
        -ArgumentList @(
            "compose", "exec", "-T", "postgres",
            "psql", "--username", $databaseUser, "--dbname", "postgres",
            "--set", "ON_ERROR_STOP=1", "--command", $createDatabaseSQL
        ) `
        -Description "create isolated integration database")
    $databaseCreated = $true

    $env:RUN_DATABASE_TESTS = "1"
    $env:RUN_PYTHON_TESTS = "1"
    Remove-Item Env:RUN_OSS_INTEGRATION_TESTS -ErrorAction SilentlyContinue
    Remove-Item Env:RUN_OSS_VERTICAL_INTEGRATION_TESTS -ErrorAction SilentlyContinue
    $env:DB_HOST = "127.0.0.1"
    $env:DB_PORT = [string]$publishedPort
    $env:DB_NAME = $testDatabaseName
    $env:DB_USER = $databaseUser
    $env:DB_PASSWORD = $databasePassword
    $env:DB_SSLMODE = "disable"
    $env:PYTHON_EXECUTABLE = $PythonExecutable
    $env:FILE_STORAGE_DRIVER = "local"

    # 本地集成只验证数据库和 Python 进程，不调用任何模型供应商。
    $env:EMBEDDING_WORKER_ENABLED = "false"
    $env:SEMANTIC_SEARCH_ENABLED = "false"
    $env:ANSWER_ENABLED = "false"
    $env:ANSWER_JOBS_ENABLED = "false"
    $env:OPENAI_API_KEY = "integration-regression-disabled"
    $env:DASHSCOPE_API_KEY = "integration-regression-disabled"

    Invoke-RegressionStep `
        -Name "database_migrations" `
        -Description "apply embedded migrations twice in an isolated PostgreSQL database" `
        -Results $stepResults `
        -Action {
            Invoke-GoTest `
                -BackendDirectory $backendDirectory `
                -Packages @("./internal/infrastructure/database") `
                -RunPattern "^TestMigrateAppliesEmbeddedMigrationsOnce$"
        }

    Invoke-RegressionStep `
        -Name "postgres_repositories" `
        -Description "run real PostgreSQL repository and transaction tests" `
        -Results $stepResults `
        -Action {
            Invoke-GoTest `
                -BackendDirectory $backendDirectory `
                -Packages @("./internal/infrastructure/postgres")
        }

    Invoke-RegressionStep `
        -Name "document_worker_database" `
        -Description "verify Application Worker orchestration against PostgreSQL" `
        -Results $stepResults `
        -Action {
            Invoke-GoTest `
                -BackendDirectory $backendDirectory `
                -Packages @("./internal/application/document") `
                -RunPattern "^TestWorkerRunOnceIntegration$"
        }

    Invoke-RegressionStep `
        -Name "go_python_process" `
        -Description "start the real Python CLI and verify the Go/Python JSON contract" `
        -Results $stepResults `
        -Action {
            Invoke-GoTest `
                -BackendDirectory $backendDirectory `
                -Packages @("./internal/infrastructure/pythonprocessor") `
                -RunPattern "^(TestPythonCLIContractRoundTrip|TestProcessorCallsRealPythonCLI)$"
        }

    Invoke-RegressionStep `
        -Name "cross_stack_document_flow" `
        -Description "verify HTTP, PDF, chunks and PostgreSQL vertical integration" `
        -Results $stepResults `
        -Action {
            Invoke-GoTest `
                -BackendDirectory $backendDirectory `
                -Packages @("./internal/integration")
        }

    if ($IncludeOSSVertical) {
        # 只有进入这个独立步骤时才打开真实 OSS 门禁。一次性数据库名称已经
        # 由脚本生成，测试还会再次校验 rag_integration_* 前缀。
        $env:FILE_STORAGE_DRIVER = "oss"
        $env:RUN_OSS_INTEGRATION_TESTS = "1"
        $env:RUN_OSS_VERTICAL_INTEGRATION_TESTS = "1"
        $env:OSS_VERTICAL_REPOSITORY_ROOT = $projectRoot

        Invoke-RegressionStep `
            -Name "oss_document_vertical" `
            -Description "verify HTTP, PostgreSQL, Document Worker, Python and real OSS lifecycle" `
            -Results $stepResults `
            -Action {
                Invoke-GoTest `
                    -BackendDirectory $backendDirectory `
                    -Packages @("./internal/integration") `
                    -RunPattern "^TestOSSDocumentLifecycleWithPostgreSQLAndPython$"
            }
    }
}
catch {
    $operationError = $_
}
finally {
    if ($databaseCreated) {
        try {
            if ($testDatabaseName -notmatch '^rag_integration_[a-zA-Z0-9_]+$') {
                throw "refusing to drop database with an unexpected name"
            }

            $quotedDatabaseName = ConvertTo-PostgresIdentifier -Value $testDatabaseName
            $dropDatabaseSQL = "DROP DATABASE IF EXISTS $quotedDatabaseName WITH (FORCE);"
            [void](Get-NativeText -FilePath "docker" `
                -ArgumentList @(
                    "compose", "exec", "-T", "postgres",
                    "psql", "--username", $databaseUser, "--dbname", "postgres",
                    "--set", "ON_ERROR_STOP=1", "--command", $dropDatabaseSQL
                ) `
                -Description "drop isolated integration database")

            $databaseNameLiteral = $testDatabaseName.Replace("'", "''")
            $databaseExistsSQL = "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$databaseNameLiteral');"
            $databaseExists = Get-NativeText -FilePath "docker" `
                -ArgumentList @(
                    "compose", "exec", "-T", "postgres",
                    "psql", "--username", $databaseUser, "--dbname", "postgres",
                    "--tuples-only", "--no-align", "--command", $databaseExistsSQL
                ) `
                -Description "verify integration database cleanup"
            if ($databaseExists -ne "f") {
                throw "integration database still exists after cleanup"
            }

            $cleanupStatus = "passed"
        }
        catch {
            $cleanupStatus = "failed"
            $cleanupError = $_.Exception.Message
            if ($null -eq $operationError) {
                $operationError = $_
            }
        }
    }

    Restore-ProcessEnvironment -Saved $savedEnvironment
    Set-Location $originalLocation

    $finishedAt = [DateTime]::UtcNow
    $report = [ordered]@{
        schema_version              = 1
        suite                       = "backend-local-integration"
        status                      = if ($null -eq $operationError) { "passed" } else { "failed" }
        started_at_utc              = $startedAt.ToString("o")
        finished_at_utc             = $finishedAt.ToString("o")
        duration_ms                 = [int64]($finishedAt - $startedAt).TotalMilliseconds
        project_root                = $projectRoot
        database_isolation          = "temporary_database"
        temporary_database          = $testDatabaseName
        temporary_database_cleanup  = $cleanupStatus
        go_python_integration_tests = $true
        oss_vertical_included       = [bool]$IncludeOSSVertical
        remote_ai_enabled            = $false
        steps                        = $stepResults
    }
    if ($null -ne $cleanupError) {
        $report["cleanup_error"] = $cleanupError
    }
    if ($null -ne $operationError) {
        $report["error"] = $operationError.Exception.Message
    }

    New-Item -ItemType Directory -Force -Path $ReportDirectory | Out-Null
    Write-Utf8Text `
        -LiteralPath $reportPath `
        -Value (($report | ConvertTo-Json -Depth 8) + [Environment]::NewLine)
}

if ($null -ne $operationError) {
    Write-Warning "Backend local integration regression failed. Report: $reportPath"
    throw $operationError
}

Write-Host ""
Write-Host "Backend local integration regression passed."
Write-Host "Database isolation: temporary database (removed)"
Write-Host "Go/Python process integration: enabled"
Write-Host "Real OSS document vertical: $([bool]$IncludeOSSVertical)"
Write-Host "Remote AI calls: disabled"
Write-Host "Report: $reportPath"
Write-Output $reportPath
