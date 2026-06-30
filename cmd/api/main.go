package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"seckill-system/api/handler"
	_ "seckill-system/docs"
	"seckill-system/internal/cron"
	mqconsumer "seckill-system/internal/mq"
	"seckill-system/internal/repository"
	"seckill-system/internal/repository/cache"
	"seckill-system/internal/router"
	"seckill-system/internal/service"
	"seckill-system/pkg/auth"
	"seckill-system/pkg/logger"
	"seckill-system/pkg/mq"
	pkgredis "seckill-system/pkg/redis"
	"seckill-system/pkg/utils"

	"github.com/apache/rocketmq-client-go/v2/rlog"
	"github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
	"go.uber.org/zap"
)

// @title           秒杀系统 API
// @version         1.0
// @description     基于 Go + Gin + Redis + RocketMQ 的高并发秒杀系统
// @host            localhost:8080
// @BasePath        /
// @securityDefinitions.apikey ApiKeyAuth
// @in              header
// @name            Authorization
// @description     Bearer JWT Token（格式：Bearer eyJhbG...)
func main() {
	// 1. 加载配置文件
	if err := initConfig(); err != nil {
		panic(fmt.Sprintf("初始化配置失败: %v", err))
	}

	// 2. 初始化日志
	if err := initLogger(); err != nil {
		panic(fmt.Sprintf("初始化日志失败: %v", err))
	}
	defer logger.Sync()

	// 静默 RocketMQ 底层日志（仅输出 warn 及以上），保持控制台清爽
	rlog.SetLogLevel("warn")

	logger.Info("秒杀系统启动中...")
	logStartupSummary()

	if err := validateRuntimeConfig(); err != nil {
		logger.Fatal("启动配置校验失败", zap.Error(err))
	}

	// 2.5 初始化 JWT
	auth.Init(&auth.Config{
		Secret:      viper.GetString("jwt.secret"),
		ExpiryHours: viper.GetInt("jwt.expiryHours"),
	})

	// 3. 初始化雪花算法
	// 优先级：环境变量 SNOWFLAKE_NODE > 配置文件 server.snowflakeNode > 默认值 1
	// 多实例部署时，每个容器通过环境变量注入不同的节点 ID，避免 ID 冲突。
	snowflakeNode := viper.GetInt64("server.snowflakeNode")
	if envNode := os.Getenv("SNOWFLAKE_NODE"); envNode != "" {
		if n, err := strconv.ParseInt(envNode, 10, 64); err == nil && n > 0 {
			snowflakeNode = n
		}
	}
	if snowflakeNode == 0 {
		snowflakeNode = 1
	}
	if err := utils.InitSnowflake(snowflakeNode); err != nil {
		logger.Fatal("初始化雪花算法失败", zap.Error(err))
	}
	logger.Info("雪花算法初始化成功", zap.Int64("node", snowflakeNode))

	// 4. 初始化 MySQL
	if err := initMySQL(); err != nil {
		logger.Fatal("初始化 MySQL 失败", zap.Error(err))
	}
	defer repository.CloseMySQL()

	// 5. 初始化 Redis
	if err := initRedis(); err != nil {
		logger.Fatal("初始化 Redis 失败", zap.Error(err))
	}
	defer pkgredis.Close()

	// 6. 初始化 RocketMQ 生产者
	if err := initMQ(); err != nil {
		logger.Fatal("初始化 RocketMQ 失败", zap.Error(err))
	}
	defer mq.Close()

	// 7. 设置 Gin 模式
	mode := viper.GetString("server.mode")
	if mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else if mode == "test" {
		gin.SetMode(gin.TestMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// 8. 初始化业务依赖（手动 DI 注入）
	//
	// 依赖链：
	//   SeckillCache + OrderRepo → SeckillService → SeckillHandler
	//   UserRepo → UserService → UserHandler
	//   ProductCache → ShopHandler
	//   OrderRepo → OrderHandler
	//   SeckillCache + DB → AdminHandler
	seckillCache := cache.NewSeckillCache(pkgredis.Client)
	orderRepo := repository.NewOrderRepo()
	txTaskRepo := repository.NewSeckillTxTaskRepo()
	timeoutTaskRepo := repository.NewOrderTimeoutTaskRepo()
	seckillSvc := service.NewSeckillService(seckillCache, orderRepo, txTaskRepo)
	seckillHandler := handler.NewSeckillHandler(seckillSvc)

	// 用户模块 DI
	userRepo := repository.NewUserRepo()
	userSvc := service.NewUserService(userRepo)
	userHandler := handler.NewUserHandler(userSvc)

	// 商品查询模块 DI（三级缓存：L1 go-cache → L2 Redis → L3 DB）
	productCache := cache.NewProductCache(pkgredis.Client, repository.DB)
	shopHandler := handler.NewShopHandler(productCache)

	// 订单查询模块 DI
	orderHandler := handler.NewOrderHandler(orderRepo)

	// 管理后台模块 DI
	adminHandler := handler.NewAdminHandler(seckillCache, repository.DB)

	// Agent 客服模块 DI（咨询Agent + 投诉RAG Agent）
	agentSvc := service.NewAgentService(orderRepo, productCache, "docs/complaints_kb.md")
	serviceHandler := handler.NewServiceHandler(agentSvc)

	// 8.5 启动 MQ 双链路消费者
	//
	// 链路 1 (seckill_tx_topic):             主流程落库 — MQ 消息到达后开启 DB 事务建单
	// 链路 2 (seckill_order_timeout_topic):   30 分钟支付超时检查
	//
	// 消费者与 Producer 共享同一个 NameServer 地址（Viper 配置）。
	// 消费者在优雅停机时需显式 Shutdown，确保正在处理的消息完成消费。
	seckillConsumer := mqconsumer.NewSeckillConsumer(orderRepo, timeoutTaskRepo, seckillCache,
		resolveNameServer(viper.GetString("rocketmq.nameServer")),
		viper.GetInt("rocketmq.txConsumerGoroutines"))
	// MQ 消费者在后台协程中启动，避免连接 NameServer 时阻塞主流程，导致 Web 服务无法启动
	go func() {
		if err := seckillConsumer.Start(); err != nil {
			logger.Error("启动 MQ 消费者失败（不影响 Web 服务）", zap.Error(err))
		} else {
			logger.Info("MQ 消费者启动成功")
		}
	}()
	defer seckillConsumer.Close()

	// 8.6 启动悬空预扣 Cron 定时检查器
	//
	// 每分钟 SCAN seckill:pending:*，对超过 5 分钟的悬空条目执行检查和清理。
	pendingChecker := cron.NewPendingChecker(
		seckillCache,
		orderRepo,
		time.Duration(viper.GetInt("redis.pendingExpireSeconds"))*time.Second,
	)
	pendingChecker.Start()
	defer pendingChecker.Stop()

	// 8.7 启动延迟消息发送失败补偿器（Outbox）
	//
	// 当链路1发送 30 分钟延迟消息失败时，任务会写入 order_timeout_tasks，
	// 由该补偿器定时重投，避免订单长期停留在 pending 状态。
	timeoutCompensator := cron.NewTimeoutMessageCompensator(timeoutTaskRepo)
	timeoutCompensator.Start()
	defer timeoutCompensator.Stop()

	// 8.8 启动 dead-letter 自动重放器
	//
	// 对 outbox 中 dead 状态任务进行自动重入队（pending），
	// 再由 timeoutCompensator 负责重投 MQ。
	deadReplayer := cron.NewDeadTaskReplayer(timeoutTaskRepo)
	deadReplayer.Start()
	defer deadReplayer.Stop()

	// 8.9 启动主消息 Outbox 调度器（可靠投递 seckill_tx_topic）
	txDispatcher := cron.NewTxMessageDispatcher(txTaskRepo, cron.TxMessageDispatcherConfig{
		Interval: time.Duration(viper.GetInt("rocketmq.txDispatchIntervalMs")) * time.Millisecond,
		Batch:    viper.GetInt("rocketmq.txDispatchBatch"),
		MaxRetry: viper.GetInt("rocketmq.txDispatchMaxRetry"),
		Lease:    time.Duration(viper.GetInt("rocketmq.txDispatchLeaseSeconds")) * time.Second,
		Workers:  viper.GetInt("rocketmq.txDispatchWorkers"),
	})
	txDispatcher.Start()
	defer txDispatcher.Stop()

	// 8.10 启动主消息 dead 自动重放器
	txReplayer := cron.NewTxTaskReplayer(txTaskRepo)
	txReplayer.Start()
	defer txReplayer.Stop()

	// 9. 初始化路由
	r := router.SetupRouter(seckillHandler, userHandler, shopHandler, orderHandler, adminHandler, serviceHandler)

	// 9.5 注册 Swagger 路由
	r.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	// 9.6 注册 pprof 性能分析路由
	//
	// 访问地址: http://127.0.0.1:8080/debug/pprof/
	// 常用命令:
	//   go tool pprof http://127.0.0.1:8080/debug/pprof/profile?seconds=30  (CPU)
	//   go tool pprof http://127.0.0.1:8080/debug/pprof/heap                (内存)
	//   go tool pprof http://127.0.0.1:8080/debug/pprof/goroutine           (协程)
	// ⚠️ 生产环境应限制访问（IP 白名单或 BasicAuth），此处仅限开发调试。
	pprof.Register(r)

	// 10. 启动服务器
	port := viper.GetInt("server.port")
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: r,
	}

	// 启动服务器（非阻塞）
	go func() {
		logger.Info("服务器启动成功",
			zap.Int("port", port),
			zap.String("mode", mode),
		)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("服务器启动失败", zap.Error(err))
		}
	}()

	// 11. 优雅停机
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("正在关闭服务器...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal("服务器强制关闭", zap.Error(err))
	}

	logger.Info("服务器已关闭")
}

