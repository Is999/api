package svc

import (
	"context"
	"net/http/httptest"
	"testing"

	"api/internal/config"
	"api/internal/security"

	"gorm.io/gorm"
)

// TestSiteDatabasesLookupWithoutNamedDBs 验证未配置扩展库时直接查询空映射仍返回 nil。
func TestSiteDatabasesLookupWithoutNamedDBs(t *testing.T) {
	if got := (SiteDatabases{}).Lookup("archive"); got != nil {
		t.Fatal("未配置的命名库不应返回连接")
	}
}

// TestScopedWithContextCopiesConfigSnapshot 确保请求作用域固定创建时的配置快照。
func TestScopedWithContextCopiesConfigSnapshot(t *testing.T) {
	svcCtx := NewServiceContext(config.Config{AppID: "root"}, "root-version", Dependencies{})
	svcCtx.configValue.Store(config.Config{AppID: "request"})

	scoped := svcCtx.ScopedWithContext(context.Background())
	if scoped == nil {
		t.Fatal("ScopedWithContext() = nil")
	}
	if got := scoped.CurrentConfig().AppID; got != "request" {
		t.Fatalf("scoped AppID = %q, want request", got)
	}
}

// TestScopedWithContextSharesSecurityRegistry 确保请求作用域共享启动期密钥快照且不跟随配置热更新替换。
func TestScopedWithContextSharesSecurityRegistry(t *testing.T) {
	registry := security.NewKeyRegistry(security.KeyRoute{AppID: "site-a", StableVersion: "v1"}, map[string]security.KeyVersion{"v1": {}})
	svcCtx := NewServiceContext(config.Config{AppID: "site-a"}, "v1", Dependencies{SecurityKeys: registry})
	svcCtx.UpdateConfig(config.Config{AppID: "changed-site"})

	if svcCtx.SecurityKeys() != registry {
		t.Fatal("ServiceContext 未保留启动期密钥注册表")
	}
	scoped := svcCtx.ScopedWithContext(context.Background())
	if scoped == nil || scoped.SecurityKeys() != registry {
		t.Fatal("请求作用域未共享启动期密钥注册表")
	}
	var nilService *ServiceContext
	if nilService.SecurityKeys() != nil {
		t.Fatal("nil ServiceContext 不应返回密钥注册表")
	}
}

// TestClientIPHonorsExplicitTrustedProxies 验证只有显式可信代理才能提供转发客户端地址。
func TestClientIPHonorsExplicitTrustedProxies(t *testing.T) {
	svcCtx := NewServiceContext(config.Config{TrustedProxies: []string{"10.0.0.0/8"}}, "", Dependencies{})

	trustedRequest := httptest.NewRequest("GET", "/", nil)
	trustedRequest.RemoteAddr = "10.0.0.10:8080"
	trustedRequest.Header.Set("X-Forwarded-For", "203.0.113.9, 10.0.0.11")
	if got := svcCtx.ClientIP(trustedRequest); got != "203.0.113.9" {
		t.Fatalf("可信代理解析客户端 IP=%q，期望 203.0.113.9", got)
	}

	untrustedRequest := httptest.NewRequest("GET", "/", nil)
	untrustedRequest.RemoteAddr = "192.0.2.20:8080"
	untrustedRequest.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := svcCtx.ClientIP(untrustedRequest); got != "192.0.2.20" {
		t.Fatalf("非可信来源不应采用转发头，实际客户端 IP=%q", got)
	}
}

// TestClientIPHandlesForwardedHeaderVariants 锁定代理库升级后的多头顺序及 IPv4/IPv6 地址归一规则。
func TestClientIPHandlesForwardedHeaderVariants(t *testing.T) {
	tests := []struct {
		name    string   // 场景名标明转发链或地址表示边界。
		proxies []string // 启动期明确配置的可信代理范围。
		remote  string   // 与服务直接建连的对端地址。
		headers []string // 按 HTTP 接收顺序排列的同名转发头。
		want    string   // 从右向左越过可信代理后选出的客户端地址。
	}{
		{
			name: "multiple headers preserve chain order", proxies: []string{"10.0.0.0/8"},
			remote: "10.0.0.10:8080", headers: []string{"203.0.113.9", "198.51.100.20, 10.0.0.11"}, want: "198.51.100.20",
		},
		{
			name: "untrusted remote ignores all headers", proxies: []string{"10.0.0.0/8"},
			remote: "192.0.2.20:8080", headers: []string{"203.0.113.9", "198.51.100.20"}, want: "192.0.2.20",
		},
		{
			name: "mapped IPv4 prefix matches IPv4 remote", proxies: []string{"::ffff:10.0.0.0/104"},
			remote: "10.1.2.3:8080", headers: []string{"203.0.113.9"}, want: "203.0.113.9",
		},
		{
			name: "IPv6 zone does not change proxy trust", proxies: []string{"fe80::/10"},
			remote: "[fe80::1%en0]:8080", headers: []string{"203.0.113.9"}, want: "203.0.113.9",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svcCtx := NewServiceContext(config.Config{TrustedProxies: test.proxies}, "", Dependencies{})
			request := httptest.NewRequest("GET", "/", nil)
			request.RemoteAddr = test.remote
			// Add 保留重复头，Set 会覆盖前值并漏掉本次升级的链顺序差异。
			for _, header := range test.headers {
				request.Header.Add("X-Forwarded-For", header)
			}
			if got := svcCtx.ClientIP(request); got != test.want {
				t.Fatalf("ClientIP() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestSiteDatabasesLookupRequiresCanonicalName 确保空值、大小写和空白变体不会回退到主库。
func TestSiteDatabasesLookupRequiresCanonicalName(t *testing.T) {
	mainDB := &gorm.DB{}
	archiveDB := &gorm.DB{}
	databases := SiteDatabases{
		MainDB:   mainDB,
		NamedDBs: map[DBName]*gorm.DB{"archive": archiveDB},
	}
	if databases.Lookup(DatabaseMain) != mainDB || databases.Lookup("archive") != archiveDB {
		t.Fatal("规范数据库名称未命中已注册连接")
	}
	for _, name := range []DBName{"", "MAIN", " main ", "Archive"} {
		if databases.Lookup(name) != nil {
			t.Fatalf("非规范数据库名称 %q 不应命中连接", name)
		}
	}
}
