package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/golang-jwt/jwt/v4"
)

// token 校验内部错误哨兵，handler 按错误类型映射业务码。
var (
	// errMissingBearerToken 表示 Authorization 头缺少 Bearer token。
	errMissingBearerToken = errors.New("缺少 Bearer Token")
	// errInvalidToken 表示 token 签名、结构或声明无效。
	errInvalidToken = errors.New("Token 无效")
	// errTokenExpired 表示 token 已超过 exp 时间。
	errTokenExpired = errors.New("Token 已过期")
	// errSessionExpired 表示服务端 Redis 会话不存在或已过期。
	errSessionExpired = errors.New("会话已过期")
	// errAuthDependencyUnavailable 表示 Redis 会话存储暂时不可用，客户端 token 本身未被判为无效。
	errAuthDependencyUnavailable = errors.New("鉴权依赖不可用")
)

// maxBearerTokenBytes 限制 JWT 文本大小，避免未认证请求用超长 Header 放大解析和日志开销。
const maxBearerTokenBytes = 8 * 1024

// UserTokenIdentity 表示通过 JWT 和 Redis 会话校验后的用户身份。
type UserTokenIdentity struct {
	UserID      int64  // 用户 ID，来自 JWT sub
	UserName    string // 用户名，来自 JWT username
	Token       string // 当前请求携带的原始 token
	SessionID   string // 稳定会话 ID，来自 JWT sid
	JTI         string // 当前访问令牌的唯一标识
	AuthVersion uint64 // 认证版本，必须与 Redis 和主库一致
	ExpiresAt   int64  // token 过期时间戳，单位秒
}

// bearerToken 从 Authorization 头中提取 Bearer token。
func bearerToken(header string) (string, error) {
	if len(header) > maxBearerTokenBytes+len("Bearer ") {
		return "", errInvalidToken
	}
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return "", errMissingBearerToken
	}
	token := strings.TrimSpace(header[len("Bearer "):])
	if token == "" {
		return "", errMissingBearerToken
	}
	if len(token) > maxBearerTokenBytes {
		return "", errInvalidToken
	}
	return token, nil
}

