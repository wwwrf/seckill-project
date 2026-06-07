# DEPLOYMENT（当前实现版）

## 1. 架构概览

系统为单体服务 + 本地中间件部署：

- API: Gin
- MySQL: 订单与任务持久化
- Redis: 库存、一人一单、pending、中间态标记
- RocketMQ: 异步建单与订单超时延迟取消

## 2. 可靠性与一致性设计

### 2.1 秒杀主链路

- Redis Lua 原子预扣：DECR + SADD + HSET pending
- HTTP 成功后写 seckill_tx_tasks（主消息 outbox）
- 主消息调度器同步发送 seckill_tx_topic 并标记 sent
- MQ 主消费者 DB 事务建单成功后删除 pending

### 2.2 防赛跑机制（Cron vs MQ）

- Cron 回滚前：先写 canceled 标记
- MQ 建单前：检查 canceled 标记 + 抢 processing 标记
- 目标：避免“Cron 回滚后迟到消息又建单”导致超卖

### 2.3 延迟消息发送失败补偿（Outbox）

- 场景：建单成功后发送 30 分钟延迟消息失败
- 处理：写 order_timeout_tasks
- 补偿器：周期扫描待重试任务并重发 MQ
- 重试策略：指数退避，超过上限标记 dead

### 2.4 pending 垃圾清理

定时清理以下残留：

- DB 已有单但 pending 未删
- 订单已 canceled 标记但 pending 残留

## 3. 关键数据表

- orders
- order_items
- seckill_activities
- stock_deduct_logs
- seckill_tx_tasks（主消息 outbox）
- order_timeout_tasks（新增，延迟消息补偿 outbox）

## 4. 关键 Redis Key

- seckill:stock:{activityID}:{productID}
- seckill:purchased:{activityID}:{productID}
- seckill:pending:{activityID}
- seckill:canceled:{orderNo}
- seckill:processing:{orderNo}

## 5. 启停与验证

### 启动

```bash
go run cmd/api/main.go
```

或使用 Docker Compose：

```bash
docker compose up -d --build
```

### 编译检查

```bash
go test ./... -run ^$ -count=1
```

### 关键观察点

- 日志中补偿器启动成功
- pending 扫描与清理日志
- outbox 重试成功/失败日志

## 6. 已知边界

- 主消息与延迟消息均采用 Outbox + 调度器方案，仍需关注 broker 不可用时的任务积压。
- 自动重放会提升自愈能力，但建议增加告警阈值（dead/pending 积压）做运维兜底。
