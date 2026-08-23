package validators

import (
	"strings"
	"testing"

	"api/internal/config"
)

// TestValidateKnownYAMLFieldsRejectsUnknownNestedField 确保嵌套拼写错误不会被 go-zero 宽松解码忽略。
func TestValidateKnownYAMLFieldsRejectsUnknownNestedField(t *testing.T) {
	data := []byte("auth:\n  login_rate_limt:\n    enabled: true\n")
	err := ValidateKnownYAMLFields(data, config.Config{})
	if err == nil || !strings.Contains(err.Error(), "auth.login_rate_limt") {
		t.Fatalf("期望未知嵌套字段被拒绝，实际为 %v", err)
	}
}

// TestValidateKnownYAMLFieldsRejectsUnknownDynamicMapValueField 确保动态业务 key 下的配置值仍按结构校验。
func TestValidateKnownYAMLFieldsRejectsUnknownDynamicMapValueField(t *testing.T) {
	data := []byte("collector:\n  tasks:\n    auth.security:\n      topik: events\n")
	err := ValidateKnownYAMLFields(data, config.Config{})
	if err == nil || !strings.Contains(err.Error(), "collector.tasks.auth.security.topik") {
		t.Fatalf("期望动态 map 值中的未知字段被拒绝，实际为 %v", err)
	}
}

// TestValidateKnownYAMLFieldsAcceptsCanonicalDynamicMap 确保命名库与 Collector 业务 key 不被误判成字段。
func TestValidateKnownYAMLFieldsAcceptsCanonicalDynamicMap(t *testing.T) {
	data := []byte("site_mysql:\n  archive:\n    write_data_source: dsn\n    max_open_conns: 10\ncollector:\n  tasks:\n    auth.security:\n      topic: events\n")
	if err := ValidateKnownYAMLFields(data, config.Config{}); err != nil {
		t.Fatalf("规范动态 map 配置不应被拒绝: %v", err)
	}
}

// TestValidateKnownYAMLFieldsRejectsDuplicateAndNonCanonicalKeys 确保重复字段和首尾空白字段没有覆盖顺序。
func TestValidateKnownYAMLFieldsRejectsDuplicateAndNonCanonicalKeys(t *testing.T) {
	for _, data := range [][]byte{
		[]byte("app_id: one\napp_id: two\n"),
		[]byte("\" app_id\": one\n"),
	} {
		if err := ValidateKnownYAMLFields(data, config.Config{}); err == nil {
			t.Fatalf("期望重复或非规范字段被拒绝: %s", data)
		}
	}
}

// TestValidateKnownYAMLFieldsUsesProjectLogFieldNames 固定项目对第三方日志结构采用的唯一键名。
func TestValidateKnownYAMLFieldsUsesProjectLogFieldNames(t *testing.T) {
	canonical := []byte("Name: api\nlog:\n  level: info\n  max_backups: 7\n")
	if err := ValidateKnownYAMLFields(canonical, config.Config{}); err != nil {
		t.Fatalf("ValidateKnownYAMLFields() error = %v", err)
	}
	for _, invalid := range [][]byte{
		[]byte("Name: api\nLog:\n  level: info\n"),
		[]byte("Name: api\nlog:\n  Level: info\n"),
	} {
		if err := ValidateKnownYAMLFields(invalid, config.Config{}); err == nil {
			t.Fatalf("ValidateKnownYAMLFields() expected non-canonical log key error for %q", invalid)
		}
	}
}

// TestValidateKnownYAMLFieldsRejectsDerivedEnvironment 确保观测环境只能由顶层 Mode 派生。
func TestValidateKnownYAMLFieldsRejectsDerivedEnvironment(t *testing.T) {
	data := []byte("observability:\n  environment: pro\n")
	if err := ValidateKnownYAMLFields(data, config.Config{}); err == nil {
		t.Fatal("expected derived observability.environment field error")
	}
}
