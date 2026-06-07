package model

import "time"

// StockDeductLog 库存扣减流水表
//
// 核心设计要点：
//  1. 每一次库存变动（扣减或回滚）都会产生一条流水记录，用于对账和问题排查
//  2. Type 字段区分操作类型：1-扣减（下单时） 2-回滚（取消订单时）
//  3. 该表只做 INSERT，不做 UPDATE/DELETE，保证流水不可篡改（审计友好）
//  4. 不使用软删除——流水记录永远不应被删除
type StockDeductLog struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	ActivityID     int64     `gorm:"column:activity_id;not null;index:idx_activity_id;comment:秒杀活动ID" json:"activityId"`
	OrderNo        string    `gorm:"column:order_no;type:varchar(64);not null;index:idx_order_no;comment:关联订单号" json:"orderNo"`
	DeductQuantity int       `gorm:"column:deduct_quantity;not null;comment:扣减数量" json:"deductQuantity"`
	Type           int8      `gorm:"column:type;type:tinyint;not null;comment:1-扣减 2-回滚" json:"type"`
	CreatedAt      time.Time `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
}

// TableName 显式指定表名
func (StockDeductLog) TableName() string {
	return "stock_deduct_logs"
}

// 库存操作类型常量
const (
	StockDeductTypeDeduct   int8 = 1 // 扣减
	StockDeductTypeRollback int8 = 2 // 回滚
)
