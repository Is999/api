package security

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestReadSecretFile 保留普通文件原始字节，并独立于调用方验证材料读取边界。
func TestReadSecretFile(t *testing.T) {
	for _, size := range []int{0, 32, maxSecretFileBytes, maxSecretFileBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			want := bytes.Repeat([]byte("x"), size)
			if err := os.WriteFile(path, want, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadSecretFile(path)
			if size > maxSecretFileBytes {
				if err == nil || !strings.Contains(err.Error(), "exceeds 65536 bytes") {
					t.Fatalf("超限文件错误 = %v", err)
				}
				return
			}
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("读取普通文件: bytes=%d want=%d err=%v", len(got), size, err)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "key")
		link := filepath.Join(dir, "key.link")
		if err := os.WriteFile(path, []byte(" key\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadSecretFile(link); err != nil || string(got) != " key\n" {
			t.Fatalf("符号链接应保留原始字节: got=%q err=%v", got, err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		if _, err := ReadSecretFile(t.TempDir()); err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("目录应被普通文件校验拒绝: %v", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		if _, err := ReadSecretFile(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
			t.Fatalf("保留调用方不存在文件判断: %v", err)
		}
	})
}
