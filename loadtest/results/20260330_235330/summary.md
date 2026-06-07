# Standard Benchmark Summary

- Generated At: 2026-03-30 23:59:42
- Config: loadtest/benchmark.config.json

## DB 璇绘浛浠ｅ熀绾?(benchmark_db_read_k6_fallback.js)

| Metric | Value |
| --- | --- |
| TPS | N/A |
| P95(ms) | 0.00 |
| P99(ms) | 0.00 |
| ErrorRate | N/A |
| RPS | 0.00 |
| QPS | 0.00 |

Note: this baseline uses HTTP read path as DB pressure approximation, not direct MySQL benchmark.

## App Hot Read (benchmark_app_read.js)

| Metric | Value |
| --- | --- |
| TPS | N/A |
| P95(ms) | 0.00 |
| P99(ms) | 0.00 |
| ErrorRate | 0.00% |
| RPS | 0.00 |
| QPS | 0.00 |

## End-to-End Transaction (benchmark_e2e_tps.js)

| Metric | Value |
| --- | --- |
| TPS | 0.00 |
| P95(ms) | 0.00 |
| P99(ms) | 0.00 |
| ErrorRate | 0.00% |
| RPS | 0.00 |
| QPS | 0.00 |

## Criteria

1. Verify app read QPS/RPS >= DB read baseline QPS.
2. Verify TPS < same-scope QPS/RPS and P95/P99 with error rate are acceptable.


