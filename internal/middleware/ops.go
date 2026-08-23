package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	codes "api/common/codes"
	i18n "api/common/i18n"
	keys "api/common/rediskeys"
	"api/internal/config"
	"api/internal/httpresp"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// 运维接口鉴权请求头常量。
const (
	// HeaderOpsToken 表示运维级接口保护令牌请求头。
	HeaderOpsToken = "X-Ops-Token"
	// HeaderOpsTimestamp 表示运维请求签名时间戳，取 Unix 秒。
	HeaderOpsTimestamp = "X-Ops-Timestamp"
	// HeaderOpsNonce 表示单次运维请求的 16 字节随机数十六进制值。
	HeaderOpsNonce = "X-Ops-Nonce"
	// HeaderOpsBodySHA256 表示运维请求体 SHA256 摘要。
	HeaderOpsBodySHA256 = "X-Ops-Body-SHA256"
	// HeaderOpsSignature 表示运维请求 HMAC-SHA256 签名。
	HeaderOpsSignature = "X-Ops-Signature"
	// opsNonceBytes 表示 nonce 解码后的固定字节数。
	opsNonceBytes = 16
	// opsSignatureBodyMaxBytes 限制参与验签的请求体大小，避免内网接口被异常大包拖垮。
	opsSignatureBodyMaxBytes = 1 << 20
	// opsSignatureWindow 表示运维请求签名允许的时间偏差。
	opsSignatureWindow = 5 * time.Minute
)

// opsRequestAuth 保存验签后写入防重放缓存所需的最小信息。
type opsRequestAuth struct {
	Nonce string        // 规范化后的十六进制 nonce
	TTL   time.Duration // nonce 覆盖当前签名剩余有效期的 TTL
}

// OpsMiddleware 保护配置热加载、运行态同步等运维级接口。
type OpsMiddleware struct {
	svc *svc.ServiceContext // 运维保护依赖的服务上下文和 Redis
}

// NewOpsMiddleware 创建运维保护中间件实例。
func NewOpsMiddleware(svcCtx *svc.ServiceContext) *OpsMiddleware {
	return &OpsMiddleware{svc: svcCtx}
}

// Handle 校验来源、令牌和 HMAC，并使用 Redis 原子拒绝重放请求。
func (m *OpsMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := config.OpsConfig{}
		var svcCtx *svc.ServiceContext
		if m != nil && m.svc != nil {
			svcCtx = m.svc
			cfg = svcCtx.CurrentConfig().Ops
		}
		auth, err := authenticateOpsWithClientIP(r, cfg, requestClientIP(svcCtx, r), time.Now())
		if err != nil {
			writeOpsFailure(w, r, http.StatusForbidden, codes.Forbidden, i18n.MsgKeyForbidden, err)
			return
		}
		// 先验签再占用 nonce，未认证请求不能消耗合法请求的防重放标记；Redis 故障不放行。
		replayed, err := m.markOpsNonce(r, auth)
		if err != nil {
			writeOpsFailure(w, r, http.StatusServiceUnavailable, codes.ServiceBusy, i18n.MsgKeyServiceBusy, err)
			return
		}
		if replayed {
			writeOpsFailure(w, r, http.StatusForbidden, codes.Forbidden, i18n.MsgKeyForbidden, errors.New("运维接口请求已重放"))
			return
		}
		next(w, r)
	}
}

// writeOpsFailure 输出统一运维鉴权失败响应，错误详情只保留在服务端错误链。
func writeOpsFailure(w http.ResponseWriter, r *http.Request, httpStatus int, code int, messageKey string, err error) {
	httpresp.NewJSONResp(r.Context(), w).
		SetHTTPStatus(httpStatus).
		SetCode(code).
		SetError(err).
		Fail(messageKey)
}

// authenticateOpsWithClientIP 校验运维接口访问边界并返回防重放信息。
func authenticateOpsWithClientIP(r *http.Request, cfg config.OpsConfig, clientIP string, now time.Time) (opsRequestAuth, error) {
	// 先用令牌与内网边界拒绝无权请求，再读取请求体计算摘要。
	token := cfg.ConfigReloadToken
	if token == "" {
		return opsRequestAuth{}, errors.Errorf("运维接口令牌未配置")
	}
	got := r.Header.Get(HeaderOpsToken)
	if got == "" {
		return opsRequestAuth{}, errors.Errorf("缺少请求头%s", HeaderOpsToken)
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		return opsRequestAuth{}, errors.Errorf("运维接口令牌不匹配")
	}
	if forwardedHeaderHasPublicAddr(r) {
		return opsRequestAuth{}, errors.Errorf("运维接口转发来源包含公网IP")
	}
	if !clientIPAllowed(clientIP, cfg.ConfigReloadAllowedIPs) {
		return opsRequestAuth{}, errors.Errorf("运维接口来源IP非内网或未命中白名单")
	}
	auth, err := validateOpsSignature(r, token, now)
	if err != nil {
		return opsRequestAuth{}, errors.Tag(err)
	}
	return auth, nil
}

