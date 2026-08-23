package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestDockerContextExcludesLocalSecrets 固定敏感路径的忽略规则不得被删除或后续反选覆盖。
func TestDockerContextExcludesLocalSecrets(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	patterns := make([]string, 0)
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "!") {
			t.Fatal("构建忽略规则不得反选恢复本地秘密")
		}
		patterns = append(patterns, line)
	}
	for _, path := range []string{"etc/config.yaml", "etc/keys", "etc/config.d/runtime.yaml", ".env", ".env.*", "*.pem", "*.key", "data", ".git"} {
		if !slices.Contains(patterns, path) {
			t.Errorf("Docker 上下文未排除 %s", path)
		}
	}
}
