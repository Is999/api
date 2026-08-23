package security

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Is999/go-utils/errors"
)

// securityVectorFile 对应前后端共享安全向量文件的顶层契约。
type securityVectorFile struct {
	Version             int                          `json:"version"`             // 固定为当前签名串协议版本 2。
	SignVectors         []securitySignVector         `json:"signVectors"`         // 固定拼接结果供不同语言实现交叉核对。
	CipherHeaderVectors []securityCipherHeaderVector `json:"cipherHeaderVectors"` // 覆盖字段列表到 Base64 请求头的编码边界。
	FieldLimitVectors   []securityFieldLimitVector   `json:"fieldLimitVectors"`   // 覆盖合法字段数量与超限拒绝。
}

// securitySignVector 固定一组签名输入及其协议结果。
type securitySignVector struct {
	Name      string         `json:"name"`      // 失败时定位共享文件中的协议用例。
	AppID     string         `json:"appID"`     // 参与拼接的应用身份，不执行运行期配置校验。
	TraceID   string         `json:"traceID"`   // 固定追踪标识，用于复算签名串。
	Timestamp string         `json:"timestamp"` // Unix 秒数字字符串，不经过浮点转换。
	Fields    []string       `json:"fields"`    // 空列表仅签基础头，星号按生产规则挑选顶层业务字段。
	Data      map[string]any `json:"data"`      // 保留 JSON Number，避免大整数先在夹具中失真。
	Expected  string         `json:"expected"`  // 长度前缀格式的明文签名串，不是密码学签名值。
}

// securityCipherHeaderVector 固定密文字段列表及其请求头编码结果。
type securityCipherHeaderVector struct {
	Name     string   `json:"name"`     // 对应共享文件中的字段头编码场景。
	Fields   []string `json:"fields"`   // 待编码的完整字段列表，非法项不能静默裁剪。
	Expected string   `json:"expected"` // Base64 编码结果；空串表示拒绝生成请求头。
}

// securityFieldLimitVector 固定安全字段数量边界及拒绝预期。
type securityFieldLimitVector struct {
	Name         string   `json:"name"`         // 对应共享文件中的字段数量边界。
	Fields       []string `json:"fields"`       // 原样交给生产校验器，不预先去重或 trim。
	ShouldReject bool     `json:"shouldReject"` // 为 true 时必须返回统一载荷超限哨兵错误。
}

// TestSecurityVectorsBuildSignString 固定前后端共享的签名串拼接样例。
func TestSecurityVectorsBuildSignString(t *testing.T) {
	vectors := loadSecurityVectors(t)
	for _, vector := range vectors.SignVectors {
		t.Run(vector.Name, func(t *testing.T) {
			got := BuildSignString(vector.Data, vector.Fields, vector.TraceID, vector.Timestamp, vector.AppID)
			if got != vector.Expected {
				t.Fatalf("BuildSignString() = %q, want %q", got, vector.Expected)
			}
		})
	}
}

// TestSecurityVectorsEncodeCipherParams 固定 X-Cipher 字段编码样例。
func TestSecurityVectorsEncodeCipherParams(t *testing.T) {
	vectors := loadSecurityVectors(t)
	for _, vector := range vectors.CipherHeaderVectors {
		t.Run(vector.Name, func(t *testing.T) {
			got := EncodeCipherParams(vector.Fields)
			if got != vector.Expected {
				t.Fatalf("EncodeCipherParams() = %q, want %q", got, vector.Expected)
			}
		})
	}
}

// TestSecurityVectorsFieldLimits 固定字段级安全处理数量边界。
func TestSecurityVectorsFieldLimits(t *testing.T) {
	vectors := loadSecurityVectors(t)
	for _, vector := range vectors.FieldLimitVectors {
		t.Run(vector.Name, func(t *testing.T) {
			err := ValidateSecurityFieldCount(vector.Fields, "security vector")
			if vector.ShouldReject && !errors.Is(err, ErrSecurityPayloadTooLarge) {
				t.Fatalf("ValidateSecurityFieldCount() error = %v, want ErrSecurityPayloadTooLarge", err)
			}
			if !vector.ShouldReject && err != nil {
				t.Fatalf("ValidateSecurityFieldCount() error = %v", err)
			}
		})
	}
}

// loadSecurityVectors 按保留数字精度的方式读取并校验第二版共享向量。
func loadSecurityVectors(t *testing.T) securityVectorFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "security_vectors.json"))
	if err != nil {
		t.Fatalf("read security vectors: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var vectors securityVectorFile
	if err := decoder.Decode(&vectors); err != nil {
		t.Fatalf("decode security vectors: %v", err)
	}
	if vectors.Version != 2 {
		t.Fatalf("security vectors version = %d, want 2", vectors.Version)
	}
	return vectors
}
