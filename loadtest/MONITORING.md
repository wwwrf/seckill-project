# 压测监控环境配置指南

## 一、监控架构

```
k6 压测脚本
    │
    ├── --out experimental-prometheus-rw ──→ Prometheus ──→ Grafana
    │                                          ↑
    │                              node_exporter (CPU/内存/磁盘/网络)
    │                              mysqld_exporter (MySQL 指标)
    │                              redis_exporter (Redis 指标)
    │
    └── --out json=result.json ──→ 离线分析
```

## 二、快速启动（Docker Compose）

### 1. 创建 `docker-compose-monitor.yml`

```yaml
version: '3.8'
services:
  prometheus:
    image: prom/prometheus:latest
    ports:
      - "9090:9090"
    volumes:
      - ./monitor/prometheus.yml:/etc/prometheus/prometheus.yml
    extra_hosts:
      - "host.docker.internal:host-gateway"
    # 启用 remote-write-receiver 接收 k6 数据
    command:
      - '--config.file=/etc/prometheus/prometheus.yml'
      - '--web.enable-remote-write-receiver'
      - '--enable-feature=native-histograms'

  grafana:
    image: grafana/grafana:latest
    ports:
      - "3000:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    volumes:
      - grafana-data:/var/lib/grafana

  # MySQL 监控（慢查询、连接数、QPS）
  mysqld-exporter:
    image: prom/mysqld-exporter:latest
    ports:
      - "9104:9104"
    environment:
      DATA_SOURCE_NAME: "root:@Wrf120855@(host.docker.internal:3306)/ecommerce_db"
    extra_hosts:
      - "host.docker.internal:host-gateway"

  # Redis 监控（命中率、内存、命令统计）
  redis-exporter:
    image: oliver006/redis_exporter:latest
    ports:
      - "9121:9121"
    environment:
      REDIS_ADDR: "host.docker.internal:6379"
    extra_hosts:
      - "host.docker.internal:host-gateway"

  # 系统资源监控（CPU、内存、磁盘IO、网络带宽）
  # Windows 用户请改用 windows_exporter，见下方说明
  node-exporter:
    image: prom/node-exporter:latest
    ports:
      - "9100:9100"

volumes:
  grafana-data:
```

### 2. 创建 `monitor/prometheus.yml`

```yaml
global:
  scrape_interval: 5s
  evaluation_interval: 5s

scrape_configs:
  # Prometheus 自监控
  - job_name: 'prometheus'
    static_configs:
      - targets: ['localhost:9090']

  # MySQL 监控
  - job_name: 'mysql'
    static_configs:
      - targets: ['mysqld-exporter:9104']

  # Redis 监控
  - job_name: 'redis'
    static_configs:
      - targets: ['redis-exporter:9121']

  # 系统资源
  - job_name: 'node'
    static_configs:
      - targets: ['node-exporter:9100']
      # Windows 用户改为: ['host.docker.internal:9182']
```

### 3. 启动监控栈

```bash
docker-compose -f docker-compose-monitor.yml up -d
```

### 4. 运行压测（k6 → Prometheus）

```powershell
# 设置 Prometheus Remote Write 地址
$env:K6_PROMETHEUS_RW_SERVER_URL = "http://localhost:9090/api/v1/write"
$env:K6_PROMETHEUS_RW_TREND_AS_NATIVE_HISTOGRAM = "true"

# 运行带监控输出的压测
k6 run --out experimental-prometheus-rw loadtest/benchmark_e2e_tps.js
```

### 5. 打开 Grafana 看板

- 访问 http://localhost:3000（admin / admin）
- 添加 Prometheus 数据源：http://prometheus:9090
- 导入以下 Dashboard：
  - **k6 结果面板**：Dashboard ID `18030` 或 `19665`
  - **MySQL Overview**：Dashboard ID `7362`
  - **Redis Dashboard**：Dashboard ID `11835`
  - **Node Exporter Full**：Dashboard ID `1860`

---

## 三、Windows 系统资源监控

Windows 不支持 node_exporter，需要安装 **windows_exporter**：

```powershell
# 下载安装
winget install prometheus-community.windows_exporter

# 或手动下载: https://github.com/prometheus-community/windows_exporter/releases
# 默认监听端口 9182
```

启动后在 prometheus.yml 中将 node-exporter 改为：
```yaml
  - job_name: 'windows'
    static_configs:
      - targets: ['host.docker.internal:9182']
```

---

## 四、MySQL 慢查询日志配置

在 MySQL 配置文件（my.ini/my.cnf）中添加：

