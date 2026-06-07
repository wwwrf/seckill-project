package cron

import (
	"context"
	"encoding/json"
	"time"

	"seckill-system/internal/model"
	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/mq"

	"go.uber.org/zap"
)

const (
	outboxScanInterval = 30 * time.Second
	outboxBatchSize    = 100
	outboxMaxRetry     = 20
	outboxLease        = 30 * time.Second
)

// TimeoutMessageCompensator 延迟消息发送失败补偿器
//
// 定时扫描 order_timeout_tasks，将失败任务重投到 seckill_order_timeout_topic。
type TimeoutMessageCompensator struct {
	repo   *repository.OrderTimeoutTaskRepo
	stopCh chan struct{}
}

type timeoutOrderMsg struct {
	OrderNo    string `json:"order_no"`
	UserID     int64  `json:"user_id"`
	ActivityID int64  `json:"activity_id"`
	ProductID  int64  `json:"product_id"`
}

func NewTimeoutMessageCompensator(repo *repository.OrderTimeoutTaskRepo) *TimeoutMessageCompensator {
	return &TimeoutMessageCompensator{
		repo:   repo,
		stopCh: make(chan struct{}),
	}
}

func (c *TimeoutMessageCompensator) Start() {
	go c.run()
	logger.Info("延迟消息补偿器已启动",
		zap.Duration("scanInterval", outboxScanInterval),
		zap.Int("batchSize", outboxBatchSize),
	)
}

func (c *TimeoutMessageCompensator) Stop() {
	close(c.stopCh)
	logger.Info("延迟消息补偿器已停止")
}

func (c *TimeoutMessageCompensator) run() {
	ticker := time.NewTicker(outboxScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.scanAndRetry()
		}
	}
}

func (c *TimeoutMessageCompensator) scanAndRetry() {
	ctx := context.Background()
	tasks, err := c.repo.ClaimPendingTasks(ctx, outboxBatchSize, outboxLease)
	if err != nil {
		logger.Error("补偿器: 查询待重试任务失败", zap.Error(err))
		return
	}
	if len(tasks) == 0 {
		return
	}

	for _, task := range tasks {
		c.handleTask(ctx, task)
	}
}

func (c *TimeoutMessageCompensator) handleTask(ctx context.Context, task *model.OrderTimeoutTask) {
	msg := timeoutOrderMsg{
		OrderNo:    task.OrderNo,
		UserID:     task.UserID,
		ActivityID: task.ActivityID,
		ProductID:  task.ProductID,
	}
	body, _ := json.Marshal(msg)

	err := mq.SendOrderTimeoutMessage(
		ctx,
		e.TopicSeckillOrderTimeout,
		body,
		task.OrderNo,
		task.UserID,
		task.ActivityID,
		task.ProductID,
	)
	if err != nil {
		_ = c.repo.MarkRetry(ctx, task, err.Error(), outboxMaxRetry)
		logger.Warn("补偿器: 重投延迟消息失败",
			zap.Int64("taskID", task.ID),
			zap.String("orderNo", task.OrderNo),
			zap.Int("retryCount", task.RetryCount+1),
			zap.Error(err),
		)
		return
	}

	if err := c.repo.MarkSent(ctx, task.ID); err != nil {
		logger.Error("补偿器: 标记任务已发送失败",
			zap.Int64("taskID", task.ID),
			zap.String("orderNo", task.OrderNo),
			zap.Error(err),
		)
		return
	}

	logger.Info("补偿器: 延迟消息补发成功",
		zap.Int64("taskID", task.ID),
		zap.String("orderNo", task.OrderNo),
	)
}
