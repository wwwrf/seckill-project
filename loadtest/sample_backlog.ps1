param(
    [string]$OutFile = "loadtest/results/backlog_sample.csv",
    [int]$IntervalSec = 5,
    [int]$DurationSec = 120,
    [string]$MysqlHost = "127.0.0.1",
    [int]$MysqlPort = 3306,
    [string]$MysqlUser = "root",
    [string]$MysqlPassword = "@Wrf120855",
    [string]$MysqlDB = "ecommerce_db"
)

$ErrorActionPreference = "Stop"

$dir = Split-Path -Parent $OutFile
if ($dir -and -not (Test-Path $dir)) {
    New-Item -ItemType Directory -Path $dir | Out-Null
}

"ts,tx_outbox_pending,tx_outbox_dead,timeout_outbox_pending,orders_pending" | Set-Content -Path $OutFile -Encoding UTF8

$mysqlExe = "mysql"
if (-not (Get-Command $mysqlExe -ErrorAction SilentlyContinue)) {
    throw "mysql client not found in PATH"
}

$loops = [Math]::Ceiling($DurationSec / [double]$IntervalSec)
for ($i = 0; $i -lt $loops; $i++) {
    $ts = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
    $sql = @"
SELECT
  (SELECT COUNT(*) FROM seckill_tx_tasks WHERE status = 0) AS tx_outbox_pending,
  (SELECT COUNT(*) FROM seckill_tx_tasks WHERE status = 2) AS tx_outbox_dead,
  (SELECT COUNT(*) FROM order_timeout_tasks WHERE status = 0) AS timeout_outbox_pending,
  (SELECT COUNT(*) FROM orders WHERE status = 0) AS orders_pending;
"@

    $row = & $mysqlExe --host=$MysqlHost --port=$MysqlPort --user=$MysqlUser --password=$MysqlPassword --database=$MysqlDB --batch --raw --skip-column-names -e $sql
    if ($LASTEXITCODE -ne 0 -or -not $row) {
        "$ts,,,," | Add-Content -Path $OutFile -Encoding UTF8
    } else {
        "$ts,$row" | Add-Content -Path $OutFile -Encoding UTF8
    }

    Start-Sleep -Seconds $IntervalSec
}

Write-Output "DONE: $OutFile"
