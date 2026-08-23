package secureid

import (
	"errors"
	"strings"
	"testing"
)

// TestNewHexReturnsExpectedLength 确保随机标识长度与调用方声明的熵字节数一致。
func TestNewHexReturnsExpectedLength(t *testing.T) {
	value, err := NewHex(16)
	if err != nil {
		t.Fatalf("NewHex() error = %v", err)
	}
	if len(value) != 32 {
		t.Fatalf("NewHex() length = %d, want 32", len(value))
	}
}

// TestNewHexPropagatesEntropyFailure 确保系统随机源不可用时返回错误，不生成可预测标识。
func TestNewHexPropagatesEntropyFailure(t *testing.T) {
	if value, err := newHex(failingReader{}, 16); err == nil || value != "" || !strings.Contains(err.Error(), "安全随机源") {
		t.Fatalf("newHex() value=%q error=%v", value, err)
	}
}

// TestNewHexRejectsShortEntropy 验证不足约定长度的随机输入不会被编码成可用标识。
func TestNewHexRejectsShortEntropy(t *testing.T) {
	if value, err := newHex(strings.NewReader("short"), 16); err == nil || value != "" {
		t.Fatalf("newHex() value=%q error=%v, want empty value and short-read error", value, err)
	}
}

// failingReader 模拟操作系统随机源故障。
type failingReader struct{}

// Read 始终返回故障，覆盖安全标识生成的极端运行条件。
func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}
