[CmdletBinding()]
param(
    # 默认报告写入 Git 已忽略的 chatgpt/运行产物，不污染项目源码。
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
$aiDirectory = Join-Path $projectRoot "ai"
$originalLocation = Get-Location
$startedAt = [DateTime]::UtcNow
$timestamp = $startedAt.ToString("yyyyMMddTHHmmssZ")
$stepResults = New-Object 'System.Collections.Generic.List[object]'
$operationError = $null

if ([string]::IsNullOrWhiteSpace($ReportDirectory)) {
    $ReportDirectory = Join-Path $projectRoot "chatgpt\运行产物\回归"
}
else {
    $ReportDirectory = [System.IO.Path]::GetFullPath(
        (Join-Path $originalLocation $ReportDirectory)
    )
}
$reportPath = Join-Path $ReportDirectory "backend-default-$timestamp.json"

# 默认回归必须隔离会访问数据库、启动 Python 集成链路或调用远程模型的开关。
# 同时保存调用者原来的进程环境，脚本结束后原样恢复。
$isolatedEnvironment = @(
    "APP_ROLE",
    "APP_READY_FILE",
    "RUN_DATABASE_TESTS",
    "RUN_PYTHON_TESTS",
    "EMBEDDING_WORKER_ENABLED",
    "SEMANTIC_SEARCH_ENABLED",
    "ANSWER_ENABLED",
    "ANSWER_JOBS_ENABLED",
    "OPENAI_API_KEY",
    "DASHSCOPE_API_KEY",
    "DB_PASSWORD"
)
$savedEnvironment = Save-ProcessEnvironment -Names $isolatedEnvironment

try {
    Set-Location $projectRoot

    foreach ($requiredCommand in @("go", "gofmt", "python", "docker")) {
        if (-not (Get-Command $requiredCommand -ErrorAction SilentlyContinue)) {
            throw "required command '$requiredCommand' was not found"
        }
    }

    if (-not (Test-Path -LiteralPath (Join-Path $backendDirectory "go.mod") -PathType Leaf)) {
        throw "backend/go.mod was not found under project root '$projectRoot'"
    }
    if (-not (Test-Path -LiteralPath (Join-Path $aiDirectory "pyproject.toml") -PathType Leaf)) {
        throw "ai/pyproject.toml was not found under project root '$projectRoot'"
    }

    # 默认回归固定使用兼容角色，避免调用者残留的 APP_ROLE/就绪文件
    # 把零费用测试意外变成专用 Worker 生命周期测试。
    $env:APP_ROLE = "all"
    $env:APP_READY_FILE = ""
    $env:RUN_DATABASE_TESTS = "0"
    $env:RUN_PYTHON_TESTS = "0"
    $env:EMBEDDING_WORKER_ENABLED = "false"
    $env:SEMANTIC_SEARCH_ENABLED = "false"
    $env:ANSWER_ENABLED = "false"
    $env:ANSWER_JOBS_ENABLED = "false"

    # 使用不可用的占位值覆盖 .env，证明默认回归不依赖真实密钥或数据库密码。
    $env:OPENAI_API_KEY = "regression-disabled"
    $env:DASHSCOPE_API_KEY = "regression-disabled"
    $env:DB_PASSWORD = "regression-placeholder-not-a-real-password"

    Invoke-RegressionStep `
        -Name "go_format" `
        -Description "check Go source formatting without modifying files" `
        -Results $stepResults `
        -Action {
            $goFiles = Get-ChildItem -LiteralPath $backendDirectory `
                -Recurse `
                -File `
                -Filter "*.go" |
                Sort-Object FullName
            if ($goFiles.Count -eq 0) {
                throw "no Go source files were found"
            }

            # Windows 对单条命令行长度有限制。项目文件增多后，如果把全部绝对
            # 路径一次性交给 gofmt，会在真正执行格式检查前就启动失败。
            # 分批只改变进程调用方式，不改变“所有 Go 文件必须已格式化”的门禁。
            $batchSize = 50
            $unformattedFiles = New-Object 'System.Collections.Generic.List[string]'
            for ($offset = 0; $offset -lt $goFiles.Count; $offset += $batchSize) {
                $lastIndex = [Math]::Min(
                    $offset + $batchSize - 1,
                    $goFiles.Count - 1
                )
                $batch = @(
                    $goFiles[$offset..$lastIndex] |
                        ForEach-Object { $_.FullName }
                )
                $batchOutput = & gofmt -l @batch
                Assert-LastExitCode -Description "gofmt check"
                foreach ($file in $batchOutput) {
                    if (-not [string]::IsNullOrWhiteSpace([string]$file)) {
                        [void]$unformattedFiles.Add([string]$file)
                    }
                }
            }
            if ($unformattedFiles.Count -gt 0) {
                throw "Go formatting check failed: $($unformattedFiles -join ', ')"
            }
        }

    Invoke-RegressionStep `
        -Name "go_test" `
        -Description "run deterministic Go unit and local integration tests" `
        -Results $stepResults `
        -Action {
            Push-Location $backendDirectory
            try {
                & go test -count=1 ./...
                Assert-LastExitCode -Description "go test"
            }
            finally {
                Pop-Location
            }
        }

    Invoke-RegressionStep `
        -Name "go_vet" `
        -Description "run Go static analysis" `
        -Results $stepResults `
        -Action {
            Push-Location $backendDirectory
            try {
                & go vet ./...
                Assert-LastExitCode -Description "go vet"
            }
            finally {
                Pop-Location
            }
        }

    Invoke-RegressionStep `
        -Name "python_test" `
        -Description "run Python document-processing unit and CLI contract tests" `
        -Results $stepResults `
        -Action {
            Push-Location $aiDirectory
            try {
                & python -m unittest discover -s tests -v
                Assert-LastExitCode -Description "Python unittest"
            }
            finally {
                Pop-Location
            }
        }

    Invoke-RegressionStep `
        -Name "compose_config" `
        -Description "validate default and remote-worker Docker Compose profiles without starting containers" `
        -Results $stepResults `
        -Action {
            Push-Location $projectRoot
            try {
                & docker compose config --quiet
                Assert-LastExitCode -Description "default docker compose config"
                & docker compose `
                    --profile embedding `
                    --profile answer `
                    config `
                    --quiet
                Assert-LastExitCode -Description "full-profile docker compose config"
            }
            finally {
                Pop-Location
            }
        }
}
catch {
    $operationError = $_
}
finally {
    Restore-ProcessEnvironment -Saved $savedEnvironment
    Set-Location $originalLocation

    $finishedAt = [DateTime]::UtcNow
    $report = [ordered]@{
        schema_version          = 1
        suite                  = "backend-default"
        status                 = if ($null -eq $operationError) { "passed" } else { "failed" }
        started_at_utc         = $startedAt.ToString("o")
        finished_at_utc        = $finishedAt.ToString("o")
        duration_ms            = [int64]($finishedAt - $startedAt).TotalMilliseconds
        project_root           = $projectRoot
        remote_ai_enabled      = $false
        database_tests_enabled = $false
        go_python_integration_tests_enabled = $false
        steps                  = $stepResults
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
    Write-Warning "Backend default regression failed. Report: $reportPath"
    throw $operationError
}

Write-Host ""
Write-Host "Backend default regression passed."
Write-Host "Remote AI calls: disabled"
Write-Host "Real database tests: disabled"
Write-Host "Report: $reportPath"
Write-Output $reportPath
