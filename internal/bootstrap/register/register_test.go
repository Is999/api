package register

import (
	"testing"

	"api/internal/handler"
)

// TestValidateNamesUniqueRejectsDuplicate 确保真实注册集合出现重复名称时会被启动校验拦截。
func TestValidateNamesUniqueRejectsDuplicate(t *testing.T) {
	if err := ValidateNamesUnique(KindRoute, []string{"user", "user"}); err == nil {
		t.Fatal("期望重复注册名称返回错误，实际为 nil")
	}
}

// TestValidateNamesUniqueRejectsNonCanonicalName 确保注册入口不会裁剪出第二套名称。
func TestValidateNamesUniqueRejectsNonCanonicalName(t *testing.T) {
	if err := ValidateNamesUnique(KindRoute, []string{" user "}); err == nil {
		t.Fatal("expected non-canonical registration name error")
	}
}

// TestRouteModuleNamesExposesNilModule 确保 nil 模块会进入名称校验并拒绝启动。
func TestRouteModuleNamesExposesNilModule(t *testing.T) {
	if err := ValidateNamesUnique(KindRoute, RouteModuleNames([]handler.RouteModule{nil})); err == nil {
		t.Fatal("expected nil route module error")
	}
}

// TestDefaultRouteModulesNamesUnique 确保默认路由模块注册名不重复。
func TestDefaultRouteModulesNamesUnique(t *testing.T) {
	names := RouteModuleNames(handler.BuiltinRouteModules())
	if err := ValidateNamesUnique(KindRoute, names); err != nil {
		t.Fatalf("默认路由模块名称不合法: %v", err)
	}
}
