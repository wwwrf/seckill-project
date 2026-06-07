package model

import (
	"time"

	"gorm.io/gorm"
)

// User 用户表
// 用于短信登录、鉴权等场景，Username 设唯一索引支持快速查询
// 软删除字段 DeletedAt 配合 GORM 自动过滤已删除记录
type User struct {
	ID           int64          `gorm:"column:id;primaryKey;autoIncrement;comment:主键ID" json:"id"`
	Username     string         `gorm:"column:username;type:varchar(64);uniqueIndex:idx_username;not null;comment:用户名" json:"username"`
	PasswordHash string         `gorm:"column:password_hash;type:varchar(255);not null;comment:密码哈希" json:"-"`
	Status       int8           `gorm:"column:status;type:tinyint;default:0;not null;comment:0-正常 1-冻结" json:"status"`
	CreatedAt    time.Time      `gorm:"column:created_at;autoCreateTime;comment:创建时间" json:"createdAt"`
	UpdatedAt    time.Time      `gorm:"column:updated_at;autoUpdateTime;comment:更新时间" json:"updatedAt"`
	DeletedAt    gorm.DeletedAt `gorm:"column:deleted_at;index;comment:软删除时间" json:"-"`
}

// TableName 显式指定表名，避免 GORM 复数推导歧义
func (User) TableName() string {
	return "users"
}
