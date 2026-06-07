param(
    [string]$K6Bin = "k6",
    [string]$K6SqlBin = "k6",
    [string]$BaseUrl = "http://127.0.0.1:8080",
    [string]$MysqlDsn = "root:root123456@tcp(127.0.0.1:3307)/ecommerce_db",
    [switch]$SkipDbRead,
    [switch]$SkipDbWrite,
    [switch]$SkipAppRead,
    [switch]$SkipSubmit,
    [switch]$SkipE2E
)

function Run-Step {
    param(
        [string]$Name,
        [string]$Command
    )
    Write-Host "=== $Name ==="
    Invoke-Expression $Command
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE"
    }
}

if (-not $SkipDbRead) {
    Run-Step "DB Read" "$K6SqlBin run loadtest/benchmark_db_read.js -e MYSQL_DSN=$MysqlDsn"
}

if (-not $SkipDbWrite) {
    Run-Step "DB Write" "$K6SqlBin run loadtest/benchmark_db_write.js -e MYSQL_DSN=$MysqlDsn"
}

if (-not $SkipAppRead) {
    Run-Step "App Read" "$K6Bin run loadtest/benchmark_app_read.js -e BASE_URL=$BaseUrl"
}

if (-not $SkipSubmit) {
    Run-Step "Seckill Submit" "$K6Bin run loadtest/benchmark_seckill_submit.js -e BASE_URL=$BaseUrl"
}

if (-not $SkipE2E) {
    Run-Step "E2E TPS" "$K6Bin run loadtest/benchmark_e2e_tps.js -e BASE_URL=$BaseUrl"
}
