package cron

import (
	"context"
	"time"

	"seckill-system/internal/model"
	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/mq"

	"go.uber.org/zap"
)

const (
	txDispatchInterval = 1 * time.Second
	txDispatchBatch    = 200
	txDispatchMaxRetry = 30
	txDispatchLease    = 15 * time.Second
	txDispatchWorkers  = 8
)

type TxMessageDispatcherConfig struct {
	Interval time.Duration
	Batch    int
	MaxRetry int
	Lease    time.Duration
	Workers  int
}

// TxMessageDispatcher 主消息 Outbox 调度器
//
// 定时扫描 seckill_tx_tasks，将 pending 任务同步发送到 seckill_tx_topic。
type TxMessageDispatcher struct {
	repo   *repository.SeckillTxTaskRepo
	cfg    TxMessageDispatcherConfig
	stopCh chan struct{}
}

func NewTxMessageDispatcher(repo *repository.SeckillTxTaskRepo, cfg TxMessageDispatcherConfig) *TxMessageDispatcher {
	if cfg.Interval <= 0 {
		cfg.Interval = txDispatchInterval
	}
	if cfg.Batch <= 0 {
		cfg.Batch = txDispatchBatch
	}
	if cfg.MaxRetry <= 0 {
		cfg.MaxRetry = txDispatchMaxRetry
	}
	if cfg.Lease <= 0 {
		cfg.Lease = txDispatchLease
	}
	if cfg.Workers <= 0 {
		cfg.Workers = txDispatchWorkers
	}
	return &TxMessageDispatcher{
		repo:   repo,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

func (d *TxMessageDispatcher) Start() {
	go d.run()
	logger.Info("主消息 Outbox 调度器已启动",
		zap.Duration("scanInterval", d.cfg.Interval),
		zap.Int("batch", d.cfg.Batch),
		zap.Int("workers", d.cfg.Workers),
	)
}

func (d *TxMessageDispatcher) Stop() {
	close(d.stopCh)
	logger.Info("主消息 Outbox 调度器已停止")
}

func (d *TxMessageDispatcher) run() {
	ticker := time.NewTicker(d.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			d.dispatchOnce()
		}
	}
}

func (d *TxMessageDispatcher) dispatchOnce() {
	ctx := context.Background()
	tasks, err := d.repo.ClaimPendingTasks(ctx, d.cfg.Batch, d.cfg.Lease)
	if err != nil {
		logger.Error("主消息调度器: 查询待发送任务失败", zap.Error(err))
		return
	}
	if len(tasks) == 0 {
		return
	}

	sem := make(chan struct{}, d.cfg.Workers)
	done := make(chan struct{}, len(tasks))
	for _, task := range tasks {
		sem <- struct{}{}
		go func(task *model.SeckillTxTask) {
			defer func() {
				<-sem
				done <- struct{}{}
			}()
			d.dispatchTask(ctx, task)
		}(task)
	}

	for range tasks {
		<-done
	}
}

func (d *TxMessageDispatcher) dispatchTask(ctx context.Context, task *model.SeckillTxTask) {
	err := mq.SendSeckillMessageSync(
		ctx,
		e.TopicSeckillTx,
		task.Payload,
		task.OrderNo,
		task.UserID,
		task.ActivityID,
		task.ProductID,
	)
	if err != nil {
		_ = d.repo.MarkRetry(ctx, task, err.Error(), d.cfg.MaxRetry)
		logger.Warn("主消息调度器: 投递失败",
			zap.Int64("taskID", task.ID),
			zap.String("orderNo", task.OrderNo),
			zap.Int("retryCount", task.RetryCount+1),
			zap.Error(err),
		)
		return
	}

	if err := d.repo.MarkSent(ctx, task.ID); err != nil {
		logger.Error("主消息调度器: 标记已发送失败",
			zap.Int64("taskID", task.ID),
			zap.String("orderNo", task.OrderNo),
			zap.Error(err),
		)
		return
	}

	logger.Info("主消息调度器: 投递成功",
		zap.Int64("taskID", task.ID),
		zap.String("orderNo", task.OrderNo),
	)
}
