package cron

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"seckill-system/internal/model/e"
	"seckill-system/internal/repository"
	"seckill-system/internal/repository/cache"
	"seckill-system/pkg/logger"

	"go.uber.org/zap"
)

// ==================== Cron 定时悬空预扣扫描 ====================
//
// 每分钟全量 SCAN seckill:pending:* Hash，
// 对所有超过 5 分钟的 pending 条目统一检查。

const (
	// pendingCheckInterval 扫描间隔
	pendingCheckInterval = 1 * time.Minute

	// pendingExpireThreshold 悬空判定阈值
	// 超过此时长仍存在于 Pending Hash 中的条目被视为「悬空预扣」
	// canceledMarkerTTL 作废标记 TTL
	// 需覆盖 MQ 重试窗口，防止迟到消息绕过拦截
	canceledMarkerTTL = 24 * time.Hour

	// pendingGarbageCleanupEvery 每 N 次扫描执行一次垃圾清理
	pendingGarbageCleanupEvery = 5
)

// PendingChecker 悬空预扣定时检查器
type PendingChecker struct {
	seckillCache *cache.SeckillCache
	orderRepo    *repository.OrderRepo
	expireAfter  time.Duration
	stopCh       chan struct{}
	scanCount    int
}

// NewPendingChecker 创建检查器实例
func NewPendingChecker(seckillCache *cache.SeckillCache, orderRepo *repository.OrderRepo, expireAfter time.Duration) *PendingChecker {
	if expireAfter <= 0 {
		expireAfter = 5 * time.Minute
	}
	return &PendingChecker{
		seckillCache: seckillCache,
		orderRepo:    orderRepo,
		expireAfter:  expireAfter,
		stopCh:       make(chan struct{}),
	}
}

// Start 启动定时扫描（非阻塞，在后台 goroutine 运行）
func (pc *PendingChecker) Start() {
	go pc.run()
	logger.Info("悬空预扣 Cron 检查器已启动",
		zap.Duration("interval", pendingCheckInterval),
		zap.Duration("threshold", pc.expireAfter),
	)
}

// Stop 优雅停止检查器
func (pc *PendingChecker) Stop() {
	close(pc.stopCh)
	logger.Info("悬空预扣 Cron 检查器已停止")
}

// run 定时扫描主循环
func (pc *PendingChecker) run() {
	ticker := time.NewTicker(pendingCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pc.stopCh:
			return
		case <-ticker.C:
			pc.scan()
			pc.scanCount++
			if pc.scanCount%pendingGarbageCleanupEvery == 0 {
				pc.cleanupPendingGarbage()
			}
		}
	}
}

// scan 执行一次全量扫描
func (pc *PendingChecker) scan() {
	ctx := context.Background()
	now := time.Now().Unix()

	// 1. SCAN 所有 seckill:pending:* Key
	keys, err := pc.seckillCache.ScanPendingKeys(ctx)
	if err != nil {
		logger.Error("Cron: SCAN pending keys 失败", zap.Error(err))
		return
	}

	if len(keys) == 0 {
		return
	}

	for _, pendingKey := range keys {
		pc.checkOnePendingHash(ctx, pendingKey, now)
	}
}

// cleanupPendingGarbage 清理 pending 残留垃圾
//
// 清理规则：
//  1. DB 已存在订单 -> 删除 pending
//  2. 订单已作废(canceled marker 存在) -> 删除 pending
func (pc *PendingChecker) cleanupPendingGarbage() {
	ctx := context.Background()
	keys, err := pc.seckillCache.ScanPendingKeys(ctx)
	if err != nil {
		logger.Error("Cron: 清理任务 SCAN pending keys 失败", zap.Error(err))
		return
	}

	if len(keys) == 0 {
		return
	}

	cleaned := 0
	for _, pendingKey := range keys {
		activityID, parseErr := parseActivityIDFromKey(pendingKey)
		if parseErr != nil {
			continue
		}

		entries, hErr := pc.seckillCache.GetPendingAll(ctx, pendingKey)
		if hErr != nil {
			continue
		}

		for orderNo := range entries {
			order, dbErr := pc.orderRepo.FindByOrderNo(ctx, orderNo)
			if dbErr != nil {
				continue
			}

			if order != nil {
				_ = pc.seckillCache.DeletePending(ctx, activityID, orderNo)
				cleaned++
				continue
			}

			canceled, cErr := pc.seckillCache.IsOrderCanceled(ctx, orderNo)
			if cErr != nil {
				continue
			}
			if canceled {
				_ = pc.seckillCache.DeletePending(ctx, activityID, orderNo)
				cleaned++
			}
		}
	}

	if cleaned > 0 {
		logger.Info("Cron: pending 垃圾清理完成", zap.Int("cleaned", cleaned))
	}
}

// pendingValue Pending Hash 中的 JSON 值结构
type pendingValue struct {
	Ts  int64 `json:"ts"`  // 时间戳（秒级）
	UID int64 `json:"uid"` // 用户 ID
	PID int64 `json:"pid"` // 商品 ID
}