// validateOpsSignature 校验运维请求签名，并恢复请求体给后续 handler 使用。
func validateOpsSignature(r *http.Request, token string, now time.Time) (opsRequestAuth, error) {
	timestamp := r.Header.Get(HeaderOpsTimestamp)
	if timestamp == "" {
		return opsRequestAuth{}, errors.Errorf("缺少请求头%s", HeaderOpsTimestamp)
	}
	// 时间窗先于请求体读取校验，使过期请求尽早失败。
	signedAt, err := validateOpsTimestamp(timestamp, now)
	if err != nil {
		return opsRequestAuth{}, errors.Tag(err)
	}
	nonce, err := validateOpsNonce(r.Header.Get(HeaderOpsNonce))
	if err != nil {
		return opsRequestAuth{}, errors.Tag(err)
	}
	// 请求体只读取一次并恢复，摘要与 handler 消费同一字节序列。
	body, err := readOpsSignedBody(r)
	if err != nil {
		return opsRequestAuth{}, errors.Tag(err)
	}
	bodyHash := opsBodySHA256(body)
	gotBodyHash := r.Header.Get(HeaderOpsBodySHA256)
	if gotBodyHash == "" {
		return opsRequestAuth{}, errors.Errorf("缺少请求头%s", HeaderOpsBodySHA256)
	}
	if len(gotBodyHash) != sha256.Size*2 || gotBodyHash != strings.ToLower(gotBodyHash) || strings.TrimSpace(gotBodyHash) != gotBodyHash {
		return opsRequestAuth{}, errors.Errorf("请求头%s必须是64位小写十六进制", HeaderOpsBodySHA256)
	}
	if _, err := hex.DecodeString(gotBodyHash); err != nil {
		return opsRequestAuth{}, errors.Errorf("请求头%s必须是64位小写十六进制", HeaderOpsBodySHA256)
	}
	if subtle.ConstantTimeCompare([]byte(gotBodyHash), []byte(bodyHash)) != 1 {
		return opsRequestAuth{}, errors.Errorf("运维接口请求体摘要不匹配")
	}
	gotSignature := r.Header.Get(HeaderOpsSignature)
	if gotSignature == "" {
		return opsRequestAuth{}, errors.Errorf("缺少请求头%s", HeaderOpsSignature)
	}
	if len(gotSignature) != sha256.Size*2 || gotSignature != strings.ToLower(gotSignature) || strings.TrimSpace(gotSignature) != gotSignature {
		return opsRequestAuth{}, errors.Errorf("请求头%s必须是64位小写十六进制", HeaderOpsSignature)
	}
	if _, err := hex.DecodeString(gotSignature); err != nil {
		return opsRequestAuth{}, errors.Errorf("请求头%s必须是64位小写十六进制", HeaderOpsSignature)
	}
	// 签名覆盖原始 RequestURI，查询串顺序和编码均不可改写。
	expectedSignature := signOpsRequest(token, r.Method, r.URL.RequestURI(), timestamp, nonce, bodyHash)
	if !opsSignatureEqual(gotSignature, expectedSignature) {
		return opsRequestAuth{}, errors.Errorf("运维接口签名不匹配")
	}
	// nonce 仅保存签名剩余有效期，窗口结束后即可回收。
	ttl := signedAt.Add(opsSignatureWindow).Sub(now)
	if ttl <= 0 {
		return opsRequestAuth{}, errors.Errorf("运维接口签名已过期")
	}
	return opsRequestAuth{Nonce: nonce, TTL: ttl}, nil
}

// validateOpsTimestamp 校验签名时间戳并返回签名时间。
func validateOpsTimestamp(raw string, now time.Time) (time.Time, error) {
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, errors.Wrap(err, "运维接口签名时间戳非法")
	}
	if raw != strconv.FormatInt(seconds, 10) {
		return time.Time{}, errors.Errorf("运维接口签名时间戳必须是规范十进制秒")
	}
	nowSeconds := now.Unix()
	windowSeconds := int64(opsSignatureWindow / time.Second)
	if seconds < nowSeconds-windowSeconds || seconds > nowSeconds+windowSeconds {
		return time.Time{}, errors.Errorf("运维接口签名已过期")
	}
	signedAt := time.Unix(seconds, 0)
	return signedAt, nil
}

