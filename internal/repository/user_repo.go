package repository

import (
	"context"
	"errors"
	"fmt"

	"seckill-system/internal/model"

	"gorm.io/gorm"
)

// ==================== UserRepo 用户仓储层 ====================

var (
	// ErrUsernameTaken 用户名已被注册（唯一索引冲突）
	ErrUsernameTaken = errors.New("用户名已被注册")
)

// UserRepo 封装用户相关的数据库操作
type UserRepo struct {
	db *gorm.DB
}

// NewUserRepo 创建用户仓储实例
func NewUserRepo() *UserRepo {
	return &UserRepo{db: DB}
}

// Create 创建用户
//
// 依赖 users 表的 idx_username 唯一索引防重。
// 如果用户名已存在，返回 ErrUsernameTaken。
func (r *UserRepo) Create(ctx context.Context, user *model.User) error {
	if err := r.db.WithContext(ctx).Create(user).Error; err != nil {
		if isDuplicateKeyError(err) {
			return ErrUsernameTaken
		}
		return fmt.Errorf("创建用户失败: %w", err)
	}
	return nil
}

// FindByUsername 根据用户名查询用户
//
// 命中索引 idx_username，O(1) 查询。
// 未找到返回 (nil, nil)。
func (r *UserRepo) FindByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).Where("username = ?", username).First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return &user, nil
}

// FindByID 根据 ID 查询用户
func (r *UserRepo) FindByID(ctx context.Context, id int64) (*model.User, error) {
	var user model.User
	err := r.db.WithContext(ctx).First(&user, id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return &user, nil
}
