# 秒杀链路压测报告 v2.0（上限探索）

> 测试日期：2026-03-31  
> 环境：本地单机（Go API + MySQL + Redis + RocketMQ）  
> 目标：通过参数阶梯加压，定位本轮系统并发上限（读链路与全链路交易分别评估）

---

## 1. 本轮改动（为上限探索准备）

1. 连接池放宽（压测配置）

- MySQL: `maxOpenConns 500 -> 1200`，`maxIdleConns 200 -> 400`
- Redis: `poolSize 500 -> 1200`，`minIdleConns 100 -> 300`

1. 限流策略可配置化

- 新增用户限流配置：`ratelimit.user.enabled/rps/burst`
- 本轮设置：`rps=12`, `burst=24`

1. seed 数据规模上调（匹配高压脚本）

- 用户池：`50000`
- 秒杀库存：`30000`
- 历史订单：`150000`

1. e2e 脚本优化

- 用户选择改为按全局迭代序号轮转，降低早期“重复用户”噪声。

---

## 2. 与上一轮结果对比（核心）

| 维度 | 上一轮（20260330_235330） | 本轮（20260331_004912） | 变化 |
| --- | --- | --- | --- |
| 应用热点读 RPS | 1199.99 | 最高 9998.08 | +733% |
| 应用热点读错误率 | 0.00% | 0.00%（到 10k） | 持平（优秀） |
| 全链路 TPS | 3.15 | L1=13.30（稳定），L2=23.55（退化） | 显著提升 |
| 全链路 HTTP 错误率 | 4.74% | 0.00% | 明显改善 |
| 全链路 P95（txn） | 618ms | L1=1223ms；L2=8103ms；L3=9268ms | 高压下显著恶化 |

结论：

- 上一轮主要受限流与脚本噪声影响，TPS 偏低。
- 本轮在限流与用户池优化后，TPS 峰值有明显提升，但高压档出现严重排队超时，暴露了全链路真实瓶颈。

---

## 3. 本轮读链路阶梯压测（benchmark_app_read.js）

| 目标 RPS | 实际 RPS | HTTP 错误率 | HTTP P95(ms) | 结果 |
| --- | ---: | ---: | ---: | --- |
| 1200 | 1199.96 | 0.00% | 0.33 | 通过 |
| 1800 | 1799.96 | 0.00% | 0.36 | 通过 |
| 2400 | 2399.92 | 0.00% | 0.51 | 通过 |
| 3000 | 2999.94 | 0.00% | 0.52 | 通过 |
| 3600 | 3599.86 | 0.00% | 0.52 | 通过 |
| 5000 | 4999.84 | 0.00% | 0.53 | 通过 |
| 7000 | 6999.70 | 0.00% | 0.58 | 通过 |
| 10000 | 9998.08 | 0.00% | 1.95 | 通过（出现 dropped_iterations=1.60/s） |

读链路结论：

- 在本机环境下，读链路到 10k RPS 仍保持 0 错误。
- 10k 档开始出现少量 dropped iterations，说明注压侧/系统已接近注入边界，但尚未出现明显服务错误拐点。

---

## 4. 本轮全链路阶梯压测（benchmark_e2e_tps.js）

| 档位 | 压力参数（峰值） | RPS | TPS | HTTP 错误率 | 交易 P95(ms) | dropped/s | 主要退化信号 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| L1 | stage3=420/s | 324.05 | 13.30 | 0.00% | 1223 | 0.00 | 稳定 |
| L2 | stage3=700/s | 3211.85 | 23.55 | 0.00% | 8103 | 289.78 | poll_timeout=8355, VU 顶满 |
| L3 | stage3=1100/s | 2629.96 | 6.24 | 0.00% | 9268 | 427.20 | 进一步退化，TPS反降 |

补充失败构成：

- L1: `txn_success=1080`, `txn_failed=0`, `txn_poll_timeout=0`
- L2: `txn_success=2100`, `txn_failed=8355`, `txn_poll_timeout=8355`
- L3: `txn_success=557`, `txn_failed=6925`, `txn_poll_timeout=6925`

全链路结论：

- 稳定上限（满足 `txn_p95<1500ms`）约在 **TPS 13 左右（L1 档）**。
- 峰值上限可冲到 **TPS 23.55（L2 档）**，但延迟与超时显著恶化，不具备稳定生产意义。
- 当继续加压到 L3，系统进入拥塞退化区，TPS 反而下降。

---

## 5. 上限判定（本轮）

1. 读链路上限（本机）

- 本轮测试范围内，未触达服务错误上限；可报告“稳定 >= 10k RPS（0 错误）”。

1. 全链路交易上限（本机）

- 稳定上限：约 **13 TPS**（满足延迟阈值）。
- 极限峰值：约 **23.55 TPS**（高超时，不建议作为容量承诺）。

---

## 6. 建议的下一轮优化方向

1. 缩短排队链路

- 降低轮询间隔并引入推送/事件通知，减少 poll 风暴。

1. 按路径拆分限流

- `seckill`、`poll`、`pay` 分开限流，避免同一用户在一个桶内互相挤压。

1. 提升消费者与落库并行度

- 针对 MQ->DB 建单链路做消费并发和批处理优化，重点观察 `txn_poll_timeout`。

1. 固化双口径报告

- 稳定口径（SLA）与峰值口径（Stress）分开发布，避免“峰值结果”被误当稳定容量。

---

## 7. 本轮补充：MQ 积压监控（代理口径）

说明：

