package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"seckill-system/internal/model"
	"gorm.io/gorm"
)

// SeckillTxTaskRepo 主消息 Outbox 仓储
type SeckillTxTaskRepo struct{}

func NewSeckillTxTaskRepo() *SeckillTxTaskRepo {
	return &SeckillTxTaskRepo{}
}

// CreateIfNotExists 创建任务（幂等）
func (r *SeckillTxTaskRepo) CreateIfNotExists(ctx context.Context, task *model.SeckillTxTask) error {
	if task.NextRetryAt.IsZero() {
		task.NextRetryAt = time.Now()
	}
	if err := DB.WithContext(ctx).Create(task).Error; err != nil {
		if isDuplicateKeyErr(err) {
			return nil
		}
		return fmt.Errorf("创建主消息任务失败: %w", err)
	}
	return nil
}

// FetchDueTasks 拉取到期待发送任务
func (r *SeckillTxTaskRepo) FetchDueTasks(ctx context.Context, limit int) ([]*model.SeckillTxTask, error) {
	if limit <= 0 {
		limit = 100
	}

	var tasks []*model.SeckillTxTask
	err := DB.WithContext(ctx).
		Where("status = ? AND next_retry_at <= ?", model.SeckillTxTaskPending, time.Now()).
		Order("next_retry_at ASC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("查询主消息待发送任务失败: %w", err)
	}
	return tasks, nil
}

// ClaimPendingTasks 抢占到期待发送任务，避免多实例调度器重复发送同一批消息。
func (r *SeckillTxTaskRepo) ClaimPendingTasks(ctx context.Context, limit int, lease time.Duration) ([]*model.SeckillTxTask, error) {
	if limit <= 0 {
		limit = 100
	}
	if lease <= 0 {
		lease = 15 * time.Second
	}

	var tasks []*model.SeckillTxTask
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []*model.SeckillTxTask
		if err := tx.
			Where("status = ? AND next_retry_at <= ?", model.SeckillTxTaskPending, time.Now()).
			Order("next_retry_at ASC").
			Limit(limit).
			Find(&candidates).Error; err != nil {
			return err
		}

		if len(candidates) == 0 {
			tasks = nil
			return nil
		}

		claimed := make([]*model.SeckillTxTask, 0, len(candidates))
		for _, task := range candidates {
			result := tx.Model(&model.SeckillTxTask{}).
				Where("id = ? AND status = ? AND next_retry_at <= ?", task.ID, model.SeckillTxTaskPending, time.Now()).
				Updates(map[string]interface{}{
					"next_retry_at": time.Now().Add(lease),
					"last_error":    "dispatch lease claimed",
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				claimed = append(claimed, task)
			}
		}

		tasks = claimed
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("抢占主消息待发送任务失败: %w", err)
	}

	return tasks, nil
}

// MarkSent 标记发送成功
func (r *SeckillTxTaskRepo) MarkSent(ctx context.Context, id int64) error {
	now := time.Now()
	return DB.WithContext(ctx).
		Model(&model.SeckillTxTask{}).
		Where("id = ? AND status = ?", id, model.SeckillTxTaskPending).
		Updates(map[string]interface{}{
			"status":  model.SeckillTxTaskSent,
			"sent_at": &now,
		}).Error
}

// MarkRetry 标记重试；超过最大重试后标记 dead
func (r *SeckillTxTaskRepo) MarkRetry(ctx context.Context, task *model.SeckillTxTask, lastErr string, maxRetry int) error {
	retryCount := task.RetryCount + 1
	status := model.SeckillTxTaskPending
	if retryCount >= maxRetry {
		status = model.SeckillTxTaskDead
	}

	nextDelay := 3 * time.Second
	for i := 1; i < retryCount; i++ {
		nextDelay *= 2
		if nextDelay >= 2*time.Minute {
			nextDelay = 2 * time.Minute
			break
		}
	}

	updates := map[string]interface{}{
		"retry_count":   retryCount,
		"last_error":    trimError(lastErr, 500),
		"status":        status,
		"next_retry_at": time.Now().Add(nextDelay),
	}

	return DB.WithContext(ctx).
		Model(&model.SeckillTxTask{}).
		Where("id = ? AND status = ?", task.ID, model.SeckillTxTaskPending).
		Updates(updates).Error
}

// FetchDeadTasks 拉取可重放 dead 任务
func (r *SeckillTxTaskRepo) FetchDeadTasks(ctx context.Context, limit int, olderThan time.Duration) ([]*model.SeckillTxTask, error) {
	if limit <= 0 {
		limit = 50
	}
	if olderThan <= 0 {
		olderThan = 5 * time.Minute
	}

	cutoff := time.Now().Add(-olderThan)
	var tasks []*model.SeckillTxTask
	err := DB.WithContext(ctx).
		Where("status = ? AND updated_at <= ?", model.SeckillTxTaskDead, cutoff).
		Order("updated_at ASC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("查询主消息 dead 任务失败: %w", err)
	}
	return tasks, nil
}

// RequeueDeadTask dead 重入队
func (r *SeckillTxTaskRepo) RequeueDeadTask(ctx context.Context, id int64) error {
	return DB.WithContext(ctx).
		Model(&model.SeckillTxTask{}).
		Where("id = ? AND status = ?", id, model.SeckillTxTaskDead).
		Updates(map[string]interface{}{
			"status":        model.SeckillTxTaskPending,
			"next_retry_at": time.Now(),
			"last_error":    "auto replay from dead queue",
		}).Error
}

func isDuplicateKeyErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1062") || strings.Contains(msg, "Duplicate entry")
}

func trimError(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