// initConfig 初始化配置
func initConfig() error {
	configName := os.Getenv("APP_CONFIG")
	if configName == "" {
		configName = "local"
	}

	viper.SetConfigName(configName)
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./config")
	viper.AddConfigPath(".")

	if err := viper.ReadInConfig(); err != nil {
		return err
	}

	return nil
}

// initLogger 初始化日志
func initLogger() error {
	config := &logger.Config{
		Level:      viper.GetString("log.level"),
		Filename:   viper.GetString("log.filename"),
		MaxSize:    viper.GetInt("log.maxSize"),
		MaxBackups: viper.GetInt("log.maxBackups"),
		MaxAge:     viper.GetInt("log.maxAge"),
		Compress:   viper.GetBool("log.compress"),
	}

	return logger.Init(config)
}

// initMySQL 初始化 MySQL 数据库连接
func initMySQL() error {
	config := &repository.DBConfig{
		Host:            viper.GetString("mysql.host"),
		Port:            viper.GetInt("mysql.port"),
		Username:        viper.GetString("mysql.username"),
		Password:        viper.GetString("mysql.password"),
		Database:        viper.GetString("mysql.database"),
		Charset:         viper.GetString("mysql.charset"),
		ParseTime:       viper.GetBool("mysql.parseTime"),
		Loc:             viper.GetString("mysql.loc"),
		MaxIdleConns:    viper.GetInt("mysql.maxIdleConns"),
		MaxOpenConns:    viper.GetInt("mysql.maxOpenConns"),
		ConnMaxLifetime: viper.GetInt("mysql.connMaxLifetime"),
	}

	return repository.InitMySQL(config)
}

