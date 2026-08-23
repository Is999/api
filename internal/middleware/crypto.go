package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"

	codes "api/common/codes"
	"api/internal/httpresp"
	"api/internal/infra/collectorx"
	"api/internal/requestctx"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// 加密链路请求头和字段标记常量。
const (
	cipherWholeBody        = security.CipherWholeBody  // 禁用的整包加解密标记
	cipherJSONPrefix       = security.CipherJSONPrefix // 字段值 JSON 编解码前缀
	secretKeyVersionHeader = "X-Key-Version"           // 本次命中的秘钥版本头
	secretKeyGrayKeyHeader = "X-Gray-Key"              // 灰度分桶键请求头
)

// CryptoMiddleware 对请求敏感字段解密并对响应敏感字段加密。
type CryptoMiddleware struct {
	svc     *svc.ServiceContext // 加密中间件依赖的运行配置
	runtime Runtime             // 密钥选路与风控事件由 handler 装配层提供
}

// NewCryptoMiddleware 创建加密中间件实例。
func NewCryptoMiddleware(svcCtx *svc.ServiceContext, runtime Runtime) *CryptoMiddleware {
	return &CryptoMiddleware{svc: svcCtx, runtime: runtime}
}

// Handle 根据 X-Cipher/X-Crypto 请求头执行加解密，未配置秘钥时走普通 JSON 链路。
func (m *CryptoMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		route := requestRouteAlias(r)
		policy, policyDeclared := security.LookupRoutePolicy(route)
		if !policyDeclared {
			m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, errors.Errorf("路由未声明安全策略 alias=%s", route))
			return
		}
		requestCipher := r.Header.Get("X-Cipher")
		// 无请求密文且路由未声明响应加密时直接透传，避免普通响应进入无上限整包缓冲。
		if requestCipher == "" && len(policy.ResponseCipher) == 0 {
			next(w, r)
			return
		}
		if securityConfigConfigured(m.svc) && !securityCryptoEnabled(m.svc) {
			if requestCipher != "" {
				m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonCryptoDisabled, errors.New("当前应用已关闭加密解密链路"))
				return
			}
			next(w, r)
			return
		}
		// 未配置秘钥且请求未声明加密时保持普通 JSON 链路。
		if !securityConfigConfigured(m.svc) && requestCipher == "" {
			next(w, r)
			return
		}
		cryptoType := security.ResolveCryptoType(r.Header.Get("X-Crypto"))
		// 普通链路已经提前返回，此处固定读取本次加解密使用的应用快照。
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
		// 加密链路关闭时拒绝已声明的密文请求，普通请求直接透传。
		if !routeConfig.CryptoEnabled {
			if requestCipher != "" {
				m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonCryptoDisabled, errors.New("当前应用已关闭加密解密链路"))
				return
			}
			next(w, r)
			return
		}
		var responseCryptor security.Cryptor
		responseVersion := ""
		if requestCipher != "" {
			requestCipherParams, err := decodeAndValidateCipherParams(requestCipher, policy.RequestCipher, "请求")
			if err != nil {
				m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonRequestDecryptFailed, err)
				return
			}
			cryptor, resolvedVersion, err := m.cryptor(r, appID, cryptoType)
			if err != nil {
				m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, err)
				return
			}
			recordResolvedSecretKeyVersion(r, resolvedVersion)
			if err := m.decryptRequest(r, requestCipherParams, cryptor); err != nil {
				m.fail(w, r, codes.AuthFailed, collectorx.AuthSecurityReasonRequestDecryptFailed, err)
				return
			}
			// 预编译 RSA 对象同时持有解密私钥和加密公钥，可与 AES-GCM 一样复用本次选路结果。
			if len(policy.ResponseCipher) > 0 {
				responseCryptor = cryptor
				responseVersion = resolvedVersion
			}
		}
		responseCipher := ""
		var responseCipherParams []string
		if len(policy.ResponseCipher) > 0 {
			// 响应加密器必须在业务处理器前锁定，版本错误不能发生在业务副作用之后。
			responseCipher = security.EncodeCipherParams(policy.ResponseCipher)
			responseCipherParams, err = decodeAndValidateCipherParams(responseCipher, policy.ResponseCipher, "响应")
			if err != nil {
				m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonResponseEncryptFailed, err)
				return
			}
			if responseCryptor == nil {
				responseCryptor, responseVersion, err = m.cryptor(r, appID, cryptoType)
				if err != nil {
					m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonSecurityKeyUnavailable, err)
					return
				}
			}
			recordResolvedSecretKeyVersion(r, responseVersion)
		}

		recorder := newBodyRecorder()
		next(recorder, r)
		if flushSecurityFailureResponse(w, recorder) {
			return
		}

		recorder.Header().Del("X-Cipher")
		// 响应加密字段只来自静态路由策略，业务 Handler 不得动态扩大或缩小安全边界。
		if responseCipher != "" && recorder.status < http.StatusBadRequest {
			recorder.Header().Set("X-Cipher", responseCipher)
		}
		if requestCipher != "" || responseCipher != "" {
			recorder.Header().Set("X-Crypto", cryptoType)
		}
		if responseCipher != "" && recorder.status < http.StatusBadRequest && recorder.body.Len() > 0 {
			if responseVersion != "" {
				recorder.Header().Set(secretKeyVersionHeader, responseVersion)
			}
			if err := m.encryptResponse(recorder, responseCipherParams, responseCryptor); err != nil {
				m.fail(w, r, codes.InternalError, collectorx.AuthSecurityReasonResponseEncryptFailed, err)
				return
			}
		}
		flushRecordedResponse(w, recorder)
	}
}

