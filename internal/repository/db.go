package repository

import (
	"fmt"
	"time"

	"seckill-system/internal/model"
	"seckill-system/pkg/logger"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// DB 全局数据库连接实例
var DB *gorm.DB

// DBConfig 数据库连接配置
type DBConfig struct {
	Host            string
	Port            int
	Username        string
	Password        string
	Database        string
	Charset         string
	ParseTime       bool
	Loc             string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime int // 秒
}

// InitMySQL 初始化 MySQL 连接
//
// 职责：
//  1. 根据配置拼接 DSN 并建立连接
//  2. 开启 GORM 的 Info 级别 SQL 日志（开发阶段可观测所有执行的 SQL）
//  3. 配置连接池参数（MaxIdle / MaxOpen / MaxLifetime）
//  4. 调用 AutoMigrate 自动同步 6 张核心业务表的 Schema
func InitMySQL(config *DBConfig) error {
	// 拼接 DSN (Data Source Name)
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=%t&loc=%s",
		config.Username,
		config.Password,
		config.Host,
		config.Port,
		config.Database,
		config.Charset,
		config.ParseTime,
		config.Loc,
	)

	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		// 开启 Info 模式，打印所有 SQL 语句，便于开发调试
		Logger: gormlogger.Default.LogMode(gormlogger.Info),
		// 禁用默认事务（提升性能，需要事务时手动开启）
		SkipDefaultTransaction: true,
		// 禁用外键约束（高并发系统不依赖外键，由应用层保证一致性）
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return fmt.Errorf("连接 MySQL 失败: %w", err)
	}

	// 获取底层 sql.DB 以配置连接池
	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("获取 sql.DB 实例失败: %w", err)
	}

	// 连接池配置
	sqlDB.SetMaxIdleConns(config.MaxIdleConns)
	sqlDB.SetMaxOpenConns(config.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(time.Duration(config.ConnMaxLifetime) * time.Second)

	// Ping 测试连接是否正常
	if err = sqlDB.Ping(); err != nil {
		return fmt.Errorf("MySQL Ping 失败: %w", err)
	}

	logger.Info("MySQL 连接成功",
		zap.String("host", config.Host),
		zap.Int("port", config.Port),
		zap.String("database", config.Database),
	)

	// AutoMigrate 自动建表/同步表结构
	// 注意：生产环境建议使用 migrate 工具管理 DDL，AutoMigrate 仅适合开发阶段
	if err = autoMigrate(); err != nil {
		return fmt.Errorf("AutoMigrate 失败: %w", err)
	}

	return nil
}

// autoMigrate 自动迁移所有数据模型
// 按照依赖关系排序：基础表 → 关联表 → 流水表
func autoMigrate() error {
	err := DB.AutoMigrate(
		&model.User{},            // 用户表
		&model.Product{},         // 商品表
		&model.SeckillActivity{}, // 秒杀活动表
		&model.Order{},           // 订单主表
		&model.OrderItem{},       // 订单子表
		&model.StockDeductLog{},  // 库存扣减流水表
		&model.OrderTimeoutTask{}, // 延迟消息补偿任务表
		&model.SeckillTxTask{},   // 主消息 Outbox 任务表
	)
	if err != nil {
		return err
	}

	logger.Info("AutoMigrate 完成，8 张核心业务表已同步")
	return nil
}

// CloseMySQL 关闭数据库连接
func CloseMySQL() {
	if DB != nil {
		sqlDB, err := DB.DB()
		if err != nil {
			logger.Error("获取 sql.DB 失败", zap.Error(err))
			return
		}
		if err = sqlDB.Close(); err != nil {
			logger.Error("关闭 MySQL 连接失败", zap.Error(err))
			return
		}
		logger.Info("MySQL 连接已关闭")
	}
}
