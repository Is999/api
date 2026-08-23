package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"api/internal/config"
	"api/internal/infra/collectorx"
	"api/internal/requestctx"
	"api/internal/svc"
)

// AuthCollectorBizType 表示认证风控事件的 Collector bizType。
const AuthCollectorBizType = collectorx.BizTypeAuthSecurity

const (
	// authEventPartitionHashLength 表示分区键使用的脱敏哈希字符数。
	authEventPartitionHashLength = 32
	// authEventEnqueueTimeout 限制 best-effort 认证事件占用请求主链路的最长时间。
	authEventEnqueueTimeout = 100 * time.Millisecond
)

// 认证风控事件动作。
const (
	AuthEventActionRegisterSuccess      = collectorx.AuthSecurityActionRegisterSuccess      // 注册成功
	AuthEventActionLoginSuccess         = collectorx.AuthSecurityActionLoginSuccess         // 登录成功
	AuthEventActionLoginFailed          = collectorx.AuthSecurityActionLoginFailed          // 登录失败
	AuthEventActionRateLimited          = collectorx.AuthSecurityActionRateLimited          // 认证入口触发限流
	AuthEventActionAuthFailed           = collectorx.AuthSecurityActionAuthFailed           // 登录态鉴权失败
	AuthEventActionSecurityFailed       = collectorx.AuthSecurityActionSecurityFailed       // 签名或加密链路失败
	AuthEventActionRefreshSuccess       = collectorx.AuthSecurityActionRefreshSuccess       // 刷新 token 成功
	AuthEventActionLogoutSuccess        = collectorx.AuthSecurityActionLogoutSuccess        // 退出登录成功
	AuthEventActionSessionInvalidateAll = collectorx.AuthSecurityActionSessionInvalidateAll // 用户全部 session 失效
)

// 认证风控事件原因。
const (
	AuthEventReasonInvalidPassword          = collectorx.AuthSecurityReasonInvalidPassword          // 账号或密码错误
	AuthEventReasonUserDisabled             = collectorx.AuthSecurityReasonUserDisabled             // 用户被禁用
	AuthEventReasonUserNotFound             = collectorx.AuthSecurityReasonUserNotFound             // 用户不存在
	AuthEventReasonMissingBearer            = collectorx.AuthSecurityReasonMissingBearer            // 缺少 Bearer token
	AuthEventReasonTokenExpired             = collectorx.AuthSecurityReasonTokenExpired             // token 已过期
	AuthEventReasonSessionExpired           = collectorx.AuthSecurityReasonSessionExpired           // Redis session 已失效
	AuthEventReasonTokenInvalid             = collectorx.AuthSecurityReasonTokenInvalid             // token 无效
	AuthEventReasonSecurityFailed           = collectorx.AuthSecurityReasonSecurityFailed           // 签名或加密链路失败
	AuthEventReasonSecurityAppIDInvalid     = collectorx.AuthSecurityReasonSecurityAppIDInvalid     // 安全链路 AppID 无效
	AuthEventReasonSecurityKeyUnavailable   = collectorx.AuthSecurityReasonSecurityKeyUnavailable   // 安全链路秘钥不可用
	AuthEventReasonSignatureFailed          = collectorx.AuthSecurityReasonSignatureFailed          // 请求验签失败
	AuthEventReasonSecurityPayloadTooLarge  = collectorx.AuthSecurityReasonSecurityPayloadTooLarge  // 安全字段或请求体超过上限
	AuthEventReasonResponseSignFailed       = collectorx.AuthSecurityReasonResponseSignFailed       // 响应回签失败
	AuthEventReasonCryptoDisabled           = collectorx.AuthSecurityReasonCryptoDisabled           // 加解密链路关闭
	AuthEventReasonRequestDecryptFailed     = collectorx.AuthSecurityReasonRequestDecryptFailed     // 请求解密失败
	AuthEventReasonResponseEncryptFailed    = collectorx.AuthSecurityReasonResponseEncryptFailed    // 响应加密失败
	AuthEventReasonLoginIPRateLimited       = collectorx.AuthSecurityReasonLoginIPRateLimited       // 登录 IP 限流
	AuthEventReasonLoginIdentityRateLimited = collectorx.AuthSecurityReasonLoginIdentityRateLimited // 登录身份限流
	AuthEventReasonRegisterIPRateLimited    = collectorx.AuthSecurityReasonRegisterIPRateLimited    // 注册 IP 限流
	AuthEventReasonSessionCreated           = collectorx.AuthSecurityReasonSessionCreated           // 新会话已创建
	AuthEventReasonSessionRotated           = collectorx.AuthSecurityReasonSessionRotated           // 会话已轮换
	AuthEventReasonCurrentSessionDeleted    = collectorx.AuthSecurityReasonCurrentSessionDeleted    // 当前会话已删除
	AuthEventReasonUserSessionsInvalidated  = collectorx.AuthSecurityReasonUserSessionsInvalidated  // 用户会话已全部失效
)

// AuthEventInput 表示认证流程内待投递的轻量风控事件。
type AuthEventInput struct {
	Action    string // 事件动作
	UserID    int64  // 用户 ID，未知时为 0
	Identity  string // 登录身份主体，仅用于生成脱敏哈希
	ClientIP  string // 客户端 IP，仅用于生成脱敏哈希
	SessionID string // 稳定会话 ID，仅用于生成脱敏哈希
	Reason    string // 事件原因
	Count     int    // 批量操作影响数量
}

