package service

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"seckill-system/internal/model"
	"seckill-system/internal/rag"
	"seckill-system/internal/repository"
	"seckill-system/internal/repository/cache"
	"seckill-system/pkg/logger"

	openaiModel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type Intent string

const (
	IntentConsult   Intent = "consult"
	IntentComplaint Intent = "complaint"
)

type ToolTrace struct {
	Tool   string `json:"tool"`
	Input  string `json:"input"`
	Output string `json:"output"`
}

type AgentReply struct {
	Intent         Intent      `json:"intent"`
	Agent          string      `json:"agent"`
	Message        string      `json:"message"`
	Evidence       []rag.Hit   `json:"evidence,omitempty"`
	ToolTraces     []ToolTrace `json:"tool_traces,omitempty"`
	DecisionSource string      `json:"decision_source"`
}

type AgentService struct {
	orderRepo       *repository.OrderRepo
	productCache    *cache.ProductCache
	ragIndex        *rag.Index
	intentModel     *openaiModel.ChatModel
	intentLLMEnable bool
}

func NewAgentService(orderRepo *repository.OrderRepo, productCache *cache.ProductCache, kbPath string) *AgentService {
	// Initialize Eino language settings.
	_ = adk.SetLanguage(adk.LanguageChinese)

	idx := rag.NewIndex(128)
	if err := idx.BuildFromMarkdown(kbPath, 420); err != nil {
		logger.Warn("投诉知识库加载失败，RAG 将退化为空召回", zap.Error(err), zap.String("kbPath", kbPath))
	}

	svc := &AgentService{
		orderRepo:    orderRepo,
		productCache: productCache,
		ragIndex:     idx,
	}

	// Optional LLM intent classifier based on Eino + OpenAI component.
	// If required config is missing, we keep rule-based routing as fallback.
	if viper.GetBool("agent.intent.llm_enabled") {
		apiKey := strings.TrimSpace(viper.GetString("agent.intent.openai.api_key"))
		modelName := strings.TrimSpace(viper.GetString("agent.intent.openai.model"))
		baseURL := strings.TrimSpace(viper.GetString("agent.intent.openai.base_url"))

		if apiKey == "" || modelName == "" {
			logger.Warn("Intent LLM 已启用但配置不完整，将回退规则路由",
				zap.Bool("hasAPIKey", apiKey != ""),
				zap.Bool("hasModel", modelName != ""),
			)
		} else {
			cfg := &openaiModel.ChatModelConfig{
				APIKey: apiKey,
				Model:  modelName,
			}
			if baseURL != "" {
				cfg.BaseURL = baseURL
			}

			intentModel, err := openaiModel.NewChatModel(context.Background(), cfg)
			if err != nil {
				logger.Warn("初始化 Intent LLM 失败，将回退规则路由", zap.Error(err))
			} else {
				svc.intentModel = intentModel
				svc.intentLLMEnable = true
				logger.Info("Intent LLM 初始化成功", zap.String("model", modelName))
			}
		}
	}

	return svc
}

func (s *AgentService) Chat(ctx context.Context, userID int64, message string) (*AgentReply, error) {
	intent := s.routeIntent(ctx, message)
	if intent == IntentComplaint {
		return s.handleComplaint(ctx, userID, message)
	}
	return s.handleConsult(ctx, userID, message)
}

func (s *AgentService) HandleComplaint(ctx context.Context, userID int64, message string) (*AgentReply, error) {
	return s.handleComplaint(ctx, userID, message)
}

func routeIntentByRule(message string) Intent {
	m := strings.ToLower(message)
	keywords := []string{"投诉", "不满", "差评", "退款", "欺骗", "虚假", "维权", "客服态度", "迟迟不发货"}
	for _, kw := range keywords {
		if strings.Contains(m, kw) {
			return IntentComplaint
		}
	}
	return IntentConsult
}

func (s *AgentService) routeIntent(ctx context.Context, message string) Intent {
	if s.intentLLMEnable && s.intentModel != nil {
		intent, err := s.routeIntentByLLM(ctx, message)
		if err == nil {
			return intent
		}
		logger.Warn("Intent LLM 路由失败，回退规则路由", zap.Error(err))
	}

	return routeIntentByRule(message)
}

