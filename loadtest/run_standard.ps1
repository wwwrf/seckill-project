param(
    [string]$ConfigPath = "loadtest/benchmark.config.json",
    [string]$K6Bin = "k6",
    [string]$K6SqlBin = "k6",
    [ValidateSet("xk6", "k6-fallback", "skip")]
    [string]$DbReadMode = "k6-fallback"
)

$ErrorActionPreference = "Stop"

function Ensure-Dir([string]$Path) {
    if (-not (Test-Path -Path $Path)) {
        New-Item -Path $Path -ItemType Directory | Out-Null
    }
}

function Read-JsonFile([string]$Path) {
    if (-not (Test-Path -Path $Path)) {
        throw "Config file not found: $Path"
    }
    return Get-Content -Path $Path -Raw | ConvertFrom-Json
}

function Get-MetricNumber($obj, [string]$metric, [string]$field, [double]$defaultValue = 0) {
    try {
        $m = $obj.metrics.$metric
        if ($null -eq $m) { return $defaultValue }
        $v = $null
        if ($m.PSObject.Properties.Name -contains "values") {
            $v = $m.values.$field
        } else {
            # k6 newer summary schema stores stats directly on metric object.
            $v = $m.$field
        }
        if ($null -eq $v) { return $defaultValue }
        return [double]$v
    } catch {
        return $defaultValue
    }
}

function Get-MetricCount($obj, [string]$metric) {
    return [int64](Get-MetricNumber $obj $metric "count" 0)
}

function Write-Section([System.Text.StringBuilder]$sb, [string]$title, [hashtable]$pairs) {
    [void]$sb.AppendLine("## $title")
    [void]$sb.AppendLine("")
    [void]$sb.AppendLine("| Metric | Value |")
    [void]$sb.AppendLine("| --- | --- |")
    foreach ($k in $pairs.Keys) {
        [void]$sb.AppendLine("| $k | $($pairs[$k]) |")
    }
    [void]$sb.AppendLine("")
}

$config = Read-JsonFile -Path $ConfigPath
$summaryDir = if ($config.common.summary_dir) { [string]$config.common.summary_dir } else { "loadtest/results" }
$ts = Get-Date -Format "yyyyMMdd_HHmmss"
$outDir = Join-Path $summaryDir $ts
Ensure-Dir -Path $outDir

Write-Host "[INFO] Summary output dir: $outDir"

$dbSummary = Join-Path $outDir "db_read.summary.json"
$appSummary = Join-Path $outDir "app_read.summary.json"
$e2eSummary = Join-Path $outDir "e2e_tps.summary.json"

switch ($DbReadMode) {
    "xk6" {
        Write-Host "[RUN] benchmark_db_read.js (xk6 direct MySQL)"
        & $K6SqlBin run loadtest/benchmark_db_read.js --summary-export $dbSummary
    }
    "k6-fallback" {
        Write-Host "[RUN] benchmark_db_read_k6_fallback.js (plain k6 API fallback)"
        & $K6Bin run loadtest/benchmark_db_read_k6_fallback.js --summary-export $dbSummary
    }
    "skip" {
        Write-Host "[SKIP] DB read benchmark"
    }
}

Write-Host "[RUN] benchmark_app_read.js"
& $K6Bin run loadtest/benchmark_app_read.js --summary-export $appSummary

Write-Host "[RUN] benchmark_e2e_tps.js"
& $K6Bin run loadtest/benchmark_e2e_tps.js --summary-export $e2eSummary

$sb = New-Object System.Text.StringBuilder
[void]$sb.AppendLine("# Standard Benchmark Summary")
[void]$sb.AppendLine("")
[void]$sb.AppendLine("- Generated At: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')")
[void]$sb.AppendLine("- Config: $ConfigPath")
[void]$sb.AppendLine("")

