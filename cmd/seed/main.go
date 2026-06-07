package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"
	"sync"
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

// ==================== 压测数据预热脚本 (Data Seeder) ====================
//
// 用途：为 k6 压测填充海量真实数据
// 运行：go run cmd/seed/main.go
//
// 执行流程：
//   1. 清空旧数据（MySQL 表 + Redis Key）
//   2. 并发生成 10,000 测试用户，批量插入 MySQL
//   3. 为每个用户生成 JWT Token，写入 CSV（供 k6 读取）
//   4. 创建秒杀活动，预热 Redis（库存 Key + 布隆过滤器 + Pending Hash）

const (
	// 数据库配置默认值
	defaultMySQLDSN = "root:@Wrf120855@tcp(127.0.0.1:3306)/ecommerce_db?charset=utf8mb4&parseTime=true&loc=Local"

	// Redis 配置默认值
	defaultRedisAddr     = "127.0.0.1:6379"
	defaultRedisPassword = ""
	defaultRedisDB       = 0

	// 数据量配置默认值
	defaultTotalUsers            = 10000
	defaultBatchSize             = 500
	defaultTokenWorkers          = 10
	defaultSeckillStock          = int64(2000)
	defaultHistoricalOrders      = 80000
	defaultHistoricalPaidRatio   = 0.7
	defaultHistoricalCancelRatio = 0.1

	// JWT 配置默认值（与主服务一致）
	defaultJWTSecret = "seckill-system-jwt-secret-key-2026-change-in-production"
	defaultJWTExpiry = 24

	// 输出文件
	defaultCSVFilePath = "loadtest/users_token.csv"

	// 历史活动 ID 起始值（用于造订单时规避 uk_user_activity 冲突）
	historicalActivityBase = int64(100000)

	// Redis 预热标记 Key（用于快速确认 seed 是否已运行）
	seedWarmupMarkerKey = "seckill:warmup:last"
)

var (
	// 运行时配置（可通过环境变量覆盖）
	mysqlDSN = getenvStr("SEED_MYSQL_DSN", defaultMySQLDSN)

	redisAddr     = getenvStr("SEED_REDIS_ADDR", defaultRedisAddr)
	redisPassword = getenvStr("SEED_REDIS_PASSWORD", defaultRedisPassword)
	redisDB       = getenvInt("SEED_REDIS_DB", defaultRedisDB)

	totalUsers            = getenvInt("SEED_TOTAL_USERS", defaultTotalUsers)
	batchSize             = getenvInt("SEED_BATCH_SIZE", defaultBatchSize)
	tokenWorkers          = getenvInt("SEED_TOKEN_WORKERS", defaultTokenWorkers)
	seckillStock          = getenvInt64("SEED_STOCK", defaultSeckillStock)
	historicalOrders      = getenvInt("SEED_HISTORICAL_ORDERS", defaultHistoricalOrders)
	historicalPaidRatio   = getenvFloat64("SEED_HISTORICAL_PAID_RATIO", defaultHistoricalPaidRatio)
	historicalCancelRatio = getenvFloat64("SEED_HISTORICAL_CANCEL_RATIO", defaultHistoricalCancelRatio)

	jwtSecret = getenvStr("SEED_JWT_SECRET", defaultJWTSecret)
	jwtExpiry = getenvInt("SEED_JWT_EXPIRY_HOURS", defaultJWTExpiry)

	csvFilePath = getenvStr("SEED_CSV_PATH", defaultCSVFilePath)
)

