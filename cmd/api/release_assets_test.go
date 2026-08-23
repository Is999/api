package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestPackageContainsOnlyReleaseAssets 通过真实 Make/tar 入口验证发布白名单，不读取本地实际配置。
func TestPackageContainsOnlyReleaseAssets(t *testing.T) {
	repo := filepath.Join("..", "..")
	// Make 只展开公开资产清单；不执行构建，也不把真实配置复制到临时目录。
	list := exec.CommandContext(t.Context(), "make", "-s", "-f", "Makefile", "-f", "-", "print-package-files")
	list.Stdin = strings.NewReader("print-package-files:\n\t@printf '%s\\n' $(PACKAGE_FILES)\n")
	list.Dir = repo
	output, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("读取发布白名单: %v\n%s", err, output)
	}
	assets := strings.Fields(string(output))
	for _, required := range []string{"bin/api", "bin/api-migrate", "etc/config.sample.yaml"} {
		if !slices.Contains(assets, required) {
			t.Fatalf("发布白名单缺少 %s", required)
		}
	}
	dir := t.TempDir()
	makefile, err := os.ReadFile(filepath.Join(repo, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0o600); err != nil {
		t.Fatal(err)
	}
	// 公开资产和假二进制均只写路径标记；额外模拟被忽略配置与旧产物。
	poison := []string{"etc/config.yaml", "etc/config.d/runtime.yaml", "etc/keys/private.pem", "bin/previous"}
	for _, asset := range append(slices.Clone(assets), poison...) {
		file := filepath.Join(dir, asset)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("fixture:"+asset), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 跳过两个编译前置项，只对临时假资产运行原始 package recipe。
	command := exec.CommandContext(t.Context(), "make", "-o", "build", "-o", "build-tools", "package", "VERSION=test")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("package: %v\n%s", err, output)
	}
	file, err := os.Open(filepath.Join(dir, "dist", "api-test.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	found := make(map[string]bool, len(assets))
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(assets, header.Name) || slices.Contains(poison, header.Name) {
			t.Fatalf("制品夹带非发布资产 %s", header.Name)
		}
		found[header.Name] = true
	}
	for _, asset := range assets {
		if !found[asset] {
			t.Errorf("制品缺少白名单资产 %s", asset)
		}
	}
}

// TestIntegrationMySQLBindsLoopback 防止公开测试口令随本地依赖发布到其它宿主网卡。
func TestIntegrationMySQLBindsLoopback(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "deploy", "integration", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "\"127.0.0.1:3311:3306\"") || strings.Contains(string(content), "\"3311:3306\"") {
		t.Fatal("集成 MySQL 必须显式绑定宿主机回环地址")
	}
}
