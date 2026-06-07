param(
    [string]$OutDir = "loadtest/results/mqadmin_levels",
    [string]$Level = "l1",
    [int]$DurationSec = 100,
    [string]$NameSrv = "127.0.0.1:9876",
    [string]$MqadminPath = "C:\middleware\rocketmq\bin\mqadmin.cmd",
    [int]$StartRPS = 80,
    [int]$Stage1 = 240,
    [int]$Stage2 = 320,
    [int]$Stage3 = 420,
    [int]$Stage4 = 220,
    [string]$StageDur = "20s"
)

$ErrorActionPreference = "Stop"
if (-not (Test-Path $OutDir)) {
    New-Item -ItemType Directory -Path $OutDir | Out-Null
}

$outDirAbs = [System.IO.Path]::GetFullPath($OutDir)

$lagCsv = Join-Path $outDirAbs ("mq_lag_{0}.csv" -f $Level)
$jsonFile = Join-Path $outDirAbs ("e2e_{0}.json" -f $Level)
$samplerScript = Join-Path $PSScriptRoot "sample_mqadmin_lag.ps1"

$job = Start-Job -ScriptBlock {
    param($path,$dur,$ns,$mq,$scriptPath)
    powershell.exe -NoProfile -ExecutionPolicy Bypass -File $scriptPath -OutFile $path -DurationSec $dur -IntervalSec 5 -NameSrv $ns -MqadminPath $mq | Out-Null
} -ArgumentList $lagCsv,$DurationSec,$NameSrv,$MqadminPath,$samplerScript

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
Receive-Job $job -ErrorAction SilentlyContinue | Out-Null
Remove-Job $job | Out-Null

Write-Output "DONE level=$Level"
Write-Output "JSON=$jsonFile"
Write-Output "LAG=$lagCsv"