```ini
[mysqld]
# 开启慢查询日志
slow_query_log = 1
slow_query_log_file = /var/log/mysql/slow.log
long_query_time = 0.1          # 超过 100ms 的查询记录
log_queries_not_using_indexes = 1  # 记录未使用索引的查询

# 或者在线开启（不需要重启）：
# SET GLOBAL slow_query_log = 'ON';
# SET GLOBAL long_query_time = 0.1;
# SET GLOBAL log_queries_not_using_indexes = 'ON';
```

在线开启（连接 MySQL 执行）：
```sql
SET GLOBAL slow_query_log = 'ON';
SET GLOBAL long_query_time = 0.1;
SET GLOBAL log_queries_not_using_indexes = 'ON';

-- 查看慢查询日志位置
SHOW VARIABLES LIKE 'slow_query_log%';

-- 查看当前连接数和活跃查询
SHOW PROCESSLIST;

-- 查看 InnoDB 锁等待
SELECT * FROM information_schema.INNODB_LOCK_WAITS;

-- 查看表的索引使用情况
EXPLAIN SELECT * FROM orders WHERE user_id = 1 AND activity_id = 1;
```

---

## 五、Redis 缓存命中率监控

```bash
# 实时查看 Redis 命中率
redis-cli INFO stats | grep -E "keyspace_hits|keyspace_misses"

# 计算命中率 = hits / (hits + misses) * 100%

# 实时监控 Redis 命令
redis-cli MONITOR

# 查看内存使用
redis-cli INFO memory

# 查看各 key 的内存占用（大key排查）
redis-cli --bigkeys
```

在 Grafana 中，Redis Exporter 会自动暴露：
- `redis_keyspace_hits_total` / `redis_keyspace_misses_total` → 命中率
- `redis_connected_clients` → 客户端连接数
- `redis_used_memory_bytes` → 内存占用
- `redis_commands_total` → 各命令执行次数

---

## 六、RocketMQ 消息积压监控

### 方式 1：RocketMQ Console（推荐）

```bash
# Docker 启动 RocketMQ Dashboard
docker run -d --name rocketmq-console \
  -e "JAVA_OPTS=-Drocketmq.namesrv.addr=host.docker.internal:9876" \
  -p 8180:8080 \
  --add-host host.docker.internal:host-gateway \
  apacherocketmq/rocketmq-dashboard:latest
```

访问 http://localhost:8180 查看：
- **Topic 列表** → 消息量、消费进度
- **Consumer Group** → 消费延迟、积压量
- **Message trace** → 单条消息追踪

### 方式 2：命令行查看积压

```bash
# 查看消费者组的消息积压
mqadmin consumerProgress -g seckill_tx_consumer_group -n 127.0.0.1:9876

# 查看 Topic 状态
mqadmin topicStatus -t seckill_tx_topic -n 127.0.0.1:9876
```

### 方式 3：RocketMQ Exporter → Prometheus

```bash
docker run -d --name rmq-exporter \
  -p 5557:5557 \
  -e "rocketmq.config.namesrvAddr=host.docker.internal:9876" \
  --add-host host.docker.internal:host-gateway \
  apache/rocketmq-exporter:latest
```

prometheus.yml 添加：
```yaml
  - job_name: 'rocketmq'
    static_configs:
      - targets: ['rmq-exporter:5557']
```

---

## 七、Grafana 自定义面板推荐查询

### k6 压测指标面板

| 面板名称 | PromQL 查询 |
|---------|------------|
| 实时 RPS | `rate(k6_http_reqs_total[30s])` |
| 秒杀接口 P95 | `histogram_quantile(0.95, rate(k6_seckill_latency_ms[30s]))` |
| 抢购成功数 | `k6_biz_success_total` |
| 售罄累计 | `k6_biz_sold_out_total` |
| 限流累计 | `k6_biz_rate_limited_total` |
| 系统错误 | `k6_biz_sys_error_total` |
| 并发用户数 | `k6_vus` |

### MySQL 关键面板

| 面板名称 | PromQL 查询 |
|---------|------------|
| 活跃连接数 | `mysql_global_status_threads_connected` |
| 执行中查询数 | `mysql_global_status_threads_running` |
| 慢查询累计 | `mysql_global_status_slow_queries` |
| QPS | `rate(mysql_global_status_queries[30s])` |
| 行锁等待 | `mysql_global_status_innodb_row_lock_waits` |

### Redis 关键面板

| 面板名称 | PromQL 查询 |
|---------|------------|
| 缓存命中率 | `rate(redis_keyspace_hits_total[1m]) / (rate(redis_keyspace_hits_total[1m]) + rate(redis_keyspace_misses_total[1m]))` |
| 连接数 | `redis_connected_clients` |
| 内存使用 | `redis_used_memory_bytes` |
| 每秒命令数 | `rate(redis_commands_total[30s])` |
