package model

import (
	"time"

	"gorm.io/gorm"
)

// SeckillActivity 秒杀活动与库存表
//
// 核心设计要点：
//  1. AvailableStock + Version 构成乐观锁机制，用于在数据库层面防止超卖
//     UPDATE SET available_stock = available_stock - 1, version = version + 1
//     WHERE id = ? AND available_stock > 0 AND version = ?
//  2. TotalStock 记录初始总库存，用于对账和监控（不参与扣减逻辑）
//  3. StartTime / EndTime 配合复合索引 idx_time 做活动时间窗口查询
//  4. Status 由定时任务或管理后台驱动，1-未开始 2-进行中 3-已结束
type SeckillActivity struct {
	ID             int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	ProductID      int64          `gorm:"column:product_id;not null;index:idx_product_id;comment:关联商品ID" json:"productId"`
	ActivityName   string         `gorm:"column:activity_name;type:varchar(128);not null;comment:活动名称" json:"activityName"`
	SeckillPrice   int64          `gorm:"column:seckill_price;not null;comment:秒杀价-单位分" json:"seckillPrice"`
	TotalStock     int64          `gorm:"column:total_stock;type:bigint unsigned;not null;comment:初始总库存" json:"totalStock"`
	AvailableStock int64          `gorm:"column:available_stock;type:bigint unsigned;not null;comment:当前可用库存" json:"availableStock"`
	Version        int64          `gorm:"column:version;default:0;not null;comment:乐观锁版本号" json:"version"`
	StartTime      time.Time      `gorm:"column:start_time;not null;index:idx_time;comment:活动开始时间" json:"startTime"`
	EndTime        time.Time      `gorm:"column:end_time;not null;index:idx_time;comment:活动结束时间" json:"endTime"`
	Status         int8           `gorm:"column:status;type:tinyint;default:1;not null;comment:1-未开始 2-进行中 3-已结束" json:"status"`
	CreatedAt      time.Time      `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
	UpdatedAt      time.Time      `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updatedAt"`
	DeletedAt      gorm.DeletedAt `gorm:"column:deleted_at;index;comment:软删除时间" json:"-"`
}

// TableName 显式指定表名
func (SeckillActivity) TableName() string {
	return "seckill_activities"
}
