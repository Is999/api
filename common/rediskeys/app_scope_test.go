package keys

import (
	"testing"
)

// TestWithPrefix 确保只为规范逻辑 key 添加一次当前 AppID 前缀。
func TestWithPrefix(t *testing.T) {
	tests := []struct {
		name  string // 用于 t.Run 区分作用域边界
		appID string // 决定当前 Redis 命名空间
		key   string // 覆盖逻辑、当前作用域和外部作用域 key
		want  string // 空值表示输入必须失败关闭
	}{
		{
			name:  "scopes logical key",
			appID: "site-a",
			key:   "config_uuid:featureFlag",
			want:  "app:site-a:config_uuid:featureFlag",
		},
		{
			name:  "rejects current app scoped key",
			appID: "site-a",
			key:   "app:site-a:user:session:42:jti",
			want:  "",
		},
		{
			name:  "rejects other app scoped key",
			appID: "site-b",
			key:   "app:site-a:user:session:42:jti",
			want:  "",
		},
		{
			name:  "rejects incomplete app prefix",
			appID: "site-b",
			key:   "app:site-a",
			want:  "",
		},
		{
			name:  "rejects whitespace logical key",
			appID: "site-a",
			key:   " user:session:42:jti ",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useAppID(t, tt.appID)
			if got := WithPrefix(tt.key); got != tt.want {
				t.Fatalf("WithPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHasPrefix 确保仅完整作用域结构被识别为带前缀 key。
func TestHasPrefix(t *testing.T) {
	tests := []struct {
		name string // 用于 t.Run 区分前缀完整性边界
		key  string // 覆盖完整、残缺和无作用域前缀
		want bool   // 仅完整作用域 key 应命中
	}{
		{name: "scoped key", key: "app:site-a:user:session:42:jti", want: true},
		{name: "empty logical key", key: "app:site-a:", want: false},
		{name: "missing logical separator", key: "app:site-a", want: false},
		{name: "missing app id", key: "app::user:session:42:jti", want: false},
		{name: "logical key", key: "user:session:42:jti", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasPrefix(tt.key); got != tt.want {
				t.Fatalf("HasPrefix() = %t, want %t", got, tt.want)
			}
		})
	}
}

// TestOwner 确保只从规范作用域 key 提取非空 AppID。
func TestOwner(t *testing.T) {
	tests := []struct {
		name   string // 用于 t.Run 区分 AppID 提取边界
		key    string // 覆盖规范、残缺和非法作用域 key
		want   string // 保存规范 key 应提取的 AppID
		wantOK bool   // 区分可解析作用域与非法输入
	}{
		{name: "scoped key", key: "app:site-a:user:session:42:jti", want: "site-a", wantOK: true},
		{name: "empty logical key", key: "app:site-a:", want: "", wantOK: false},
		{name: "missing logical separator", key: "app:site-a", want: "", wantOK: false},
		{name: "missing app id", key: "app::user:session:42:jti", want: "", wantOK: false},
		{name: "invalid app id", key: "app:site name:user:session:42:jti", want: "", wantOK: false},
		{name: "logical key", key: "user:session:42:jti", want: "", wantOK: false},
		{name: "whitespace scoped key", key: " app:site-a:user:session:42:jti ", want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Owner(tt.key)
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("Owner() = %q, %t, want %q, %t", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// TestIsForeignKey 确保仅其它 AppID 的完整作用域 key 被判定为外部 key。
func TestIsForeignKey(t *testing.T) {
	tests := []struct {
		name  string // 用于 t.Run 区分当前、外部和非法 key
		appID string // 作为 Redis 命名空间比较基准
		key   string // 覆盖当前、外部和残缺作用域
		want  bool   // 仅完整外部作用域应为 true
	}{
		{name: "current app key", appID: "site-a", key: "app:site-a:user:session:42:jti", want: false},
		{name: "other app key", appID: "site-a", key: "app:site-b:user:session:42:jti", want: true},
		{name: "logical key", appID: "site-a", key: "user:session:42:jti", want: false},
		{name: "empty app id", appID: "", key: "app:site-a:user:session:42:jti", want: true},
		{name: "incomplete prefix", appID: "site-a", key: "app:site-b", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useAppID(t, tt.appID)
			if got := IsForeignKey(tt.key); got != tt.want {
				t.Fatalf("IsForeignKey() = %t, want %t", got, tt.want)
			}
		})
	}
}

// TestWithPrefixWithEmptyAppIDFailsClosed 确保缺少 AppID 时不生成或接受作用域前缀。
func TestWithPrefixWithEmptyAppIDFailsClosed(t *testing.T) {
	useAppID(t, "")
	if got := Prefix(); got != "" {
		t.Fatalf("Prefix(empty) = %q, want empty", got)
	}
	if got := WithPrefix("user:session:42:jti"); got != "" {
		t.Fatalf("WithPrefix(empty app, logical key) = %q, want empty", got)
	}
	if got := WithPrefix("app:site-a:user:session:42:jti"); got != "" {
		t.Fatalf("WithPrefix(empty app, scoped key) = %q, want empty", got)
	}
}

// TestTrimPrefix 确保只移除完整作用域前缀，普通或残缺 key 保持原值。
func TestTrimPrefix(t *testing.T) {
	tests := []struct {
		name string // 用于 t.Run 区分裁剪和原样保留分支
		key  string // 覆盖当前作用域、普通和残缺 key
		want string // 保存裁剪结果或应保留的原值
	}{
		{
			name: "trims scoped key",
			key:  "app:site-a:user:session:42:jti",
			want: "user:session:42:jti",
		},
		{
			name: "keeps logical key",
			key:  "user:session:42:jti",
			want: "user:session:42:jti",
		},
		{
			name: "keeps incomplete prefix",
			key:  "app:site-a",
			want: "app:site-a",
		},
		{
			name: "keeps missing app id prefix",
			key:  "app::user:session:42:jti",
			want: "app::user:session:42:jti",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TrimPrefix(tt.key); got != tt.want {
				t.Fatalf("TrimPrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}