// checkOnePendingHash 检查单个 pending Hash 中的所有条目
func (pc *PendingChecker) checkOnePendingHash(ctx context.Context, pendingKey string, now int64) {
	// 解析 activityID: seckill:pending:{activityID}
	activityID, err := parseActivityIDFromKey(pendingKey)
	if err != nil {
		logger.Error("Cron: 解析 pending key 失败",
			zap.String("key", pendingKey),
			zap.Error(err),
		)
		return
	}

	// HGETALL 获取该 Hash 所有条目
	entries, err := pc.seckillCache.GetPendingAll(ctx, pendingKey)
	if err != nil {
		logger.Error("Cron: HGETALL 失败",
			zap.String("key", pendingKey),
			zap.Error(err),
		)
		return
	}

	for orderNo, valStr := range entries {
		// 解析 JSON 格式的 pending value
		var pv pendingValue
		if err := json.Unmarshal([]byte(valStr), &pv); err != nil {
			// 兼容旧格式（纯 timestamp）
			ts, parseErr := strconv.ParseInt(valStr, 10, 64)
			if parseErr != nil {
				logger.Warn("Cron: 解析 pending value 失败",
					zap.String("orderNo", orderNo),
					zap.String("value", valStr),
				)
				continue
			}
			pv = pendingValue{Ts: ts}
		}

		// 跳过未超过阈值的条目
		elapsed := time.Duration(now-pv.Ts) * time.Second
		if elapsed < pc.expireAfter {
			continue
		}

		// 超过 5 分钟 → 执行悬空检查
		pc.checkAndRollback(ctx, activityID, orderNo, pv, elapsed)
	}
}

// checkAndRollback 对单条悬空 pending 执行检查+完整回滚
//
// 升级版：Pending value 为 JSON 格式，携带 userID 和 productID，
// 可直接执行 Lua 回滚脚本（INCR 库存 + SREM 用户 + HDEL pending）。
func (pc *PendingChecker) checkAndRollback(ctx context.Context, activityID int64, orderNo string, pv pendingValue, elapsed time.Duration) {
	// 1. 查 DB
	order, err := pc.orderRepo.FindByOrderNo(ctx, orderNo)
	if err != nil {
		logger.Error("Cron: 查询 DB 订单失败",
			zap.String("orderNo", orderNo),
			zap.Error(err),
		)
		return
	}

	if order != nil {
		// DB 已建单 → 链路 1 HDEL 可能失败了，这里兜底清理
		_ = pc.seckillCache.DeletePending(ctx, activityID, orderNo)
		logger.Debug("Cron: DB 订单已存在，兜底 HDEL pending",
			zap.String("orderNo", orderNo),
		)
		return
	}

	// 2. 先原子写入作废标记（含 pending/processing 检查）
	markRet, err := pc.seckillCache.TryMarkOrderCanceledIfPending(ctx, activityID, orderNo, canceledMarkerTTL)
	if err != nil {
		logger.Error("Cron: 写入作废标记失败",
			zap.String("orderNo", orderNo),
			zap.Error(err),
		)
		return
	}

	switch markRet {
	case cache.CancelMarkNoPending:
		logger.Debug("Cron: pending 已不存在，跳过回滚",
			zap.String("orderNo", orderNo),
		)
		return
	case cache.CancelMarkProcessing:
		logger.Info("Cron: 订单正在 MQ 处理中，跳过本轮回滚",
			zap.String("orderNo", orderNo),
		)
		return
	case cache.CancelMarkAlreadyExists:
		logger.Debug("Cron: 作废标记已存在，跳过重复回滚",
			zap.String("orderNo", orderNo),
		)
		return
	}

	// 3. DB 无订单且已成功打标 → 执行完整回滚
	if pv.UID > 0 && pv.PID > 0 {
		// 新格式：携带 userID 和 productID，执行完整 Lua 回滚
		if err := pc.seckillCache.RollbackPreDeduct(ctx, activityID, pv.PID, pv.UID, orderNo); err != nil {
			logger.Error("Cron: 完整回滚失败",
				zap.String("orderNo", orderNo),
				zap.Error(err),
			)
			return
		}

		logger.Warn("Cron: 悬空预扣完整回滚成功（INCR 库存 + SREM 用户 + HDEL pending）",
			zap.String("orderNo", orderNo),
			zap.Int64("activityID", activityID),
			zap.Int64("productID", pv.PID),
			zap.Int64("userID", pv.UID),
			zap.Duration("elapsed", elapsed),
		)
		return
	}

	// 旧格式：仅清理 pending（缺少 uid/pid 无法回滚库存）
	if err := pc.seckillCache.DeletePending(ctx, activityID, orderNo); err != nil {
		logger.Error("Cron: HDEL pending 失败",
			zap.String("orderNo", orderNo),
			zap.Error(err),
		)
		return
	}

	logger.Warn("Cron: 旧格式悬空预扣，仅清理 pending",
		zap.String("orderNo", orderNo),
		zap.Int64("activityID", activityID),
		zap.Duration("elapsed", elapsed),
	)
}

// parseActivityIDFromKey 从 pending key 中解析 activityID
// 格式: seckill:pending:{activityID}
func parseActivityIDFromKey(key string) (int64, error) {
	prefix := e.KeySeckillPendingPrefix
	if !strings.HasPrefix(key, prefix) {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseInt(key[len(prefix):], 10, 64)
}