if (($DbReadMode -ne "skip") -and (Test-Path $dbSummary)) {
    $db = Read-JsonFile -Path $dbSummary
    $dbQPS = if ($DbReadMode -eq "xk6") { Get-MetricNumber $db "iterations" "rate" } else { Get-MetricNumber $db "http_reqs" "rate" }
    $dbP95 = if ($DbReadMode -eq "xk6") { Get-MetricNumber $db "db_read_latency_ms" "p(95)" } else { Get-MetricNumber $db "db_fallback_read_latency_ms" "p(95)" }
    $dbP99 = if ($DbReadMode -eq "xk6") { Get-MetricNumber $db "db_read_latency_ms" "p(99)" } else { Get-MetricNumber $db "db_fallback_read_latency_ms" "p(99)" }
    $dbErr = if ($DbReadMode -eq "xk6") { Get-MetricCount $db "db_read_err" } else { Get-MetricCount $db "db_fallback_read_err" }
    $dbOk = if ($DbReadMode -eq "xk6") { Get-MetricCount $db "db_read_ok" } else { Get-MetricCount $db "db_fallback_read_ok" }
    $dbTotal = $dbErr + $dbOk
    $dbErrRate = if ($dbTotal -gt 0) { "{0:N2}%" -f (100.0 * $dbErr / $dbTotal) } else { "N/A" }
    $dbTitle = if ($DbReadMode -eq "xk6") { "裸 DB 读上限 (benchmark_db_read.js)" } else { "DB 读替代基线 (benchmark_db_read_k6_fallback.js)" }

    Write-Section -sb $sb -title $dbTitle -pairs @{
        "QPS" = ("{0:N2}" -f $dbQPS)
        "RPS" = if ($DbReadMode -eq "xk6") { "N/A" } else { ("{0:N2}" -f $dbQPS) }
        "TPS" = "N/A"
        "P95(ms)" = ("{0:N2}" -f $dbP95)
        "P99(ms)" = ("{0:N2}" -f $dbP99)
        "ErrorRate" = $dbErrRate
    }

    if ($DbReadMode -eq "k6-fallback") {
        [void]$sb.AppendLine("Note: this baseline uses HTTP read path as DB pressure approximation, not direct MySQL benchmark.")
        [void]$sb.AppendLine("")
    }
}

if (Test-Path $appSummary) {
    $app = Read-JsonFile -Path $appSummary
    $appRPS = Get-MetricNumber $app "http_reqs" "rate"
    $appP95 = Get-MetricNumber $app "app_read_latency_ms" "p(95)"
    $appP99 = Get-MetricNumber $app "app_read_latency_ms" "p(99)"
    $appErrRate = 100 * (Get-MetricNumber $app "http_req_failed" "rate")

    Write-Section -sb $sb -title "App Hot Read (benchmark_app_read.js)" -pairs @{
        "RPS" = ("{0:N2}" -f $appRPS)
        "QPS" = ("{0:N2}" -f $appRPS)
        "TPS" = "N/A"
        "P95(ms)" = ("{0:N2}" -f $appP95)
        "P99(ms)" = ("{0:N2}" -f $appP99)
        "ErrorRate" = ("{0:N2}%" -f $appErrRate)
    }
}

if (Test-Path $e2eSummary) {
    $e2e = Read-JsonFile -Path $e2eSummary
    $e2eRPS = Get-MetricNumber $e2e "http_reqs" "rate"
    $e2eTPS = Get-MetricNumber $e2e "txn_success" "rate"
    $e2eP95 = Get-MetricNumber $e2e "txn_e2e_latency_ms" "p(95)"
    $e2eP99 = Get-MetricNumber $e2e "txn_e2e_latency_ms" "p(99)"
    $e2eErrRate = 100 * (Get-MetricNumber $e2e "http_req_failed" "rate")

    Write-Section -sb $sb -title "End-to-End Transaction (benchmark_e2e_tps.js)" -pairs @{
        "RPS" = ("{0:N2}" -f $e2eRPS)
        "QPS" = ("{0:N2}" -f $e2eRPS)
        "TPS" = ("{0:N2}" -f $e2eTPS)
        "P95(ms)" = ("{0:N2}" -f $e2eP95)
        "P99(ms)" = ("{0:N2}" -f $e2eP99)
        "ErrorRate" = ("{0:N2}%" -f $e2eErrRate)
    }
}

[void]$sb.AppendLine("## Criteria")
[void]$sb.AppendLine("")
[void]$sb.AppendLine("1. Verify app read QPS/RPS >= DB read baseline QPS.")
[void]$sb.AppendLine("2. Verify TPS < same-scope QPS/RPS and P95/P99 with error rate are acceptable.")
[void]$sb.AppendLine("")

$reportPath = Join-Path $outDir "summary.md"
Set-Content -Path $reportPath -Value $sb.ToString() -Encoding UTF8

Write-Host "[DONE] Standard benchmark report: $reportPath"
