package redis

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"seckill-system/pkg/logger"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// ==================== Redis 客户端 ====================

// Client 全局 Redis 客户端实例
var Client *redis.Client

// isRedisHealthy 全局健康状态标记
// 使用 atomic.Bool 保证并发读写安全，避免加锁开销
var isRedisHealthy atomic.Bool

// Config Redis 连接配置
type Config struct {
	Host         string
	Port         int
	Password     string
	DB           int
	PoolSize     int
	MinIdleConns int
}

// Init 初始化 Redis 客户端
//
// 核心职责：
//  1. 创建 go-redis 客户端，配置高性能连接池（PoolSize=200, MinIdle=50）
//  2. 执行首次 Ping 确认连通性
//  3. 启动异步健康检查 Goroutine
//
// 关于 Prometheus 监控（预留）：
//   此处未来将挂接 go-redis 的 Prometheus Hook，导出 PoolStats
//   (等待连接数、空闲连接数、活跃连接数) 至 /metrics 端点。
//   实现方式：使用 redis.NewClient 返回后调用 AddHook(prometheusHook)，
//   prometheusHook 在 ProcessHook 中记录命令耗时直方图，
//   同时定期采集 Client.PoolStats() 暴露 gauge 指标。
func Init(config *Config) error {
	// 连接池参数兜底：如果配置文件未设置或为 0，使用秒杀场景推荐默认值
	poolSize := config.PoolSize
	if poolSize <= 0 {
		poolSize = 200
	}
	minIdleConns := config.MinIdleConns
	if minIdleConns <= 0 {
		minIdleConns = 50
	}

	Client = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", config.Host, config.Port),
		Password: config.Password,
		DB:       config.DB,

		// ===== 连接池配置（高并发调优） =====
		PoolSize:     poolSize,     // 最大连接数，秒杀场景需要较大的池
		MinIdleConns: minIdleConns, // 最小空闲连接，避免突发流量时大量建连

		// ===== 超时配置 =====
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolTimeout:  4 * time.Second,
	})

	// 首次 Ping 验证连通性
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := Client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis Ping 失败: %w", err)
	}

	// 标记为健康
	isRedisHealthy.Store(true)

	logger.Info("Redis 连接成功",
		zap.String("addr", fmt.Sprintf("%s:%d", config.Host, config.Port)),
		zap.Int("db", config.DB),
		zap.Int("poolSize", poolSize),
		zap.Int("minIdleConns", minIdleConns),
	)

	// TODO: 挂接 Prometheus Hook
	// Client.AddHook(newPrometheusHook())
	// 未来在此处注册 go-redis 的 Hook，在 ProcessHook / ProcessPipelineHook 中
	// 记录每条 Redis 命令的耗时直方图 (histogram)，并定期采集 Client.PoolStats()
	// 导出 redis_pool_hit_total / redis_pool_miss_total / redis_pool_timeout_total
	// 以及 redis_pool_idle_conns / redis_pool_active_conns 等 gauge 指标。

	// 启动异步健康检查
	go asyncHealthCheck()

	return nil
}

// asyncHealthCheck 异步健康检查
//
// 每隔 5 秒执行一次 Ping，维护全局 isRedisHealthy 状态。
// 业务层在调用 Redis 前可先检查 CheckHealth()，实现快速熔断：
//   - 如果 Redis 不可用，直接降级走 DB 或返回友好错误
//   - 避免大量请求堆积在 Redis 超时上，拖垮整个服务
//
// 该 Goroutine 随进程生命周期存在，不需要显式停止。
func asyncHealthCheck() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := Client.Ping(ctx).Err()
		cancel()

		if err != nil {
			if isRedisHealthy.Load() {
				// 状态变更：健康 → 不健康，打一条 Error 日志
				logger.Error("Redis 健康检查失败，标记为不可用", zap.Error(err))
			}
			isRedisHealthy.Store(false)
		} else {
			if !isRedisHealthy.Load() {
				// 状态变更：不健康 → 恢复，打一条 Info 日志
				logger.Info("Redis 健康检查恢复正常")
			}
			isRedisHealthy.Store(true)
		}
	}
}

// CheckHealth 返回 Redis 当前是否可用
//
// 业务层在执行 Redis 操作前调用此方法做快速熔断判断：
//
//	if !redis.CheckHealth() {
//	    // 降级逻辑：直接走 DB 或返回服务繁忙
//	}
//
// 该方法是无锁的 atomic 读取，性能开销可忽略（< 1ns）。
func CheckHealth() bool {
	return isRedisHealthy.Load()
}

// Close 关闭 Redis 连接
func Close() {
	if Client != nil {
		if err := Client.Close(); err != nil {
			logger.Error("关闭 Redis 连接失败", zap.Error(err))
			return
		}
		logger.Info("Redis 连接已关闭")
	}
}