// authEventPayload 表示写入 Collector 的脱敏认证事件负载。
type authEventPayload struct {
	Action         string `json:"action"`                   // 事件动作
	UserID         string `json:"user_id,omitempty"`        // 用户 ID 十进制字符串，避免跨语言整数精度丢失
	IdentityHash   string `json:"identity_hash,omitempty"`  // 登录身份 HMAC 哈希
	ClientIPHash   string `json:"client_ip_hash,omitempty"` // 客户端 IP HMAC 哈希
	SessionHash    string `json:"session_hash,omitempty"`   // 稳定 sid 的 HMAC 哈希
	AppID          string `json:"app_id"`                   // 当前站点命名空间
	Route          string `json:"route,omitempty"`          // 路由别名
	TraceID        string `json:"trace_id,omitempty"`       // 链路追踪 ID
	SpanID         string `json:"span_id,omitempty"`        // 当前服务 span ID
	Node           string `json:"node,omitempty"`           // 当前服务节点
	Mode           string `json:"mode,omitempty"`           // 当前运行模式
	Reason         string `json:"reason,omitempty"`         // 事件原因
	Count          int    `json:"count,omitempty"`          // 批量影响数量
	OccurredAtUnix int64  `json:"occurred_at_unix"`         // 事件发生时间，Unix 秒
}

// emitAuthEvent 在不阻断认证流程的前提下投递风控事件。
func (l *AuthLogic) emitAuthEvent(input AuthEventInput) {
	if l == nil {
		return
	}
	RecordAuthEvent(l.Ctx, l.Svc, input)
}

// RecordAuthEvent 将认证风控事件按旁路策略写入 Collector。
func RecordAuthEvent(ctx context.Context, svcCtx *svc.ServiceContext, input AuthEventInput) {
	if svcCtx == nil || svcCtx.Collector == nil {
		return
	}
	if strings.TrimSpace(input.Action) == "" {
		return
	}
	cfg := svcCtx.CurrentConfig()
	if !cfg.Collector.Enabled {
		return
	}
	payload := buildAuthEventPayload(ctx, cfg, input)
	data, err := json.Marshal(payload)
	if err != nil {
		// 负载序列化异常同样按旁路失败处理，避免监控链反向阻断认证请求。
		return
	}
	// 响应结束后仍允许已产生的旁路事件继续投递。
	enqueueBaseCtx := context.WithoutCancel(ctx)
	// 短超时限制 Kafka 投递占用认证请求资源的时间。
	enqueueCtx, cancel := context.WithTimeout(enqueueBaseCtx, authEventEnqueueTimeout)
	defer cancel()
	// Collector 已负责失败告警，因此认证主链忽略投递返回值。
	_, _ = svcCtx.Collector.Enqueue(enqueueCtx, collectorx.Event{
		BizType:      AuthCollectorBizType,
		PartitionKey: authEventPartitionKey(payload),
		Payload:      json.RawMessage(data),
	})
}

// buildAuthEventPayload 构造脱敏后的认证事件负载。
func buildAuthEventPayload(ctx context.Context, cfg config.Config, input AuthEventInput) authEventPayload {
	meta := requestctx.FromContext(ctx)
	clientIP := strings.TrimSpace(input.ClientIP)
	payload := authEventPayload{
		Action:         strings.TrimSpace(input.Action),
		AppID:          cfg.AppID,
		Reason:         strings.TrimSpace(input.Reason),
		Count:          input.Count,
		OccurredAtUnix: time.Now().Unix(),
	}
	if input.UserID > 0 {
		payload.UserID = strconv.FormatInt(input.UserID, 10)
	}
	if meta != nil {
		if clientIP == "" {
			// 调用方未提供客户端 IP 时复用已校验的请求元数据。
			clientIP = meta.ClientIP
		}
		payload.Route = strings.TrimSpace(meta.Route)
		payload.TraceID = strings.TrimSpace(meta.TraceID)
		payload.SpanID = strings.TrimSpace(meta.SpanID)
		payload.Node = strings.TrimSpace(meta.Node)
		payload.Mode = strings.TrimSpace(meta.Mode)
	}
	if payload.Node == "" {
		payload.Node = strings.TrimSpace(cfg.InstanceID)
	}
	if payload.Mode == "" {
		payload.Mode = strings.TrimSpace(cfg.Mode)
	}
	// 原始身份、IP 和 sid 到此为止，Collector 负载只携带可关联的 HMAC 摘要。
	payload.IdentityHash = authSensitiveValueHash(cfg, input.Identity)
	payload.ClientIPHash = authSensitiveValueHash(cfg, clientIP)
	payload.SessionHash = authSensitiveValueHash(cfg, input.SessionID)
	return payload
}

// authEventPartitionKey 返回 Collector 分区键，优先按用户聚合。
func authEventPartitionKey(payload authEventPayload) string {
	if payload.UserID != "" {
		return payload.AppID + ":" + payload.UserID
	}
	// hash 只用于 Kafka 分区，截短后仍保留足够离散度且不会超过 Collector 契约。
	hash := payload.IdentityHash
	if hash == "" {
		hash = payload.ClientIPHash
	}
	if hash == "" {
		return payload.AppID
	}
	if len(hash) > authEventPartitionHashLength {
		hash = hash[:authEventPartitionHashLength]
	}
	return payload.AppID + ":" + hash
}

// authSensitiveValueHash 使用应用密钥对认证链路中的敏感字段做不可逆关联哈希。
func authSensitiveValueHash(cfg config.Config, value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(cfg.AppKey))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
