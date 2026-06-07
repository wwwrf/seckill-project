param(
    [string]$OutFile = "loadtest/results/mqadmin_lag.csv",
    [int]$IntervalSec = 5,
    [int]$DurationSec = 100,
    [string]$NameSrv = "127.0.0.1:9876",
    [string]$MqadminPath = "C:\middleware\rocketmq\bin\mqadmin.cmd",
    [string]$TxGroup = "seckill_tx_consumer_group",
    [string]$TimeoutGroup = "seckill_order_timeout_consumer_group"
)

$ErrorActionPreference = "Continue"
if ($null -ne (Get-Variable -Name PSNativeCommandUseErrorActionPreference -ErrorAction SilentlyContinue)) {
    $PSNativeCommandUseErrorActionPreference = $false
}

function Get-ConsumerTotals([string]$group) {
    $res = @{
        diff = -1
        inflight = -1
    }

    try {
        $txt = & $MqadminPath consumerProgress -g $group -n $NameSrv | Out-String
        $m1 = [regex]::Match($txt, 'Consume Diff Total:\s*(\d+)')
        if ($m1.Success) { $res.diff = [int64]$m1.Groups[1].Value }
        $m2 = [regex]::Match($txt, 'Consume Inflight Total:\s*(\d+)')
        if ($m2.Success) { $res.inflight = [int64]$m2.Groups[1].Value }
    } catch {
        # keep defaults
    }

    return $res
}

function Get-TopicMaxOffset([string]$topicEscaped) {
    # Returns @{exists=0/1; max=-1 if unknown}
    $ret = @{ exists = 0; max = -1 }
    try {
        $txt = & $MqadminPath topicStatus -t $topicEscaped -n $NameSrv 2>$null | Out-String
        if ($txt -match 'No topic route info') {
            return $ret
        }

        $lines = $txt -split "`r?`n"
        foreach ($line in $lines) {
            if ($line -match '^\s*broker-') {
                $parts = ($line -split '\s+') | Where-Object { $_ -ne '' }
                # Expected: broker-a qid min max
                if ($parts.Length -ge 4) {
                    $ret.exists = 1
                    $ret.max = [int64]$parts[3]
                    break
                }
            }
        }
    } catch {
        # keep defaults
    }
    return $ret
}

$dir = Split-Path -Parent $OutFile
if ($dir -and -not (Test-Path $dir)) {
    New-Item -ItemType Directory -Path $dir | Out-Null
}

"ts,tx_diff,tx_inflight,timeout_diff,timeout_inflight,retry_tx_max,retry_timeout_max,dlq_tx_exists,dlq_timeout_exists" | Set-Content -Path $OutFile -Encoding UTF8

$retryTxTopic = "%%RETRY%%$TxGroup"
$retryTimeoutTopic = "%%RETRY%%$TimeoutGroup"
$dlqTxTopic = "%%DLQ%%$TxGroup"
$dlqTimeoutTopic = "%%DLQ%%$TimeoutGroup"

$loops = [Math]::Ceiling($DurationSec / [double]$IntervalSec)
for ($i = 0; $i -lt $loops; $i++) {
    $ts = Get-Date -Format "yyyy-MM-dd HH:mm:ss"

    $tx = Get-ConsumerTotals -group $TxGroup
    $timeout = Get-ConsumerTotals -group $TimeoutGroup

    $retryTx = Get-TopicMaxOffset -topicEscaped $retryTxTopic
    $retryTimeout = Get-TopicMaxOffset -topicEscaped $retryTimeoutTopic
    $dlqTx = Get-TopicMaxOffset -topicEscaped $dlqTxTopic
    $dlqTimeout = Get-TopicMaxOffset -topicEscaped $dlqTimeoutTopic

    "$ts,$($tx.diff),$($tx.inflight),$($timeout.diff),$($timeout.inflight),$($retryTx.max),$($retryTimeout.max),$($dlqTx.exists),$($dlqTimeout.exists)" | Add-Content -Path $OutFile -Encoding UTF8
    Start-Sleep -Seconds $IntervalSec
}

Write-Output "DONE: $OutFile"
