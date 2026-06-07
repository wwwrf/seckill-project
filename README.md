# 秒杀系统（当前实现说明）

基于 Go + Gin + Redis + MySQL + RocketMQ 的高并发秒杀系统，当前版本已实现：

- 用户注册/登录（JWT）
- 商品/活动三级缓存查询（L1 本地 + L2 Redis + L3 MySQL）
- 秒杀下单主链路（Redis Lua 预扣 + 主消息 Outbox + MQ 异步建单）
- 订单查询与支付
- 订单 30 分钟未支付自动取消（MQ 延迟消息）
- 悬空预扣补偿（Cron 扫描 pending）
- 延迟消息发送失败补偿（Outbox 表 + 定时重投）
- dead-letter 自动重放（dead 任务自动重入队）
- pending 垃圾清理（DB 有单/已作废标记时清理）

## 1. 技术栈

- Go 1.23+
- Gin
- GORM + MySQL 8.0
- Redis 7.0
- RocketMQ 5.x
- JWT (golang-jwt/jwt/v5)

## 2. 当前核心链路

### 2.1 秒杀下单

1. Trace + JWT + 限流
2. 本地连击拦截 / 本地售罄拦截
3. Bloom 过滤活动商品存在性（不是用户判重）
4. Redis Lua 原子预扣：DECR 库存 + SADD 已购 + HSET pending
5. 写入主消息 Outbox（seckill_tx_tasks）
6. Outbox 调度器同步发送到 seckill_tx_topic
7. 消费端 DB 事务建单成功后 HDEL pending

### 2.2 超时取消

1. 建单成功后发送 30 分钟延迟消息（seckill_order_timeout_topic）
2. 延迟消费时若订单仍 pending：
   - DB 事务改状态为 cancelled，并回滚 DB 库存
   - Redis Lua 回滚库存与已购标记

### 2.3 悬空预扣补偿

- Cron 每分钟扫描 pending，超过 5 分钟触发检查
- DB 无单则先写 canceled 标记，再执行 Redis 回滚
- MQ 主消费者建单前检查 canceled/processing 标记，避免赛跑超卖

### 2.4 延迟消息发送失败补偿

- 若发送 30 分钟延迟消息失败，写入 order_timeout_tasks（Outbox）
- 补偿器定时扫描并重发，指数退避重试，超限转 dead

### 2.5 主消息可靠投递

- HTTP 链路不直接发主消息，而是先落 seckill_tx_tasks
- 后台调度器按 pending 任务同步投递主消息并标记 sent
- dead 任务自动重放，避免长尾任务永久滞留

## 3. Redis 关键数据

- seckill:stock:{activityID}:{productID} (String)
- seckill:purchased:{activityID}:{productID} (Set)
- seckill:pending:{activityID} (Hash)
- seckill:canceled:{orderNo} (String)
- seckill:processing:{orderNo} (String)
- seckill:bloom:items (BloomFilter)

## 4.1 关键 Outbox 表

- seckill_tx_tasks（主消息投递任务）
- order_timeout_tasks（30 分钟超时消息补偿任务）

## 4. 主要 API

- GET /ping
- POST /api/v1/user/register
- POST /api/v1/user/login
- GET /api/v1/product/:id
- GET /api/v1/activity/:id
- POST /api/v1/seckill
- GET /api/v1/seckill/result
- GET /api/v1/orders
- GET /api/v1/order/:orderNo
- POST /api/v1/order/:orderNo/pay
- POST /api/v1/service/chat
- POST /api/v1/service/complaint
- POST /api/v1/admin/warmup
- POST /api/v1/admin/seed

客服 Agent 说明：

- `/api/v1/service/chat`：意图识别 + 路由（咨询Agent / 投诉Agent）
- `/api/v1/service/complaint`：直接走投诉Agent（RAG）
- `config/local.yaml` 可配置 `agent.intent.llm_enabled`，开启后将用 Eino(OpenAI 组件) 做意图分类，失败自动回退规则路由

管理接口需携带请求头：X-Admin-Token（值来自 config/local.yaml 的 admin.token）。

详见 API.md。

## 5. 快速启动

```bash
go mod download
go run cmd/api/main.go
```

默认端口由 config/local.yaml 的 server.port 控制（当前默认 8080）。

### Docker 一键启动

```bash
docker compose up -d --build
```

启动后：

- API: `http://127.0.0.1:8080`
- RocketMQ Dashboard: `http://127.0.0.1:8081`

容器化启动会自动读取 `config/docker.yaml`。

## 6. 文档索引

- QUICK_START.md：启动与自检步骤
- API.md：接口入参与返回
- PROJECT_STRUCTURE.md：代码结构与职责
- DEPLOYMENT.md：架构、补偿链路与运维要点
- CHECKLIST.md：当前版本交付清单
- loadtest/README_STANDARD.md：异机真实压测标准流程与三套脚本口径
- docs/complaints_kb.md：投诉Agent知识库样例文档（RAG数据源）
