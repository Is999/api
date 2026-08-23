package validators

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/security"
)

// TestCompileSecurityRegistryBoundsSecretRefs 锁定启动材料文件边界；FIFO 通过独立子进程超时回收，不遗留阻塞读取。
func TestCompileSecurityRegistryBoundsSecretRefs(t *testing.T) {
	if fifo := os.Getenv("API_SECRET_REF_FIFO_TEST"); fifo != "" {
		_, err := CompileSecurityRegistry(config.Config{AppID: "file-boundary", Security: config.SecurityConfig{SecretKey: config.SecuritySecretKeyConfig{
			SignStatus: 1, StableVersion: "v1", Versions: []config.SecuritySecretKeyVersionConfig{{KeyVersion: "v1", AESKeyRef: fifo}},
		}}})
		if err == nil || !strings.Contains(err.Error(), "must be a regular file") {
			t.Fatalf("FIFO 应在读取前拒绝，err=%v", err)
		}
		return
	}
	rsaKeys := registryRSAKeys(t)
	for _, field := range []string{"aes", "user_public", "server_public", "server_private"} {
		for _, mode := range []string{"regular", "limit", "over_limit", "symlink", "directory"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				version := config.SecuritySecretKeyVersionConfig{KeyVersion: "v1", AESKey: "1234567890123456", RSAPublicKeyUser: rsaKeys.clientPublicPEM, RSAPublicKeyServer: rsaKeys.serverPublicPEM, RSAPrivateKeyServer: rsaKeys.serverPrivatePEM}
				value, ref := &version.AESKey, &version.AESKeyRef
				switch field {
				case "user_public":
					value, ref = &version.RSAPublicKeyUser, &version.RSAPublicKeyUserRef
				case "server_public":
					value, ref = &version.RSAPublicKeyServer, &version.RSAPublicKeyServerRef
				case "server_private":
					value, ref = &version.RSAPrivateKeyServer, &version.RSAPrivateKeyServerRef
				}
				dir := t.TempDir()
				*ref = filepath.Join(dir, "material")
				material := *value
				// 文件预算按原始字节计算，不能在修剪尾部空白后绕过上限。
				if mode == "limit" || mode == "over_limit" {
					size := 64 << 10
					if mode == "over_limit" {
						size++
					}
					material += strings.Repeat("\n", size-len(material))
				}
				if mode == "directory" {
					*ref = dir
				} else {
					writeRegistrySecret(t, *ref, material)
					if mode == "symlink" {
						link := filepath.Join(dir, "current")
						if err := os.Symlink(*ref, link); err != nil {
							t.Fatal(err)
						}
						*ref = link
					}
				}
				*value = ""
				_, err := CompileSecurityRegistry(config.Config{AppID: "file-boundary", Security: config.SecurityConfig{SecretKey: config.SecuritySecretKeyConfig{
					SignStatus: 1, CryptoStatus: 1, StableVersion: "v1", Versions: []config.SecuritySecretKeyVersionConfig{version},
				}}})
				wantErr := mode == "over_limit" || mode == "directory"
				if (err != nil) != wantErr {
					t.Fatalf("启动文件边界 err=%v，期望拒绝=%t", err, wantErr)
				}
			})
		}
	}
	t.Run("fifo", func(t *testing.T) {
		fifo := filepath.Join(t.TempDir(), "material.fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCompileSecurityRegistryBoundsSecretRefs$")
		cmd.Env = append(os.Environ(), "API_SECRET_REF_FIFO_TEST="+fifo)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("启动文件读取未及时受控拒绝：err=%v timeout=%v output=%s", err, ctx.Err(), output)
		}
	})
}

