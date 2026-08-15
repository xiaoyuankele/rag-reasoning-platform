[CmdletBinding()]
param(
    # Keep the Python command configurable because a rebuilt Windows machine may have
    # more than one Python installation on PATH.
    [string]$PythonExecutable = "python",

    # Container lifecycle verification rebuilds and starts the backend container, so it
    # must be requested explicitly instead of becoming a hidden default side effect.
    [switch]$IncludeContainerLifecycle,

    [ValidateRange(1, 65535)]
    [int]$ContainerHostPort = 18080,

    # Reports live in a Git-ignored directory by default.
    [string]$ReportDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Invoke-AcceptanceStep {
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

function Invoke-ChildPowerShellScript {
    param(
        [Parameter(Mandatory)]
        [string]$LiteralPath,

        [string[]]$Arguments = @()
    )

    # Starting a fresh process proves that each child script can run independently and
    # prevents one suite's local variables from leaking into the next suite.
    & powershell.exe `
        -NoProfile `
        -ExecutionPolicy Bypass `
        -File $LiteralPath `
        @Arguments

    if ($LASTEXITCODE -ne 0) {
        throw "acceptance child script failed with exit code $LASTEXITCODE`: $LiteralPath"
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

$projectRoot = [System.IO.Path]::GetFullPath(
    (Join-Path $PSScriptRoot "..\..")
)
$defaultRegressionScript = Join-Path $PSScriptRoot "run-backend-regression.ps1"
$localIntegrationScript = Join-Path $PSScriptRoot "run-backend-local-integration.ps1"
$containerLifecycleScript = Join-Path `
    $projectRoot `
    "scripts\maintenance\verify-container-lifecycle.ps1"

if ([string]::IsNullOrWhiteSpace($ReportDirectory)) {
    $ReportDirectory = Join-Path $projectRoot "chatgpt\运行产物\回归"
}
else {
    $ReportDirectory = [System.IO.Path]::GetFullPath($ReportDirectory)
}

$requiredScripts = @(
    $defaultRegressionScript,
    $localIntegrationScript
)
if ($IncludeContainerLifecycle) {
    $requiredScripts += $containerLifecycleScript
}

foreach ($requiredScript in $requiredScripts) {
    if (-not (Test-Path -LiteralPath $requiredScript -PathType Leaf)) {
        throw "required acceptance script was not found: $requiredScript"
    }
}

$startedAt = [DateTime]::UtcNow
$reportName = "backend-release-$($startedAt.ToString('yyyyMMddTHHmmssZ')).json"
$reportPath = Join-Path $ReportDirectory $reportName
$stepResults = [System.Collections.Generic.List[object]]::new()
$operationError = $null
$originalLocation = Get-Location

try {
    Set-Location $projectRoot

    Invoke-AcceptanceStep `
        -Name "default_regression" `
        -Description "run the fast deterministic backend regression" `
        -Results $stepResults `
        -Action {
            Invoke-ChildPowerShellScript `
                -LiteralPath $defaultRegressionScript
        }

    Invoke-AcceptanceStep `
        -Name "local_integration" `
        -Description "run PostgreSQL and Go/Python integration in a disposable database" `
        -Results $stepResults `
        -Action {
            Invoke-ChildPowerShellScript `
                -LiteralPath $localIntegrationScript `
                -Arguments @("-PythonExecutable", $PythonExecutable)
        }

    if ($IncludeContainerLifecycle) {
        Invoke-AcceptanceStep `
            -Name "container_lifecycle" `
            -Description "rebuild the backend image and verify shutdown and recovery" `
            -Results $stepResults `
            -Action {
                Invoke-ChildPowerShellScript `
                    -LiteralPath $containerLifecycleScript `
                    -Arguments @("-HostPort", [string]$ContainerHostPort)
            }
    }
}
catch {
    $operationError = $_
}
finally {
    Set-Location $originalLocation

    $finishedAt = [DateTime]::UtcNow
    $report = [ordered]@{
        schema_version                       = 1
        suite                                = "backend-release-acceptance"
        status                               = if ($null -eq $operationError) { "passed" } else { "failed" }
        started_at_utc                       = $startedAt.ToString("o")
        finished_at_utc                      = $finishedAt.ToString("o")
        duration_ms                          = [int64]($finishedAt - $startedAt).TotalMilliseconds
        project_root                         = $projectRoot
        default_regression_included          = $true
        disposable_database_suite_included   = $true
        container_lifecycle_included         = [bool]$IncludeContainerLifecycle
        remote_ai_enabled                    = $false
        real_external_pdf_automated           = $false
        steps                                = $stepResults
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
    Write-Warning "Backend release acceptance failed. Report: $reportPath"
    throw $operationError
}

Write-Host ""
Write-Host "Backend release acceptance passed."
Write-Host "Default regression: enabled"
Write-Host "Disposable database integration: enabled"
Write-Host "Container lifecycle: $([bool]$IncludeContainerLifecycle)"
Write-Host "Remote AI calls: disabled"
Write-Host "Report: $reportPath"
Write-Output $reportPath
