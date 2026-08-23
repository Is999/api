package configload

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"api/internal/config"
)

// TestValidateConfigRejectsSecurityWithoutAppID 确保配置安全链路时必须绑定 AppID。
func TestValidateConfigRejectsSecurityWithoutAppID(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = ""
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		SignStatus:    0,
		CryptoStatus:  0,
		Versions:      []config.SecuritySecretKeyVersionConfig{{KeyVersion: "v1"}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected configured security without app_id to be rejected")
	}
}

// TestValidateConfigRejectsSecurityMissingRSAWhenSignEnabled 确保启用签名时必须配置 RSA 材料。
func TestValidateConfigRejectsSecurityMissingRSAWhenSignEnabled(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	// AES/HMAC 主密钥先通过校验，才能断言后续 RSA 材料缺失。
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		SignStatus:    1,
		CryptoStatus:  0,
		Versions:      []config.SecuritySecretKeyVersionConfig{{KeyVersion: "v1", AESKey: "1234567890123456"}},
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "rsa_public_key_user") {
		t.Fatalf("期望缺少 RSA 公钥错误，实际为 %v", err)
	}
}

// TestValidateConfigRejectsSecurityMissingAESWhenSignEnabled 确保 H 类型签名不会到运行期才因主密钥缺失失败。
func TestValidateConfigRejectsSecurityMissingAESWhenSignEnabled(t *testing.T) {
	userPublicPEM, serverPrivatePEM := testSecurityRSAKeys(t)
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		SignStatus:    1,
		CryptoStatus:  0,
		Versions: []config.SecuritySecretKeyVersionConfig{{
			KeyVersion:          "v1",
			RSAPublicKeyUser:    userPublicPEM,
			RSAPrivateKeyServer: serverPrivatePEM,
		}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected sign-enabled security without aes material to be rejected")
	}
}

// TestValidateConfigRejectsSecurityMissingAESWhenCryptoEnabled 确保启用加密时必须配置 AES 材料。
func TestValidateConfigRejectsSecurityMissingAESWhenCryptoEnabled(t *testing.T) {
	userPublicPEM, serverPrivatePEM := testSecurityRSAKeys(t)
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		SignStatus:    1,
		CryptoStatus:  1,
		Versions: []config.SecuritySecretKeyVersionConfig{{
			KeyVersion:          "v1",
			RSAPublicKeyUser:    userPublicPEM,
			RSAPrivateKeyServer: serverPrivatePEM,
		}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected crypto-enabled security without aes material to be rejected")
	}
}

// TestValidateConfigRequiresSignatureBeforeCrypto 确保可单独验签，但禁止未验签密文进入解密器。
func TestValidateConfigRequiresSignatureBeforeCrypto(t *testing.T) {
	for _, baseCfg := range []config.Config{validBootstrapConfig(), validProductionBootstrapConfig()} {
		cfg := baseCfg
		cfg.AppID = "demo-app"
		cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
		cfg.Security.SecretKey.SignStatus = 1
		cfg.Security.SecretKey.CryptoStatus = 0
		if err := Validate(cfg); err != nil {
			t.Fatalf("mode %q sign-only error = %v", cfg.Mode, err)
		}

		cfg.Security.SecretKey.SignStatus = 0
		cfg.Security.SecretKey.CryptoStatus = 1
		if err := Validate(cfg); err == nil {
			t.Fatalf("mode %q crypto-only config must be rejected", cfg.Mode)
		}
	}
}

// TestValidateConfigAcceptsEmptySecurity 确保空安全段保持关闭且不要求密钥材料。
func TestValidateConfigAcceptsEmptySecurity(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() empty security error = %v", err)
	}
}

// TestValidateConfigRejectsEnabledSecurityWithoutMaterials 防止显式开启安全链后因空材料被当作关闭配置。
func TestValidateConfigRejectsEnabledSecurityWithoutMaterials(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{SignStatus: 1, CryptoStatus: 1}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected enabled security without version and key materials to be rejected")
	}
}

