package cron

import (
	"context"
	"time"

	"seckill-system/internal/repository"
	"seckill-system/pkg/logger"

	"go.uber.org/zap"
)

const (
	txDeadReplayInterval  = 2 * time.Minute
	txDeadReplayBatch     = 100
	txDeadReplayOlderThan = 10 * time.Minute
)

// TxTaskReplayer 主消息 dead 任务自动重放器
type TxTaskReplayer struct {
	repo   *repository.SeckillTxTaskRepo
	stopCh chan struct{}
}

func NewTxTaskReplayer(repo *repository.SeckillTxTaskRepo) *TxTaskReplayer {
	return &TxTaskReplayer{
		repo:   repo,
		stopCh: make(chan struct{}),
	}
}

func (r *TxTaskReplayer) Start() {
	go r.run()
	logger.Info("主消息 dead 自动重放器已启动",
		zap.Duration("interval", txDeadReplayInterval),
		zap.Int("batch", txDeadReplayBatch),
	)
}

func (r *TxTaskReplayer) Stop() {
	close(r.stopCh)
	logger.Info("主消息 dead 自动重放器已停止")
}

func (r *TxTaskReplayer) run() {
	ticker := time.NewTicker(txDeadReplayInterval)
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

func (r *TxTaskReplayer) replayOnce() {
	ctx := context.Background()
	tasks, err := r.repo.FetchDeadTasks(ctx, txDeadReplayBatch, txDeadReplayOlderThan)
	if err != nil {
		logger.Error("主消息 dead 重放查询失败", zap.Error(err))
		return
	}
	if len(tasks) == 0 {
		return
	}

	requeued := 0
	for _, t := range tasks {
		if err := r.repo.RequeueDeadTask(ctx, t.ID); err != nil {
			logger.Warn("主消息 dead 重放失败",
				zap.Int64("taskID", t.ID),
				zap.String("orderNo", t.OrderNo),
				zap.Error(err),
			)
			continue
		}
		requeued++
	}

	if requeued > 0 {
		logger.Info("主消息 dead 自动重放完成", zap.Int("requeued", requeued))
	}
}
