package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"seckill-system/internal/model"
	"seckill-system/pkg/logger"

	gocache "github.com/patrickmn/go-cache"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// ==================== 多级缓存（L1 本地 → L2 Redis → L3 DB） ====================
//
// 技术选型：Cache-Aside（旁路缓存）模式
//
// 选型原因：
//   - 读多写少场景（商品/活动信息几乎不变）的最佳模式
//   - 应用层完全控制缓存读写时机，灵活度最高
//   - 缓存失效时走 DB 回源，天然保证最终一致性
//
// 替代方案对比：
//
//   | 模式              | 优点                    | 缺点                          |
//   |-------------------|-------------------------|-------------------------------|
//   | Cache-Aside(选用) | 灵活，实现简单           | 需要处理缓存一致性问题         |
//   | Read-Through      | 对业务透明               | 缓存层耦合 DB 查询逻辑        |
//   | Write-Through     | 写入即缓存，一致性好     | 写入延迟增加（双写）           |
//   | Write-Behind      | 写入最快（异步落 DB）    | 数据丢失风险，实现复杂         |
//
// 三大缓存问题解决方案：
//
//  1. 缓存穿透（Penetration）：缓存空值 + BloomFilter
//     - 查不到数据时也缓存 "null" 值（TTL=1 分钟），避免恶意 ID 反复打穿 DB
//     - BloomFilter 在秒杀场景中已使用，商品查询可复用同一套思路
//
//  2. 缓存击穿（Breakdown / Hot Key）：singleflight
//     - golang.org/x/sync/singleflight 确保同一 Key 的并发请求只有 1 个打到 DB
//     - 其他 Goroutine 等待第一个的结果，避免热 Key 过期瞬间 DB 压力暴增
//     - 替代方案：分布式互斥锁（Redis SETNX），但增加网络开销和实现复杂度
//
//  3. 缓存雪崩（Avalanche）：随机 TTL 抖动
//     - 每个 Key 的 TTL = baseTTL + random(0, baseTTL/5)
//     - 避免大批 Key 同时过期导致 DB 瞬间流量洪峰
//     - 替代方案：永不过期 + 后台异步刷新，但实现更复杂

const (
	// L1 本地缓存 TTL（进程级，go-cache）
	productLocalTTL     = 30 * time.Second
	productLocalCleanup = 60 * time.Second

	// L2 Redis 缓存 TTL
	productRedisTTL = 10 * time.Minute

	// 空值缓存 TTL（防穿透）
	nullCacheTTL = 1 * time.Minute

	// 空值标记
	nullValue = "__NULL__"
)

// ProductCache 商品/活动多级缓存
//
// 数据流：Get 请求
//
//	L1 go-cache (30s TTL, 进程内) ← 命中率最高，延迟 < 200ns
//	  ↓ miss
//	L2 Redis (10min TTL, 分布式)  ← 跨实例共享，延迟 ~1ms
//	  ↓ miss
//	L3 MySQL (source of truth)    ← 兜底，延迟 ~5ms
//	  ↓ 回写 L2 + L1
type ProductCache struct {
	rdb        *redis.Client
	db         *gorm.DB
	localCache *gocache.Cache
	sfGroup    singleflight.Group // 防击穿：合并并发请求
}

// NewProductCache 创建商品缓存实例
func NewProductCache(rdb *redis.Client, db *gorm.DB) *ProductCache {
	return &ProductCache{
		rdb:        rdb,
		db:         db,
		localCache: gocache.New(productLocalTTL, productLocalCleanup),
	}
}

// GetProduct 获取商品信息（三级缓存 + 防穿透 + 防击穿 + 防雪崩）
func (c *ProductCache) GetProduct(ctx context.Context, id int64) (*model.Product, error) {
	key := fmt.Sprintf("product:%d", id)

	// L1: 本地缓存
	if val, found := c.localCache.Get(key); found {
		if val == nil {
			return nil, nil // 空值缓存命中（防穿透）
		}
		return val.(*model.Product), nil
	}

	// singleflight: 合并并发请求（防击穿）
	val, err, _ := c.sfGroup.Do(key, func() (interface{}, error) {
		return c.loadProduct(ctx, key, id)
	})

	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	return val.(*model.Product), nil
}

