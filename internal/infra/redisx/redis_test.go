package redisx

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"api/internal/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestNewClusterUsesIPv6HostMap 验证启动预探测和正式节点连接均保留 IPv6 映射的原端口。
func TestNewClusterUsesIPv6HostMap(t *testing.T) {
	// miniredis 只提供本机 IPv6 与单节点槽位协议，不代表多节点集群验证。
	server := miniredis.NewMiniRedis()
	if err := server.StartAddr("[::1]:0"); err != nil {
		t.Fatalf("启动 IPv6 Redis 测试节点失败: %v", err)
	}
	t.Cleanup(server.Close)
	server.Set("probe", "value")

	_, port, err := net.SplitHostPort(server.Addr())
	if err != nil {
		t.Fatalf("解析测试节点地址失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	client, err := New(ctx, config.RedisConfig{
		Type:     "cluster",
		Addrs:    []string{net.JoinHostPort("redis-1", port)},
		AddrMap:  map[string]string{"redis-1": "::1", "::1": "::1"},
		PoolSize: 1,
	}, config.ObservabilityConfig{})
	if err != nil {
		t.Fatalf("通过 IPv6 主机映射创建 Cluster 客户端失败: %v", err)
	}
	defer client.Close()

	// 读取槽位数据使正式客户端访问发现的节点，不能只验证种子 PING。
	if got, err := client.Get(ctx, "probe").Result(); err != nil || got != "value" {
		t.Fatalf("读取映射后的节点数据=%q, error=%v", got, err)
	}
}

// TestRewriteClusterAddr 验证精确映射优先，主机映射保留端口并正确格式化 IPv6。
func TestRewriteClusterAddr(t *testing.T) {
	tests := []struct {
		name    string            // 区分目标地址格式和映射优先级。
		addr    string            // Redis 拓扑返回的节点地址。
		addrMap map[string]string // 当前配置的完整地址或主机映射。
		want    string            // 最终拨号地址，裸 IPv6 必须补原端口。
	}{
		{name: "no map", addr: "redis-1:7001", want: "redis-1:7001"},
		{name: "unmatched", addr: "redis-2:7002", addrMap: map[string]string{"redis-1": "127.0.0.1"}, want: "redis-2:7002"},
		{name: "invalid source", addr: "redis-1", addrMap: map[string]string{"redis-1": ""}, want: "redis-1"},
		{name: "empty host mapping", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": ""}, want: "redis-1:7001"},
		{name: "hostname", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "localhost"}, want: "localhost:7001"},
		{name: "ipv4", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "127.0.0.1"}, want: "127.0.0.1:7001"},
		{name: "hostname endpoint", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "localhost:8001"}, want: "localhost:8001"},
		{name: "ipv4 endpoint", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "127.0.0.1:8001"}, want: "127.0.0.1:8001"},
		{name: "bare ipv6", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "::1"}, want: "[::1]:7001"},
		{name: "bare ipv6 zone", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "fe80::1%en0"}, want: "[fe80::1%en0]:7001"},
		{name: "ipv6 endpoint", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "[::1]:8001"}, want: "[::1]:8001"},
		{name: "ipv6 zone endpoint", addr: "redis-1:7001", addrMap: map[string]string{"redis-1": "[fe80::1%en0]:8001"}, want: "[fe80::1%en0]:8001"},
		{name: "ipv6 source host", addr: "[2001:db8::1]:7001", addrMap: map[string]string{"2001:db8::1": "::1"}, want: "[::1]:7001"},
		{name: "exact address first", addr: "redis-1:7001", addrMap: map[string]string{"redis-1:7001": "[::1]:8001", "redis-1": "localhost"}, want: "[::1]:8001"},
		{name: "empty exact falls through", addr: "redis-1:7001", addrMap: map[string]string{"redis-1:7001": "", "redis-1": "localhost"}, want: "localhost:7001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rewriteClusterAddr(tt.addr, tt.addrMap); got != tt.want {
				t.Fatalf("rewriteClusterAddr() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestNewEnablesContextTimeout 验证客户端启用 deadline 选项，不模拟超时或真实网络故障。
func TestNewEnablesContextTimeout(t *testing.T) {
	server := miniredis.RunT(t)
	client, err := New(context.Background(), config.RedisConfig{
		Type:     "single",
		Addrs:    []string{server.Addr()},
		PoolSize: 1,
	}, config.ObservabilityConfig{})
	if err != nil {
		t.Fatalf("创建 Redis 客户端失败: %v", err)
	}
	defer client.Close()

	singleClient, ok := client.(*redis.Client)
	if !ok || !singleClient.Options().ContextTimeoutEnabled {
		t.Fatalf("Redis 客户端必须启用 context deadline: type=%T", client)
	}
}

// TestApplyClusterTLSConfigHonorsExplicitVerification 验证开发环境标识不能隐式关闭 Cluster 证书校验。
func TestApplyClusterTLSConfigHonorsExplicitVerification(t *testing.T) {
	for _, insecure := range []bool{false, true} {
		option := &redis.ClusterOptions{}
		applyClusterTLSConfig(option, config.RedisConfig{TLS: true, TLSInsecureSkipVerify: insecure})
		if option.TLSConfig == nil {
			t.Fatal("TLS 配置为空")
		}
		if option.TLSConfig.MinVersion != tls.VersionTLS12 {
			t.Fatalf("TLS 最低版本=%d，期望=%d", option.TLSConfig.MinVersion, tls.VersionTLS12)
		}
		if option.TLSConfig.InsecureSkipVerify != insecure {
			t.Fatalf("InsecureSkipVerify=%t，期望=%t", option.TLSConfig.InsecureSkipVerify, insecure)
		}
	}
}

// TestNewRejectsNonCanonicalConfig 确保直接装配与启动校验使用同一严格配置契约。
func TestNewRejectsNonCanonicalConfig(t *testing.T) {
	tests := []config.RedisConfig{
		{Type: "standalone", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 1},
		{Type: "", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 1},
		{Type: "single", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 0},
		{Type: "single", Addrs: []string{" 127.0.0.1:6379"}, PoolSize: 1},
		{Type: "cluster", Addrs: []string{"127.0.0.1:6379"}, AddrMap: map[string]string{" node": "127.0.0.1"}, PoolSize: 1},
	}
	for _, cfg := range tests {
		if client, err := New(context.Background(), cfg, config.ObservabilityConfig{}); err == nil {
			_ = client.Close()
			t.Fatalf("New() 应拒绝非规范 Redis 配置: %+v", cfg)
		}
	}
}
