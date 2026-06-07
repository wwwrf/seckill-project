param(
    [string]$OutDir = "loadtest/results/limit_20260331_mq",
    [string]$Level = "l2",
    [int]$StartRPS = 120,
    [int]$Stage1 = 360,
    [int]$Stage2 = 520,
    [int]$Stage3 = 700,
    [int]$Stage4 = 350,
    [string]$StageDur = "20s",
    [string]$MysqlHost = "127.0.0.1",
    [int]$MysqlPort = 3306,
    [string]$MysqlUser = "root",
    [string]$MysqlPassword = "@Wrf120855",
    [string]$MysqlDB = "ecommerce_db"
)

$ErrorActionPreference = "Stop"
if (-not (Test-Path $OutDir)) {
    New-Item -ItemType Directory -Path $OutDir | Out-Null
}

$backlogFile = Join-Path $OutDir ("backlog_{0}.csv" -f $Level)
$jsonFile = Join-Path $OutDir ("e2e_{0}.json" -f $Level)

"ts,tx_outbox_pending,tx_outbox_dead,timeout_outbox_pending,orders_pending" | Set-Content -Path $backlogFile -Encoding UTF8

$sampleScript = {
    param($Path,$MyHost,$Port,$User,$Pass,$DB)
    $end = (Get-Date).AddSeconds(110)
    while ((Get-Date) -lt $end) {
        $ts = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
        $sql = @"
SELECT
  (SELECT COUNT(*) FROM seckill_tx_tasks WHERE status = 0) AS tx_outbox_pending,
  (SELECT COUNT(*) FROM seckill_tx_tasks WHERE status = 2) AS tx_outbox_dead,
  (SELECT COUNT(*) FROM order_timeout_tasks WHERE status = 0) AS timeout_outbox_pending,
  (SELECT COUNT(*) FROM orders WHERE status = 0) AS orders_pending;
"@
        $row = & mysql --host=$MyHost --port=$Port --user=$User --password=$Pass --database=$DB --batch --raw --skip-column-names -e $sql
        if ($LASTEXITCODE -ne 0 -or -not $row) {
            "$ts,,,," | Add-Content -Path $Path -Encoding UTF8
        } else {
            "$ts,$row" | Add-Content -Path $Path -Encoding UTF8
        }
        Start-Sleep -Seconds 5
    }
}

$job = Start-Job -ScriptBlock $sampleScript -ArgumentList $backlogFile,$MysqlHost,$MysqlPort,$MysqlUser,$MysqlPassword,$MysqlDB

k6 run loadtest/benchmark_e2e_tps.js `
    -e START_RPS=$StartRPS `
    -e RPS_STAGE_1=$Stage1 `
    -e RPS_STAGE_2=$Stage2 `
    -e RPS_STAGE_3=$Stage3 `
    -e RPS_STAGE_4=$Stage4 `
    -e STAGE_1=$StageDur `
    -e STAGE_2=$StageDur `
    -e STAGE_3=$StageDur `
    -e STAGE_4=$StageDur `
    --summary-export $jsonFile

Wait-Job $job | Out-Null
Receive-Job $job | Out-Null
Remove-Job $job | Out-Null

Write-Output "DONE level=$Level"
Write-Output "JSON=$jsonFile"
Write-Output "BACKLOG=$backlogFile"
