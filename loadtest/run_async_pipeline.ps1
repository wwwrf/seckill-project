param(
    [string]$BaseUrl = "http://127.0.0.1:8080",
    [int]$ActivityId = 1,
    [int]$ProductId = 1,
    [string]$AdminToken = "change-this-admin-token-in-production",
    [string]$K6Bin = "C:\Program Files\k6\k6.exe",
    [string]$ResultDir = "",
    [int]$DrainTimeoutSeconds = 180,
    [int]$PollIntervalSeconds = 1
)

$ErrorActionPreference = "Stop"

function Get-Timestamp {
    Get-Date -Format "yyyyMMdd_HHmmss"
}

function Ensure-Dir([string]$Path) {
    if (-not (Test-Path $Path)) {
        New-Item -ItemType Directory -Path $Path | Out-Null
    }
}

function Get-Metrics {
    $url = "$BaseUrl/api/v1/admin/benchmark/metrics?activity_id=$ActivityId&product_id=$ProductId"
    $resp = Invoke-WebRequest -UseBasicParsing -Headers @{ "X-Admin-Token" = $AdminToken } -Uri $url
    return ($resp.Content | ConvertFrom-Json)
}

if ([string]::IsNullOrWhiteSpace($ResultDir)) {
    $ResultDir = Join-Path "C:\项目\电商平台\loadtest\results" (Get-Timestamp)
}
Ensure-Dir $ResultDir

$projectRoot = Split-Path -Parent $PSScriptRoot
$summaryPath = Join-Path $ResultDir "seckill_submit.summary.json"
$drainTracePath = Join-Path $ResultDir "async_drain_trace.csv"
$reportPath = Join-Path $ResultDir "async_pipeline_report.md"

$submitStart = Get-Date
Push-Location $projectRoot
& $K6Bin run ".\loadtest\benchmark_seckill_submit.js" `
    --summary-export $summaryPath `
    -e "BASE_URL=$BaseUrl"
Pop-Location
$submitEnd = Get-Date

$submitSummary = Get-Content $summaryPath -Raw | ConvertFrom-Json
$submitMetrics = $submitSummary.metrics
$submitDurationSeconds = [math]::Max(1.0, ($submitEnd - $submitStart).TotalSeconds)

$samples = New-Object System.Collections.Generic.List[object]
$deadline = (Get-Date).AddSeconds($DrainTimeoutSeconds)
$finalMetrics = $null

while ((Get-Date) -lt $deadline) {
    $body = Get-Metrics
    if ($body.code -ne 0) {
        throw "读取 benchmark metrics 失败: $($body.msg)"
    }

    $data = $body.data
    $sample = [pscustomobject]@{
        ts = (Get-Date).ToString("o")
        accepted_total = [int64]$data.accepted_total
        created_total = [int64]$data.created_total
        backlog_pending = [int64]$data.backlog_pending
        avg_create_latency_ms = [int64]$data.avg_create_latency_ms
    }
    $samples.Add($sample) | Out-Null
    $finalMetrics = $sample

    if ($sample.backlog_pending -eq 0) {
        break
    }

    Start-Sleep -Seconds $PollIntervalSeconds
}

$samples | Export-Csv -NoTypeInformation -Encoding UTF8 -Path $drainTracePath

$drainEnd = Get-Date
$totalPipelineSeconds = [math]::Max(1.0, ($drainEnd - $submitStart).TotalSeconds)
$drainDurationSeconds = [math]::Max(0.0, ($drainEnd - $submitEnd).TotalSeconds)

$ingressRps = [double]$submitMetrics.http_reqs.values.rate
$clientAcceptedCount = if ($submitMetrics.submit_ok) { [int64]$submitMetrics.submit_ok.values.count } else { 0 }
$clientAcceptedTps = [math]::Round($clientAcceptedCount / $submitDurationSeconds, 2)
$serverAcceptedTotal = if ($finalMetrics) { $finalMetrics.accepted_total } else { 0 }
$serverAcceptedTps = [math]::Round($serverAcceptedTotal / $submitDurationSeconds, 2)
$createdTotal = if ($finalMetrics) { $finalMetrics.created_total } else { 0 }
$createdTps = [math]::Round($createdTotal / $totalPipelineSeconds, 2)
$finalBacklog = if ($finalMetrics) { $finalMetrics.backlog_pending } else { -1 }
$avgCreateLatencyMs = if ($finalMetrics) { $finalMetrics.avg_create_latency_ms } else { 0 }
$submitP95 = if ($submitMetrics.submit_latency_ms) { [double]$submitMetrics.submit_latency_ms.values."p(95)" } else { 0 }
$submitP99 = if ($submitMetrics.submit_latency_ms) { [double]$submitMetrics.submit_latency_ms.values."p(99)" } else { 0 }

$report = @"
# Async Pipeline Benchmark

## Metric Definition

- Ingress RPS: k6 提交窗口内 HTTP 请求速率
- Accepted TPS: 成功完成 Redis 预扣并写入 Outbox 的吞吐
- Async Created TPS: 从开始施压到 backlog 清空的完整窗口内 DB 建单吞吐
- Drain Time: 停止施压后 backlog 清空所需时间
- Avg Create Latency: accepted -> DB 建单成功 的平均时延

## Result

- Submit window seconds: $([math]::Round($submitDurationSeconds, 2))
- Total pipeline seconds: $([math]::Round($totalPipelineSeconds, 2))
- Drain seconds: $([math]::Round($drainDurationSeconds, 2))
- Ingress RPS: $([math]::Round($ingressRps, 2))
- Client accepted total: $clientAcceptedCount
- Client accepted TPS: $clientAcceptedTps
- Server accepted total: $serverAcceptedTotal
- Server accepted TPS: $serverAcceptedTps
- Async created total: $createdTotal
- Async created TPS: $createdTps
- Submit P95 ms: $([math]::Round($submitP95, 2))
- Submit P99 ms: $([math]::Round($submitP99, 2))
- Avg create latency ms: $avgCreateLatencyMs
- Final backlog pending: $finalBacklog

## Artifacts

- k6 summary: $summaryPath
- drain trace: $drainTracePath
"@

Set-Content -Path $reportPath -Value $report -Encoding UTF8
Write-Output $report