// TestCompileSecurityRegistrySnapshotsSecretRefs 确保注册表编译完成后不再读取密钥引用文件。
func TestCompileSecurityRegistrySnapshotsSecretRefs(t *testing.T) {
	// 首次编译从文件读取 AES 与两端 RSA 密钥并构造全部运行对象。
	rsaKeys := registryRSAKeys(t)
	dir := t.TempDir()
	aesRef := filepath.Join(dir, "aes.key")
	userPublicRef := filepath.Join(dir, "user-public.pem")
	serverPrivateRef := filepath.Join(dir, "server-private.pem")
	serverPublicRef := filepath.Join(dir, "server-public.pem")
	writeRegistrySecret(t, aesRef, "1234567890123456")
	writeRegistrySecret(t, userPublicRef, rsaKeys.clientPublicPEM)
	writeRegistrySecret(t, serverPrivateRef, rsaKeys.serverPrivatePEM)
	writeRegistrySecret(t, serverPublicRef, rsaKeys.serverPublicPEM)

	cfg := config.Config{
		AppID: "site-a",
		Security: config.SecurityConfig{SecretKey: config.SecuritySecretKeyConfig{
			SignStatus:    1,
			CryptoStatus:  1,
			StableVersion: "v1",
			Versions: []config.SecuritySecretKeyVersionConfig{{
				KeyVersion:             "v1",
				AESKeyRef:              aesRef,
				RSAPublicKeyUserRef:    userPublicRef,
				RSAPublicKeyServerRef:  serverPublicRef,
				RSAPrivateKeyServerRef: serverPrivateRef,
			}},
		}},
	}
	registry, err := CompileSecurityRegistry(cfg)
	if err != nil {
		t.Fatalf("CompileSecurityRegistry() error = %v", err)
	}
	// 文件和配置在首次查找前失效，防止懒加载实现误通过快照测试。
	writeRegistrySecret(t, aesRef, "6543210987654321")
	if err := os.Remove(userPublicRef); err != nil {
		t.Fatalf("Remove(user public ref) error = %v", err)
	}
	if err := os.Remove(serverPrivateRef); err != nil {
		t.Fatalf("Remove(server private ref) error = %v", err)
	}
	if err := os.Remove(serverPublicRef); err != nil {
		t.Fatalf("Remove(server public ref) error = %v", err)
	}
	cfg.AppID = "changed-site"
	cfg.Security.SecretKey.Versions = nil

	// HMAC 与 AES 必须继续使用编译时旧密钥，并复用同一对象实例。
	wantHMACSigner, err := security.NewHMACSigner("1234567890123456")
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	wantHMAC, err := wantHMACSigner.Sign("snapshot")
	if err != nil {
		t.Fatalf("expected HMAC Sign() error = %v", err)
	}
	hmacSigner, _, err := registry.Signer("site-a", "v1", "", security.SignatureTypeHMAC)
	if err != nil {
		t.Fatalf("HMAC Signer() after ref change error = %v", err)
	}
	gotHMAC, err := hmacSigner.Sign("snapshot")
	if err != nil || gotHMAC != wantHMAC {
		t.Fatalf("HMAC Sign() after ref change = %q, %v", gotHMAC, err)
	}
	resolvedHMAC, _, err := registry.Signer("site-a", "v1", "", security.SignatureTypeHMAC)
	if err != nil || resolvedHMAC != hmacSigner {
		t.Fatalf("HMAC Signer() repeated lookup = %T, %v", resolvedHMAC, err)
	}
	wantAESCryptor, err := security.NewAESGCMCipher("1234567890123456")
	if err != nil {
		t.Fatalf("NewAESGCMCipher() error = %v", err)
	}
	aesCryptor, _, err := registry.Cryptor("site-a", "v1", "", security.CryptoTypeAES)
	if err != nil {
		t.Fatalf("AES Cryptor() after ref change error = %v", err)
	}
	aesCiphertext, err := aesCryptor.Encrypt("aes-snapshot")
	if err != nil {
		t.Fatalf("AES Encrypt() after ref change error = %v", err)
	}
	plain, err := wantAESCryptor.Decrypt(aesCiphertext)
	if err != nil || plain != "aes-snapshot" {
		t.Fatalf("AES snapshot decrypt = %q, %v", plain, err)
	}
	resolvedAES, _, err := registry.Cryptor("site-a", "v1", "", security.CryptoTypeAES)
	if err != nil || resolvedAES != aesCryptor {
		t.Fatalf("AES Cryptor() repeated lookup = %T, %v", resolvedAES, err)
	}

	// RSA 签名器必须同时支持客户端请求验签和服务端响应签名。
	rsaSigner, _, err := registry.Signer("site-a", "v1", "", security.SignatureTypeRSA)
	if err != nil {
		t.Fatalf("RSA Signer() after ref deletion error = %v", err)
	}
	clientSigner, err := security.NewRSASigner(rsaKeys.clientPrivatePEM, rsaKeys.serverPublicPEM)
	if err != nil {
		t.Fatalf("NewRSASigner(client) error = %v", err)
	}
	requestSignature, err := clientSigner.Sign("rsa-request")
	if err != nil {
		t.Fatalf("client Sign() error = %v", err)
	}
	verified, err := rsaSigner.Verify("rsa-request", requestSignature)
	if err != nil || !verified {
		t.Fatalf("server Verify() after ref deletion = %t, %v", verified, err)
	}
	responseSignature, err := rsaSigner.Sign("rsa-response")
	if err != nil {
		t.Fatalf("server Sign() after ref deletion error = %v", err)
	}
	verified, err = clientSigner.Verify("rsa-response", responseSignature)
	if err != nil || !verified {
		t.Fatalf("client Verify() after ref deletion = %t, %v", verified, err)
	}
	resolvedRSA, _, err := registry.Signer("site-a", "v1", "", security.SignatureTypeRSA)
	if err != nil || resolvedRSA != rsaSigner {
		t.Fatalf("RSA Signer() repeated lookup = %T, %v", resolvedRSA, err)
	}
	// RSA 加解密器同样覆盖客户端到服务端及反向两个方向。
	rsaCryptor, _, err := registry.Cryptor("site-a", "v1", "", security.CryptoTypeRSA)
	if err != nil {
		t.Fatalf("RSA Cryptor() after ref deletion error = %v", err)
	}
	clientCryptor, err := security.NewRSACipher(rsaKeys.clientPrivatePEM, rsaKeys.serverPublicPEM)
	if err != nil {
		t.Fatalf("NewRSACipher(client) error = %v", err)
	}
	requestCiphertext, err := clientCryptor.Encrypt("rsa-request")
	if err != nil {
		t.Fatalf("client Encrypt() error = %v", err)
	}
	plain, err = rsaCryptor.Decrypt(requestCiphertext)
	if err != nil || plain != "rsa-request" {
		t.Fatalf("server Decrypt() after ref deletion = %q, %v", plain, err)
	}
	responseCiphertext, err := rsaCryptor.Encrypt("rsa-response")
	if err != nil {
		t.Fatalf("server Encrypt() after ref deletion error = %v", err)
	}
	plain, err = clientCryptor.Decrypt(responseCiphertext)
	if err != nil || plain != "rsa-response" {
		t.Fatalf("client Decrypt() after ref deletion = %q, %v", plain, err)
	}
	resolvedRSACryptor, _, err := registry.Cryptor("site-a", "v1", "", security.CryptoTypeRSA)
	if err != nil || resolvedRSACryptor != rsaCryptor {
		t.Fatalf("RSA Cryptor() repeated lookup = %T, %v", resolvedRSACryptor, err)
	}
}