func main() {
	start := time.Now()
	fmt.Println("🚀 压测数据预热脚本启动...")

	// ---- 1. 初始化基础设施 ----
	db := initDB()
	rdb := initRedis()
	ctx := context.Background()

	// 初始化最小日志（auth.Init 依赖 logger）
	logger.Init(&logger.Config{
		Level:    "error",
		Filename: "logs/seed.log",
		MaxSize:  10,
	})

	// 初始化 JWT（用于生成 Token）
	auth.Init(&auth.Config{
		Secret:      jwtSecret,
		ExpiryHours: jwtExpiry,
	})

	// ---- 2. 清空旧数据 ----
	fmt.Println("🧹 清空旧数据...")
	cleanData(db, rdb, ctx)
	fmt.Printf("   ✅ 旧数据清理完成 [%v]\n", time.Since(start))

	// ---- 3. 并发生成用户 + Token ----
	fmt.Printf("👥 开始生成 %d 个测试用户...\n", totalUsers)
	users := generateUsers(totalUsers)

	// 批量插入 MySQL
	insertStart := time.Now()
	if err := db.CreateInBatches(&users, batchSize).Error; err != nil {
		log.Fatalf("❌ 批量插入用户失败: %v", err)
	}
	fmt.Printf("   ✅ MySQL 批量插入完成 [%v]\n", time.Since(insertStart))

	// 并发生成 JWT Token 并写入 CSV
	tokenStart := time.Now()
	records := generateTokensConcurrently(users)
	writeCSV(records)
	fmt.Printf("   ✅ Token 生成 + CSV 写入完成 [%v]\n", time.Since(tokenStart))

	// ---- 4. 创建秒杀活动 + Redis 预热 ----
	fmt.Println("🎯 创建秒杀活动并预热 Redis...")
	productID, activityID := seedActivity(db, rdb, ctx)
	verifyRedisWarmup(rdb, ctx, activityID, productID)
	fmt.Printf("   ✅ 秒杀活动创建 + Redis 预热完成\n")

	// ---- 5. 批量生成历史订单（用于 DB 压测基线） ----
	if historicalOrders > 0 {
		fmt.Printf("🧱 开始批量造历史订单（目标 %d）...\n", historicalOrders)
		orderStart := time.Now()
		seedHistoricalOrders(db, users, productID, historicalOrders)
		fmt.Printf("   ✅ 历史订单造数完成 [%v]\n", time.Since(orderStart))
	}

	// ---- 6. 打印汇总 ----
	elapsed := time.Since(start)
	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("🎉 数据预热完成！总耗时: %v\n", elapsed)
	fmt.Printf("   用户数: %d\n", totalUsers)
	fmt.Printf("   历史订单数: %d\n", historicalOrders)
	fmt.Printf("   秒杀库存: %d\n", seckillStock)
	fmt.Printf("   CSV 文件: %s\n", csvFilePath)
	fmt.Printf("   Redis 库存 Key: seckill:stock:%d:%d\n", activityID, productID)
	fmt.Println("========================================")
}

// ==================== 基础设施初始化 ====================

func initDB() *gorm.DB {
	db, err := gorm.Open(mysql.Open(mysqlDSN), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		SkipDefaultTransaction:                   true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		log.Fatalf("❌ 连接 MySQL 失败: %v", err)
	}

	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetMaxIdleConns(20)

	// AutoMigrate 确保表存在
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
		PoolSize: 50,
	})
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("❌ 连接 Redis 失败: %v", err)
	}
	return rdb
}

// ==================== 清空旧数据 ====================

func cleanData(db *gorm.DB, rdb *redis.Client, ctx context.Context) {
	// 清理 MySQL 表（使用 TRUNCATE 极速清空）
	tables := []string{"order_items", "orders", "order_timeout_tasks", "seckill_tx_tasks", "stock_deduct_logs", "seckill_activities", "products", "users"}
	for _, table := range tables {
		if err := db.Exec("TRUNCATE TABLE " + table).Error; err != nil {
			log.Fatalf("❌ TRUNCATE %s 失败: %v", table, err)
		}
	}

	// 清理 Redis Key（通配符扫描删除）
	patterns := []string{
		"seckill:stock:*",
		"seckill:purchased:*",
		"seckill:pending:*",
		"seckill:canceled:*",
		"seckill:processing:*",
		"seckill:bloom:*",
		"benchmark:summary:*",
		"product:*",
		"activity:*",
	}
	for _, pattern := range patterns {
		keys := make([]string, 0, 256)
		iter := rdb.Scan(ctx, 0, pattern, 1000).Iterator()
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
			if len(keys) >= 500 {
				if err := rdb.Del(ctx, keys...).Err(); err != nil {
					log.Fatalf("❌ Redis DEL 失败 pattern=%s: %v", pattern, err)
				}
				keys = keys[:0]
			}
		}
		if err := iter.Err(); err != nil {
			log.Fatalf("❌ Redis SCAN 失败 pattern=%s: %v", pattern, err)
		}
		if len(keys) > 0 {
			if err := rdb.Del(ctx, keys...).Err(); err != nil {
				log.Fatalf("❌ Redis DEL 失败 pattern=%s: %v", pattern, err)
			}
		}
	}
}

// ==================== 生成用户 ====================

