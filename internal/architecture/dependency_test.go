package architecture_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// dependencyRule 描述需要长期保持的单向依赖边界。
type dependencyRule struct {
	dirs        []string // dirs 是相对仓库根目录扫描的生产代码目录
	forbidden   []string // forbidden 是禁止出现的 import 前缀
	description string   // description 是失败时给开发者的修复方向
}

// TestProductionDependencyDirection 防止公共包和 HTTP 中间件再次穿透业务层。
func TestProductionDependencyDirection(t *testing.T) {
	_, currentFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	rules := []dependencyRule{
		{dirs: []string{"common", "helper"}, forbidden: []string{"api/internal/"}, description: "顶层公共包不得反向依赖 internal"},
		{dirs: []string{"internal/middleware"}, forbidden: []string{"api/internal/logic"}, description: "middleware 只能通过 handler 装配的窄接口访问业务逻辑"},
		{dirs: []string{"internal/types"}, forbidden: []string{"api/internal/requestctx"}, description: "DTO 不得读取请求上下文"},
	}
	for _, rule := range rules {
		for _, dir := range rule.dirs {
			scanProductionImports(t, filepath.Join(repoRoot, dir), rule)
		}
	}
}

// scanProductionImports 解析目录内非测试 Go 文件，避免字符串搜索误报注释和测试适配器。
func scanProductionImports(t *testing.T, root string, rule dependencyRule) {
	t.Helper()
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			// helper 等已清理目录在干净检出中不存在；未来目录重新出现时仍会进入下方扫描。
			return
		}
		t.Fatalf("读取依赖边界目录失败 root=%s: %v", root, err)
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// 只检查静态 import 声明，不把通过结果当成运行时注册或反射依赖证明。
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, item := range file.Imports {
			importPath, err := strconv.Unquote(item.Path.Value)
			if err != nil {
				return err
			}
			for _, prefix := range rule.forbidden {
				if strings.HasPrefix(importPath, prefix) {
					position := ""
					if item.Pos().IsValid() {
						position = filepath.ToSlash(path)
					}
					t.Errorf("%s: %s imports %q", rule.description, position, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描依赖边界失败 root=%s: %v", root, err)
	}
}
