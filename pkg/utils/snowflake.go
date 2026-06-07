package utils

import (
	"fmt"
	"sync"

	"github.com/bwmarrin/snowflake"
)

// snowflakeNode 全局雪花节点，线程安全
var (
	snowflakeNode *snowflake.Node
	snowflakeOnce sync.Once
)

// InitSnowflake 初始化雪花算法节点
//
// node 参数为机器节点编号（0-1023），在分布式部署时每个实例应配置不同的值
// 以保证全局生成的 ID 不冲突。单机开发阶段传 1 即可。
//
// 该函数通过 sync.Once 保证只初始化一次，即使被多次调用也是安全的。
func InitSnowflake(node int64) error {
	var err error
	snowflakeOnce.Do(func() {
		snowflakeNode, err = snowflake.NewNode(node)
	})
	if err != nil {
		return fmt.Errorf("初始化雪花算法失败, node=%d: %w", node, err)
	}
	return nil
}

// GenOrderNo 生成全局唯一订单号
//
// 返回雪花算法生成的 int64 ID 的十进制字符串表示
// 特性：趋势递增、全局唯一、包含时间戳信息、高性能（无锁）
//
// 注意：必须在调用 InitSnowflake 之后才能使用，否则 panic
func GenOrderNo() string {
	if snowflakeNode == nil {
		panic("雪花算法未初始化，请先调用 InitSnowflake")
	}
	return snowflakeNode.Generate().String()
}

// GenID 生成全局唯一 int64 ID
//
// 与 GenOrderNo 的区别在于返回 int64 类型，适用于需要数字 ID 的场景
func GenID() int64 {
	if snowflakeNode == nil {
		panic("雪花算法未初始化，请先调用 InitSnowflake")
	}
	return snowflakeNode.Generate().Int64()
}
