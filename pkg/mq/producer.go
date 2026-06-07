package mq

import (
	"context"
	"fmt"
	"time"

	"seckill-system/pkg/logger"

	"github.com/apache/rocketmq-client-go/v2"
	"github.com/apache/rocketmq-client-go/v2/primitive"
	"github.com/apache/rocketmq-client-go/v2/producer"
	"go.uber.org/zap"
)

// ==================== RocketMQ 生产者 ====================

// globalProducer 全局 RocketMQ 普通生产者实例
var globalProducer rocketmq.Producer

// Config RocketMQ 生产者配置
type Config struct {
	NameServer string // NameServer 地址，格式 "host:port"
	GroupName  string // 生产者组名
	RetryTimes int    // 发送失败重试次数（不含首次发送）
	Timeout    int    // 发送超时时间（秒）
}

// Init 初始化 RocketMQ 普通生产者（Normal Producer）
func Init(config *Config) error {
	retryTimes := config.RetryTimes
	if retryTimes <= 0 {
		retryTimes = 2
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 3
	}

	var err error
	globalProducer, err = rocketmq.NewProducer(
		producer.WithNameServer([]string{config.NameServer}),
		producer.WithGroupName(config.GroupName),
		producer.WithRetry(retryTimes),
		producer.WithSendMsgTimeout(time.Duration(timeout)*time.Second),
	)
	if err != nil {
		return fmt.Errorf("创建 RocketMQ Producer 失败: %w", err)
	}

	if err = globalProducer.Start(); err != nil {
		return fmt.Errorf("启动 RocketMQ Producer 失败: %w", err)
	}

	logger.Info("RocketMQ Producer 初始化成功",
		zap.String("nameServer", config.NameServer),
		zap.String("groupName", config.GroupName),
		zap.Int("retryTimes", retryTimes),
		zap.Int("timeoutSeconds", timeout),
	)

	return nil
}

// SendSeckillMessageSync 同步发送秒杀主流程消息
//
// 用于主消息 Outbox 调度器，要求拿到 Broker ACK 再标记任务 sent。
func SendSeckillMessageSync(
	ctx context.Context,
	topic string,
	body []byte,
	orderNo string,
	userID, activityID, productID int64,
) error {
	msg := &primitive.Message{
		Topic: topic,
		Body:  body,
	}

	result, err := globalProducer.SendSync(ctx, msg)
	if err != nil {
		logger.Error("MQ 同步投递失败",
			zap.String("action", "SyncSendFailed"),
			zap.String("orderNo", orderNo),
			zap.Int64("userID", userID),
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Error(err),
		)
		return fmt.Errorf("seckill tx sync send error: %w", err)
	}

	logger.Info("主消息同步投递成功",
		zap.String("orderNo", orderNo),
		zap.String("msgID", result.MsgID),
	)
	return nil
}

// Close 关闭 RocketMQ Producer
func Close() {
	if globalProducer != nil {
		if err := globalProducer.Shutdown(); err != nil {
			logger.Error("关闭 RocketMQ Producer 失败", zap.Error(err))
			return
		}
		logger.Info("RocketMQ Producer 已关闭")
	}
}

// SendOrderTimeoutMessage 同步发送订单超时取消延迟消息
//
// 投递到 seckill_order_timeout_topic，延迟 30 分钟后消费端检查订单支付状态。
//
// 调用时机：链路 1 主流程消费者建单成功后立即发送。
//
// RocketMQ 延迟级别：Level 16 = 30 分钟
//
// 技术选型：延迟消息 vs 定时任务
//
//	| 方案              | 优点                     | 缺点                         |
//	|-------------------|--------------------------|------------------------------|
//	| 延迟消息(选用)     | 精确到单个订单，无扫描开销  | 依赖 MQ 可靠性                |
//	| 定时扫描(cron)    | 实现简单，不依赖 MQ       | 扫描频率与精度矛盾，DB 压力    |
//	| Redis Key 过期回调 | 精确，轻量                | 不保证可靠投递（可能丢失）      |
//	| 时间轮(HashedWheelTimer) | 内存高效          | 进程重启后任务丢失             |
func SendOrderTimeoutMessage(
	ctx context.Context,
	topic string,
	body []byte,
	orderNo string,
	userID, activityID, productID int64,
) error {
	msg := primitive.NewMessage(topic, body)
	msg.WithDelayTimeLevel(16) // Level 16 = 30 分钟

	result, err := globalProducer.SendSync(ctx, msg)
	if err != nil {
		logger.Error("订单超时取消延迟消息发送失败",
			zap.String("action", "OrderTimeoutSendFailed"),
			zap.String("orderNo", orderNo),
			zap.Int64("userID", userID),
			zap.Int64("activityID", activityID),
			zap.Int64("productID", productID),
			zap.Error(err),
		)
		return fmt.Errorf("order timeout delay msg send error: %w", err)
	}

	logger.Info("订单超时取消延迟消息发送成功（30min 后触发检查）",
		zap.String("orderNo", orderNo),
		zap.String("msgID", result.MsgID),
		zap.Int("delayLevel", 16),
	)
	return nil
}
