package larkx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
)

const (
	defaultTimeoutSeconds = 5   // Lark HTTP 请求默认超时时间。
	defaultMaxErrorBytes  = 800 // 告警错误摘要默认最大字节数。
)

// Notifier 负责向 Lark 群机器人发送告警。
type Notifier struct {
	webhookURL   string           // Lark 机器人回调地址
	secret       string           // Lark 签名密钥
	atAll        bool             // 是否 @所有人
	maxErrorByte int              // 错误摘要最大字节数
	client       *http.Client     // HTTP 客户端
	now          func() time.Time // 当前时间函数，测试中可替换
}

// messagePayload 定义 Lark 群机器人消息请求体。
type messagePayload struct {
	Timestamp string          `json:"timestamp,omitempty"` // 秒级时间戳，启用签名时必填
	Sign      string          `json:"sign,omitempty"`      // Lark 签名值，启用签名时必填
	MsgType   string          `json:"msg_type"`            // 消息类型，支持 text 和 interactive
	Content   *messageContent `json:"content,omitempty"`   // text 消息正文
	Card      *messageCard    `json:"card,omitempty"`      // interactive 消息卡片
}

// messageContent 定义 Lark text 消息内容。
type messageContent struct {
	Text string `json:"text"` // 文本消息内容
}

// messageCard 定义 Lark interactive 卡片内容。
type messageCard struct {
	Config   messageCardConfig    `json:"config,omitempty"`   // 卡片展示配置
	Header   *messageCardHeader   `json:"header,omitempty"`   // 卡片标题
	Elements []messageCardElement `json:"elements,omitempty"` // 卡片正文元素
}

// messageCardConfig 定义 Lark 卡片展示配置。
type messageCardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"` // 是否启用宽屏模式
}

// messageCardHeader 定义 Lark 卡片头部。
type messageCardHeader struct {
	Template string          `json:"template,omitempty"` // 头部颜色模板
	Title    messageCardText `json:"title"`              // 头部标题
}

// messageCardElement 定义 Lark 卡片正文元素。
type messageCardElement struct {
	Tag      string             `json:"tag"`                // 元素类型
	Text     *messageCardText   `json:"text,omitempty"`     // 文本内容
	Fields   []messageCardField `json:"fields,omitempty"`   // 字段布局
	Elements []messageCardText  `json:"elements,omitempty"` // note 元素内容
}

// messageCardField 定义 Lark 卡片字段布局项。
type messageCardField struct {
	IsShort bool            `json:"is_short"` // 是否按短字段展示
	Text    messageCardText `json:"text"`     // 字段文本
}

// messageCardText 定义 Lark 卡片文本节点。
type messageCardText struct {
	Tag     string `json:"tag"`     // 文本类型，plain_text 或 lark_md
	Content string `json:"content"` // 文本内容
}

// responsePayload 定义 Lark 机器人响应字段。
type responsePayload struct {
	Code          *int   `json:"code,omitempty"`          // 小写响应业务码，0 表示成功
	Msg           string `json:"msg,omitempty"`           // 小写响应消息
	StatusCode    *int   `json:"StatusCode,omitempty"`    // 大写响应业务码，0 表示成功
	StatusMessage string `json:"StatusMessage,omitempty"` // 大写响应消息
}

// New 创建 Lark 告警器；未启用时返回 nil。
func New(cfg config.LarkAlertConfig) (*Notifier, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	webhookURL, err := resolveConfigValue(cfg.WebhookURL, cfg.WebhookURLRef)
	if err != nil {
		return nil, errors.Wrap(err, "读取 Lark webhook 配置失败")
	}
	if webhookURL == "" {
		return nil, errors.Errorf("缺少 Lark webhook_url 配置")
	}
	if err := validateWebhookURL(webhookURL); err != nil {
		return nil, errors.Tag(err)
	}
	secret, err := resolveConfigValue(cfg.Secret, cfg.SecretRef)
	if err != nil {
		return nil, errors.Wrap(err, "读取 Lark secret 配置失败")
	}
	maxErrorBytes := cfg.MaxErrorBytes
	if maxErrorBytes <= 0 {
		maxErrorBytes = defaultMaxErrorBytes
	}
	return &Notifier{
		webhookURL:   webhookURL,
		secret:       secret,
		atAll:        cfg.AtAll,
		maxErrorByte: maxErrorBytes,
		client:       &http.Client{Timeout: boundedTimeout(cfg.TimeoutSeconds)},
		now:          time.Now,
	}, nil
}

