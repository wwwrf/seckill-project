package cron

import (
	"context"
	"time"

	"seckill-system/internal/repository"
	"seckill-system/pkg/logger"

	"go.uber.org/zap"
)

const (
	deadReplayInterval = 2 * time.Minute
	deadReplayBatch    = 50
	deadReplayOlderThan = 10 * time.Minute
)

// DeadTaskReplayer dead-letter 自动重放器
//
// 将 order_timeout_tasks 中的 dead 任务自动重置为 pending，
// 再由 TimeoutMessageCompensator 扫描并重投消息。
type DeadTaskReplayer struct {
	repo   *repository.OrderTimeoutTaskRepo
	stopCh chan struct{}
}

func NewDeadTaskReplayer(repo *repository.OrderTimeoutTaskRepo) *DeadTaskReplayer {
	return &DeadTaskReplayer{
		repo:   repo,
		stopCh: make(chan struct{}),
	}
}

func (r *DeadTaskReplayer) Start() {
	go r.run()
	logger.Info("dead-letter 自动重放器已启动",
		zap.Duration("interval", deadReplayInterval),
		zap.Int("batch", deadReplayBatch),
	)
}

func (r *DeadTaskReplayer) Stop() {
	close(r.stopCh)
	logger.Info("dead-letter 自动重放器已停止")
}

func (r *DeadTaskReplayer) run() {
	ticker := time.NewTicker(deadReplayInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.replayOnce()
		}
	}
}

func (r *DeadTaskReplayer) replayOnce() {
	ctx := context.Background()
	tasks, err := r.repo.FetchDeadTasks(ctx, deadReplayBatch, deadReplayOlderThan)
	if err != nil {
		logger.Error("dead-letter 重放查询失败", zap.Error(err))
		return
	}
	if len(tasks) == 0 {
		return
	}

	requeued := 0
	for _, t := range tasks {
		if err := r.repo.RequeueDeadTask(ctx, t.ID); err != nil {
			logger.Warn("dead-letter 重放失败",
				zap.Int64("taskID", t.ID),
				zap.String("orderNo", t.OrderNo),
				zap.Error(err),
			)
			continue
		}
		requeued++
	}

	if requeued > 0 {
		logger.Info("dead-letter 自动重放完成", zap.Int("requeued", requeued))
	}
}
