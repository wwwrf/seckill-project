package model

import (
	"time"

	"gorm.io/gorm"
)

// Order 交易主单表
//
// 核心设计要点：
//  1. OrderNo 采用雪花算法生成全局唯一单号，建立唯一索引 uk_order_no 防止重复
//  2. (UserID, ActivityID) 复合唯一索引 uk_user_activity：
//     这是实现「一人一单」的数据库层终极兜底。即使 Redis 层的分布式锁失效，
//     DB 层的 Unique Key 也能保证同一用户在同一场活动中不会产生重复订单。
//     同时也是 MQ 消费端做幂等去重的核心依赖——INSERT 触发 Duplicate Key 则视为已处理。
//  3. 金额一律以「分」为单位存储，TotalAmount / PayAmount 均为 int64
//  4. Status 状态机：0-新建/待支付 → 10-已支付 → 20-已取消
//  5. OrderType 区分普通订单(0)和秒杀订单(1)，为后续分流提供依据
type Order struct {
	ID          int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	OrderNo     string         `gorm:"column:order_no;type:varchar(64);uniqueIndex:uk_order_no;not null;comment:全局唯一订单号-雪花算法" json:"orderNo"`
	UserID      int64          `gorm:"column:user_id;not null;index:idx_user_id;uniqueIndex:uk_user_activity;comment:用户ID" json:"userId"`
	ActivityID  int64          `gorm:"column:activity_id;not null;uniqueIndex:uk_user_activity;comment:秒杀活动ID" json:"activityId"`
	TotalAmount int64          `gorm:"column:total_amount;not null;comment:订单总金额-单位分" json:"totalAmount"`
	PayAmount   int64          `gorm:"column:pay_amount;not null;comment:实际支付金额-单位分" json:"payAmount"`
	Status      int8           `gorm:"column:status;type:tinyint;default:0;not null;comment:0-待支付 10-已支付 20-已取消" json:"status"`
	OrderType   int8           `gorm:"column:order_type;type:tinyint;default:1;not null;comment:0-普通订单 1-秒杀订单" json:"orderType"`
	PayTime     *time.Time     `gorm:"column:pay_time;comment:支付时间" json:"payTime"`
	CancelTime  *time.Time     `gorm:"column:cancel_time;comment:取消时间" json:"cancelTime"`
	CreatedAt   time.Time      `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
	UpdatedAt   time.Time      `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updatedAt"`
	DeletedAt   gorm.DeletedAt `gorm:"column:deleted_at;index;comment:软删除时间" json:"-"`
}

// TableName 显式指定表名
func (Order) TableName() string {
	return "orders"
}

// 订单状态常量
const (
	OrderStatusPending   int8 = 0  // 新建/待支付
	OrderStatusPaid      int8 = 10 // 已支付
	OrderStatusCancelled int8 = 20 // 已取消
)

// 订单类型常量
const (
	OrderTypeNormal  int8 = 0 // 普通订单
	OrderTypeSeckill int8 = 1 // 秒杀订单
)
