package resources

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"api/internal/bootstrap/configload/validators"
	"api/internal/config"
	"api/internal/security"
)

// TestBuildServiceContextRequiresCompiledSecurityRegistry 确保启用安全链时禁止缺少编译结果降级启动。
func TestBuildServiceContextRequiresCompiledSecurityRegistry(t *testing.T) {
	cfg := config.Config{
		AppID: "site-a",
		Security: config.SecurityConfig{SecretKey: config.SecuritySecretKeyConfig{
			SignStatus:    1,
			StableVersion: "v1",
		}},
	}
	svcCtx, shutdown, err := BuildServiceContext(t.Context(), cfg, "test-version", nil)
	if err == nil || !strings.Contains(err.Error(), "密钥注册表未注入") {
		t.Fatalf("BuildServiceContext() error = %v", err)
	}
	if svcCtx != nil || shutdown != nil {
		t.Fatalf("密钥注册表缺失不应返回已初始化资源: svc_nil=%t shutdown_nil=%t", svcCtx == nil, shutdown == nil)
	}
}

// TestBuildServiceContextReusesCompiledSecurityRegistry 确保资源装配不再读取已经编译过的密钥文件。
func TestBuildServiceContextReusesCompiledSecurityRegistry(t *testing.T) {
	keyDir := t.TempDir()
	aesPath := filepath.Join(keyDir, "aes.key")
	userPublicPath := filepath.Join(keyDir, "user-public.pem")
	serverPrivatePath := filepath.Join(keyDir, "server-private.pem")
	userPrivateKey, err := rsa.GenerateKey(rand.Reader, security.MinRSAKeyBits)
	if err != nil {
		t.Fatalf("GenerateKey(user) error = %v", err)
	}
	serverPrivateKey, err := rsa.GenerateKey(rand.Reader, security.MinRSAKeyBits)
	if err != nil {
		t.Fatalf("GenerateKey(server) error = %v", err)
	}
	userPublicDER, err := x509.MarshalPKIXPublicKey(&userPrivateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	if err := os.WriteFile(aesPath, []byte("1234567890123456"), 0o600); err != nil {
		t.Fatalf("WriteFile(aes) error = %v", err)
	}
	if err := os.WriteFile(userPublicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: userPublicDER}), 0o600); err != nil {
		t.Fatalf("WriteFile(user public) error = %v", err)
	}
	if err := os.WriteFile(serverPrivatePath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverPrivateKey)}), 0o600); err != nil {
		t.Fatalf("WriteFile(server private) error = %v", err)
	}
	cfg := config.Config{
		AppID: "site-a",
		Security: config.SecurityConfig{SecretKey: config.SecuritySecretKeyConfig{
			SignStatus:    1,
			CryptoStatus:  1,
			StableVersion: "v1",
			Versions: []config.SecuritySecretKeyVersionConfig{{
				KeyVersion:             "v1",
				AESKeyRef:              aesPath,
				RSAPublicKeyUserRef:    userPublicPath,
				RSAPrivateKeyServerRef: serverPrivatePath,
			}},
		}},
	}
	registry, err := validators.CompileSecurityRegistry(cfg)
	if err != nil {
		t.Fatalf("CompileSecurityRegistry() error = %v", err)
	}
	// 删除密钥来源，确保资源装配阶段无法再次读取。
	for _, path := range []string{aesPath, userPublicPath, serverPrivatePath} {
		if err := os.Remove(path); err != nil {
			t.Fatalf("Remove(%s) error = %v", filepath.Base(path), err)
		}
	}

	svcCtx, shutdown, err := BuildServiceContext(t.Context(), cfg, "test-version", registry)
	// 缺失 MySQL 的错误证明装配过程已经越过密钥阶段。
	if err == nil || !strings.Contains(err.Error(), "mysql.write_data_source") {
		t.Fatalf("BuildServiceContext() should reach infrastructure validation without rereading keys: %v", err)
	}
	if strings.Contains(err.Error(), "aes.key") || strings.Contains(err.Error(), "user-public.pem") || strings.Contains(err.Error(), "server-private.pem") {
		t.Fatalf("BuildServiceContext() reread compiled key material: %v", err)
	}
	if svcCtx != nil || shutdown != nil {
		t.Fatalf("基础设施失败不应返回部分资源: svc_nil=%t shutdown_nil=%t", svcCtx == nil, shutdown == nil)
	}
}
