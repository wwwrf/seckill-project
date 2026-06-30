package service

import (
	"context"

	"seckill-system/internal/repository"
	"seckill-system/internal/repository/cache"
)

// AgentService 客服 Agent 服务（咨询 + 投诉 RAG）
//
// 当前为 stub 实现：LLM/Eino 依赖未集成时直接返回兜底文案。
// 如需启用 LLM，参考 config/local.yaml 的 agent.intent.llm_enabled 配置。
type AgentService struct {
	orderRepo    *repository.OrderRepo
	productCache *cache.ProductCache
	kbPath       string
}

// NewAgentService 创建客服 Agent 实例
func NewAgentService(
	orderRepo *repository.OrderRepo,
	productCache *cache.ProductCache,
	kbPath string,
) *AgentService {
	return &AgentService{
		orderRepo:    orderRepo,
		productCache: productCache,
		kbPath:       kbPath,
	}
}

// ChatReply 客服咨询回复
type ChatReply struct {
	Answer string `json:"answer"`
	Intent string `json:"intent,omitempty"`
}

// Chat 意图识别 + 路由（咨询 Agent / 投诉 Agent）
func (s *AgentService) Chat(_ context.Context, _ int64, _ string) (*ChatReply, error) {
	return &ChatReply{
		Answer: "您好，客服功能正在升级中，如有疑问请通过官方渠道联系我们。",
		Intent: "unknown",
	}, nil
}

// HandleComplaint 直接走投诉 Agent（RAG）
func (s *AgentService) HandleComplaint(_ context.Context, _ int64, _ string) (*ChatReply, error) {
	return &ChatReply{
		Answer: "您的投诉已收到，我们会尽快处理并回复您。感谢您的反馈。",
		Intent: "complaint",
	}, nil
}
