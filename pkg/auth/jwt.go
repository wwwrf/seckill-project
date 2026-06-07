package auth

import (
	"errors"
	"time"

	"seckill-system/pkg/logger"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// ==================== JWT 令牌管理 ====================
//
// 技术选型: golang-jwt/jwt/v5
//
// 选型原因：
//   - Go 生态 JWT 库的事实标准（原 dgrijalva/jwt-go 的官方继承者）
//   - 支持 JWT/JWS/JWE 标准，签名算法覆盖 HMAC/RSA/ECDSA/EdDSA
//   - v5 版本完全兼容 Go 1.21+ 的 errors.Is/As 语义
//
// 替代方案对比：
//
//   | 库                        | 优点                          | 缺点                         |
//   |---------------------------|-------------------------------|------------------------------|
//   | golang-jwt/jwt/v5 (选用)  | 社区标准，文档丰富，维护活跃  | HMAC 仅对称加密，无法公钥验证 |
//   | lestrrat-go/jwx/v2        | 支持 JWK/JWE，最完整          | API 复杂，学习成本高          |
//   | cristalhq/jwt             | 零分配高性能                  | 社区较小，功能精简            |
//
// 签名算法选择: HMAC-SHA256
//   - 对称密钥，单体/内部服务架构下最简单高效
//   - 如果需要跨服务验证（微服务网关场景），应切换为 RSA-256 或 EdDSA
//     使用非对称密钥：签发方持有私钥，验证方仅需公钥

var (
	jwtSecret   []byte
	tokenExpiry time.Duration

	// ErrInvalidToken Token 无效（签名错误、格式不正确）
	ErrInvalidToken = errors.New("jwt: invalid token")

	// ErrTokenExpired Token 已过期
	ErrTokenExpired = errors.New("jwt: token expired")
)

// Claims JWT 载荷
//
// 自定义字段：
//   - UserID:   用户主键 ID，业务鉴权的核心标识
//   - Username: 用户名，用于日志追踪（非安全字段，不依赖其做鉴权）
//
// 标准字段（RegisteredClaims）：
//   - ExpiresAt: 过期时间
//   - IssuedAt:  签发时间
//   - Issuer:    签发者标识
type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	jwt.RegisteredClaims
}

// Config JWT 配置
type Config struct {
	Secret      string // HMAC 签名密钥
	ExpiryHours int    // Token 有效期（小时）
}

// Init 初始化 JWT 配置
//
// 必须在应用启动时调用（main.go），且在任何 GenerateToken/ParseToken 之前。
// 安全检查：密钥长度不得低于 32 字节，防止弱密钥被暴力破解。
func Init(config *Config) {
	if len(config.Secret) < 32 {
		logger.Warn("JWT 密钥长度不足 32 字节，存在安全风险，请在生产环境更换强密钥",
			zap.Int("secretLen", len(config.Secret)),
		)
	}
	jwtSecret = []byte(config.Secret)
	if config.ExpiryHours <= 0 {
		config.ExpiryHours = 24
	}
	tokenExpiry = time.Duration(config.ExpiryHours) * time.Hour

	logger.Info("JWT 初始化完成",
		zap.Int("expiryHours", config.ExpiryHours),
	)
}

// GenerateToken 签发 JWT Token
//
// 参数：
//   - userID:   用户 ID（来自 DB 主键）
//   - username: 用户名（写入 Token 便于日志，非鉴权依据）
//
// 返回 Bearer Token 字符串（不含 "Bearer " 前缀）。
func GenerateToken(userID int64, username string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID:   userID,
		Username: username,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(tokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    "seckill-system",
			Audience:  jwt.ClaimStrings{"seckill-system"},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// ParseToken 解析并验证 JWT Token
//
// 安全防护：
//   - 强制校验签名算法为 HMAC，防止 alg=none/RS256 混淆攻击
//   - 校验 Audience 值匹配，防止跨系统 Token 混用
//   - 校验 Issuer 值匹配，防止伪造签发者
//
// 返回值：
//   - *Claims: Token 有效时返回载荷
//   - ErrTokenExpired: Token 已过期
//   - ErrInvalidToken: Token 签名无效或格式错误
func ParseToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// 强制校验签名算法为 HMAC（防止 alg=none 和 RS256/HS256 混淆攻击）
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return jwtSecret, nil
	},
		// 显式指定期望的签名算法
		jwt.WithValidMethods([]string{"HS256"}),
		// 校验 Audience
		jwt.WithAudience("seckill-system"),
		// 校验 Issuer
		jwt.WithIssuer("seckill-system"),
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}
