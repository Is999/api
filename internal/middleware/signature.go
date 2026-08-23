package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	codes "api/common/codes"
	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/httpresp"
	"api/internal/infra/collectorx"
	"api/internal/infra/loggerx"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
)

// SignatureMiddleware 对敏感请求验签并对敏感响应回签。
type SignatureMiddleware struct {
	svc     *svc.ServiceContext // 签名中间件依赖的配置和防重放缓存
	runtime Runtime             // 密钥选路与风控事件由 handler 装配层提供
}

// signatureReplayTTL 是请求时间戳允许的前后偏差，防重放记录须覆盖签名剩余有效期。
const signatureReplayTTL = 5 * time.Minute

// NewSignatureMiddleware 创建签名中间件实例。
func NewSignatureMiddleware(svcCtx *svc.ServiceContext, runtime Runtime) *SignatureMiddleware {
	return &SignatureMiddleware{svc: svcCtx, runtime: runtime}
}

// Handle 按路由别名执行请求验签和响应回签。
func (m *SignatureMiddleware) Handle(next http.HandlerFunc, alias routealias.Alias) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := requestctx.New(r.Context())
		if alias != "" && alias != routealias.Ignore {
			requestctx.SetRoute(ctx, string(alias))
		}
		r = r.WithContext(ctx)

		policy, policyDeclared := security.LookupRoutePolicy(string(alias))
		if !policyDeclared {
			m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, errors.Errorf("路由未声明安全策略 alias=%s", alias))
			return
		}
		if !securityConfigConfigured(m.svc) {
			next(w, r)
			return
		}
		if !securitySignEnabled(m.svc) {
			next(w, r)
			return
		}
		// 没有请求验签和响应回签策略的路由不参与签名链路。
		if policy.RequestSign == nil && policy.ResponseSign == nil {
			next(w, r)
			return
		}
		appID, err := requestAppID(r)
		if err != nil {
			m.fail(w, r, codes.ParamError, collectorx.AuthSecurityReasonSecurityAppIDInvalid, err)
			return
		}
		runtime, err := requireRuntime(m.runtime)
		if err != nil {
			m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, err)
			return
		}
		routeConfig, err := runtime.SecurityRoute(r.Context(), appID)
		if err != nil {
			m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, err)
			return
		}
		signEnabled := routeConfig.SignEnabled
		if !signEnabled {
			next(w, r)
			return
		}
		signatureType := security.ResolveSignatureType(r.Header.Get("X-Signature"))
		traceID, err := signatureTraceID(r)
		if err != nil {
			m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonSignatureFailed, err)
			return
		}
		timestamp, expiresAt, err := requestTimestamp(r)
		if err != nil {
			m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonSignatureFailed, err)
			return
		}
		var responseSigner security.Signer
		responseVersion := ""
		if policy.RequestSign != nil {
			requestSigner, requestVersion, err := m.verifyRequest(r, policy, appID, traceID, timestamp, signatureType, expiresAt)
			if err != nil {
				m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonSignatureFailed, err)
				return
			}
			// RSA 注册表对象同时持有请求公钥和响应私钥，可与 HMAC 一样复用本次选路结果。
			if policy.ResponseSign != nil {
				responseSigner = requestSigner
				responseVersion = requestVersion
			}
		}
		// 响应签名器必须在业务处理器前锁定，版本错误不能发生在业务副作用之后。
		if policy.ResponseSign != nil && responseSigner == nil {
			responseSigner, responseVersion, err = m.signer(r, appID, signatureType)
			if err != nil {
				m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, err)
				return
			}
			recordResolvedSecretKeyVersion(r, responseVersion)
		}
		// 仅请求验签的路由不需要改写响应，直接透传可保留 Flusher 等底层接口和即时写出语义。
		if policy.ResponseSign == nil {
			w.Header().Set("X-Signature", signatureType)
			w.Header().Set(requestctx.HeaderTraceID, traceID)
			w.Header().Set(requestctx.HeaderTimestamp, timestamp)
			next(w, r)
			return
		}

		recorder := newBodyRecorder()
		next(recorder, r)
		if flushSecurityFailureResponse(w, recorder) {
			return
		}
		recorder.Header().Set("X-Signature", signatureType)
		recorder.Header().Set(requestctx.HeaderTraceID, traceID)
		recorder.Header().Set(requestctx.HeaderTimestamp, timestamp)
		if recorder.status < http.StatusBadRequest && recorder.body.Len() > 0 {
			resolvedVersion, err := m.signResponse(recorder, policy, appID, traceID, timestamp, responseSigner, responseVersion)
			if err != nil {
				m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonResponseSignFailed, err)
				return
			}
			if resolvedVersion != "" {
				recorder.Header().Set(secretKeyVersionHeader, resolvedVersion)
			}
		}
		flushRecordedResponse(w, recorder)
	}
}