// TestCompileSecurityRegistryIncludesAllVersionsAndGrayRoute 固定编译范围和稳定、灰度、显式版本映射。
func TestCompileSecurityRegistryIncludesAllVersionsAndGrayRoute(t *testing.T) {
	rsaKeys := registryRSAKeys(t)
	versions := make([]config.SecuritySecretKeyVersionConfig, 0, 3)
	for _, version := range []string{"v1", "v2", "v3"} {
		versions = append(versions, config.SecuritySecretKeyVersionConfig{
			KeyVersion:          version,
			AESKey:              "1234567890123456",
			RSAPublicKeyUser:    rsaKeys.clientPublicPEM,
			RSAPublicKeyServer:  rsaKeys.serverPublicPEM,
			RSAPrivateKeyServer: rsaKeys.serverPrivatePEM,
		})
	}
	cfg := config.Config{
		AppID: "site-a",
		Security: config.SecurityConfig{SecretKey: config.SecuritySecretKeyConfig{
			SignStatus:    1,
			CryptoStatus:  1,
			StableVersion: "v1",
			GrayVersion:   "v2",
			GrayPercent:   100,
			GraySalt:      "release-salt",
			Versions:      versions,
		}},
	}
	registry, err := CompileSecurityRegistry(cfg)
	if err != nil {
		t.Fatalf("CompileSecurityRegistry() error = %v", err)
	}
	route, err := registry.Route(cfg.AppID)
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if route.StableVersion != "v1" || route.GrayVersion != "v2" || route.GrayPercent != 100 || route.GraySalt != "release-salt" {
		t.Fatalf("route = %+v, want stable v1 and 100%% gray v2", route)
	}
	for _, testCase := range []struct {
		name        string // 用于 t.Run 区分显式和灰度选路
		versionHint string // 空值触发默认灰度选路
		wantVersion string // 断言最终命中的预编译版本
	}{
		{name: "stable explicit", versionHint: "v1", wantVersion: "v1"},
		{name: "gray default", wantVersion: "v2"},
		{name: "non-current explicit", versionHint: "v3", wantVersion: "v3"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, version, err := registry.Signer(cfg.AppID, testCase.versionHint, "user-42", security.SignatureTypeHMAC); err != nil || version != testCase.wantVersion {
				t.Fatalf("Signer() version = %q, %v, want %q", version, err, testCase.wantVersion)
			}
		})
	}

	broken := cfg
	broken.Security.SecretKey.Versions = append([]config.SecuritySecretKeyVersionConfig(nil), versions...)
	broken.Security.SecretKey.Versions[2].AESKey = "short"
	if _, err := CompileSecurityRegistry(broken); err == nil {
		t.Fatal("CompileSecurityRegistry() expected non-current version material error")
	}
}

