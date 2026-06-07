# PROJECT_STRUCTURE（当前实现版）

## 1. 目录总览

```text
cmd/
  api/main.go                 # 启动入口，初始化 DB/Redis/MQ，启动消费者与定时任务，启动配置校验
  seed/main.go                # 批量压测数据初始化（用户+活动+Redis预热+历史订单）
  seed_single/main.go         # 单点调试数据初始化

api/handler/
  user_handler.go             # 注册/登录
  shop_handler.go             # 商品与活动查询
  seckill_handler.go          # 秒杀下单与结果轮询
  order_handler.go            # 订单列表/详情/支付
  admin_handler.go            # 预热与测试数据

internal/
  router/router.go            # 路由与中间件装配
  middleware/                 # Trace/JWT/限流
  service/                    # 业务编排层
  repository/
    db.go                     # DB 初始化与迁移
    order_repo.go             # 订单事务与状态流转
    seckill_tx_task_repo.go   # 主消息 Outbox 仓储
    order_timeout_task_repo.go# Outbox 任务仓储
    user_repo.go              # 用户仓储
    cache/
      seckill_cache.go        # 秒杀 Redis/Lua 与标记能力
      product_cache.go        # 三级缓存查询
  mq/consumer.go              # MQ 主链路与超时链路消费
  cron/
    pending_checker.go        # 悬空预扣扫描 + 垃圾清理
    tx_message_dispatcher.go  # 主消息 Outbox 调度发送
    tx_task_replayer.go       # 主消息 dead 自动重放
    timeout_message_compensator.go # Outbox 重投补偿器
    dead_task_replayer.go     # 超时消息 dead 自动重放
  model/
    order.go
    order_item.go
    seckill_activity.go
    stock_deduct_log.go
    seckill_tx_task.go
    order_timeout_task.go
    user.go
    product.go
    e/                        # 错误码、Key 常量、Topic 常量

pkg/
  auth/jwt.go
  mq/producer.go
  redis/redis.go
  logger/logger.go
  response/response.go
  utils/snowflake.go

loadtest/
  lib/common.js               # 压测脚本公共工具（env/CSV/headers/json）
  benchmark_db_read.js        # 直压 MySQL 读上限（xk6-sql）
  benchmark_db_read_k6_fallback.js # k6-only DB 读替代基线（HTTP）
  benchmark_app_read.js       # 应用层热点读压测
  benchmark_e2e_tps.js        # 全链路交易压测（下单-轮询-支付）
  benchmark.config.json       # 统一参数配置
  run_standard.ps1            # 一键批量执行与汇总（Windows）
  run_standard.sh             # 一键批量执行与汇总（Linux/macOS）
  TEST_MATRIX.md              # 单点/集成/E2E 分类矩阵
```

## 2. 启动时后台任务

- MQ 消费者：
  - 主建单链路
  - 30 分钟超时取消链路
- Cron：
  - 主消息 Outbox 调度发送
  - 主消息 dead 自动重放
  - pending 悬空预扣扫描
  - pending 垃圾清理
  - outbox 延迟消息重投补偿器
  - outbox dead 自动重放

## 3. 新增关键对象

- seckill_tx_tasks（主消息 Outbox）
- order_timeout_tasks（超时消息 Outbox）
- Redis canceled/processing 标记能力
- Seed 预热标记键：seckill:warmup:last

## 4. 分层职责

- Handler：参数绑定与返回
- Service：业务策略编排
- Repository：DB 与缓存读写细节
- Cron/MQ：异步与补偿执行层

## 5. 当前主链路

1. HTTP 秒杀请求经过鉴权/限流进入 Service。
2. Redis Lua 完成库存预扣 + pending 记录。
3. 主消息写入 seckill_tx_tasks（Outbox）。
4. TxMessageDispatcher 同步发送至 seckill_tx_topic。
5. MQ 消费者事务建单并清理 pending。
6. 发送 30 分钟超时消息；失败落 order_timeout_tasks，由补偿器重投。
