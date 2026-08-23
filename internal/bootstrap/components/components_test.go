package components

import (
	"testing"

	"api/internal/config"
	"api/internal/infra/collectorx"
	"api/internal/svc"

	"gorm.io/gorm"
)

// TestNewRegistryIncludesCoreDependencies 确保核心依赖进入组件生命周期清单。
func TestNewRegistryIncludesCoreDependencies(t *testing.T) {
	// 构造主库、两个命名扩展库和 Collector，覆盖动态组件清单。
	cfg := config.Config{Collector: config.CollectorConfig{
		Enabled: true,
		Kafka:   config.CollectorKafkaConfig{Brokers: []string{"127.0.0.1:9092"}},
		Tasks: map[string]config.CollectorTaskConfig{
			config.CollectorBizTypeAuthSecurity: {Topic: config.CollectorTopicAuthSecurity},
		},
	}}
	svcCtx := svc.NewServiceContext(cfg, "test-version", svc.Dependencies{
		SiteDBs: svc.SiteDatabases{
			NamedDBs: map[svc.DBName]*gorm.DB{
				svc.DBName("user"):    nil,
				svc.DBName("archive"): nil,
			},
		},
	})
	collector, err := collectorx.New(cfg.Collector)
	if err != nil {
		t.Fatalf("collectorx.New() error = %v", err)
	}
	svcCtx.Collector = collector
	registry, err := NewRegistry(svcCtx)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	// 组件必须齐全，命名扩展库还需按名称保持稳定顺序。
	got := componentItemNames(registry.Items())
	indexByName := make(map[string]int, len(got))
	for index, name := range got {
		indexByName[name] = index
	}
	for _, name := range []string{nameMySQL, "mysql_archive", "mysql_user", nameRedis, nameSnowflake, nameCollector} {
		if _, ok := indexByName[name]; !ok {
			t.Fatalf("组件生命周期清单缺少核心依赖 %q，实际为 %v", name, got)
		}
	}
	if indexByName["mysql_archive"] > indexByName["mysql_user"] {
		t.Fatalf("命名扩展库组件应按名称稳定排序，实际为 %v", got)
	}
}

// TestDefaultSpecsValid 确保组件生命周期来源规格完整且名称唯一。
func TestDefaultSpecsValid(t *testing.T) {
	specs := DefaultSpecs()
	if len(specs) == 0 {
		t.Fatal("默认组件规格不能为空")
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.build == nil {
			t.Fatalf("默认组件规格缺少构造函数: %s", spec.Name)
		}
		if spec.File == "" || spec.Method == "" || spec.Description == "" {
			t.Fatalf("默认组件规格清单字段不完整: %+v", spec)
		}
		if _, ok := seen[spec.Name]; ok {
			t.Fatalf("默认组件规格名称重复: %s", spec.Name)
		}
		seen[spec.Name] = struct{}{}
	}
}

// componentItemNames 保留组件注册顺序提取名称，便于校验生命周期顺序。
func componentItemNames(items []svc.Component) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Name)
	}
	return names
}
