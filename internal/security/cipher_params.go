package security

import (
	"encoding/base64"
	"encoding/json"
)

const (
	// CipherWholeBody 表示禁用的整包加密标记，仅用于识别并拒绝非法输入。
	CipherWholeBody = "cipher"
	// CipherJSONPrefix 表示字段值在加解密前需要按 JSON 编解码。
	CipherJSONPrefix = "json:"
)

// EncodeCipherParams 把字段级加密配置编码成请求头值；整包加密标记不再生成请求头。
func EncodeCipherParams(params []string) string {
	if len(params) == 0 {
		return ""
	}
	if err := ValidateSecurityFieldCount(params, "加密"); err != nil {
		return ""
	}
	for _, param := range params {
		if param == CipherWholeBody {
			return ""
		}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(body)
}
