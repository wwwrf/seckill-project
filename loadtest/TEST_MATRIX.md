# LoadTest 测试类型矩阵（精简版）

## 1. 推荐脚本（保留 3+1 套核心口径）

- benchmark_db_read.js：裸 DB 读上限（QPS）
  - 覆盖：仅 MySQL 读查询（xk6-sql 直连）
  - 指标：QPS、P95/P99、错误率

- benchmark_db_read_k6_fallback.js：DB 读替代基线（仅 k6）
  - 覆盖：通过 HTTP 读接口近似施压 DB
  - 指标：RPS/QPS、P95/P99、错误率
  - 注意：不等价于直连 MySQL 压测

- benchmark_app_read.js：应用层热点读（RPS/QPS）
  - 覆盖：商品/活动读接口（走 L1/L2/L3 缓存）
  - 指标：RPS/QPS、P95/P99、错误率

- benchmark_e2e_tps.js：全链路交易闭环（TPS）
  - 覆盖：下单 -> 轮询 -> 支付
  - 指标：TPS、端到端 P95/P99、错误率与业务分布

## 2. 判读口径（必须按场景）

- 热点读：应用层（含 Redis 缓存）QPS/RPS 应 >= 裸 DB 读 QPS
- 交易闭环：TPS 必然低于同口径 QPS/RPS，但要观察稳定收敛（P95/P99、错误率、积压）

## 3. 推荐执行顺序

1. benchmark_db_read.js（有 xk6）或 benchmark_db_read_k6_fallback.js（仅 k6）
2. benchmark_app_read.js
3. benchmark_e2e_tps.js

## 4. 一键执行

- PowerShell: `powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -DbReadMode k6-fallback`
- PowerShell (xk6): `powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -DbReadMode xk6 -K6SqlBin .\\k6-sql.exe`
