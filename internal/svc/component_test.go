package svc

import (
	"context"
	"reflect"
	"testing"

	"github.com/Is999/go-utils/errors"
)

// TestNewComponentRegistryRejectsDuplicate 确保组件名称重复会被拦截。
func TestNewComponentRegistryRejectsDuplicate(t *testing.T) {
	if _, err := NewComponentRegistry(Component{Name: "mysql"}, Component{Name: "mysql"}); err == nil {
		t.Fatal("expected duplicate component name error")
	}
	if _, err := NewComponentRegistry(Component{Name: " mysql "}); err == nil {
		t.Fatal("expected non-canonical component name error")
	}
}

// TestComponentRegistryCloseReverseAndOnce 确保组件按反向顺序关闭且只执行一次。
func TestComponentRegistryCloseReverseAndOnce(t *testing.T) {
	closed := make([]string, 0, 2)
	registry, err := NewComponentRegistry(
		Component{Name: "mysql", Close: func(context.Context) error {
			closed = append(closed, "mysql")
			return nil
		}},
		Component{Name: "redis", Close: func(context.Context) error {
			closed = append(closed, "redis")
			return nil
		}},
	)
	if err != nil {
		t.Fatalf("NewComponentRegistry() error = %v", err)
	}

	if err = registry.Close(context.Background()); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	if err = registry.Close(context.Background()); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
	want := []string{"redis", "mysql"}
	if !reflect.DeepEqual(closed, want) {
		t.Fatalf("close order = %v, want %v", closed, want)
	}
}

// TestComponentRegistryCloseReturnsFirstError 确保关闭流程返回首个错误。
func TestComponentRegistryCloseReturnsFirstError(t *testing.T) {
	wantErr := errors.Errorf("redis close failed")
	registry, err := NewComponentRegistry(
		Component{Name: "mysql", Close: func(context.Context) error { return nil }},
		Component{Name: "redis", Close: func(context.Context) error { return wantErr }},
	)
	if err != nil {
		t.Fatalf("NewComponentRegistry() error = %v", err)
	}
	if err = registry.Close(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("Close() error = %v, want %v", err, wantErr)
	}
}

// TestComponentRegistryCloseAfterContextDone 确保期限到达后仍尝试释放可立即关闭的后续资源。
func TestComponentRegistryCloseAfterContextDone(t *testing.T) {
	closed := make([]string, 0, 2)
	registry, err := NewComponentRegistry(
		Component{Name: "mysql", Close: func(context.Context) error {
			closed = append(closed, "mysql")
			return nil
		}},
		Component{Name: "redis", Close: func(context.Context) error {
			closed = append(closed, "redis")
			return nil
		}},
	)
	if err != nil {
		t.Fatalf("NewComponentRegistry() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = registry.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close() error = %v, want context canceled", err)
	}
	want := []string{"redis", "mysql"}
	if !reflect.DeepEqual(closed, want) {
		t.Fatalf("context canceled 后关闭顺序 = %v, want %v", closed, want)
	}
}

// TestComponentRegistryItemsReturnsCopy 确保组件快照不会修改内部状态。
func TestComponentRegistryItemsReturnsCopy(t *testing.T) {
	registry, err := NewComponentRegistry(Component{Name: "mysql", Check: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatalf("NewComponentRegistry() error = %v", err)
	}
	items := registry.Items()
	items[0].Name = "changed"
	if got := registry.Items()[0].Name; got != "mysql" {
		t.Fatalf("registry item name = %s, want mysql", got)
	}
}
