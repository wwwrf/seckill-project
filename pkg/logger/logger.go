package logger

import (
	"os"
	"path/filepath"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

var Logger *zap.Logger

// Config 日志配置
type Config struct {
	Level      string
	Filename   string
	MaxSize    int
	MaxBackups int
	MaxAge     int
	Compress   bool
}

// Init 初始化日志
func Init(config *Config) error {
	// 日志级别
	level := zapcore.InfoLevel
	switch config.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	// 编码器配置
	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	// 确保日志目录存在
	logDir := filepath.Dir(config.Filename)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}

	// 文件输出
	fileWriter := zapcore.AddSync(&lumberjack.Logger{
		Filename:   config.Filename,
		MaxSize:    config.MaxSize,
		MaxBackups: config.MaxBackups,
		MaxAge:     config.MaxAge,
		Compress:   config.Compress,
	})

	// 控制台输出
	consoleWriter := zapcore.AddSync(os.Stdout)

	// 创建核心
	core := zapcore.NewTee(
		zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), fileWriter, level),
		zapcore.NewCore(zapcore.NewConsoleEncoder(encoderConfig), consoleWriter, level),
	)

	// 创建 Logger
	Logger = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1))

	return nil
}

// Debug 日志
func Debug(msg string, fields ...zap.Field) {
	Logger.Debug(msg, fields...)
}

// Info 日志
func Info(msg string, fields ...zap.Field) {
	Logger.Info(msg, fields...)
}

// Warn 日志
func Warn(msg string, fields ...zap.Field) {
	Logger.Warn(msg, fields...)
}

// Error 日志
func Error(msg string, fields ...zap.Field) {
	Logger.Error(msg, fields...)
}

// Fatal 日志
func Fatal(msg string, fields ...zap.Field) {
	Logger.Fatal(msg, fields...)
}

// Sync 刷新日志缓冲区
func Sync() {
	_ = Logger.Sync()
}
