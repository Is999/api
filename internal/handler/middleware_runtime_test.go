package handler

import (
	"context"
	"strings"
	"testing"

	"api/internal/config"
	"api/internal/middleware"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestMiddlewareRuntimeClassifiesDatabaseFailure 验证主库故障会进入依赖不可用分类，而不是 token 无效分类。
func TestMiddlewareRuntimeClassifiesDatabaseFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:middleware-runtime?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB() error = %v", err)
	}
	// 主动关闭已经创建的连接池，确保错误来自查询阶段而不是测试缺少数据库驱动。
	if err = sqlDB.Close(); err != nil {
		t.Fatalf("sqlDB.Close() error = %v", err)
	}
	service := svc.NewServiceContext(config.Config{}, "v1", svc.Dependencies{
		SiteDBs: svc.SiteDatabases{MainDB: db},
	})
	runtime := &middlewareRuntime{svc: service}

	if _, err = runtime.ActiveUser(context.Background(), 42); !errors.Is(err, middleware.ErrRuntimeDependencyUnavailable) {
		t.Fatalf("ActiveUser() error = %v, want ErrRuntimeDependencyUnavailable", err)
	}
}

// TestMiddlewareRuntimeRejectsMissingSecurityRegistry 确保 ServiceContext 未注入启动注册表时失败关闭。
func TestMiddlewareRuntimeRejectsMissingSecurityRegistry(t *testing.T) {
	runtime := &middlewareRuntime{svc: svc.NewServiceContext(config.Config{AppID: "site-a"}, "v1", svc.Dependencies{})}
	if _, err := runtime.SecurityRoute(context.Background(), "site-a"); err == nil || !strings.Contains(err.Error(), "安全密钥注册表未初始化") {
		t.Fatalf("SecurityRoute() error = %v, want missing registry", err)
	}
	if _, _, err := runtime.Signer(context.Background(), "site-a", "", "", security.SignatureTypeHMAC); err == nil || !strings.Contains(err.Error(), "安全密钥注册表未初始化") {
		t.Fatalf("Signer() error = %v, want missing registry", err)
	}
	if _, _, err := runtime.Cryptor(context.Background(), "site-a", "", "", security.CryptoTypeAES); err == nil || !strings.Contains(err.Error(), "安全密钥注册表未初始化") {
		t.Fatalf("Cryptor() error = %v, want missing registry", err)
	}
}