// initRedis 初始化 Redis 连接
func initRedis() error {
	config := &pkgredis.Config{
		Host:         viper.GetString("redis.host"),
		Port:         viper.GetInt("redis.port"),
		Password:     viper.GetString("redis.password"),
		DB:           viper.GetInt("redis.db"),
		PoolSize:     viper.GetInt("redis.poolSize"),
		MinIdleConns: viper.GetInt("redis.minIdleConns"),
	}

	return pkgredis.Init(config)
}

// initMQ 初始化 RocketMQ 普通消息生产者
func initMQ() error {
	config := &mq.Config{
		NameServer: resolveNameServer(viper.GetString("rocketmq.nameServer")),
		GroupName:  viper.GetString("rocketmq.groupName"),
		RetryTimes: viper.GetInt("rocketmq.retryTimes"),
		Timeout:    viper.GetInt("rocketmq.timeout"),
	}

	return mq.Init(config)
}

// resolveNameServer 将 RocketMQ NameServer 地址中的 hostname 解析为 IP。
//
// RocketMQ Go 客户端内部校验要求 nameserver 必须是 IP:port 格式，
// 在 Docker 环境中 hostname（如 rocketmq-namesrv）不会被客户端自动解析，
// 需要在此处提前做 DNS 查询。
// 解析失败时直接返回原始地址，由 RocketMQ 客户端自行处理。
func resolveNameServer(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	// 已经是 IP 地址则直接返回
	if net.ParseIP(host) != nil {
		return addr
	}
	// hostname → IP
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		logger.Warn("RocketMQ NameServer DNS 解析失败，使用原始地址",
			zap.String("addr", addr),
		)
		return addr
	}
	resolved := net.JoinHostPort(ips[0], port)
	logger.Info("RocketMQ NameServer DNS 解析成功",
		zap.String("original", addr),
		zap.String("resolved", resolved),
	)
	return resolved
}

func validateRuntimeConfig() error {
	if viper.GetString("jwt.secret") == "" {
		return fmt.Errorf("jwt.secret 不能为空")
	}
	if viper.GetString("rocketmq.nameServer") == "" {
		return fmt.Errorf("rocketmq.nameServer 不能为空")
	}
	if viper.GetInt("server.port") <= 0 {
		return fmt.Errorf("server.port 必须大于 0")
	}

	mode := viper.GetString("server.mode")
	adminToken := viper.GetString("admin.token")
	if mode == "release" && adminToken == "" {
		return fmt.Errorf("release 模式下 admin.token 不能为空")
	}

	if mode != "release" && adminToken == "" {
		logger.Warn("admin.token 为空，管理接口将无法通过鉴权")
	}

	return nil
}

func logStartupSummary() {
	logger.Info("启动配置摘要",
		zap.String("mode", viper.GetString("server.mode")),
		zap.Int("port", viper.GetInt("server.port")),
		zap.String("mysqlHost", viper.GetString("mysql.host")),
		zap.Int("mysqlPort", viper.GetInt("mysql.port")),
		zap.String("redisHost", viper.GetString("redis.host")),
		zap.Int("redisPort", viper.GetInt("redis.port")),
		zap.String("rocketmqNameServer", viper.GetString("rocketmq.nameServer")),
	)
}
