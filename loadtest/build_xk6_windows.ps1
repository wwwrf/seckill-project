param(
    [string]$Output = "k6-sql.exe"
)

$ErrorActionPreference = "Stop"

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go not found in PATH. Please install Go first."
}

Write-Host "[INFO] Installing xk6..."
go install go.k6.io/xk6/cmd/xk6@latest

$goBin = Join-Path (go env GOPATH) "bin"
$xk6 = Join-Path $goBin "xk6.exe"
if (-not (Test-Path $xk6)) {
    throw "xk6.exe not found at $xk6"
}

Write-Host "[INFO] Building k6 with SQL extensions..."
& $xk6 build --with github.com/grafana/xk6-sql --with github.com/grafana/xk6-sql-driver-mysql --output $Output

Write-Host "[DONE] Built $Output"
