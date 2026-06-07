#!/usr/bin/env bash
set -euo pipefail

CONFIG_PATH="${1:-loadtest/benchmark.config.json}"
K6_BIN="${K6_BIN:-k6}"
K6_SQL_BIN="${K6_SQL_BIN:-k6}"
DB_READ_MODE="${DB_READ_MODE:-k6-fallback}"

summary_dir=$(jq -r '.common.summary_dir // "loadtest/results"' "$CONFIG_PATH")
ts=$(date +%Y%m%d_%H%M%S)
out_dir="$summary_dir/$ts"
mkdir -p "$out_dir"

run_k6() {
  local bin="$1"
  local script="$2"
  local summary="$3"
  "$bin" run "$script" --summary-export "$summary"
}

case "$DB_READ_MODE" in
  xk6)
    run_k6 "$K6_SQL_BIN" loadtest/benchmark_db_read.js "$out_dir/db_read.summary.json"
    ;;
  k6-fallback)
    run_k6 "$K6_BIN" loadtest/benchmark_db_read_k6_fallback.js "$out_dir/db_read.summary.json"
    ;;
  skip)
    echo "[SKIP] DB read benchmark"
    ;;
  *)
    echo "Unsupported DB_READ_MODE=$DB_READ_MODE, expected: xk6|k6-fallback|skip"
    exit 1
    ;;
esac
run_k6 "$K6_BIN" loadtest/benchmark_app_read.js "$out_dir/app_read.summary.json"
run_k6 "$K6_BIN" loadtest/benchmark_e2e_tps.js "$out_dir/e2e_tps.summary.json"

echo "Report output dir: $out_dir"
