# 标准压测实施手册（异机真实压测）

本手册保留 3 套核心脚本，并提供 k6-only 的 DB 读替代基线，统一输出 QPS/RPS/TPS、P95/P99、错误率。

## 1. 脚本清单

- `loadtest/benchmark_db_read.js`
  - 目标：裸 MySQL 读上限（QPS）
  - 类型：xk6-sql 直连数据库（无 HTTP）

- `loadtest/benchmark_db_read_k6_fallback.js`
  - 目标：仅 k6 环境下的 DB 读替代基线
  - 类型：HTTP 读接口近似施压（非直连 MySQL）

- `loadtest/benchmark_app_read.js`
  - 目标：应用层热点读吞吐（RPS/QPS）
  - 类型：HTTP 读接口（走 L1/L2/L3 缓存）

- `loadtest/benchmark_e2e_tps.js`
  - 目标：全链路交易吞吐（TPS）与端到端时延
  - 类型：下单 -> 轮询 -> 支付

公共配置文件：`loadtest/benchmark.config.json`

- 三套脚本统一从该文件读取默认参数。
- 环境变量优先级高于配置文件，可用于临时覆盖。

## 2. 异机压测前检查

1. 服务机与压测机网络互通（API/MySQL/Redis/RocketMQ）。
2. 服务监听地址可被压测机访问（不要仅绑定 127.0.0.1）。
3. `config/local.yaml` 中中间件配置可控：
   - `ratelimit.ip.enabled`
   - `ratelimit.ip.rps`
   - `ratelimit.ip.burst`
4. 压测期间建议仅放宽 IP 限流，保留用户限流。
5. 压测机可访问 `users_token.csv`，并使用真实用户 token。

## 3. 运行命令

### 3.0 一键批量执行 + 自动汇总

```powershell
# 仅 k6（默认，DB基线走 fallback）
powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -DbReadMode k6-fallback

# 有 xk6-sql 时，启用直连 MySQL 压测
powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -DbReadMode xk6 -K6SqlBin ".\\k6-sql.exe"

# 跳过 DB 基线，仅跑应用层和全链路
powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -DbReadMode skip

# 指定自定义 k6 路径
powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -K6Bin ".\\k6.exe"
```

执行完成后会在 `loadtest/results/<timestamp>/` 输出：

- `db_read.summary.json`
- `app_read.summary.json`
- `e2e_tps.summary.json`
- `summary.md`（自动汇总报告）

### 3.1 裸 DB 读上限（QPS）

```powershell
# 仅一次：构建带 sql 插件的 k6
xk6 build --with github.com/grafana/xk6-sql --with github.com/grafana/xk6-sql-driver-mysql

# 运行
.\k6.exe run loadtest/benchmark_db_read.js `
  -e MYSQL_DSN="root:pass@tcp(10.0.0.10:3306)/ecommerce_db" `
  -e RATE_LIST=600 -e RATE_DETAIL=900
```

如果你只有 k6 没有 xk6，请改用：

```powershell
k6 run loadtest/benchmark_db_read_k6_fallback.js
```

说明：fallback 是 API 层近似基线，不等价于直连 MySQL 压测。

Windows 可用一键构建脚本生成 `k6-sql.exe`：

```powershell
powershell -ExecutionPolicy Bypass -File loadtest/build_xk6_windows.ps1 -Output k6-sql.exe
```

### 3.2 应用层热点读（RPS/QPS）

```powershell
k6 run loadtest/benchmark_app_read.js `
  -e BASE_URL="http://10.0.0.10:8080" `
  -e TARGET_RPS=1200 -e DURATION=2m
```

### 3.3 全链路交易（TPS）

```powershell
k6 run loadtest/benchmark_e2e_tps.js `
  -e BASE_URL="http://10.0.0.10:8080" `
  -e START_RPS=50 -e RPS_STAGE_1=120 -e RPS_STAGE_2=240 -e RPS_STAGE_3=320
```

## 4. 报告判读规则

1. 热点读场景：应用层（含 Redis）QPS/RPS 应 >= 裸 DB 读 QPS。
2. 交易闭环场景：TPS 应低于同口径 QPS/RPS。
3. 任何结论都要同时看 P95/P99 与错误率，避免只看吞吐。

## 5. 推荐执行顺序

1. `benchmark_db_read.js`（k6-only 环境用 `benchmark_db_read_k6_fallback.js` 代替）
2. `benchmark_app_read.js`
3. `benchmark_e2e_tps.js`

## 6. 产出建议

每轮压测记录以下字段：

- 环境：机器规格、网络、配置版本
- 参数：并发模型、持续时间、rate/vu
- 结果：QPS/RPS/TPS、P95/P99、错误率、错误码分布
- 结论：瓶颈位置（DB/缓存/MQ/应用）和下一步优化项