// signatureTraceID 校验签名使用的追踪标识与实际请求链路一致。
func signatureTraceID(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("签名请求为空")
	}
	raw := r.Header.Get(requestctx.HeaderTraceID)
	if raw == "" {
		return "", errors.New("缺少请求头X-Trace-Id")
	}
	parsed, ok := parseHeaderTraceID(raw)
	if !ok || raw != parsed.String() {
		return "", errors.New("请求头X-Trace-Id必须是32位小写十六进制")
	}
	if meta := requestctx.FromContext(r.Context()); meta != nil {
		actual := strings.TrimSpace(meta.TraceID)
		if actual != "" && actual != raw {
			return "", errors.New("请求头X-Trace-Id与实际链路不一致")
		}
	}
	return raw, nil
}

// verifyRequest 校验请求 sign 字段，并返回本次选路对象供响应回签复用。
func (m *SignatureMiddleware) verifyRequest(r *http.Request, policy security.RouteSecurityPolicy, appID string, traceID string, timestamp string, signatureType string, expiresAt time.Time) (security.Signer, string, error) {
	// 请求验签必须使用显式字段清单，禁止把未审计字段纳入全量签名。
	if hasSignFieldAll(policy.RequestSign) {
		return nil, "", errors.New("请求签名不允许使用全量字段")
	}
	if err := security.ValidateSecurityFieldCount(policy.RequestSign, "请求签名"); err != nil {
		return nil, "", errors.Tag(err)
	}
	// 参数和值通过边界校验后再构造稳定签名串。
	params, err := requestParams(r)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	signValue, ok := params["sign"]
	if !ok || security.SignValueString(signValue) == "" {
		return nil, "", errors.New("缺少签名参数sign")
	}
	if err := security.ValidateSecurityTextValue("请求签名值", "sign", security.SignValueString(signValue), security.MaxSecurityFieldBytes); err != nil {
		return nil, "", errors.Tag(err)
	}
	if err := validateSignValues(params, policy.RequestSign, "请求签名"); err != nil {
		return nil, "", errors.Tag(err)
	}
	signStr := security.BuildSignString(params, policy.RequestSign, traceID, timestamp, appID)
	// 密钥版本在业务执行前锁定，并记录到请求上下文供响应复用。
	signer, resolvedVersion, err := m.signer(r, appID, signatureType)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	recordResolvedSecretKeyVersion(r, resolvedVersion)
	ok, err = signer.Verify(signStr, security.SignValueString(signValue))
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	if !ok {
		return nil, "", errors.New("签名错误")
	}
	// 验签成功后再写防重放标记，失败请求不会占用合法 traceID。
	if err := m.markRequestVerified(r, appID, traceID, expiresAt); err != nil {
		return nil, "", errors.Tag(err)
	}
	return signer, resolvedVersion, nil
}

// signResponse 使用业务执行前锁定的密钥对成功响应回签，避免业务副作用后再次读取密钥失败。
func (m *SignatureMiddleware) signResponse(recorder *bodyRecorder, policy security.RouteSecurityPolicy, appID string, traceID string, timestamp string, signer security.Signer, resolvedVersion string) (string, error) {
	// 响应只允许对路由声明字段回签，失败响应保持统一错误契约。
	if hasSignFieldAll(policy.ResponseSign) {
		return "", errors.New("响应签名不允许使用全量字段")
	}
	if err := security.ValidateSecurityFieldCount(policy.ResponseSign, "响应签名"); err != nil {
		return "", errors.Tag(err)
	}
	envelope, data, success, err := parseSecurityResponseData(recorder, "签名")
	if err != nil {
		return "", errors.Tag(err)
	}
	if !success {
		return "", nil
	}
	if err := validateSignValues(data, policy.ResponseSign, "响应签名"); err != nil {
		return "", errors.Tag(err)
	}
	if signer == nil {
		return "", errors.New("响应签名器未初始化")
	}
	// 使用请求阶段锁定的签名器，避免业务副作用后重新选路失败。
	signStr := security.BuildSignString(data, policy.ResponseSign, traceID, timestamp, appID)
	signValue, err := signer.Sign(signStr)
	if err != nil {
		return "", errors.Tag(err)
	}
	data["sign"] = signValue
	envelope["data"] = data
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", errors.Tag(err)
	}
	// 签名成功后一次性替换响应体，并删除已失效的长度头。
	recorder.body.Reset()
	_, _ = recorder.body.Write(body)
	recorder.Header().Del("Content-Length")
	return resolvedVersion, nil
}

