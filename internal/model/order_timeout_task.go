package model

import "time"

// OrderTimeoutTask 订单超时取消延迟消息补偿任务（Outbox）
//
// 场景：链路1建单成功后发送 30 分钟延迟消息失败。
// 该表作为持久化补偿任务，由后台定时器重投 MQ。
type OrderTimeoutTask struct {
	ID         int64      `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	OrderNo    string     `gorm:"column:order_no;type:varchar(64);not null;uniqueIndex:uk_order_no;comment:订单号" json:"orderNo"`
	UserID     int64      `gorm:"column:user_id;not null;comment:用户ID" json:"userId"`
	ActivityID int64      `gorm:"column:activity_id;not null;comment:活动ID" json:"activityId"`
	ProductID  int64      `gorm:"column:product_id;not null;comment:商品ID" json:"productId"`
	Status     int8       `gorm:"column:status;type:tinyint;not null;default:0;index:idx_status_next_retry;comment:0-待重试 1-已发送 2-放弃" json:"status"`
	RetryCount int        `gorm:"column:retry_count;not null;default:0;comment:重试次数" json:"retryCount"`
	NextRetryAt time.Time `gorm:"column:next_retry_at;not null;index:idx_status_next_retry;comment:下次重试时间" json:"nextRetryAt"`
	LastError  string     `gorm:"column:last_error;type:varchar(512);comment:最后错误信息" json:"lastError"`
	SentAt     *time.Time `gorm:"column:sent_at;comment:发送成功时间" json:"sentAt"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updatedAt"`
}

func (OrderTimeoutTask) TableName() string {
	return "order_timeout_tasks"
}

const (
	OrderTimeoutTaskPending int8 = 0
	OrderTimeoutTaskSent    int8 = 1
	OrderTimeoutTaskDead    int8 = 2
)