// TestCompileSecurityRegistryAllowsEmptySecurity 固定空安全配置不创建无意义注册表。
func TestCompileSecurityRegistryAllowsEmptySecurity(t *testing.T) {
	registry, err := CompileSecurityRegistry(config.Config{})
	if err != nil || registry != nil {
		t.Fatalf("CompileSecurityRegistry(empty) = %v, %v", registry, err)
	}
}

// registryRSAKeySet 保存两端独立的测试密钥，避免自环掩盖 RSA 方向错误。
type registryRSAKeySet struct {
	clientPrivatePEM string // clientPrivatePEM 用于模拟客户端请求签名和响应解密
	clientPublicPEM  string // clientPublicPEM 是服务端验签和响应加密配置
	serverPrivatePEM string // serverPrivatePEM 是服务端响应签名和请求解密配置
	serverPublicPEM  string // serverPublicPEM 用于模拟客户端验签和请求加密
}

// registryRSAKeys 生成客户端和服务端两组 RSA 密钥，避免仓库固化私钥材料。
func registryRSAKeys(t *testing.T) registryRSAKeySet {
	t.Helper()
	clientPrivate, clientPublic := registryRSAKeyPair(t)
	serverPrivate, serverPublic := registryRSAKeyPair(t)
	return registryRSAKeySet{
		clientPrivatePEM: clientPrivate,
		clientPublicPEM:  clientPublic,
		serverPrivatePEM: serverPrivate,
		serverPublicPEM:  serverPublic,
	}
}

// registryRSAKeyPair 生成单组测试 RSA 私钥和公钥 PEM。
func registryRSAKeyPair(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, security.MinRSAKeyBits)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return string(privatePEM), string(publicPEM)
}

// writeRegistrySecret 写入权限收敛的测试密钥文件，覆盖动作模拟原地轮换。
func writeRegistrySecret(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}