// signer 根据 X-Signature 返回启动期预编译的签名器。
func (m *SignatureMiddleware) signer(r *http.Request, appID string, signatureType string) (security.Signer, string, error) {
	runtime, err := requireRuntime(m.runtime)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	versionHint := requestSecretKeyVersionHint(r)
	grayKey := requestSecretKeyGrayKey(r)
	return runtime.Signer(r.Context(), appID, versionHint, grayKey, signatureType)
}

// fail 写出签名中间件失败响应，错误详情只进入日志链路。
func (m *SignatureMiddleware) fail(w http.ResponseWriter, r *http.Request, code int, reason string, err error) {
	markSecurityFailure(w)
	clearSecurityResponseHeaders(w.Header())
	code = resolveSecurityFailureCode(reason, code, err)
	httpStatus := codes.HTTPStatus(code)
	reason = resolveSecurityFailureReason(reason, err)
	emitSecurityFailureEvent(r.Context(), m.runtime, reason)
	fields := []logx.LogField{
		logx.Field("http_status", httpStatus),
		logx.Field("biz_code", code),
	}
	loggerx.Errorw(r.Context(), "签名 处理失败", err, fields...)
	httpresp.NewJSONResp(r.Context(), w).
		SetHTTPStatus(httpStatus).
		SetCode(code).
		SetError(err).
		Fail("")
}

// requestTimestamp 解析 X-Timestamp，并限制请求只在防重放窗口内有效。
func requestTimestamp(r *http.Request) (string, time.Time, error) {
	raw := r.Header.Get(requestctx.HeaderTimestamp)
	if raw == "" {
		return "", time.Time{}, errors.New("缺少请求头X-Timestamp")
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 {
		return "", time.Time{}, errors.New("请求头X-Timestamp格式错误")
	}
	canonical := strconv.FormatInt(seconds, 10)
	if raw != canonical {
		return "", time.Time{}, errors.New("请求头X-Timestamp格式错误")
	}
	nowSeconds := time.Now().Unix()
	windowSeconds := int64(signatureReplayTTL / time.Second)
	if seconds < nowSeconds-windowSeconds || seconds > nowSeconds+windowSeconds {
		return "", time.Time{}, errors.New("请求头X-Timestamp已过期")
	}
	// 边界秒内仍可验签，记录保留到下一秒才过期。
	return canonical, time.Unix(seconds+windowSeconds+1, 0), nil
}

// hasSignFieldAll 判断签名策略是否要求全量字段签名。
func hasSignFieldAll(fields []string) bool {
	return slices.Contains(fields, security.SignFieldAll)
}

// validateSignValues 校验参与签名的字段值；显式策略允许小型数组或对象按稳定 JSON 参与签名。
func validateSignValues(data map[string]any, fields []string, scope string) error {
	if err := security.ValidateSecurityFieldCount(fields, scope); err != nil {
		return errors.Tag(err)
	}
	for _, field := range fields {
		value, ok := security.SignFieldValue(data, field)
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok && text == "" {
			continue
		}
		if err := security.ValidateSecurityTextValue(scope, field, security.SignValueString(value), security.MaxSecurityFieldBytes); err != nil {
			return errors.Tag(err)
		}
	}
	return nil
}

// markRequestVerified 使用 Redis 记录已验签请求，避免同一个 trace_id 在时间窗口内重复提交。
func (m *SignatureMiddleware) markRequestVerified(r *http.Request, appID string, traceID string, expiresAt time.Time) error {
	if appID == "" || traceID == "" || appID != strings.TrimSpace(appID) || traceID != strings.TrimSpace(traceID) {
		return errors.New("签名请求标识不能为空")
	}
	if m.svc == nil || m.svc.Rds == nil {
		return errors.New("签名防重放缓存未初始化")
	}
	runtimeAppID := runtimecfg.AppID()
	if runtimeAppID == "" || appID != runtimeAppID || m.svc.CurrentConfig().AppID != runtimeAppID {
		return errors.New("签名 app_id 与运行配置不一致")
	}
	key := keys.WithPrefix(fmt.Sprintf(keys.SignatureReplayRequest, traceID))
	if key == "" {
		return errors.New("签名防重放缓存 key 为空")
	}
	// 客户端时间可领先服务端，固定五分钟 TTL 会早于签名失效而允许重放。
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return errors.New("请求头X-Timestamp已过期")
	}
	ok, err := m.svc.Rds.SetNX(r.Context(), key, "1", ttl).Result()
	if err != nil {
		return errors.Tag(err)
	}
	if !ok {
		return errors.New("重复请求")
	}
	return nil
}
