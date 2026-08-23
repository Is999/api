package register

import (
	"strings"

	"api/internal/handler"

	"github.com/Is999/go-utils/errors"
)

const (
	// KindComponent 表示启动期组件来源注册项。
	KindComponent = "component"
	// KindRoute 表示 HTTP 路由模块注册项。
	KindRoute = "route"
	// KindRuntimeRegistry 表示轻量运行时扩展入口注册项。
	KindRuntimeRegistry = "runtime_registry"
)

// Spec 描述一个可派生默认注册清单的注册规格。
type Spec struct {
	Name        string // 注册名称，必须在同类型内保持唯一
	File        string // 注册实现所在文件
	Method      string // 注册入口方法或构造方法
	Description string // 注册项中文说明
}

// Item 描述一个默认注册项，供文档、测试和启动装配核对。
type Item struct {
	Kind        string // 注册类型，如 component / route / runtime_registry
	Name        string // 注册名称，必须在同类型内保持唯一
	File        string // 注册实现所在文件
	Method      string // 注册入口方法或构造方法
	Description string // 注册项中文说明
}

// NewItem 从注册规格派生统一清单项。
func NewItem(kind string, spec Spec) Item {
	return Item{
		Kind:        kind,
		Name:        spec.Name,
		File:        spec.File,
		Method:      spec.Method,
		Description: spec.Description,
	}
}

// ValidateNamesUnique 校验注册列表内部名称唯一，避免同一能力被重复装配。
func ValidateNamesUnique(kind string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" || name != strings.TrimSpace(name) {
			return errors.Errorf("注册集合[%s]存在空名称或名称包含首尾空白", kind)
		}
		if _, ok := seen[name]; ok {
			return errors.Errorf("注册集合[%s]存在重复名称: %s", kind, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// RouteModuleNames 提取路由模块名称列表，供启动装配和测试复用。
func RouteModuleNames(items []handler.RouteModule) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			names = append(names, "")
			continue
		}
		names = append(names, item.Name())
	}
	return names
}
