package runtimefile

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSourceFingerprintUsesReturnedBytes 验证同尺寸同时间戳改写也由内容摘要识别，而非仅依赖文件状态。
func TestReadSourceFingerprintUsesReturnedBytes(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	firstContent := []byte("name: first\n")
	if err := os.WriteFile(file, firstContent, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, firstFingerprint, err := ReadSource(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(firstContent) || !strings.HasSuffix(firstFingerprint, fmt.Sprintf("|%x", sha256.Sum256(firstBytes))) {
		t.Fatal("首次指纹必须由返回给解码器的同一份字节计算")
	}
	// 保留时间戳并写入等长内容，排除尺寸或 mtime 变化替内容摘要兜底。
	secondContent := []byte("name: third\n")
	if err = os.WriteFile(file, secondContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	secondBytes, secondFingerprint, err := ReadSource(file)
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint == secondFingerprint || string(secondBytes) != string(secondContent) {
		t.Fatal("同尺寸同时间戳的新内容必须产生新指纹")
	}
	if !strings.HasSuffix(secondFingerprint, fmt.Sprintf("|%x", sha256.Sum256(secondBytes))) {
		t.Fatal("第二次指纹不能复用先前读取的内容")
	}
}
