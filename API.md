# API 文档（当前版本）

Base URL: http://localhost:8080

统一响应：

```json
{
  "code": 0,
  "msg": "success",
  "data": {}
}
```

## 鉴权

鉴权接口需携带：

Authorization: Bearer <token>

## 公开接口

### GET /ping

健康检查。

### POST /api/v1/user/register

请求体：

```json
{
  "username": "demo_user",
  "password": "demo_pass_123"
}
```

### POST /api/v1/user/login

请求体：

```json
{
  "username": "demo_user",
  "password": "demo_pass_123"
}
```

成功返回：

```json
{
  "code": 0,
  "msg": "登录成功",
  "data": {
    "token": "<jwt>",
    "user_id": 1
  }
}
```

### GET /api/v1/product/:id

商品详情（三级缓存）。

### GET /api/v1/activity/:id

活动详情（三级缓存）。

## 鉴权接口

### POST /api/v1/seckill

请求体：

```json
{
  "activity_id": 1,
  "product_id": 1
}
```

成功：返回 order_no，前端进入轮询。

### GET /api/v1/seckill/result

查询参数：

- activity_id: 必填
- product_id: 必填
- order_no: 可选，建议传，能更精准命中 pending 中间态

返回状态：

- SUCCESS: 已落库
- PROCESSING: 排队中/处理中
- FAILED: 失败或已售罄

### GET /api/v1/orders

查询当前用户订单列表。

参数：

- page: 可选，默认 1
- page_size: 可选，默认 10，最大 50

### GET /api/v1/order/:orderNo

查询订单详情（仅允许本人）。

### POST /api/v1/order/:orderNo/pay

模拟支付：pending -> paid。

## 管理接口（开发环境）

请求头：

- X-Admin-Token: <admin_token>

### POST /api/v1/admin/warmup

请求体：

```json
{
  "activity_id": 1
}
```

将活动库存/Bloom 预热到 Redis。

### POST /api/v1/admin/seed

创建测试用户商品活动并自动预热。

## 业务码（核心）

- 0: 成功
- 20002: 已在排队中
- 40000: 参数错误
- 40001: 已售罄
- 40003: 活动未开始或已结束
- 40100: 未认证
- 42900: 限流
- 50001: 系统繁忙
