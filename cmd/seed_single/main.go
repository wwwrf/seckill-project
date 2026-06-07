package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"seckill-system/internal/model"
	"seckill-system/internal/model/e"
	"seckill-system/pkg/auth"
	"seckill-system/pkg/logger"

	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ==================== 单点测试数据脚本 (Swagger 调试用) ====================
//
// 用途：为 Swagger 手动调试生成最小测试数据集
// 运行：go run cmd/seed_single/main.go
//
// 生成内容：
//   - 1 个测试用户（用户名: test_user, 密码: test123456）
//   - 1 个商品 + 1 个秒杀活动（库存 100）
//   - 打印 JWT Token 供 Swagger 直接使用

const (
	mysqlDSN      = "root:@Wrf120855@tcp(127.0.0.1:3306)/ecommerce_db?charset=utf8mb4&parseTime=true&loc=Local"
	redisAddr     = "127.0.0.1:6379"
	redisPassword = ""
	redisDB       = 0
	jwtSecret     = "seckill-system-jwt-secret-key-2026-change-in-production"
	jwtExpiry     = 24
	stock         = int64(100)
)

func main() {
	fmt.Println("🔧 单点测试数据生成脚本启动...")
	fmt.Println()

	// ---- 初始化 ----
	logger.Init(&logger.Config{
		Level:    "error",
		Filename: "logs/seed_single.log",
		MaxSize:  10,
	})

	auth.Init(&auth.Config{
		Secret:      jwtSecret,
		ExpiryHours: jwtExpiry,
	})

	db := initDB()
	rdb := initRedis()
	ctx := context.Background()

	// ---- 清空旧数据 ----
	fmt.Println("🧹 清空旧数据...")
	tables := []string{"order_items", "orders", "order_timeout_tasks", "seckill_tx_tasks", "stock_deduct_logs", "seckill_activities", "products", "users"}
	for _, t := range tables {
		db.Exec("TRUNCATE TABLE " + t)
	}
	// 清 Redis
	for _, pattern := range []string{"seckill:*", "product:*", "activity:*"} {
		iter := rdb.Scan(ctx, 0, pattern, 1000).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
	}
	fmt.Println("   ✅ 旧数据已清空")

	// ---- 创建测试用户 ----
	hash, err := bcrypt.GenerateFromPassword([]byte("test123456"), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("❌ 密码哈希失败: %v", err)
	}
	user := &model.User{
		Username:     "test_user",
		PasswordHash: string(hash),
		Status:       0,
	}
	if err := db.Create(user).Error; err != nil {
		log.Fatalf("❌ 创建用户失败: %v", err)
	}
	fmt.Printf("   👤 用户创建成功: ID=%d, 用户名=test_user, 密码=test123456\n", user.ID)

	// ---- 生成 JWT Token ----
	token, err := auth.GenerateToken(user.ID, user.Username)
	if err != nil {
		log.Fatalf("❌ 生成 Token 失败: %v", err)
	}

	// ---- 创建商品 + 秒杀活动 ----
	product := &model.Product{
		Title:         "iPhone 16 Pro Max 256GB",
		Description:   "Apple 年度旗舰，A18 Pro 芯片",
		OriginalPrice: 999900,
		Status:        1,
	}
	if err := db.Create(product).Error; err != nil {
		log.Fatalf("❌ 创建商品失败: %v", err)
	}

	now := time.Now()
	activity := &model.SeckillActivity{
		ProductID:      product.ID,
		ActivityName:   "测试秒杀活动 - iPhone 直降8000",
		SeckillPrice:   199900,
		TotalStock:     stock,
		AvailableStock: stock,
		Version:        0,
		StartTime:      now.Add(-1 * time.Hour),
		EndTime:        now.Add(24 * time.Hour),
		Status:         2, // 进行中
	}
	if err := db.Create(activity).Error; err != nil {
		log.Fatalf("❌ 创建秒杀活动失败: %v", err)
	}
	fmt.Printf("   📦 商品 ID=%d, 活动 ID=%d, 库存=%d\n", product.ID, activity.ID, stock)

	// ---- Redis 预热 ----
	stockKey := e.BuildStockKey(activity.ID, product.ID)
	bloomMember := e.BuildBloomMember(activity.ID, product.ID)
	pendingKey := e.BuildPendingKey(activity.ID)

	rdb.Set(ctx, stockKey, stock, 0)
	fmt.Printf("   🔑 Redis SET %s = %d\n", stockKey, stock)

	if err := rdb.Do(ctx, "BF.ADD", e.KeySeckillBloomItems, bloomMember).Err(); err != nil {
		fmt.Printf("   ⚠️  BloomFilter 跳过（RedisBloom 未加载）: %v\n", err)
	} else {
		fmt.Printf("   🌸 BloomFilter BF.ADD → %s\n", bloomMember)
	}
	rdb.Del(ctx, pendingKey)

	// ---- 打印汇总 ----
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("✅ 单点测试数据生成完成！")
	fmt.Println()
	fmt.Println("📋 Swagger 测试信息：")
	fmt.Printf("   用户名: test_user\n")
	fmt.Printf("   密码:   test123456\n")
	fmt.Printf("   用户ID: %d\n", user.ID)
	fmt.Printf("   商品ID: %d\n", product.ID)
	fmt.Printf("   活动ID: %d\n", activity.ID)
	fmt.Printf("   库存:   %d\n", stock)
	fmt.Println()
	fmt.Println("🔑 JWT Token（复制到 Swagger Authorize 中使用）：")
	fmt.Printf("   Bearer %s\n", token)
	fmt.Println()
	fmt.Println("📝 秒杀请求 Body：")
	fmt.Printf("   {\"activity_id\": %d, \"product_id\": %d}\n", activity.ID, product.ID)
	fmt.Println("========================================")
}

func initDB() *gorm.DB {
	db, err := gorm.Open(mysql.Open(mysqlDSN), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		SkipDefaultTransaction:                   true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		log.Fatalf("❌ 连接 MySQL 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Product{}, &model.SeckillActivity{},
		&model.Order{}, &model.OrderItem{}, &model.StockDeductLog{}, &model.OrderTimeoutTask{}, &model.SeckillTxTask{},
	); err != nil {
		log.Fatalf("❌ AutoMigrate 失败: %v", err)
	}
	return db
}

func initRedis() *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPassword,
		DB:       redisDB,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("❌ 连接 Redis 失败: %v", err)
	}
	return rdb
}