func (s *AgentService) routeIntentByLLM(ctx context.Context, message string) (Intent, error) {
	in := []*schema.Message{
		schema.SystemMessage("你是电商客服意图分类器。仅输出一个单词：consult 或 complaint。若用户明显在投诉/维权/退款争议，输出 complaint；否则输出 consult。不要输出其他内容。"),
		schema.UserMessage(message),
	}

	out, err := s.intentModel.Generate(ctx, in)
	if err != nil {
		return "", err
	}

	content := strings.TrimSpace(strings.ToLower(out.Content))
	if strings.Contains(content, "complaint") {
		return IntentComplaint, nil
	}
	if strings.Contains(content, "consult") {
		return IntentConsult, nil
	}

	return "", fmt.Errorf("invalid intent response: %s", content)
}

func (s *AgentService) handleConsult(ctx context.Context, userID int64, message string) (*AgentReply, error) {
	traces := make([]ToolTrace, 0, 3)
	lower := strings.ToLower(message)

	if strings.Contains(lower, "订单") || strings.Contains(lower, "支付") || strings.Contains(lower, "发货") {
		orderNo := pickOrderNo(message)
		if orderNo != "" {
			order, err := s.orderRepo.FindByOrderNo(ctx, orderNo)
			if err != nil {
				return nil, err
			}
			if order == nil || order.UserID != userID {
				return &AgentReply{
					Intent:         IntentConsult,
					Agent:          "consultation_agent",
					Message:        "我没有查到该订单号对应的可访问订单，请确认订单号是否正确。",
					DecisionSource: "eino+rules",
					ToolTraces: append(traces, ToolTrace{
						Tool:   "order_lookup",
						Input:  orderNo,
						Output: "not_found_or_forbidden",
					}),
				}, nil
			}

			msg := fmt.Sprintf("已为你查询订单 %s，当前状态：%s，支付金额：%.2f 元。", order.OrderNo, orderStatusText(order.Status), float64(order.PayAmount)/100.0)
			return &AgentReply{
				Intent:         IntentConsult,
				Agent:          "consultation_agent",
				Message:        msg,
				DecisionSource: "eino+rules",
				ToolTraces: append(traces, ToolTrace{
					Tool:   "order_lookup",
					Input:  orderNo,
					Output: "found",
				}),
			}, nil
		}

		orders, _, err := s.orderRepo.ListByUserID(ctx, userID, 1, 5)
		if err != nil {
			return nil, err
		}
		if len(orders) == 0 {
			return &AgentReply{
				Intent:         IntentConsult,
				Agent:          "consultation_agent",
				Message:        "你当前没有可查询的订单记录。",
				DecisionSource: "eino+rules",
				ToolTraces:     append(traces, ToolTrace{Tool: "order_list", Input: "page=1,size=5", Output: "empty"}),
			}, nil
		}

		lines := make([]string, 0, len(orders))
		for _, o := range orders {
			lines = append(lines, fmt.Sprintf("%s(%s)", o.OrderNo, orderStatusText(o.Status)))
		}
		return &AgentReply{
			Intent:         IntentConsult,
			Agent:          "consultation_agent",
			Message:        "我帮你查到了最近订单：" + strings.Join(lines, "，") + "。如果你提供具体订单号，我可以继续给出处理建议。",
			DecisionSource: "eino+rules",
			ToolTraces:     append(traces, ToolTrace{Tool: "order_list", Input: "page=1,size=5", Output: fmt.Sprintf("count=%d", len(orders))}),
		}, nil
	}

	// Product/activity consultation.
	if strings.Contains(lower, "商品") || strings.Contains(lower, "活动") || strings.Contains(lower, "库存") {
		id := pickFirstInt(message)
		if id <= 0 {
			id = 1
		}

		if strings.Contains(lower, "活动") {
			act, err := s.productCache.GetActivity(ctx, int64(id))
			if err != nil {
				return nil, err
			}
			if act == nil {
				return &AgentReply{
					Intent:         IntentConsult,
					Agent:          "consultation_agent",
					Message:        "未找到对应活动，请确认活动ID。",
					DecisionSource: "eino+rules",
					ToolTraces:     []ToolTrace{{Tool: "activity_lookup", Input: fmt.Sprintf("activity_id=%d", id), Output: "not_found"}},
				}, nil
			}

			msg := fmt.Sprintf("活动[%d] %s，秒杀价 %.2f 元，可用库存 %d。", act.ID, act.ActivityName, float64(act.SeckillPrice)/100.0, act.AvailableStock)
			return &AgentReply{
				Intent:         IntentConsult,
				Agent:          "consultation_agent",
				Message:        msg,
				DecisionSource: "eino+rules",
				ToolTraces:     []ToolTrace{{Tool: "activity_lookup", Input: fmt.Sprintf("activity_id=%d", id), Output: "found"}},
			}, nil
		}

		prod, err := s.productCache.GetProduct(ctx, int64(id))
		if err != nil {
			return nil, err
		}
		if prod == nil {
			return &AgentReply{
				Intent:         IntentConsult,
				Agent:          "consultation_agent",
				Message:        "未找到对应商品，请确认商品ID。",
				DecisionSource: "eino+rules",
				ToolTraces:     []ToolTrace{{Tool: "product_lookup", Input: fmt.Sprintf("product_id=%d", id), Output: "not_found"}},
			}, nil
		}

		msg := fmt.Sprintf("商品[%d] %s，原价 %.2f 元，当前状态：%s。", prod.ID, prod.Title, float64(prod.OriginalPrice)/100.0, productStatusText(prod.Status))
		return &AgentReply{
			Intent:         IntentConsult,
			Agent:          "consultation_agent",
			Message:        msg,
			DecisionSource: "eino+rules",
			ToolTraces:     []ToolTrace{{Tool: "product_lookup", Input: fmt.Sprintf("product_id=%d", id), Output: "found"}},
		}, nil
	}

	return &AgentReply{
		Intent:         IntentConsult,
		Agent:          "consultation_agent",
		Message:        "我可以帮你查订单、活动、商品和库存。你可以直接说：查订单12345，或活动1库存。",
		DecisionSource: "eino+rules",
	}, nil
}

