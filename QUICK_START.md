# QUICK_START（当前版本）

## 1. 环境要求

- Go 1.23+
- MySQL 8.0（127.0.0.1:3306）
- Redis 7.0（127.0.0.1:6379）
- RocketMQ NameServer（127.0.0.1:9876）

## 2. 启动

```bash
go mod download
go run cmd/api/main.go
```

或直接使用 Docker：

```bash
docker compose up -d --build
```

默认映射端口：

- API: `8080`
- RocketMQ Dashboard: `8081`

或使用脚本：

- Windows: start.bat
- Linux/Mac: ./start.sh

## 3. 自检

### 3.1 健康检查

```bash
curl http://localhost:8080/ping
```

### 3.2 创建测试数据

```bash
curl -X POST http://localhost:8080/api/v1/admin/seed
```

### 3.3 登录获取 token

```bash
curl -X POST http://localhost:8080/api/v1/user/login \
  -H "Content-Type: application/json" \
  -d '{"username":"test_user","password":"test123456"}'
```

### 3.4 发起秒杀

```bash
curl -X POST http://localhost:8080/api/v1/seckill \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"activity_id":1,"product_id":1}'
```

### 3.5 轮询结果

```bash
curl "http://localhost:8080/api/v1/seckill/result?activity_id=1&product_id=1&order_no=<order_no>" \
  -H "Authorization: Bearer <token>"
```

## 4. 当前可靠性机制

- Redis Lua 原子预扣（库存/已购/pending）
- Cron 悬空补偿（5 分钟阈值）
- canceled/processing 标记防 Cron 与 MQ 赛跑
- 订单超时延迟消息发送失败写 outbox，后台自动重投
- pending 垃圾清理（DB 有单或已作废）

## 5. 常见问题

1. 端口占用：修改 config/local.yaml 的 server.port。
2. RocketMQ 未启动：MQ 生产/消费会报连接错误。
3. RedisBloom 未安装：系统会降级放行，不影响主流程。
4. Docker 模式使用 `config/docker.yaml`；本地直跑默认使用 `config/local.yaml`。