// requestRouteAlias 从请求上下文读取统一路由别名。
func requestRouteAlias(r *http.Request) string {
	if r == nil {
		return ""
	}
	if meta := requestctx.FromContext(r.Context()); meta != nil {
		return strings.TrimSpace(meta.Route)
	}
	return ""
}

// decryptRequest 解密请求体首层字段。
func (m *CryptoMiddleware) decryptRequest(r *http.Request, cipherParams []string, cryptor security.Cryptor) error {
	// 请求只允许字段级解密，整包密文无法执行字段白名单校验。
	if hasCipherWholeBody(cipherParams) {
		return errors.New("请求解密不允许整包加密")
	}
	bodyMap, err := requestJSONMap(r)
	if err != nil {
		return errors.Tag(err)
	}
	for _, param := range cipherParams {
		isJSON := strings.HasPrefix(param, cipherJSONPrefix)
		field := strings.TrimPrefix(param, cipherJSONPrefix)
		value, ok := bodyMap[field]
		if !ok || isEmptySecurityFieldValue(value) {
			continue
		}
		// 密文只接受有界标量，不能借字段加密入口传入大对象。
		if err := security.ValidateSecurityScalarValue("请求加密密文", field, value); err != nil {
			return errors.Tag(err)
		}
		ciphertext := security.SignValueString(value)
		// 外层已对密文验签，通过后才进入解密器，避免未验签请求探测解密结果。
		plain, err := cryptor.Decrypt(ciphertext)
		if err != nil {
			return errors.Wrapf(err, "请求字段[%s]解密失败", field)
		}
		if isJSON {
			// 只有显式 json: 字段恢复 JSON 类型，且解密后的明文仍受独立大小上限约束。
			if err := security.ValidateSecurityTextValue("请求加密明文", field, plain, security.MaxSecurityJSONFieldBytes); err != nil {
				return errors.Tag(err)
			}
			var jsonValue any
			if plain != "" {
				// 保留对象内数字的十进制文本，仍只接受一个完整 JSON 值。
				decoder := json.NewDecoder(strings.NewReader(plain))
				decoder.UseNumber()
				if err := decoder.Decode(&jsonValue); err != nil {
					return errors.Wrapf(err, "请求字段[%s] JSON解码失败", field)
				}
				var extra any
				if err := decoder.Decode(&extra); err != io.EOF {
					return errors.Errorf("请求字段[%s]只能包含一个JSON值", field)
				}
			}
			bodyMap[field] = jsonValue
		} else {
			if err := security.ValidateSecurityTextValue("请求加密明文", field, plain, security.MaxSecurityFieldBytes); err != nil {
				return errors.Tag(err)
			}
			bodyMap[field] = plain
		}
	}
	// 所有字段成功后一次性替换请求体，失败不会留下半解密内容。
	return replaceJSONBody(r, bodyMap)
}

