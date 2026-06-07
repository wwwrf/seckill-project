# 压测指标与名词释义

## 1. 吞吐类指标

| 指标 | 含义 | 口径说明 |
| --- | --- | --- |
| RPS | Requests Per Second，每秒请求数 | 通常对应 HTTP 请求吞吐 |
| QPS | Queries Per Second，每秒查询/请求数 | 在本项目中读接口场景近似等同 RPS |
| TPS | Transactions Per Second，每秒成功交易数 | 在 e2e 脚本中等于 `txn_success.rate`，需完整闭环成功 |

## 2. 延迟类指标

| 指标 | 含义 | 口径说明 |
| --- | --- | --- |
| P50 | 50 分位延迟 | 一半请求不超过该值 |
| P90 | 90 分位延迟 | 90% 请求不超过该值 |
| P95 | 95 分位延迟 | 常用 SLA 指标 |
| P99 | 99 分位延迟 | 尾延迟敏感指标 |
| http_req_duration | HTTP 请求总耗时 | 含连接、等待、接收等时间 |
| txn_e2e_latency_ms | 交易端到端耗时 | 从发起 seckill 到 pay 成功的总耗时 |

## 3. 错误与失败类指标

| 指标 | 含义 | 常见原因 |
| --- | --- | --- |
| http_req_failed | HTTP 层失败率 | 超时、连接失败、状态码异常 |
| txn_failed | 交易失败数 | 轮询超时、支付失败、业务码失败 |
| txn_poll_timeout | 轮询窗口内未等到 SUCCESS | MQ/落库慢、排队拥塞、轮询窗口过短 |
| txn_repeated | 重复购买拦截次数 | 一人一单、同用户并发冲突 |
| txn_sold_out | 售罄次数 | 库存耗尽或库存预扣失败 |
| dropped_iterations | k6 丢弃的迭代数 | 目标注压超过当前可调度能力 |

## 4. k6 执行器与并发相关

| 名词 | 含义 | 在本项目中的使用 |
| --- | --- | --- |
| constant-arrival-rate | 固定到达率执行器 | 用于读链路，按目标 RPS 注压 |
| ramping-arrival-rate | 阶梯到达率执行器 | 用于 e2e，全链路逐级加压 |
| preAllocatedVUs | 预分配虚拟用户数 | 提前准备 worker，减少动态扩容抖动 |
| maxVUs | 最大虚拟用户数上限 | 到达上限后可能出现 dropped_iterations |
| VU 顶满 | 活跃 VU 达到 maxVUs | 表示压测端/系统进入拥塞区 |

## 5. 缓存与秒杀链路名词

| 名词 | 含义 | 实现位置 |
| --- | --- | --- |
| L1 缓存 | 进程内本地缓存（go-cache） | product/activity 查询热点数据 |
| L2 缓存 | Redis 缓存 | 跨实例共享缓存层 |
| L3 | MySQL 数据源 | 最终一致性数据来源 |
| singleflight | 并发请求合并机制 | 防止热 key 失效时缓存击穿 |
| Pending Hash | 预扣后待确认集合 | 追踪“已预扣未建单”条目 |

## 6. MQ 积压观测口径

| 指标 | 含义 | 说明 |
| --- | --- | --- |
| tx_outbox_pending | `seckill_tx_tasks` 待投递数量（status=0） | 生产侧积压代理指标 |
| tx_outbox_dead | `seckill_tx_tasks` dead 数量（status=2） | 投递失败需重放或人工处理 |
| timeout_outbox_pending | `order_timeout_tasks` 待补偿数量（status=0） | 延迟消息补偿积压代理指标 |
| orders_pending | `orders.status=0` 待支付订单数 | 业务积压与支付转化观察指标 |

说明：若无法直接使用 mqadmin/rocketmq exporter，本项目可先使用上述代理指标评估“积压趋势”。
