package service

import (
	"context"
	"errors"
	"fmt"

	"seckill-system/internal/model"
	"seckill-system/internal/repository"
	"seckill-system/pkg/auth"

	"golang.org/x/crypto/bcrypt"
)

// ==================== 用户 Service ====================
//
// 密码加密技术选型: bcrypt (golang.org/x/crypto/bcrypt)
//
// 选型原因：
//   - 自适应哈希：cost 参数可随硬件升级调高，抗暴力破解
//   - 自带盐值：每次哈希自动生成随机 salt，无需手动管理
//   - 行业标准：OWASP 推荐，GitHub/Shopify/Stripe 等大厂广泛使用
//
// 替代方案对比：
//
//   | 算法         | 优点                              | 缺点                         |
//   |------------- |-----------------------------------|------------------------------|
//   | bcrypt(选用)  | 成熟稳定，Go 官方库支持           | CPU-hard 但非 Memory-hard     |
//   | argon2id     | Memory-hard，抗 GPU/ASIC 攻击     | 参数调优复杂，Go 库 API 不直觉 |
//   | scrypt       | Memory-hard，也是不错的选择       | 社区采用率低于 bcrypt/argon2   |
//   | SHA256+salt  | 速度快                            | 非自适应，不安全（❌ 不推荐）   |
//
// DefaultCost=10：每次 Hash 约 100ms（单核），注册/登录低频场景可接受。

var (
	// ErrInvalidCredentials 用户名或密码错误
	ErrInvalidCredentials = errors.New("用户名或密码错误")

	// dummyHash 预生成的 bcrypt 哈希值，用于防御时序攻击。
	// 当用户名不存在时，仍执行一次 bcrypt.CompareHashAndPassword，
	// 使得"用户不存在"和"密码错误"两种情况的响应时间一致，
	// 阻止攻击者通过响应时间差异枚举有效用户名。
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy-timing-attack-defense"), bcrypt.DefaultCost)
)

// UserService 用户业务 Service
type UserService struct {
	userRepo *repository.UserRepo
}

// NewUserService 创建用户 Service 实例
func NewUserService(userRepo *repository.UserRepo) *UserService {
	return &UserService{userRepo: userRepo}
}

// Register 用户注册
//
// 流程：
//  1. bcrypt 加密密码（cost=10, ~100ms）
//  2. 创建用户记录（username 唯一索引防重）
//
// 返回：
//   - nil: 注册成功
//   - ErrUsernameTaken: 用户名已被注册
//   - 其他 error: 系统错误
func (s *UserService) Register(ctx context.Context, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("密码加密失败: %w", err)
	}

	user := &model.User{
		Username:     username,
		PasswordHash: string(hash),
		Status:       0,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		if errors.Is(err, repository.ErrUsernameTaken) {
			return repository.ErrUsernameTaken
		}
		return err
	}
	return nil
}

// Login 用户登录
//
// 流程：
//  1. 根据用户名查询用户
//  2. bcrypt 比对密码
//  3. 签发 JWT Token（HMAC-SHA256）
//
// 返回 JWT Token 字符串（不含 "Bearer " 前缀）。
func (s *UserService) Login(ctx context.Context, username, password string) (string, int64, error) {
	user, err := s.userRepo.FindByUsername(ctx, username)
	if err != nil {
		return "", 0, err
	}
	if user == nil {
		// 防时序攻击：用户不存在时仍执行一次 bcrypt 比对，
		// 使响应时间与"密码错误"场景一致，阻止用户名枚举。
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return "", 0, ErrInvalidCredentials
	}

	// bcrypt 密码比对（常量时间比较，防时序攻击）
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", 0, ErrInvalidCredentials
	}

	// 签发 JWT
	token, err := auth.GenerateToken(user.ID, user.Username)
	if err != nil {
		return "", 0, fmt.Errorf("生成 Token 失败: %w", err)
	}

	return token, user.ID, nil
}
