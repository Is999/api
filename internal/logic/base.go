package logic

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	i18n "api/common/i18n"
	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/infra/loggerx"
	"api/internal/requestctx"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/logx"
)

// BaseLogic 是所有业务 logic 的公共基座。
type BaseLogic struct {
	logx.Logger                     // 已绑定当前请求上下文的日志记录器
	Ctx         context.Context     // 当前 logic 处理链路使用的上下文
	Svc         *svc.ServiceContext // 绑定当前上下文后的服务依赖集合
}

// NewBaseLogicWithContext 为当前请求克隆一份带上下文的 ServiceContext。
func NewBaseLogicWithContext(ctx context.Context, svcCtx *svc.ServiceContext) *BaseLogic {
	ctx, _ = requestctx.New(ctx)
	ctx = loggerx.BindContext(ctx)
	var scopedSvc *svc.ServiceContext
	if svcCtx != nil {
		// 请求只克隆上下文视图，数据库连接池和 Redis 客户端仍由进程共享。
		scopedSvc = svcCtx.ScopedWithContext(ctx)
	}
	return &BaseLogic{
		Logger: logx.WithContext(ctx),
		Ctx:    ctx,
		Svc:    scopedSvc,
	}
}

// Redis 返回共享 Redis 客户端。
func (l *BaseLogic) Redis() redis.UniversalClient {
	if l.Svc == nil {
		return nil
	}
	return l.Svc.Rds
}

// AppID 返回当前 Redis 缓存命名空间使用的 app_id。
func (l *BaseLogic) AppID() string {
	if l == nil || l.Svc == nil {
		return ""
	}
	return l.Svc.CurrentConfig().AppID
}

// AppRedisKey 给业务 Redis key 追加当前 app_id 命名空间。
func (l *BaseLogic) AppRedisKey(key string) string {
	if l == nil {
		return ""
	}
	appID := l.AppID()
	// 请求持有的服务配置必须与全局 key 前缀一致，不能把旧上下文写入另一站点。
	if appID == "" || appID != runtimecfg.AppID() {
		return ""
	}
	return keys.WithPrefix(key)
}

// Meta 返回当前请求链路元数据。
func (l *BaseLogic) Meta() *requestctx.Meta {
	return requestctx.FromContext(l.Ctx)
}

// Locale 返回当前请求语言，缺省时使用中文。
func (l *BaseLogic) Locale() string {
	if meta := l.Meta(); meta != nil && meta.Locale != "" {
		return meta.Locale
	}
	return i18n.LocaleZHCN
}

// Message 按当前请求语言解析多语言文案。
func (l *BaseLogic) Message(key string, args ...any) string {
	return i18n.MessageByKey(key, l.Locale(), args...)
}

// ClientIP 返回当前请求的客户端 IP。
func (l *BaseLogic) ClientIP() string {
	if meta := l.Meta(); meta != nil {
		return meta.ClientIP
	}
	return ""
}

// AccessToken 返回当前请求的访问令牌。
func (l *BaseLogic) AccessToken() string {
	if meta := l.Meta(); meta != nil {
		return meta.AccessToken
	}
	return ""
}

// GetCtxUser 返回鉴权写入的用户信息；缺失时返回 ID 为零的对象，不返回 nil。
func (l *BaseLogic) GetCtxUser() *requestctx.User {
	user := requestctx.UserFromContext(l.Ctx)
	if user == nil {
		return &requestctx.User{}
	}
	return user
}

// RdsGetJSONObj 从当前 app_id 命名空间读取 JSON 字符串并反序列化到目标对象。
func (l *BaseLogic) RdsGetJSONObj(key string, dest any) error {
	if l == nil || l.Svc == nil || l.Svc.Rds == nil {
		return errors.New("Redis 未初始化")
	}
	key = l.AppRedisKey(key)
	if key == "" {
		return errors.New("Redis key 为空")
	}
	val, err := l.Svc.Rds.Get(l.Ctx, key).Result()
	if err != nil {
		return errors.Tag(err)
	}
	return errors.Tag(json.Unmarshal([]byte(val), dest))
}

// RdsSetJSONValue 将值序列化为 JSON 后写入当前 app_id 命名空间。
func (l *BaseLogic) RdsSetJSONValue(key string, value any, expireSec int64) error {
	if l == nil || l.Svc == nil || l.Svc.Rds == nil {
		return errors.New("Redis 未初始化")
	}
	key = l.AppRedisKey(key)
	if key == "" {
		return errors.New("Redis key 为空")
	}
	// JitterTTL 最多增加 10%，因此上限需为 duration 最大值预留抖动空间。
	const maxJitterTTLSeconds = int64(((1<<63 - 1) / 11 * 10) / int64(time.Second))
	if expireSec <= 0 || expireSec > maxJitterTTLSeconds {
		return errors.Errorf("Redis JSON 缓存 TTL 必须在 1-%d 秒之间", maxJitterTTLSeconds)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return errors.Tag(err)
	}
	return errors.Tag(l.Svc.Rds.Set(l.Ctx, key, data, JitterTTL(time.Duration(expireSec)*time.Second)).Err())
}

// RdsDelKeys 通过独立 DEL 命令批量删除当前 app_id 命名空间下的 Redis 键。
// 普通 pipeline 会按节点分发不同 slot，避免 Redis Cluster 多 key DEL 返回 CROSSSLOT。
func (l *BaseLogic) RdsDelKeys(keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if l == nil || l.Svc == nil || l.Svc.Rds == nil {
		return errors.New("Redis 未初始化")
	}
	normalized := make([]string, 0, len(keys))
	// 先校验全部键再发送删除，避免参数后半段非法时前半段已产生副作用。
	for _, key := range keys {
		key = l.AppRedisKey(key)
		if key == "" {
			return errors.New("Redis key 为空或不是规范逻辑 key")
		}
		normalized = append(normalized, key)
	}
	if len(normalized) == 1 {
		return errors.Tag(l.Svc.Rds.Del(l.Ctx, normalized[0]).Err())
	}
	// 分节点流水线不是事务，执行失败时可能已有部分键删除，调用方可按原键集重试。
	pipe := l.Svc.Rds.Pipeline()
	for _, key := range normalized {
		pipe.Del(l.Ctx, key)
	}
	_, err := pipe.Exec(l.Ctx)
	return errors.Tag(err)
}

// WrapLogicError 给业务错误补充调用点上下文。
func WrapLogicError(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	format = strings.TrimSpace(format)
	if format == "" {
		return errors.Tag(err)
	}
	if len(args) > 0 {
		return errors.Wrapf(err, format, args...)
	}
	return errors.Wrap(err, format)
}

// FormatDateTime 按时间值自身时区输出年月日时分秒，零时间返回空字符串。
func FormatDateTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.DateTime)
}