// TestValidateConfigAcceptsSecurityVersionSet 确保只有一个版本的规范 versions 配置可通过启动校验。
func TestValidateConfigAcceptsSecurityVersionSet(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestValidateConfigAcceptsSecurityMultiVersionGray 确保多版本稳定和灰度选路配置可通过生产校验。
func TestValidateConfigAcceptsSecurityMultiVersionGray(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		GrayVersion:   "v2",
		GrayPercent:   50,
		GraySalt:      "prod-gray-salt",
		SignStatus:    1,
		CryptoStatus:  1,
		Versions: []config.SecuritySecretKeyVersionConfig{
			validSecuritySecretKeyVersion(t, "v1"),
			validSecuritySecretKeyVersion(t, "v2"),
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestValidateConfigAcceptsPEMPlaceholderSubstring 确保合法 PEM 内的随机文本不会被普通密钥占位词规则误杀。
func TestValidateConfigAcceptsPEMPlaceholderSubstring(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
	publicBlock, rest := pem.Decode([]byte(cfg.Security.SecretKey.Versions[0].RSAPublicKeyUser))
	if publicBlock == nil || len(rest) != 0 {
		t.Fatal("test RSA public key should be a single PEM block")
	}
	publicBlock.Headers = map[string]string{"Comment": "todo"}
	cfg.Security.SecretKey.Versions[0].RSAPublicKeyUser = string(pem.EncodeToMemory(publicBlock))
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestValidateConfigRejectsWeakRSAKeys 确保启动校验不会接受可被正常解析但强度不足的 RSA 材料。
func TestValidateConfigRejectsWeakRSAKeys(t *testing.T) {
	weakPublicPEM, weakPrivatePEM := testSecurityRSAKeysWithBits(t, 1024)
	t.Run("用户公钥", func(t *testing.T) {
		cfg := validBootstrapConfig()
		cfg.AppID = "demo-app"
		cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
		cfg.Security.SecretKey.Versions[0].RSAPublicKeyUser = weakPublicPEM
		if err := Validate(cfg); err == nil {
			t.Fatal("expected weak user RSA public key to be rejected")
		}
	})
	t.Run("服务端私钥", func(t *testing.T) {
		cfg := validBootstrapConfig()
		cfg.AppID = "demo-app"
		cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
		cfg.Security.SecretKey.Versions[0].RSAPrivateKeyServer = weakPrivatePEM
		if err := Validate(cfg); err == nil {
			t.Fatal("expected weak server RSA private key to be rejected")
		}
	})
}

// TestValidateConfigRejectsMismatchedServerRSAKeyPair 确保显式对外公钥始终对应实际响应签名私钥。
func TestValidateConfigRejectsMismatchedServerRSAKeyPair(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
	otherPublicPEM, _ := testSecurityRSAKeys(t)
	cfg.Security.SecretKey.Versions[0].RSAPublicKeyServer = otherPublicPEM
	if err := Validate(cfg); err == nil {
		t.Fatal("expected mismatched server RSA key pair to be rejected")
	}
}

// TestValidateConfigRejectsSecurityDuplicateVersion 确保重复版本不能通过启动校验。
func TestValidateConfigRejectsSecurityDuplicateVersion(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		SignStatus:    0,
		CryptoStatus:  0,
		Versions: []config.SecuritySecretKeyVersionConfig{
			{KeyVersion: "v1"},
			{KeyVersion: "v1"},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected duplicate security key version to be rejected")
	}
}

// TestValidateConfigRejectsNonCanonicalSecurityVersions 确保版本选路不会按 trim 后的另一套名称运行。
func TestValidateConfigRejectsNonCanonicalSecurityVersions(t *testing.T) {
	tests := []struct {
		name string                                // 子场景名称
		edit func(*config.SecuritySecretKeyConfig) // 注入非规范版本字段
	}{
		{name: "stable version 首尾空白", edit: func(cfg *config.SecuritySecretKeyConfig) { cfg.StableVersion = " v1" }},
		{name: "key version 首尾空白", edit: func(cfg *config.SecuritySecretKeyConfig) { cfg.Versions[0].KeyVersion = "v1 " }},
		{name: "gray version 首尾空白", edit: func(cfg *config.SecuritySecretKeyConfig) {
			cfg.GrayVersion = " v2"
			cfg.Versions = append(cfg.Versions, config.SecuritySecretKeyVersionConfig{KeyVersion: "v2"})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBootstrapConfig()
			cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
				StableVersion: "v1",
				SignStatus:    0,
				CryptoStatus:  0,
				Versions:      []config.SecuritySecretKeyVersionConfig{{KeyVersion: "v1"}},
			}
			tt.edit(&cfg.Security.SecretKey)
			if err := Validate(cfg); err == nil {
				t.Fatal("expected non-canonical security version to be rejected")
			}
		})
	}
}

// TestValidateConfigRejectsSecurityUnknownStableVersion 确保稳定版本必须存在于版本材料中。
func TestValidateConfigRejectsSecurityUnknownStableVersion(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v2",
		SignStatus:    0,
		CryptoStatus:  0,
		Versions: []config.SecuritySecretKeyVersionConfig{
			{KeyVersion: "v1"},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected unknown stable security key version to be rejected")
	}
}

// TestValidateConfigRejectsProductionSecurityGrayWithoutSalt 确保生产灰度秘钥必须配置哈希盐值。
func TestValidateConfigRejectsProductionSecurityGrayWithoutSalt(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.AppID = "demo-app"
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{
		StableVersion: "v1",
		GrayVersion:   "v2",
		GrayPercent:   10,
		SignStatus:    1,
		CryptoStatus:  1,
		Versions: []config.SecuritySecretKeyVersionConfig{
			validSecuritySecretKeyVersion(t, "v1"),
			validSecuritySecretKeyVersion(t, "v2"),
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected production gray security key without salt to be rejected")
	}
}

// validSecuritySecretKey 构造只包含一个版本的规范安全配置。
func validSecuritySecretKey(t *testing.T, version string) config.SecuritySecretKeyConfig {
	versionCfg := validSecuritySecretKeyVersion(t, version)
	return config.SecuritySecretKeyConfig{
		StableVersion: version,
		SignStatus:    1,
		CryptoStatus:  1,
		Versions:      []config.SecuritySecretKeyVersionConfig{versionCfg},
	}
}

// validSecuritySecretKeyVersion 构造完整版本材料。
func validSecuritySecretKeyVersion(t *testing.T, version string) config.SecuritySecretKeyVersionConfig {
	userPublicPEM, serverPrivatePEM := testSecurityRSAKeys(t)
	return config.SecuritySecretKeyVersionConfig{
		KeyVersion:          version,
		AESKey:              "1234567890123456",
		RSAPublicKeyUser:    userPublicPEM,
		RSAPrivateKeyServer: serverPrivatePEM,
	}
}

// testSecurityRSAKeys 生成测试用 RSA 公钥和私钥 PEM。
func testSecurityRSAKeys(t *testing.T) (string, string) {
	return testSecurityRSAKeysWithBits(t, 2048)
}

// testSecurityRSAKeysWithBits 按指定模数位数生成测试材料，用于验证最低安全位数。
func testSecurityRSAKeysWithBits(t *testing.T, bits int) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	publicBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes})
	return string(publicPEM), string(privatePEM)
}