// SendText 发送一条通用文本消息。
func (n *Notifier) SendText(ctx context.Context, text string) error {
	if n == nil {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.Errorf("Lark 文本消息为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return n.sendText(ctx, text)
}

// sendText 复用 Lark 文本消息协议发送内容。
func (n *Notifier) sendText(ctx context.Context, text string) error {
	payload := messagePayload{
		MsgType: "text",
		Content: &messageContent{Text: text},
	}
	return n.sendPayload(ctx, payload)
}

// sendCard 复用 Lark interactive 消息协议发送卡片内容。
func (n *Notifier) sendCard(ctx context.Context, card messageCard) error {
	payload := messagePayload{
		MsgType: "interactive",
		Card:    &card,
	}
	return n.sendPayload(ctx, payload)
}

// sendPayload 统一补齐签名并发送 Lark webhook 请求。
func (n *Notifier) sendPayload(ctx context.Context, payload messagePayload) error {
	// 签名按发送时刻生成，复用卡片内容也不能复用旧时间戳。
	if n.secret != "" {
		timestamp := strconv.FormatInt(n.now().Unix(), 10)
		payload.Timestamp = timestamp
		payload.Sign = sign(timestamp, n.secret)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.Tag(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.Tag(err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	// 请求同时受调用方 context 和客户端总超时约束；发送结果不明确时交由上层决定是否重试。
	resp, err := n.client.Do(req)
	if err != nil {
		return errors.Tag(err)
	}
	defer resp.Body.Close()
	// 只保留 4 KiB 响应摘要，读取失败不能当作通知成功。
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return errors.Wrap(err, "读取 Lark 告警响应失败")
	}
	return checkResponse(resp.StatusCode, respBody)
}

// resolveConfigValue 读取唯一配置来源；明文值和文件引用同时出现时拒绝启动。
func resolveConfigValue(value string, ref string) (string, error) {
	if value != strings.TrimSpace(value) {
		return "", errors.Errorf("配置值不能包含首尾空白")
	}
	if ref != strings.TrimSpace(ref) {
		return "", errors.Errorf("配置引用路径不能包含首尾空白")
	}
	if value != "" && ref != "" {
		return "", errors.Errorf("配置值与引用路径只能提供一个")
	}
	if value != "" {
		return value, nil
	}
	if ref == "" {
		return "", nil
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		return "", errors.Tag(err)
	}
	return strings.TrimSpace(string(data)), nil
}

// boundedTimeout 返回受默认值和最大值保护的 HTTP 超时时间。
func boundedTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = defaultTimeoutSeconds
	}
	if seconds > config.MaxLarkAlertTimeoutSeconds {
		seconds = config.MaxLarkAlertTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// validateWebhookURL 在启动期校验 Lark webhook URL 的基础格式。
func validateWebhookURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.Wrap(err, "Lark webhook_url 非法")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.Errorf("Lark webhook_url 仅支持 http/https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return errors.Errorf("Lark webhook_url 缺少 host")
	}
	return nil
}

// sign 按 Lark 自定义机器人规则生成 HMAC-SHA256 签名。
func sign(timestamp string, secret string) string {
	// Lark 将时间戳和密钥组成 HMAC key，消息体本身不参与该签名。
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// checkResponse 同时校验 HTTP 状态码和 Lark 业务响应码。
func checkResponse(statusCode int, body []byte) error {
	bodyText := strings.TrimSpace(string(body))
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return errors.Errorf("Lark 告警发送失败 status=%d body=%s", statusCode, bodyText)
	}
	// 现有协议接受 2xx 空响应；非空响应必须可解码，且任一已返回的业务码非零都算失败。
	if bodyText == "" {
		return nil
	}
	var resp responsePayload
	if err := json.Unmarshal(body, &resp); err != nil {
		return errors.Wrapf(err, "解析 Lark 告警响应失败 body=%s", bodyText)
	}
	if resp.Code != nil && *resp.Code != 0 {
		return errors.Errorf("Lark 告警发送失败 code=%d msg=%s", *resp.Code, strings.TrimSpace(resp.Msg))
	}
	if resp.StatusCode != nil && *resp.StatusCode != 0 {
		return errors.Errorf("Lark 告警发送失败 status_code=%d msg=%s", *resp.StatusCode, strings.TrimSpace(resp.StatusMessage))
	}
	return nil
}