// loadProduct 从 L2/L3 加载商品（仅 singleflight 内调用）
func (c *ProductCache) loadProduct(ctx context.Context, key string, id int64) (interface{}, error) {
	// L2: Redis
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err == nil {
		if string(data) == nullValue {
			// Redis 中存的是空值标记（防穿透）
			c.localCache.Set(key, nil, nullCacheTTL)
			return nil, nil
		}
		var product model.Product
		if err := json.Unmarshal(data, &product); err == nil {
			c.localCache.Set(key, &product, randomTTL(productLocalTTL))
			return &product, nil
		}
	}

	// L3: MySQL（source of truth）
	var product model.Product
	if err := c.db.WithContext(ctx).First(&product, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			// 防穿透：缓存空值
			c.rdb.Set(ctx, key, nullValue, nullCacheTTL)
			c.localCache.Set(key, nil, nullCacheTTL)
			logger.Info("商品不存在，缓存空值（防穿透）", zap.Int64("productID", id))
			return nil, nil
		}
		return nil, fmt.Errorf("查询商品失败: %w", err)
	}

	// 回写 L2 + L1（随机 TTL 防雪崩）
	if bytes, err := json.Marshal(product); err == nil {
		c.rdb.Set(ctx, key, bytes, randomTTL(productRedisTTL))
	}
	c.localCache.Set(key, &product, randomTTL(productLocalTTL))

	return &product, nil
}

// GetActivity 获取秒杀活动信息（三级缓存）
func (c *ProductCache) GetActivity(ctx context.Context, id int64) (*model.SeckillActivity, error) {
	key := fmt.Sprintf("activity:%d", id)

	// L1
	if val, found := c.localCache.Get(key); found {
		if val == nil {
			return nil, nil
		}
		return val.(*model.SeckillActivity), nil
	}

	// singleflight
	val, err, _ := c.sfGroup.Do(key, func() (interface{}, error) {
		return c.loadActivity(ctx, key, id)
	})

	if err != nil {
		return nil, err
	}
	if val == nil {
		return nil, nil
	}
	return val.(*model.SeckillActivity), nil
}

// loadActivity 从 L2/L3 加载活动
func (c *ProductCache) loadActivity(ctx context.Context, key string, id int64) (interface{}, error) {
	// L2
	data, err := c.rdb.Get(ctx, key).Bytes()
	if err == nil {
		if string(data) == nullValue {
			c.localCache.Set(key, nil, nullCacheTTL)
			return nil, nil
		}
		var activity model.SeckillActivity
		if err := json.Unmarshal(data, &activity); err == nil {
			c.localCache.Set(key, &activity, randomTTL(productLocalTTL))
			return &activity, nil
		}
	}

	// L3
	var activity model.SeckillActivity
	if err := c.db.WithContext(ctx).First(&activity, id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.rdb.Set(ctx, key, nullValue, nullCacheTTL)
			c.localCache.Set(key, nil, nullCacheTTL)
			return nil, nil
		}
		return nil, fmt.Errorf("查询活动失败: %w", err)
	}

	if bytes, err := json.Marshal(activity); err == nil {
		c.rdb.Set(ctx, key, bytes, randomTTL(productRedisTTL))
	}
	c.localCache.Set(key, &activity, randomTTL(productLocalTTL))

	return &activity, nil
}

// randomTTL 在 baseTTL 基础上增加 0~20% 的随机抖动（防雪崩）
//
// 原理：如果 1000 个 Key 的 TTL 都是精确的 10 分钟，
// 它们会在同一秒过期，导致 1000 个并发 DB 查询。
// 增加随机抖动后，这 1000 个 Key 的过期时间分散在 10~12 分钟，
// DB 压力被平均到 2 分钟内，单秒峰值降低 120 倍。
func randomTTL(base time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(base / 5)))
	return base + jitter
}
