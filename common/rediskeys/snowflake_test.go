package keys

import "testing"

// TestSnowflakeNodeLeaseKey 验证雪花 node_id 租约 key 同时包含部署 scope 和业务命名空间。
func TestSnowflakeNodeLeaseKey(t *testing.T) {
	if got := SnowflakeNodeLeaseKey("main", "user", 12); got != "snowflake:node:main:user:12" {
		t.Fatalf("SnowflakeNodeLeaseKey() = %q", got)
	}
	if got := SnowflakeNodeLeaseKey(" main ", "user", 12); got != "" {
		t.Fatalf("SnowflakeNodeLeaseKey(non-canonical scope) = %q, want empty", got)
	}
	if got := SnowflakeNodeLeaseKey(" ", "user", 12); got != "" {
		t.Fatalf("SnowflakeNodeLeaseKey(empty scope) = %q, want empty", got)
	}
	if got := SnowflakeNodeLeaseKey("main", " ", 12); got != "" {
		t.Fatalf("SnowflakeNodeLeaseKey(empty namespace) = %q, want empty", got)
	}
}

// TestIDSegmentCounterKey 验证业务号段 key 同时包含部署 scope 和业务命名空间。
func TestIDSegmentCounterKey(t *testing.T) {
	if got := IDSegmentCounterKey("main", "recharge.order"); got != "idgen:segment:main:recharge.order" {
		t.Fatalf("IDSegmentCounterKey() = %q", got)
	}
	if got := IDSegmentCounterKey("main", " recharge.order "); got != "" {
		t.Fatalf("IDSegmentCounterKey(non-canonical namespace) = %q, want empty", got)
	}
	if got := IDSegmentCounterKey(" ", "recharge.order"); got != "" {
		t.Fatalf("IDSegmentCounterKey(empty scope) = %q, want empty", got)
	}
	if got := IDSegmentCounterKey("main", " "); got != "" {
		t.Fatalf("IDSegmentCounterKey(empty namespace) = %q, want empty", got)
	}
}
