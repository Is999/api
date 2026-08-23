package runtimefile

import (
	"os"
	"path/filepath"
	"testing"

	"api/internal/config"
)

// TestSectionSpecsValid 验证运行时配置分段注册完整且 key 集合同步。
func TestSectionSpecsValid(t *testing.T) {
	specs := sectionSpecs()
	if len(specs) == 0 {
		t.Fatal("runtime config section specs should not be empty")
	}
	keys := sectionKeys()
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.Key == "" {
			t.Fatal("runtime config section spec has empty key")
		}
		if spec.apply == nil {
			t.Fatalf("runtime config section %s missing apply function", spec.Key)
		}
		if _, ok := seen[spec.Key]; ok {
			t.Fatalf("runtime config section duplicate key=%s", spec.Key)
		}
		if _, ok := keys[spec.Key]; !ok {
			t.Fatalf("runtime config section key %s missing from key set", spec.Key)
		}
		seen[spec.Key] = struct{}{}
	}
}

// TestApplyRejectsNonCanonicalIncludePath 确保外部配置路径不会被 trim 后指向另一文件。
func TestApplyRejectsNonCanonicalIncludePath(t *testing.T) {
	cfg := config.Config{ConfigFiles: config.ConfigFilesConfig{Runtime: " runtime.yaml "}}
	if _, err := Apply(filepath.Join(t.TempDir(), "config.yaml"), &cfg); err == nil {
		t.Fatal("带首尾空白的 config_files.runtime 必须被拒绝")
	}
	if paths := IncludePaths("config.yaml", cfg.ConfigFiles); len(paths) != 0 {
		t.Fatalf("非规范 include path 不应进入指纹列表: %v", paths)
	}
}

// TestKnownContentRejectsUnknownSection 确保运行期配置拼写错误不会被静默过滤。
func TestKnownContentRejectsUnknownSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte("auth_typo: {}\n"), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	if _, _, _, err := validatedContent(path); err == nil {
		t.Fatal("未知运行期配置段必须返回错误")
	}
}

// TestKnownContentRejectsUnknownNestedField 确保外置配置的嵌套拼写错误也不会静默采用零值。
func TestKnownContentRejectsUnknownNestedField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte("auth:\n  profile_cache_ttl_second: 30\n"), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	if _, _, _, err := validatedContent(path); err == nil {
		t.Fatal("未知运行期嵌套字段必须返回错误")
	}
}

// TestKnownContentRejectsRemovedSecurityFields 确保外置安全配置也不能继续使用顶层单版本字段。
func TestKnownContentRejectsRemovedSecurityFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	body := []byte("security:\n  secret_key:\n    key_version: v1\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	if _, _, _, err := validatedContent(path); err == nil {
		t.Fatal("旧安全配置字段必须返回错误")
	}
}