func generateUsers(count int) []model.User {
	// 预计算一个通用的 bcrypt hash（所有测试用户使用相同密码 "test123456"）
	hash, err := bcrypt.GenerateFromPassword([]byte("test123456"), bcrypt.MinCost) // MinCost 极速
	if err != nil {
		log.Fatalf("❌ 密码哈希失败: %v", err)
	}
	passwordHash := string(hash)

	users := make([]model.User, count)
	for i := 0; i < count; i++ {
		users[i] = model.User{
			Username:     fmt.Sprintf("loadtest_user_%05d", i+1),
			PasswordHash: passwordHash,
			Status:       0,
		}
	}
	return users
}

// ==================== 并发生成 Token ====================

type userToken struct {
	UserID int64
	Token  string
}

func generateTokensConcurrently(users []model.User) []userToken {
	results := make([]userToken, len(users))
	var wg sync.WaitGroup

	ch := make(chan int, len(users))
	for i := range users {
		ch <- i
	}
	close(ch)

	for w := 0; w < tokenWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range ch {
				u := &users[idx]
				token, err := auth.GenerateToken(u.ID, u.Username)
				if err != nil {
					log.Printf("⚠️  用户 %d Token 生成失败: %v", u.ID, err)
					continue
				}
				results[idx] = userToken{UserID: u.ID, Token: token}
			}
		}()
	}

	wg.Wait()
	return results
}

// ==================== 写入 CSV ====================