// VerifyUserToken 统一校验前台 JWT，并按需校验 Redis 中的登录会话。
func VerifyUserToken(ctx context.Context, svcCtx *svc.ServiceContext, tokenString string, requireSession bool) (*UserTokenIdentity, error) {
	if svcCtx == nil {
		return nil, errInvalidToken
	}
	cfg := svcCtx.CurrentConfig()
	if cfg.JwtSecret == "" {
		return nil, errInvalidToken
	}
	if strings.TrimSpace(tokenString) == "" || len(tokenString) > maxBearerTokenBytes {
		return nil, errInvalidToken
	}

	claims := jwt.MapClaims{}
	parser := jwt.NewParser(
		// JSON Number 防止认证版本等整数声明丢失精度。
		jwt.WithJSONNumber(),
		// 算法白名单拒绝客户端替换 JWT 签名方法。
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
	)
	token, err := parser.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		// 回调再次固定算法，避免解析器配置变化时接受其它 HMAC 方法。
		if t.Method != jwt.SigningMethodHS256 {
			return nil, errors.Wrap(errInvalidToken, "签名算法不匹配")
		}
		return []byte(cfg.JwtSecret), nil
	})
	if err != nil || !token.Valid {
		if errors.Is(err, jwt.ErrTokenExpired) {
			// 过期 token 保留独立错误类型供外层映射。
			return nil, errTokenExpired
		}
		// 其它解析和签名错误统一收敛为无效 token。
		return nil, errInvalidToken
	}

	// JWT 声明使用严格类型读取，缺失或非规范值均拒绝。
	userID, ok := jwtPositiveStringInt64Claim(claims, "sub")
	if !ok {
		return nil, errInvalidToken
	}
	exp, ok := jwtPositiveInt64Claim(claims, "exp")
	if !ok {
		return nil, errInvalidToken
	}
	if exp < time.Now().Unix() {
		return nil, errTokenExpired
	}
	jti, ok := jwtStringClaim(claims, "jti")
	if !ok {
		return nil, errInvalidToken
	}
	sessionID, ok := jwtStringClaim(claims, "sid")
	if !ok {
		return nil, errInvalidToken
	}
	authVersion, ok := jwtPositiveUint64Claim(claims, "auth_version")
	if !ok {
		return nil, errInvalidToken
	}
	claimAppID, ok := jwtStringClaim(claims, "app_id")
	if !ok || !tokenAppIDMatches(cfg.AppID, claimAppID) {
		return nil, errInvalidToken
	}
	issuer, ok := jwtStringClaim(claims, "iss")
	if !ok || cfg.Auth.Issuer == "" || issuer != cfg.Auth.Issuer {
		return nil, errInvalidToken
	}
	userName, ok := jwtStringClaim(claims, "username")
	if !ok {
		return nil, errInvalidToken
	}
	// 配置快照必须与进程级 Redis 命名空间一致，避免跨 AppID 查询会话。
	if cfg.AppID != runtimecfg.AppID() {
		return nil, errInvalidToken
	}
	identity := &UserTokenIdentity{
		UserID:      userID,
		UserName:    userName,
		Token:       tokenString,
		SessionID:   sessionID,
		JTI:         jti,
		AuthVersion: authVersion,
		ExpiresAt:   exp,
	}
	// 仅解析场景在 JWT 契约通过后返回，不访问 Redis 会话。
	if !requireSession {
		return identity, nil
	}
	// 强会话校验缺少 Redis 时禁止退化为纯 JWT。
	if svcCtx.Rds == nil {
		return identity, errors.Wrap(errAuthDependencyUnavailable, "Redis 会话存储未初始化")
	}
	// Lua 原子核对 Redis 会话记录，避免校验期间被并发轮换。
	verified, err := userSessionVerifyScript.Run(
		ctx,
		svcCtx.Rds,
		keys.UserSessionKeys(identity.UserID),
		identity.AuthVersion,
		identity.SessionID,
		identity.Token,
		time.Now().UnixMilli(),
	).Int64()
	if err != nil {
		// Redis 故障归类为依赖不可用，不能伪装成会话过期。
		return identity, errors.Wrap(errAuthDependencyUnavailable, err.Error())
	}
	if verified != 1 {
		return identity, errSessionExpired
	}
	return identity, nil
}

// jwtPositiveStringInt64Claim 读取使用十进制字符串承载的正整数 JWT 声明。
func jwtPositiveStringInt64Claim(claims jwt.MapClaims, name string) (int64, bool) {
	item, ok := claims[name].(string)
	if !ok || item == "" || item != strings.TrimSpace(item) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(item, 10, 64)
	return parsed, err == nil && parsed > 0
}

// jwtPositiveInt64Claim 读取使用 JSON 数字承载的正整数 JWT 声明。
func jwtPositiveInt64Claim(claims jwt.MapClaims, name string) (int64, bool) {
	item, ok := claims[name].(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(item.String(), 10, 64)
	return parsed, err == nil && parsed > 0
}

// jwtPositiveUint64Claim 读取必须为正整数的 uint64 JWT 声明。
func jwtPositiveUint64Claim(claims jwt.MapClaims, name string) (uint64, bool) {
	item, ok := claims[name].(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseUint(item.String(), 10, 64)
	return parsed, err == nil && parsed > 0
}

// jwtStringClaim 读取必须为非空字符串的 JWT 声明。
func jwtStringClaim(claims jwt.MapClaims, name string) (string, bool) {
	value, ok := claims[name].(string)
	return value, ok && value != "" && value == strings.TrimSpace(value)
}

// VerifyUserTokenFromRequest 从 HTTP 请求中提取并校验前台用户 token。
func VerifyUserTokenFromRequest(ctx context.Context, svcCtx *svc.ServiceContext, r *http.Request, requireSession bool) (*UserTokenIdentity, error) {
	tokenString, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return nil, errors.Tag(err)
	}
	return VerifyUserToken(ctx, svcCtx, tokenString, requireSession)
}

// tokenAppIDMatches 判断 token 中的 app_id 是否匹配当前服务命名空间。
func tokenAppIDMatches(configAppID string, claimAppID string) bool {
	return configAppID != "" && claimAppID != "" && claimAppID == configAppID
}
