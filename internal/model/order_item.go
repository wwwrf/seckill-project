package model

import (
	"time"

	"gorm.io/gorm"
)

// OrderItem 履约子单表
//
// 核心设计要点：
//  1. SnapshotTitle / SnapshotPrice 记录下单瞬间的商品信息快照
//     即使商品后续改名或调价，历史订单的对账数据不会受到影响
//  2. 一个 Order 对应多个 OrderItem（当前秒杀场景为 1:1，预留扩展能力）
//  3. TotalPrice = SnapshotPrice * Quantity，冗余存储便于报表聚合
type OrderItem struct {
	ID            int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	OrderID       int64          `gorm:"column:order_id;not null;index:idx_order_id;comment:关联订单ID" json:"orderId"`
	ProductID     int64          `gorm:"column:product_id;not null;comment:关联商品ID" json:"productId"`
	SnapshotTitle string         `gorm:"column:snapshot_title;type:varchar(128);not null;comment:下单时商品名称快照" json:"snapshotTitle"`
	SnapshotPrice int64          `gorm:"column:snapshot_price;not null;comment:下单时商品价格快照-单位分" json:"snapshotPrice"`
	Quantity      int            `gorm:"column:quantity;default:1;not null;comment:购买数量" json:"quantity"`
	TotalPrice    int64          `gorm:"column:total_price;not null;comment:小计金额-单位分" json:"totalPrice"`
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
	UpdatedAt     time.Time      `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updatedAt"`
	DeletedAt     gorm.DeletedAt `gorm:"column:deleted_at;index;comment:软删除时间" json:"-"`
}

// TableName 显式指定表名
func (OrderItem) TableName() string {
	return "order_items"
}