func writeCSV(records []userToken) {
	// 确保目录存在
	if err := os.MkdirAll("loadtest", 0755); err != nil {
		log.Fatalf("❌ 创建 loadtest 目录失败: %v", err)
	}

	file, err := os.Create(csvFilePath)
	if err != nil {
		log.Fatalf("❌ 创建 CSV 文件失败: %v", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 写入 CSV 头
	if err := writer.Write([]string{"user_id", "token"}); err != nil {
		log.Fatalf("❌ 写入 CSV 头失败: %v", err)
	}

	count := 0
	for _, r := range records {
		if r.Token == "" {
			continue
		}
		if err := writer.Write([]string{
			strconv.FormatInt(r.UserID, 10),
			r.Token,
		}); err != nil {
			log.Fatalf("❌ 写入 CSV 记录失败: %v", err)
		}
		count++
	}

	if err := writer.Error(); err != nil {
		log.Fatalf("❌ 刷新 CSV 失败: %v", err)
	}

	fmt.Printf("   📄 CSV 写入 %d 条记录 → %s\n", count, csvFilePath)
}

// ==================== 创建秒杀活动 + Redis 预热 ====================

func seedActivity(db *gorm.DB, rdb *redis.Client, ctx context.Context) (int64, int64) {
	// 1. 创建商品
	product := &model.Product{
		Title:         "【压测】iPhone 16 Pro Max 256GB",
		Description:   "k6 压测专用商品",
		OriginalPrice: 999900,
		Status:        1,
	}
	if err := db.Create(product).Error; err != nil {
		log.Fatalf("❌ 创建商品失败: %v", err)
	}

	// 2. 创建秒杀活动
	now := time.Now()
	activity := &model.SeckillActivity{
		ProductID:      product.ID,
		ActivityName:   "【压测】秒杀活动 - iPhone 直降8000",
		SeckillPrice:   199900,
		TotalStock:     seckillStock,
		AvailableStock: seckillStock,
		Version:        0,
		StartTime:      now.Add(-1 * time.Hour), // 已开始
		EndTime:        now.Add(24 * time.Hour),
		Status:         2, // 进行中
	}
	if err := db.Create(activity).Error; err != nil {
		log.Fatalf("❌ 创建秒杀活动失败: %v", err)
	}

	fmt.Printf("   📦 商品 ID: %d, 活动 ID: %d\n", product.ID, activity.ID)

	// 3. 极其关键的 Redis 预热
	stockKey := e.BuildStockKey(activity.ID, product.ID)
	pendingKey := e.BuildPendingKey(activity.ID)
	bloomMember := e.BuildBloomMember(activity.ID, product.ID)

	// 3.1 写入库存 Key
	if err := rdb.Set(ctx, stockKey, seckillStock, 0).Err(); err != nil {
		log.Fatalf("❌ Redis 写入库存失败: %v", err)
	}
	fmt.Printf("   🔑 Redis SET %s = %d\n", stockKey, seckillStock)

	// 3.2 布隆过滤器（BF.ADD，如果 RedisBloom 模块未加载则跳过）
	if err := rdb.Do(ctx, "BF.ADD", e.KeySeckillBloomItems, bloomMember).Err(); err != nil {
		fmt.Printf("   ⚠️  BloomFilter BF.ADD 失败（RedisBloom 模块可能未加载，跳过）: %v\n", err)
	} else {
		fmt.Printf("   🌸 BloomFilter BF.ADD %s → %s\n", e.KeySeckillBloomItems, bloomMember)
	}

	// 3.3 初始化 Pending Hash（空 Hash，后续由 Lua 脚本写入）
	// 确保 Key 存在以避免 HSET 在空 Key 上的首次延迟
	if err := rdb.Del(ctx, pendingKey).Err(); err != nil {
		log.Fatalf("❌ 初始化 Pending Hash 失败: %v", err)
	}
	fmt.Printf("   📋 Pending Hash 已初始化: %s\n", pendingKey)

	marker := fmt.Sprintf("activity=%d,product=%d,stock=%d,ts=%s", activity.ID, product.ID, seckillStock, time.Now().Format(time.RFC3339))
	if err := rdb.Set(ctx, seedWarmupMarkerKey, marker, 24*time.Hour).Err(); err != nil {
		log.Fatalf("❌ 写入 Seed 预热标记失败: %v", err)
	}
	fmt.Printf("   🏷️  Seed 预热标记已写入: %s\n", seedWarmupMarkerKey)

	return product.ID, activity.ID
}

func verifyRedisWarmup(rdb *redis.Client, ctx context.Context, activityID, productID int64) {
	stockKey := e.BuildStockKey(activityID, productID)
	purchasedKey := e.BuildPurchasedKey(activityID, productID)
	pendingKey := e.BuildPendingKey(activityID)
	bloomMember := e.BuildBloomMember(activityID, productID)

	stockVal, err := rdb.Get(ctx, stockKey).Result()
	if err != nil {
		log.Fatalf("❌ Redis 自检失败: 读取库存 Key 失败 key=%s err=%v", stockKey, err)
	}

	purchasedCount, err := rdb.SCard(ctx, purchasedKey).Result()
	if err != nil {
		log.Fatalf("❌ Redis 自检失败: 读取已购集合失败 key=%s err=%v", purchasedKey, err)
	}

	pendingLen, err := rdb.HLen(ctx, pendingKey).Result()
	if err != nil {
		log.Fatalf("❌ Redis 自检失败: 读取 pending 失败 key=%s err=%v", pendingKey, err)
	}

	bloomCmd := rdb.Do(ctx, "BF.EXISTS", e.KeySeckillBloomItems, bloomMember)
	bloomRaw, bloomErr := bloomCmd.Result()
	if bloomErr != nil {
		fmt.Printf("   ⚠️  Redis 自检: Bloom BF.EXISTS 失败（可能未安装 RedisBloom）: %v\n", bloomErr)
	} else {
		bloomExists := false
		switch v := bloomRaw.(type) {
		case bool:
			bloomExists = v
		case int64:
			bloomExists = v == 1
		case string:
			bloomExists = v == "1" || v == "true"
		default:
			fmt.Printf("   ⚠️  Redis 自检: Bloom BF.EXISTS 返回未知类型=%T，按不存在处理\n", bloomRaw)
		}
		fmt.Printf("   ✅ Redis 自检: Bloom member exists=%t (%s)\n", bloomExists, bloomMember)
	}

	marker, markerErr := rdb.Get(ctx, seedWarmupMarkerKey).Result()
	if markerErr != nil {
		log.Fatalf("❌ Redis 自检失败: 读取预热标记失败 key=%s err=%v", seedWarmupMarkerKey, markerErr)
	}

	fmt.Printf("   ✅ Redis 自检: stock=%s value=%s\n", stockKey, stockVal)
	fmt.Printf("   ✅ Redis 自检: purchased=%s count=%d\n", purchasedKey, purchasedCount)
	fmt.Printf("   ✅ Redis 自检: pending=%s hlen=%d（seed 后应为 0）\n", pendingKey, pendingLen)
	fmt.Printf("   ✅ Redis 自检: marker=%s value=%s\n", seedWarmupMarkerKey, marker)
}

// ==================== 批量生成历史订单（DB 压测基线） ====================

func seedHistoricalOrders(db *gorm.DB, users []model.User, productID int64, total int) {
	if total <= 0 || len(users) == 0 {
		return
	}

	if historicalPaidRatio < 0 {
		historicalPaidRatio = 0
	}
	if historicalCancelRatio < 0 {
		historicalCancelRatio = 0
	}
	if historicalPaidRatio+historicalCancelRatio > 1 {
		historicalCancelRatio = 1 - historicalPaidRatio
		if historicalCancelRatio < 0 {
			historicalCancelRatio = 0
		}
	}

	bucketCount := (total + len(users) - 1) / len(users)
	createHistoricalActivities(db, productID, bucketCount)

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	created := 0

	for created < total {
		remain := total - created
		currBatch := batchSize
		if currBatch <= 0 {
			currBatch = 500
		}
		if remain < currBatch {
			currBatch = remain
		}

		orders := make([]model.Order, 0, currBatch)
		for i := 0; i < currBatch; i++ {
			globalIndex := created + i
			u := users[globalIndex%len(users)]
			bucket := globalIndex / len(users)
			activityID := historicalActivityBase + int64(bucket)

			createdAt := time.Now().Add(-time.Duration(rng.Intn(14*24*60)) * time.Minute)
			status, payTime, cancelTime := randomOrderStatus(rng, createdAt)

			orders = append(orders, model.Order{
				OrderNo:     fmt.Sprintf("seed_hist_%d_%d_%d", activityID, u.ID, globalIndex),
				UserID:      u.ID,
				ActivityID:  activityID,
				TotalAmount: 199900,
				PayAmount:   199900,
				Status:      status,
				OrderType:   model.OrderTypeSeckill,
				PayTime:     payTime,
				CancelTime:  cancelTime,
				CreatedAt:   createdAt,
				UpdatedAt:   createdAt,
			})
		}

		if err := db.CreateInBatches(&orders, len(orders)).Error; err != nil {
			log.Fatalf("❌ 批量插入历史订单失败: %v", err)
		}

		items := make([]model.OrderItem, 0, len(orders))
		for _, order := range orders {
			items = append(items, model.OrderItem{
				OrderID:       order.ID,
				ProductID:     productID,
				SnapshotTitle: "【压测】历史订单商品",
				SnapshotPrice: 199900,
				Quantity:      1,
				TotalPrice:    199900,
				CreatedAt:     order.CreatedAt,
				UpdatedAt:     order.UpdatedAt,
			})
		}
		if err := db.CreateInBatches(&items, len(items)).Error; err != nil {
			log.Fatalf("❌ 批量插入历史订单明细失败: %v", err)
		}

		created += currBatch
	}
}

func createHistoricalActivities(db *gorm.DB, productID int64, bucketCount int) {
	if bucketCount <= 0 {
		return
	}

	baseTime := time.Now().Add(-30 * 24 * time.Hour)
	activities := make([]model.SeckillActivity, 0, bucketCount)
	for i := 0; i < bucketCount; i++ {
		start := baseTime.Add(time.Duration(i) * time.Hour)
		end := start.Add(2 * time.Hour)
		activities = append(activities, model.SeckillActivity{
			ID:             historicalActivityBase + int64(i),
			ProductID:      productID,
			ActivityName:   fmt.Sprintf("【压测】历史活动-%d", i+1),
			SeckillPrice:   199900,
			TotalStock:     100000,
			AvailableStock: 100000,
			Version:        0,
			StartTime:      start,
			EndTime:        end,
			Status:         3, // 已结束
		})
	}

	if err := db.CreateInBatches(&activities, batchSize).Error; err != nil {
		log.Fatalf("❌ 批量插入历史活动失败: %v", err)
	}
}

func randomOrderStatus(rng *rand.Rand, createdAt time.Time) (int8, *time.Time, *time.Time) {
	r := rng.Float64()
	if r < historicalPaidRatio {
		pt := createdAt.Add(time.Duration(rng.Intn(180)+1) * time.Minute)
		return model.OrderStatusPaid, &pt, nil
	}
	if r < historicalPaidRatio+historicalCancelRatio {
		ct := createdAt.Add(time.Duration(rng.Intn(120)+1) * time.Minute)
		return model.OrderStatusCancelled, nil, &ct
	}
	return model.OrderStatusPending, nil, nil
}

func getenvStr(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getenvInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func getenvFloat64(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}
