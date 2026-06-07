package model

import (
	"time"

	"gorm.io/gorm"
)

// Product 商品基础表 (SPU)
// OriginalPrice 以「分」为单位存储，彻底避免浮点精度问题
// 这是电商领域的行业惯例：金额一律用 int64 分来表示
type Product struct {
	ID            int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	Title         string         `gorm:"column:title;type:varchar(128);not null;comment:商品标题" json:"title"`
	Description   string         `gorm:"column:description;type:text;comment:商品描述" json:"description"`
	OriginalPrice int64          `gorm:"column:original_price;not null;comment:原价-单位分" json:"originalPrice"`
	Status        int8           `gorm:"column:status;type:tinyint;default:1;not null;comment:1-上架 2-下架" json:"status"`
	CreatedAt     time.Time      `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
	UpdatedAt     time.Time      `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updatedAt"`
	DeletedAt     gorm.DeletedAt `gorm:"column:deleted_at;index;comment:软删除时间" json:"-"`
}

// TableName 显式指定表名
func (Product) TableName() string {
	return "products"
}