// validateOpsNonce 校验 nonce 为 16 字节随机值的规范小写十六进制编码。
func validateOpsNonce(raw string) (string, error) {
	if raw == "" {
		return "", errors.Errorf("缺少请求头%s", HeaderOpsNonce)
	}
	if raw != strings.TrimSpace(raw) || raw != strings.ToLower(raw) {
		return "", errors.Errorf("请求头%s非法", HeaderOpsNonce)
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != opsNonceBytes {
		return "", errors.Errorf("请求头%s非法", HeaderOpsNonce)
	}
	return raw, nil
}

// markOpsNonce 使用 Redis SET NX 原子占用 nonce；true 表示 nonce 已存在。
func (m *OpsMiddleware) markOpsNonce(r *http.Request, auth opsRequestAuth) (bool, error) {
	if m == nil || m.svc == nil || m.svc.Rds == nil {
		return false, errors.New("运维防重放缓存未初始化")
	}
	key := keys.OpsReplayNonceRedisKey(auth.Nonce)
	if key == "" {
		return false, errors.New("运维防重放缓存 key 为空")
	}
	ok, err := m.svc.Rds.SetNX(r.Context(), key, "1", auth.TTL).Result()
	if err != nil {
		return false, errors.Wrap(err, "写入运维防重放缓存失败")
	}
	return !ok, nil
}

// readOpsSignedBody 读取参与签名的请求体并重置 Body，保证后续业务仍可读取。
func readOpsSignedBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, opsSignatureBodyMaxBytes+1))
	if err != nil {
		return nil, errors.Wrap(err, "读取运维接口请求体失败")
	}
	if len(body) > opsSignatureBodyMaxBytes {
		return nil, errors.Errorf("运维接口请求体超过大小限制")
	}
	if len(bytes.TrimSpace(body)) > 0 {
		contentType, parseErr := requestMediaType(r)
		if parseErr != nil {
			return nil, errors.Tag(parseErr)
		}
		if contentType != "application/json" {
			return nil, errors.Errorf("运维接口非空请求体 Content-Type 必须为 application/json")
		}
		// 与公开安全链保持相同解析契约，避免合法大小写变体通过验签后被 go-zero 当成空请求体。
		r.Header.Set("Content-Type", "application/json")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	// 验签已完整缓冲 chunked/未知长度请求体，下游 httpx.Parse 依赖正数 ContentLength 才会解析 JSON。
	r.ContentLength = int64(len(body))
	return body, nil
}

// opsBodySHA256 返回请求体 SHA256 十六进制摘要。
func opsBodySHA256(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// signOpsRequest 按 method、URI、timestamp、nonce、bodyHash 生成 HMAC-SHA256 签名。
func signOpsRequest(secret string, method string, requestURI string, timestamp string, nonce string, bodyHash string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strings.Join([]string{
		strings.ToUpper(strings.TrimSpace(method)),
		requestURI,
		timestamp,
		nonce,
		bodyHash,
	}, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

// opsSignatureEqual 以常量时间比较十六进制签名。
func opsSignatureEqual(got string, expected string) bool {
	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	expectedBytes, err := hex.DecodeString(expected)
	if err != nil {
		return false
	}
	return hmac.Equal(gotBytes, expectedBytes)
}

// clientIPAllowed 校验客户端 IP 是否来自内网，并按白名单进一步收窄。
func clientIPAllowed(clientIP string, allowed []string) bool {
	addr, ok := parseAddrValue(clientIP)
	if !ok || !isInternalClientAddr(addr) {
		return false
	}
	if len(allowed) == 0 {
		// 空白名单只省略进一步收窄，前面的私网/回环限制仍然生效。
		return true
	}
	for _, item := range allowed {
		if strings.Contains(item, "/") {
			prefix, err := netip.ParsePrefix(item)
			if err == nil && prefix.Contains(addr) {
				return true
			}
			continue
		}
		if allowedAddr, ok := parseAddrValue(item); ok && allowedAddr == addr {
			return true
		}
		if ip := net.ParseIP(item); ip != nil && ip.String() == addr.String() {
			return true
		}
	}
	return false
}

// forwardedHeaderHasPublicAddr 拦截反代转发头中的公网真实来源。
func forwardedHeaderHasPublicAddr(r *http.Request) bool {
	if r == nil {
		return false
	}
	// 同名头可以分行传输，逐值检查才能拦截后续行中的公网来源。
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		for _, value := range r.Header.Values(header) {
			for item := range strings.SplitSeq(value, ",") {
				addr, ok := parseAddrValue(item)
				if ok && !isInternalClientAddr(addr) {
					return true
				}
			}
		}
	}
	return false
}

// isInternalClientAddr 判断来源地址是否属于本机或私有地址。
func isInternalClientAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate()
}

// parseAddrValue 解析 IP、host:port 或 [IPv6]:port 形式的地址。
func parseAddrValue(raw string) (netip.Addr, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, false
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	} else {
		raw = strings.Trim(raw, "[]")
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
