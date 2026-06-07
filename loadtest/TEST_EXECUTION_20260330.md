# 测试执行记录（2026-03-30）

## 测试范围

- 构建与基础测试
- API 冒烟测试（公开接口）
- API 业务闭环测试（登录/秒杀/查询/支付）
- Agent 客服接口测试
- 管理接口测试
- 标准压测（三套脚本）

## 测试前提

- Redis：已启动（用户确认）
- RocketMQ：已启动（用户确认）
- 数据准备：已执行 `go run cmd/seed/main.go`（用户确认）
- 服务启动：`./api.exe`（本次测试由我启动）
- 压测前调整：`config/local.yaml` 中 `ratelimit.ip.enabled=false`（避免压测被 IP 限流污染）

## 结果汇总

| 项目 | 结果 | 关键数据 | 结论 |
| --- | --- | --- | --- |
| Go 全量测试 | 通过 | `go test ./...` 全部 `no test files` | 当前仓库无单元测试用例，需要后续补齐 |
| 构建检查 | 通过 | `go build -o api.exe ./cmd/api` 成功 | 可构建 |
| 公开接口冒烟 | 通过 | `/ping` 200；`/api/v1/product/1` 200；`/api/v1/activity/1` 200 | 公开读接口可用 |
| 鉴权业务闭环 | 通过 | 登录成功；秒杀返回 `order_no`；结果 `SUCCESS`；支付成功 | 核心交易链路可用 |
| Agent 咨询接口 | 通过 | `/api/v1/service/chat` 返回 consult agent 响应 | 咨询路由可用 |
| Agent 投诉接口 | 通过 | `/api/v1/service/complaint` 返回 RAG 证据与建议 | 投诉 RAG 可用 |
| 管理预热接口 | 通过 | `/api/v1/admin/warmup` 返回 success | 后台预热接口可用 |
| DB 读替代基线 | 通过 | RPS/QPS=899.98；P95=2.00ms；错误率=0.00% | 近似 DB 读压力稳定 |
| 应用热点读 | 通过 | RPS/QPS=1199.99；P95≈0ms；P99≈0.53ms；错误率=0.00% | 应用读性能优于 DB 替代基线 |
| 全链路交易 | 有风险 | RPS=259.80；TPS=3.15；P95=618ms；P99≈1074ms；HTTP 错误率=4.73% | 接近阈值，且库存耗尽导致大量失败场景 |

## 压测执行明细

- 执行命令：`powershell -ExecutionPolicy Bypass -File loadtest/run_standard.ps1 -DbReadMode k6-fallback`
- 结果目录：`loadtest/results/20260330_235330/`
- 数据来源：
  - `db_read.summary.json`
  - `app_read.summary.json`
  - `e2e_tps.summary.json`

## 问题与风险

1. `benchmark_*` 脚本中的 `handleSummary()` 对部分指标字段做 `toFixed` 时出现空值异常（控制台出现 TypeError），但不影响主流程执行与 summary-export 产出。
2. 本轮 e2e 场景中 `txn_sold_out` 很高（23519），`pay` 校验失败较多（1589），说明在当前库存与压测强度下，失败流量占比偏高。
3. 当前 `go test ./...` 无实际测试用例，自动化回归保障不足。

## 本轮结论

1. 功能层面：公开接口、交易闭环、Agent、管理接口均可用。
2. 性能层面：热点读吞吐显著高于 DB 替代基线，符合预期。
3. 交易层面：TPS 明显低于 QPS/RPS，符合预期；但在高并发下失败流量占比偏高，建议后续分库存档位与分阶段压力继续验证。