func (s *AgentService) handleComplaint(_ context.Context, _ int64, message string) (*AgentReply, error) {
	hits := s.ragIndex.Retrieve(message, 3)
	if len(hits) == 0 {
		return &AgentReply{
			Intent:         IntentComplaint,
			Agent:          "complaint_agent",
			Message:        "已收到你的投诉，我会先登记问题并建议人工客服优先处理。请补充订单号、时间和期望处理结果。",
			DecisionSource: "eino+rag",
		}, nil
	}

	tips := make([]string, 0, len(hits))
	for i, h := range hits {
		tips = append(tips, fmt.Sprintf("%d) %s", i+1, h.Text))
	}

	reply := "已收到你的投诉。结合知识库，我建议你按以下步骤处理：" + strings.Join(tips, " ") + "。如你同意，我可以继续引导你补齐投诉信息。"
	return &AgentReply{
		Intent:         IntentComplaint,
		Agent:          "complaint_agent",
		Message:        reply,
		Evidence:       hits,
		DecisionSource: "eino+rag",
	}, nil
}

func pickOrderNo(message string) string {
	// Match a conservative order-no pattern with at least 10 chars.
	re := regexp.MustCompile(`[A-Za-z0-9_-]{10,}`)
	return re.FindString(message)
}

func pickFirstInt(message string) int {
	re := regexp.MustCompile(`\d+`)
	s := re.FindString(message)
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

func orderStatusText(status int8) string {
	switch status {
	case model.OrderStatusPending:
		return "待支付"
	case model.OrderStatusPaid:
		return "已支付"
	case model.OrderStatusCancelled:
		return "已取消"
	default:
		return "未知"
	}
}

func productStatusText(status int8) string {
	switch status {
	case 1:
		return "上架"
	case 2:
		return "下架"
	default:
		return "未知"
	}
}
