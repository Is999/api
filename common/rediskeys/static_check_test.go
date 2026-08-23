package keys

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRedisKeysFileOnlyDefinesConstants 确保集中清单只包含 Redis Key 常量声明。
func TestRedisKeysFileOnlyDefinesConstants(t *testing.T) {
	fset := token.NewFileSet()
	file := parseRedisKeysFile(t, fset)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			pos := fset.Position(decl.Pos())
			t.Fatalf("redis_keys.go 只允许定义 Redis key 常量，line=%d", pos.Line)
		}
		if gen.Tok != token.CONST {
			pos := fset.Position(gen.Pos())
			t.Fatalf("redis_keys.go 不允许出现 %s 声明，line=%d", gen.Tok, pos.Line)
		}
	}
}

// TestRedisKeyCommentsIncludeTypeAndTTL 只检查类型与 TTL 标识存在；声明是否与读写命令、过期行为一致仍需语义复核。
func TestRedisKeyCommentsIncludeTypeAndTTL(t *testing.T) {
	fset := token.NewFileSet()
	file := parseRedisKeysFile(t, fset)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			comment := valueSpec.Doc.Text()
			for _, name := range valueSpec.Names {
				if !strings.Contains(comment, "Redis 类型：") || !strings.Contains(comment, "TTL 过期规则：") {
					pos := fset.Position(valueSpec.Pos())
					t.Fatalf("%s 注释必须包含 Redis 类型和 TTL 过期规则，line=%d", name.Name, pos.Line)
				}
			}
		}
	}
}

// TestRedisKeyConstantsStayInRedisKeysFile 确保 Redis Key 常量不散落在集中清单外。
func TestRedisKeyConstantsStayInRedisKeysFile(t *testing.T) {
	dir := redisKeysPackageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "redis_keys.go" {
			continue
		}
		fset := token.NewFileSet()
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			pos := fset.Position(gen.Pos())
			t.Fatalf("%s 不应定义 Redis key 常量，请放到 redis_keys.go，line=%d", name, pos.Line)
		}
	}
}

// TestNoReferenceOnlyMoneyBalanceTemplateName 确保示例余额模板名不会进入生产 key 契约。
func TestNoReferenceOnlyMoneyBalanceTemplateName(t *testing.T) {
	dir := redisKeysPackageDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// 扫描包含当前测试源码，拆开禁用名称避免断言字面量把自身误判为生产模板。
		if strings.Contains(string(data), "RedisHash"+"MoneyBalanceTemplate") {
			t.Fatalf("%s 包含仅用于示例的余额模板名", name)
		}
	}
}

// parseRedisKeysFile 解析集中 Redis Key 清单及其注释。
func parseRedisKeysFile(t *testing.T, fset *token.FileSet) *ast.File {
	t.Helper()
	path := filepath.Join(redisKeysPackageDir(t), "redis_keys.go")
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse redis_keys.go: %v", err)
	}
	return file
}

// redisKeysPackageDir 返回当前包源码目录，供静态契约测试扫描。
func redisKeysPackageDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	return filepath.Dir(file)
}