- 本机环境未安装 `mqadmin`，无法直接拉取 broker/group 的官方 lag。
- 本轮使用 DB 代理指标按 5s 采样形成 backlog 曲线：
  - `tx_outbox_pending`（seckill_tx_tasks status=0）
  - `tx_outbox_dead`（seckill_tx_tasks status=2）
  - `timeout_outbox_pending`（order_timeout_tasks status=0）
  - `orders_pending`（orders status=0）

### 7.1 档位曲线摘要（每 5s 采样）

| 档位 | 采样点数 | tx_outbox_pending 起点 | 终点 | 峰值 | orders_pending 起点 | 终点 | 峰值 | 结论 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| L1 | 20 | 0 | 0 | 0 | 29979 | 29979 | 29979 | 基本无积压增长 |
| L2 | 22 | 0 | 806 | 806 | 30040 | 30138 | 30139 | 出现中度积压爬升 |
| L3 | 20 | 0 | 1804 | 1804 | 29979 | 30203 | 30203 | 积压显著上升，进入拥塞区 |

### 7.2 曲线文件

- `loadtest/results/limit_20260331_mq/backlog_l1_curve.csv`
- `loadtest/results/limit_20260331_mq/backlog_l2_run.csv`
- `loadtest/results/limit_20260331_mq/backlog_l3_curve.csv`

---

## 8. 指标与名词释义

完整词典见：

- `loadtest/METRICS_GLOSSARY.md`

---

## 9. 2026-04-02 真实 mqadmin 口径复测（L1/L2/L3）

说明：

- 本次改为官方命令口径采样：`mqadmin consumerProgress` + `mqadmin topicStatus`。
- 采样脚本：`loadtest/sample_mqadmin_lag.ps1`；联动脚本：`loadtest/run_e2e_with_mqadmin.ps1`。
- 每档执行前均使用 `go run ./cmd/seed/main.go -users 50000 -stock 30000 -orders 150000` 重置测试基线。

### 9.1 全链路业务指标（同档 e2e）

| 档位 | RPS | TPS | 交易 P95(ms) | txn_success | txn_failed | txn_poll_timeout | dropped_iterations |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| L1 | 3082.75 | 127.29 | 9075 | 11192 | 877 | 877 | 10530 |
| L2 | 3468.47 | 127.59 | 9136 | 11131 | 584 | 584 | 17284 |
| L3 | 3644.59 | 141.61 | 8891 | 12340 | 0 | 0 | 23459 |

观察：

- 三档均出现高 `dropped_iterations`，说明注压端和被测端都处在高拥塞区。
- `txn_poll_timeout` 在 L1/L2 可见，L3 归零，表现出非线性抖动（与本地机资源竞争、轮询时序和消费者进度有关）。
- 该轮结果属于“极限压力区表现”，不等价于稳定 SLA 容量。

### 9.2 真实 MQ 积压/重试/死信（mqadmin）

| 档位 | tx_diff_max | tx_inflight_max | timeout_diff_max | timeout_inflight_max | retry_tx_max | retry_timeout_max | dlq_tx_exists | dlq_timeout_exists |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| L1 | 200 | 200 | 0 | 0 | 0 | 0 | 0 | 0 |
| L2 | 8 | 8 | 21 | 21 | 0 | 0 | 0 | 0 |
| L3 | 8 | 8 | 15 | 15 | 0 | 0 | 0 | 0 |

结论：

- 已观测到官方消费积压（`Consume Diff Total`）与 inflight 波动，说明链路在高压下确实出现短时 backlog。
- 本轮未观测到重试堆积（`%RETRY%` offset 峰值为 0），也未观测到死信主题存在（`%DLQ%` 未出现）。

### 9.3 产物文件

- `loadtest/results/mqadmin_levels/e2e_l1.json`
- `loadtest/results/mqadmin_levels/e2e_l2.json`
- `loadtest/results/mqadmin_levels/e2e_l3.json`
- `loadtest/results/mqadmin_levels/mq_lag_l1.csv`
- `loadtest/results/mqadmin_levels/mq_lag_l2.csv`
- `loadtest/results/mqadmin_levels/mq_lag_l3.csv`

---

## 10. 全测试方法与覆盖（精炼版）

测试方法：

1. 先压读链路（`benchmark_app_read.js`）定位缓存/查询路径上限。
2. 再压全链路交易（`benchmark_e2e_tps.js`）观察“下单-异步-支付”闭环。
3. 采用阶梯加压（L1/L2/L3）识别稳定区、退化区、拥塞区。
4. 每档重置 seed，避免“重复用户/脏库存”污染结论。
5. 并行采集业务指标（TPS/P95/timeout/dropped）与 MQ 官方指标（lag/inflight/retry/dlq）。

为什么这样测：

- 读链路与交易链路瓶颈位置不同，必须拆开测。
- 仅看 TPS 会掩盖拥塞与排队问题，必须结合 P95 与 `txn_poll_timeout`。
- 仅看业务日志会误判 MQ 状态，必须引入 `mqadmin` 官方口径验证。

已覆盖：

- 热点读吞吐与错误率。
- 全链路交易吞吐、延迟、超时与注压丢弃。
- MQ 消费差值、inflight、重试与死信主题状态。

尚未覆盖/残余盲区：

- 未接入持续化 exporter（Prometheus）做长时间趋势与告警。
- 未拆分到单路径限流压测（`seckill`/`poll`/`pay`）独立瓶颈。
- 未做多机/分布式场景，当前结论仅对本地单机有效。
- k6 当前存在高基数 tags 警告（>100k series），会放大压测端资源干扰，需在下一轮降基数后复测。