// encryptResponse 加密响应 data 下的字段；响应禁止整包加密。
func (m *CryptoMiddleware) encryptResponse(recorder *bodyRecorder, cipherParams []string, cryptor security.Cryptor) error {
	if hasCipherWholeBody(cipherParams) {
		return errors.New("响应加密不允许整包加密")
	}
	envelope, data, success, err := parseSecurityResponseData(recorder, "加密")
	if err != nil {
		return errors.Tag(err)
	}
	if !success {
		// 失败响应保持原文，避免加密覆盖统一错误契约。
		return nil
	}
	if cryptor == nil {
		return errors.New("响应加密器未初始化")
	}
	for _, param := range cipherParams {
		// json: 字段按 JSON 明文加密，普通字段只接受标量。
		isJSON := strings.HasPrefix(param, cipherJSONPrefix)
		fieldPath := strings.TrimPrefix(param, cipherJSONPrefix)
		value, ok := nestedCipherValue(data, fieldPath)
		if !ok || isEmptySecurityFieldValue(value) {
			continue
		}
		plain := ""
		if isJSON {
			body, err := security.ValidateSecurityJSONValue("响应加密明文", fieldPath, value)
			if err != nil {
				return errors.Wrapf(err, "响应字段[%s] JSON编码失败", fieldPath)
			}
			plain = string(body)
		} else if err := security.ValidateSecurityScalarValue("响应加密明文", fieldPath, value); err != nil {
			return errors.Tag(err)
		} else {
			plain = security.SignValueString(value)
		}
		encrypted, err := cryptor.Encrypt(plain)
		if err != nil {
			return errors.Wrapf(err, "响应字段[%s]加密失败", fieldPath)
		}
		if ok := setNestedCipherValue(data, fieldPath, encrypted); !ok {
			return errors.Errorf("响应字段[%s]写回加密结果失败", fieldPath)
		}
	}
	// 全部字段成功后一次性替换缓冲区，避免返回半加密响应。
	envelope["data"] = data
	body, err := json.Marshal(envelope)
	if err != nil {
		return errors.Tag(err)
	}
	recorder.body.Reset()
	_, _ = recorder.body.Write(body)
	// 密文长度变化后删除旧 Content-Length，由 HTTP 层重新计算。
	recorder.Header().Del("Content-Length")
	return nil
}

// decodeAndValidateCipherParams 解码加密字段并校验是否在路由策略白名单内。
func decodeAndValidateCipherParams(raw string, allowed []string, scope string) ([]string, error) {
	if raw == cipherWholeBody {
		return nil, errors.Errorf("%s加密不允许整包加密", scope)
	}
	// 解码前限制原始头长度，避免异常输入造成无界分配。
	if err := security.ValidateSecurityTextValue(scope+"加密头", "X-Cipher", raw, security.MaxSecurityJSONFieldBytes); err != nil {
		return nil, errors.Tag(err)
	}
	params, err := decodeCipherParams(raw)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if len(params) == 0 {
		return nil, errors.Errorf("%s加密字段不能为空", scope)
	}
	// 字段格式、重复项和数量只校验一次，禁止裁剪客户端声明。
	if err := security.ValidateSecurityFieldCount(params, scope+"加密"); err != nil {
		return nil, errors.Tag(err)
	}
	if hasCipherWholeBody(params) {
		return nil, errors.Errorf("%s加密不允许整包加密", scope)
	}
	if len(allowed) == 0 {
		return nil, errors.Errorf("%s加密字段未在路由策略中声明", scope)
	}
	// 运行期再次约束路由字段数量，防止自定义策略绕过注册校验。
	if err := security.ValidateSecurityFieldCount(allowed, scope+"路由策略加密"); err != nil {
		return nil, errors.Tag(err)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = struct{}{}
	}
	// 客户端声明只能落在路由白名单内，不能扩大加密范围。
	for _, field := range params {
		if _, ok := allowedSet[field]; !ok {
			return nil, errors.Errorf("%s加密字段不允许: %s", scope, field)
		}
	}
	return params, nil
}

