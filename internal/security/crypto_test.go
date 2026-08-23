package security

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

// TestAESGCMCipherRoundTripAndTamperResistance 验证重复加密结果不同，并拒绝篡改标签或缺少 GCM 结构的密文。
func TestAESGCMCipherRoundTripAndTamperResistance(t *testing.T) {
	cipherObj, err := NewAESGCMCipher("1234567890123456")
	if err != nil {
		t.Fatalf("NewAESGCMCipher() error = %v", err)
	}
	first, err := cipherObj.Encrypt("sensitive-value")
	if err != nil {
		t.Fatalf("Encrypt(first) error = %v", err)
	}
	second, err := cipherObj.Encrypt("sensitive-value")
	if err != nil {
		t.Fatalf("Encrypt(second) error = %v", err)
	}
	if first == second {
		t.Fatal("AES-GCM 重复明文不应生成相同密文")
	}
	plain, err := cipherObj.Decrypt(first)
	if err != nil || plain != "sensitive-value" {
		t.Fatalf("Decrypt() = %q, %v", plain, err)
	}

	tampered, err := base64.StdEncoding.DecodeString(first)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	tampered[len(tampered)-1] ^= 1
	if plain, err = cipherObj.Decrypt(base64.StdEncoding.EncodeToString(tampered)); err == nil || plain != "" {
		t.Fatalf("tampered Decrypt() = %q, %v, want uniform rejection", plain, err)
	}
	// 只有块对齐数据并不构成 GCM 密文，缺少 nonce 和认证标签必须拒绝。
	legacyBlock := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err = cipherObj.Decrypt(legacyBlock); err == nil {
		t.Fatal("旧 AES-CBC 密文不应被新协议接受")
	}
}

// TestHMACSignerRoundTripAndTamperResistance 验证 HMAC 拒绝非法编码、错误长度和被修改的数据，不测量比较耗时。
func TestHMACSignerRoundTripAndTamperResistance(t *testing.T) {
	signer, err := NewHMACSigner("1234567890123456")
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	signature, err := signer.Sign("canonical-data")
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if ok, err := signer.Verify("canonical-data", signature); err != nil || !ok {
		t.Fatalf("Verify(valid) = %t, %v", ok, err)
	}
	for _, value := range []string{signature + " ", "%%%", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if ok, err := signer.Verify("canonical-data", value); err != nil || ok {
			t.Fatalf("Verify(%q) = %t, %v, want false", value, ok, err)
		}
	}
	if ok, err := signer.Verify("changed-data", signature); err != nil || ok {
		t.Fatalf("Verify(tampered data) = %t, %v, want false", ok, err)
	}
}

// TestRSACipherOAEPOnly 验证 OAEP-SHA256 多块往返，并拒绝篡改或其它填充算法的密文。
func TestRSACipherOAEPOnly(t *testing.T) {
	privateKey, privatePEM, publicPEM := testRSAKeyPair(t)
	cipherObj, err := NewRSACipher(privatePEM, publicPEM)
	if err != nil {
		t.Fatalf("NewRSACipher() error = %v", err)
	}
	message := strings.Repeat("multi-block-payload-", 30)
	ciphertext, err := cipherObj.Encrypt(message)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	plain, err := cipherObj.Decrypt(ciphertext)
	if err != nil || plain != message {
		t.Fatalf("Decrypt() length=%d error=%v", len(plain), err)
	}

	tampered, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	tampered[len(tampered)/2] ^= 1
	if plain, err = cipherObj.Decrypt(base64.StdEncoding.EncodeToString(tampered)); err == nil || plain != "" {
		t.Fatalf("tampered Decrypt() = %q, %v, want uniform rejection", plain, err)
	}
	legacy, err := rsa.EncryptPKCS1v15(rand.Reader, &privateKey.PublicKey, []byte("legacy-ciphertext"))
	if err != nil {
		t.Fatalf("EncryptPKCS1v15() error = %v", err)
	}
	if _, err = cipherObj.Decrypt(base64.StdEncoding.EncodeToString(legacy)); err == nil {
		t.Fatal("旧 RSA PKCS#1 v1.5 密文不应被新协议接受")
	}
}

// TestSymmetricConstructorsRejectInvalidMasterKey 确保错误长度的主密钥在启动期就被两种算法一致拒绝。
func TestSymmetricConstructorsRejectInvalidMasterKey(t *testing.T) {
	if _, err := NewAESGCMCipher("short"); err == nil {
		t.Fatal("NewAESGCMCipher() expected invalid key length error")
	}
	if _, err := NewHMACSigner("short"); err == nil {
		t.Fatal("NewHMACSigner() expected invalid key length error")
	}
}

// TestRSAConstructorsRejectWeakKeys 确保所有签名、验签、加密和解密入口都拒绝低于 2048 位的 RSA 材料。
func TestRSAConstructorsRejectWeakKeys(t *testing.T) {
	_, privatePEM, publicPEM := testRSAKeyPairWithBits(t, 1024)
	if _, err := NewRSASigner(privatePEM, ""); err == nil {
		t.Fatal("NewRSASigner(private) expected weak key error")
	}
	if _, err := NewRSASigner("", publicPEM); err == nil {
		t.Fatal("NewRSASigner(public) expected weak key error")
	}
	if _, err := NewRSACipher(privatePEM, ""); err == nil {
		t.Fatal("NewRSACipher(private) expected weak key error")
	}
	if _, err := NewRSACipher("", publicPEM); err == nil {
		t.Fatal("NewRSACipher(public) expected weak key error")
	}
}

// TestParseRSAPrivateKeyPrecomputesCRT 确保两种私钥格式都在启动解析时完成 CRT 预计算。
func TestParseRSAPrivateKeyPrecomputesCRT(t *testing.T) {
	if _, err := prepareRSAPrivateKey(nil); err == nil {
		t.Fatal("prepareRSAPrivateKey(nil) error = nil")
	}
	privateKey, _, _ := testRSAKeyPair(t)
	formats := map[string][]byte{
		"PKCS#1": x509.MarshalPKCS1PrivateKey(privateKey),
		"PKCS#8": mustMarshalPKCS8PrivateKey(t, privateKey),
	}
	for name, der := range formats {
		t.Run(name, func(t *testing.T) {
			key, err := ParseRSAPrivateKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
			if err != nil {
				t.Fatalf("ParseRSAPrivateKey() error = %v", err)
			}
			if key.Precomputed.Dp == nil || key.Precomputed.Dq == nil || key.Precomputed.Qinv == nil {
				t.Fatal("ParseRSAPrivateKey() 未在启动期生成 CRT 预计算结果")
			}
		})
	}
}

// mustMarshalPKCS8PrivateKey 生成测试所需的 PKCS#8 私钥，编码失败时终止当前用例。
func mustMarshalPKCS8PrivateKey(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}
	return der
}

// testRSAKeyPair 生成测试专用 2048 位 RSA 密钥，避免仓库固化私钥材料。
func testRSAKeyPair(t *testing.T) (*rsa.PrivateKey, string, string) {
	return testRSAKeyPairWithBits(t, MinRSAKeyBits)
}

// testRSAKeyPairWithBits 按指定模数位数生成测试密钥，用于覆盖安全边界。
func testRSAKeyPairWithBits(t *testing.T, bits int) (*rsa.PrivateKey, string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey() error = %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return privateKey, string(privatePEM), string(publicPEM)
}
