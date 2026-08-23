package security

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// TestEncodeCipherParams 确保密文字段头只编码规范字段列表，全量或非法字段拒绝生成协议头。
func TestEncodeCipherParams(t *testing.T) {
	if got := EncodeCipherParams([]string{CipherWholeBody}); got != "" {
		t.Fatalf("EncodeCipherParams whole body = %q, want empty", got)
	}

	if got := EncodeCipherParams([]string{"token", " token ", "", "user.email"}); got != "" {
		t.Fatalf("EncodeCipherParams non-canonical fields = %q, want empty", got)
	}

	got := EncodeCipherParams([]string{"token", "user.email"})
	body, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	var params []string
	if err := json.Unmarshal(body, &params); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	want := []string{"token", "user.email"}
	if len(params) != len(want) {
		t.Fatalf("params len = %d, want %d", len(params), len(want))
	}
	for i := range want {
		if params[i] != want[i] {
			t.Fatalf("params[%d] = %q, want %q", i, params[i], want[i])
		}
	}
}
