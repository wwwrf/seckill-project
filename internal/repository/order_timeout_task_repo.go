package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"seckill-system/internal/model"
	"gorm.io/gorm"
)

// OrderTimeoutTaskRepo 延迟消息补偿任务仓储
type OrderTimeoutTaskRepo struct{}

func NewOrderTimeoutTaskRepo() *OrderTimeoutTaskRepo {
	return &OrderTimeoutTaskRepo{}
}

// CreateIfNotExists 创建补偿任务（幂等）
func (r *OrderTimeoutTaskRepo) CreateIfNotExists(ctx context.Context, task *model.OrderTimeoutTask) error {
	if task.NextRetryAt.IsZero() {
		task.NextRetryAt = time.Now().Add(30 * time.Second)
	}
	if err := DB.WithContext(ctx).Create(task).Error; err != nil {
		if isDuplicateKeyErrLocal(err) {
			return nil
		}
		return fmt.Errorf("创建补偿任务失败: %w", err)
	}
	return nil
}

// FetchDueTasks 拉取到期待重试任务
func (r *OrderTimeoutTaskRepo) FetchDueTasks(ctx context.Context, limit int) ([]*model.OrderTimeoutTask, error) {
	if limit <= 0 {
		limit = 100
	}

	var tasks []*model.OrderTimeoutTask
	err := DB.WithContext(ctx).
		Where("status = ? AND next_retry_at <= ?", model.OrderTimeoutTaskPending, time.Now()).
		Order("next_retry_at ASC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("查询到期补偿任务失败: %w", err)
	}
	return tasks, nil
}

// ClaimPendingTasks 抢占到期补偿任务，避免多实例补偿器重复发送。
func (r *OrderTimeoutTaskRepo) ClaimPendingTasks(ctx context.Context, limit int, lease time.Duration) ([]*model.OrderTimeoutTask, error) {
	if limit <= 0 {
		limit = 100
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}

	var tasks []*model.OrderTimeoutTask
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []*model.OrderTimeoutTask
		if err := tx.
			Where("status = ? AND next_retry_at <= ?", model.OrderTimeoutTaskPending, time.Now()).
			Order("next_retry_at ASC").
			Limit(limit).
			Find(&candidates).Error; err != nil {
			return err
		}

		if len(candidates) == 0 {
			tasks = nil
			return nil
		}

		claimed := make([]*model.OrderTimeoutTask, 0, len(candidates))
		for _, task := range candidates {
			result := tx.Model(&model.OrderTimeoutTask{}).
				Where("id = ? AND status = ? AND next_retry_at <= ?", task.ID, model.OrderTimeoutTaskPending, time.Now()).
				Updates(map[string]interface{}{
					"next_retry_at": time.Now().Add(lease),
					"last_error":    "compensator lease claimed",
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
		return nil, fmt.Errorf("抢占到期补偿任务失败: %w", err)
	}

	return tasks, nil
}

// MarkSent 标记任务发送成功
func (r *OrderTimeoutTaskRepo) MarkSent(ctx context.Context, id int64) error {
	now := time.Now()
	return DB.WithContext(ctx).
		Model(&model.OrderTimeoutTask{}).
		Where("id = ? AND status = ?", id, model.OrderTimeoutTaskPending).
		Updates(map[string]interface{}{
			"status":  model.OrderTimeoutTaskSent,
			"sent_at": &now,
		}).Error
}

// MarkRetry 标记重试信息；超过最大重试后标记为 dead
func (r *OrderTimeoutTaskRepo) MarkRetry(ctx context.Context, task *model.OrderTimeoutTask, lastErr string, maxRetry int) error {
	retryCount := task.RetryCount + 1
	status := model.OrderTimeoutTaskPending
	if retryCount >= maxRetry {
		status = model.OrderTimeoutTaskDead
	}

	// 指数退避：30s、60s、120s...上限 30 分钟
	nextDelay := 30 * time.Second
	for i := 1; i < retryCount; i++ {
		nextDelay *= 2
		if nextDelay >= 30*time.Minute {
			nextDelay = 30 * time.Minute
			break
		}
	}

	updates := map[string]interface{}{
		"retry_count":   retryCount,
		"last_error":    trimErr(lastErr, 500),
		"status":        status,
		"next_retry_at": time.Now().Add(nextDelay),
	}

	return DB.WithContext(ctx).
		Model(&model.OrderTimeoutTask{}).
		Where("id = ? AND status = ?", task.ID, model.OrderTimeoutTaskPending).
		Updates(updates).Error
}

// FetchDeadTasks 拉取可重放的 dead 任务
func (r *OrderTimeoutTaskRepo) FetchDeadTasks(ctx context.Context, limit int, olderThan time.Duration) ([]*model.OrderTimeoutTask, error) {
	if limit <= 0 {
		limit = 50
	}
	if olderThan <= 0 {
		olderThan = 10 * time.Minute
	}

	cutoff := time.Now().Add(-olderThan)
	var tasks []*model.OrderTimeoutTask
	err := DB.WithContext(ctx).
		Where("status = ? AND updated_at <= ?", model.OrderTimeoutTaskDead, cutoff).
		Order("updated_at ASC").
		Limit(limit).
		Find(&tasks).Error
	if err != nil {
		return nil, fmt.Errorf("查询 dead 任务失败: %w", err)
	}

	return tasks, nil
}

// RequeueDeadTask 将 dead 任务重置为 pending，交由补偿器继续投递
func (r *OrderTimeoutTaskRepo) RequeueDeadTask(ctx context.Context, id int64) error {
	return DB.WithContext(ctx).
		Model(&model.OrderTimeoutTask{}).
		Where("id = ? AND status = ?", id, model.OrderTimeoutTaskDead).
		Updates(map[string]interface{}{
			"status":        model.OrderTimeoutTaskPending,
			"next_retry_at": time.Now(),
			"last_error":    "auto replay from dead-letter queue",
		}).Error
}

func isDuplicateKeyErrLocal(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "1062") || strings.Contains(msg, "Duplicate entry")
}

func trimErr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
