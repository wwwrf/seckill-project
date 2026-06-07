# CHECKLIST（当前交付状态）

## 1. 核心功能

- [x] 用户注册/登录（JWT）
- [x] 商品/活动查询三级缓存
- [x] 秒杀主链路（Redis Lua + 主消息 Outbox + MQ 异步建单）
- [x] 一人一单（Redis Set + DB 唯一索引兜底）
- [x] 订单列表/详情/支付
- [x] 30 分钟超时取消（MQ 延迟消息）

## 2. 一致性与补偿

- [x] pending 中间态记录（Redis 已扣、DB 未建）
- [x] Cron 悬空预扣补偿（>5m）
- [x] canceled/processing 标记防 Cron 与 MQ 赛跑
- [x] 延迟消息发送失败补偿（Outbox: order_timeout_tasks）
- [x] Outbox 定时重投补偿器
- [x] 主消息 Outbox（seckill_tx_tasks）
- [x] 主消息 Outbox 调度器 + dead 自动重放
- [x] pending 垃圾清理任务

## 3. 数据与约束

- [x] order_no 唯一索引
- [x] (user_id, activity_id) 唯一约束（防重复下单）
- [x] 活动库存字段使用无符号类型（防负值）

## 4. 可观测性

- [x] 结构化日志
- [x] pprof 路由
- [x] 监控文档（Prometheus/Grafana）
- [x] Seed Redis 预热自检（stock/bloom/pending/marker）
- [x] 启动配置摘要与关键配置校验（api main）

## 5. 压测体系

- [x] 统一压测配置（benchmark.config.json）
- [x] 直压 MySQL 读基线（benchmark_db_read.js，xk6-sql）
- [x] k6-only DB 读替代基线（benchmark_db_read_k6_fallback.js）
- [x] 应用层热点读压测（benchmark_app_read.js）
- [x] 全链路交易压测（benchmark_e2e_tps.js）
- [x] 一键批量执行与汇总（run_standard.ps1 / run_standard.sh）
- [x] 压测公共工具库（loadtest/lib/common.js）
- [x] 测试类型矩阵文档（loadtest/TEST_MATRIX.md）

## 6. 当前待做（非阻塞）

- [ ] 增加 Outbox 积压告警（pending/dead 阈值）
- [ ] 增加 Outbox 任务运维查询接口（按状态筛选/手动重放）
- [ ] 增加 Agent 客服链路专项压测脚本
- [ ] 提供 xk6 自定义二进制构建脚本（Windows 一键）
