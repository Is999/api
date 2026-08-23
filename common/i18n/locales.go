package i18n

import (
	"strings"

	"golang.org/x/text/language"
)

// 支持的响应语言标签。
const (
	// LocaleZHCN 表示简体中文。
	LocaleZHCN = "zh-CN"
	// LocaleENUS 表示美式英文。
	LocaleENUS = "en-US"
)

// supportedLocales 表示后端响应文案当前维护的语种。
var supportedLocales = []string{LocaleZHCN, LocaleENUS}

// NormalizeLocale 归一化请求语言，未知语言默认中文。
func NormalizeLocale(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return LocaleZHCN
	}
	// 先按浏览器 q 权重解析，再选首个受支持语种。
	tags, _, err := language.ParseAcceptLanguage(locale)
	if err != nil || len(tags) == 0 {
		tag, parseErr := language.Parse(locale)
		if parseErr != nil {
			return LocaleZHCN
		}
		tags = []language.Tag{tag}
	}
	for _, tag := range tags {
		if locale := supportedLocale(tag); locale != "" {
			return locale
		}
	}
	// 请求只包含未维护语种时仍使用项目默认中文。
	return LocaleZHCN
}

// supportedLocale 按基础语种选择资源，地区和书写系统变体共用当前文案。
func supportedLocale(tag language.Tag) string {
	base, _ := tag.Base()
	switch base.String() {
	case "en":
		return LocaleENUS
	case "zh":
		return LocaleZHCN
	default:
		return ""
	}
}
