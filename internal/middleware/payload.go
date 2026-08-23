package middleware

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// readRequestBody 读取请求体并重新写回，避免中间件读取后影响 handler 解析参数。
func readRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	if r.ContentLength > security.MaxSecurityRequestBodyBytes {
		return nil, errors.Wrapf(security.ErrSecurityPayloadTooLarge, "安全请求体长度超过上限: %d", security.MaxSecurityRequestBodyBytes)
	}
	// 多读一个字节才能区分恰好达到上限与真实超限，未知长度请求不能只信 ContentLength。
	limited := io.LimitReader(r.Body, security.MaxSecurityRequestBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if len(body) > security.MaxSecurityRequestBodyBytes {
		return nil, errors.Wrapf(security.ErrSecurityPayloadTooLarge, "安全请求体长度超过上限: %d", security.MaxSecurityRequestBodyBytes)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	// 中间件已把 chunked/未知长度请求体完整缓冲，下游 httpx.Parse 依赖正数 ContentLength 才解析 JSON。
	r.ContentLength = int64(len(body))
	return body, nil
}

// replaceJSONBody 用新的 JSON 对象覆盖请求体。
func replaceJSONBody(r *http.Request, data map[string]any) error {
	body, err := json.Marshal(data)
	if err != nil {
		return errors.Tag(err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Type", "application/json")
	return nil
}

// requestParams 读取 query、form 和 JSON body 中的首层参数。
func requestParams(r *http.Request) (map[string]any, error) {
	params := make(map[string]any)
	for key, values := range r.URL.Query() {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}
	body, err := readRequestBody(r)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return params, nil
	}
	contentType, err := requestMediaType(r)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if contentType == "application/x-www-form-urlencoded" {
		// 同名 body 字段覆盖 query；ParseForm 会消费 body，成功和失败后都恢复给下游解析器。
		if err := r.ParseForm(); err != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
			return nil, errors.Tag(err)
		}
		for key, values := range r.PostForm {
			if len(values) > 0 {
				params[key] = values[0]
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		return params, nil
	}
	if contentType != "application/json" {
		return nil, errors.Errorf("安全请求体 Content-Type 必须为 application/json 或 application/x-www-form-urlencoded")
	}
	// go-zero 目前按大小写敏感的字符串匹配判断 JSON；验签层接受合法的大小写变体后必须规范化，保证下游解析同一份请求体。
	r.Header.Set("Content-Type", "application/json")
	bodyMap, err := decodeSingleJSONMap(body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return nil, errors.Tag(err)
	}
	for key, value := range bodyMap {
		params[key] = value
	}
	return params, nil
}

// requestJSONMap 读取 JSON 请求体；空 body 返回空 map。
func requestJSONMap(r *http.Request) (map[string]any, error) {
	body, err := readRequestBody(r)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}
	contentType, err := requestMediaType(r)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if contentType != "application/json" {
		return nil, errors.Errorf("加密请求体 Content-Type 必须为 application/json")
	}
	r.Header.Set("Content-Type", "application/json")
	return decodeSingleJSONMap(body)
}

// requestMediaType 严格解析请求媒体类型，拒绝仅包含 JSON 字样的畸形 Content-Type。
func requestMediaType(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("请求不能为空")
	}
	raw := strings.TrimSpace(r.Header.Get("Content-Type"))
	if raw == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", errors.Wrap(err, "请求体 Content-Type 格式错误")
	}
	return strings.ToLower(mediaType), nil
}

// decodeSingleJSONMap 解码单个 JSON 对象，并拒绝尾随的第二个 JSON 值或非法内容。
func decodeSingleJSONMap(body []byte) (map[string]any, error) {
	var bodyMap map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	// 签名保留客户端数字的原始十进制文本，不能先转 float64 再丢失大整数精度。
	decoder.UseNumber()
	if err := decoder.Decode(&bodyMap); err != nil {
		return nil, errors.Tag(err)
	}
	if bodyMap == nil {
		return nil, errors.Errorf("JSON请求体必须是对象")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.Errorf("JSON请求体只能包含一个对象")
	}
	return bodyMap, nil
}

// decodeBase64Header 解码 base64 请求头。
func decodeBase64Header(raw string) (string, error) {
	if raw == "" {
		return "", errors.Errorf("请求头不能为空")
	}
	if strings.TrimSpace(raw) != raw {
		return "", errors.Errorf("请求头base64格式不合法")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(raw)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != raw {
		return "", errors.Errorf("请求头base64格式不合法")
	}
	return string(decoded), nil
}

// decodeCipherParams 解码 X-Cipher 中的字段加密配置。
func decodeCipherParams(raw string) ([]string, error) {
	text, err := decodeBase64Header(raw)
	if err != nil {
		return nil, errors.Tag(err)
	}
	var params []string
	if err := json.Unmarshal([]byte(text), &params); err != nil {
		return nil, errors.Tag(err)
	}
	return params, nil
}

// securityConfigConfigured 判断当前服务是否配置了可选路的安全链路秘钥。
func securityConfigConfigured(svcCtx *svc.ServiceContext) bool {
	if svcCtx == nil {
		return false
	}
	cfg := svcCtx.CurrentConfig()
	secretCfg := cfg.Security.SecretKey
	return cfg.AppID != "" &&
		secretCfg.StableVersion != "" &&
		len(secretCfg.Versions) > 0
}

// securitySignEnabled 读取当前签名开关；关闭时不应要求普通请求携带 AppID 或签名头。
func securitySignEnabled(svcCtx *svc.ServiceContext) bool {
	return securityConfigConfigured(svcCtx) && svcCtx.CurrentConfig().Security.SecretKey.SignStatus == 1
}

// securityCryptoEnabled 读取当前加密开关；关闭时仅拒绝主动声明的密文请求。
func securityCryptoEnabled(svcCtx *svc.ServiceContext) bool {
	return securityConfigConfigured(svcCtx) && svcCtx.CurrentConfig().Security.SecretKey.CryptoStatus == 1
}