// hasCipherWholeBody 判断字段列表是否包含整包加密标记。
func hasCipherWholeBody(fields []string) bool {
	return slices.Contains(fields, cipherWholeBody)
}

// isEmptySecurityFieldValue 判断安全字段是否为空，空值不参与加密处理。
func isEmptySecurityFieldValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return text == ""
	}
	return false
}

// nestedCipherValue 按点路径读取 map 中的嵌套字段值。
func nestedCipherValue(data map[string]any, fieldPath string) (any, bool) {
	parts := splitCipherFieldPath(fieldPath)
	if len(parts) == 0 {
		return nil, false
	}
	current := any(data)
	for _, part := range parts {
		node, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, exists := node[part]
		if !exists {
			return nil, false
		}
		current = value
	}
	return current, true
}

// setNestedCipherValue 按点路径回写 map 中的嵌套字段值。
func setNestedCipherValue(data map[string]any, fieldPath string, value any) bool {
	parts := splitCipherFieldPath(fieldPath)
	if len(parts) == 0 {
		return false
	}
	current := data
	for index, part := range parts {
		if index == len(parts)-1 {
			current[part] = value
			return true
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			return false
		}
		current = next
	}
	return false
}

// splitCipherFieldPath 把点路径拆成逐级键名。
func splitCipherFieldPath(fieldPath string) []string {
	fieldPath = strings.TrimSpace(fieldPath)
	if fieldPath == "" {
		return nil
	}
	rawParts := strings.Split(fieldPath, ".")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// cryptor 根据 X-Crypto 返回启动期预编译的加解密器。
func (m *CryptoMiddleware) cryptor(r *http.Request, appID string, cryptoType string) (security.Cryptor, string, error) {
	runtime, err := requireRuntime(m.runtime)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	versionHint := requestSecretKeyVersionHint(r)
	grayKey := requestSecretKeyGrayKey(r)
	return runtime.Cryptor(r.Context(), appID, versionHint, grayKey, cryptoType)
}

// requestAppID 从 X-App-Id 请求头解析真实 AppID。
func requestAppID(r *http.Request) (string, error) {
	raw := r.Header.Get("X-App-Id")
	if raw == "" {
		return "", errors.New("缺少请求头X-App-Id")
	}
	appID, err := decodeBase64Header(raw)
	if err != nil {
		return "", errors.New("请求头X-App-Id格式错误")
	}
	return appID, nil
}

// requestSecretKeyVersionHint 读取请求头中显式指定的秘钥版本。
func requestSecretKeyVersionHint(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Header.Get(secretKeyVersionHeader)
}

// requestSecretKeyGrayKey 读取请求头中的灰度分桶键。
func requestSecretKeyGrayKey(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Header.Get(secretKeyGrayKeyHeader)
}

// recordResolvedSecretKeyVersion 把最终命中的秘钥版本写回请求头。
func recordResolvedSecretKeyVersion(r *http.Request, resolvedVersion string) {
	if r == nil || resolvedVersion == "" {
		return
	}
	r.Header.Set(secretKeyVersionHeader, resolvedVersion)
}

// fail 写出加密中间件失败响应。
func (m *CryptoMiddleware) fail(w http.ResponseWriter, r *http.Request, code int, reason string, err error) {
	markSecurityFailure(w)
	clearSecurityResponseHeaders(w.Header())
	code = resolveSecurityFailureCode(reason, code, err)
	httpStatus := codes.HTTPStatus(code)
	reason = resolveSecurityFailureReason(reason, err)
	emitSecurityFailureEvent(r.Context(), m.runtime, reason)
	httpresp.NewJSONResp(r.Context(), w).
		SetHTTPStatus(httpStatus).
		SetCode(code).
		SetError(err).
		Fail("")
}
