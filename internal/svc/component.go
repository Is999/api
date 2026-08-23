package svc

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/Is999/go-utils/errors"
)

// ComponentCheckFunc 表示组件健康探测函数。
type ComponentCheckFunc func(ctx context.Context) error

// ComponentCloseFunc 表示受应用停止期限约束的组件资源关闭函数。
type ComponentCloseFunc func(context.Context) error

// Component 描述一个启动期组件的健康探测和关闭入口。
type Component struct {
	Name      string             // 组件名称，需在注册表内唯一
	ErrorCode int                // 健康异常时对应的业务码
	Check     ComponentCheckFunc // ready 检查函数，为空时视为无需探测
	Close     ComponentCloseFunc // 停机释放函数，为空时视为无需释放
}

// ComponentRegistry 保存启动期组件的轻量生命周期清单。
type ComponentRegistry struct {
	closed atomic.Bool // 标记关闭流程是否已执行
	items  []Component // 按注册顺序保存组件
}

// NewComponentRegistry 创建组件生命周期注册表。
func NewComponentRegistry(items ...Component) (*ComponentRegistry, error) {
	registry := &ComponentRegistry{}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.Name == "" || item.Name != strings.TrimSpace(item.Name) {
			return nil, errors.Errorf("组件注册表存在空名称或名称包含首尾空白")
		}
		if _, ok := seen[item.Name]; ok {
			return nil, errors.Errorf("组件注册表存在重复名称: %s", item.Name)
		}
		seen[item.Name] = struct{}{}
		registry.items = append(registry.items, item)
	}
	return registry, nil
}

// Items 返回组件快照，避免调用方修改内部切片。
func (r *ComponentRegistry) Items() []Component {
	if r == nil || len(r.items) == 0 {
		return nil
	}
	items := make([]Component, len(r.items))
	copy(items, r.items)
	return items
}

// Close 按注册顺序反向释放组件资源，并保证只执行一次。
func (r *ComponentRegistry) Close(ctx context.Context) error {
	if r == nil || r.closed.Swap(true) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var firstErr error
	for i := len(r.items) - 1; i >= 0; i-- {
		closeFunc := r.items[i].Close
		if closeFunc == nil {
			continue
		}
		// 单个组件失败不阻断其前置依赖关闭，最后向主入口返回首个错误。
		if err := closeFunc(ctx); err != nil && firstErr == nil {
			firstErr = errors.Tag(err)
		}
	}
	if err := ctx.Err(); err != nil && firstErr == nil {
		firstErr = errors.Tag(err)
	}
	return firstErr
}
