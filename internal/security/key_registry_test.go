package security

import (
	"hash/fnv"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// registryTestSigner 用标识区分测试中的预编译签名器实例。
type registryTestSigner struct {
	id string // 嵌入签名结果以断言实际命中的版本实例
}

// Sign 返回包含实例标识的测试签名，不执行真实密码学运算。
func (s *registryTestSigner) Sign(data string) (string, error) {
	return s.id + ":" + data, nil
}

// Verify 校验测试签名是否来自当前实例。
func (s *registryTestSigner) Verify(data, sign string) (bool, error) {
	return sign == s.id+":"+data, nil
}

// registryTestCryptor 用标识区分测试中的预编译加解密器实例。
type registryTestCryptor struct {
	id string // 嵌入密文前缀以断言实际命中的版本实例
}

// Encrypt 返回可逆测试密文，不执行真实密码学运算。
func (c *registryTestCryptor) Encrypt(data string) (string, error) {
	return c.id + ":" + data, nil
}

// Decrypt 只接受当前实例生成的测试密文。
func (c *registryTestCryptor) Decrypt(data string) (string, error) {
	prefix := c.id + ":"
	if !strings.HasPrefix(data, prefix) {
		return "", nil
	}
	return strings.TrimPrefix(data, prefix), nil
}

// TestKeyRegistryPreservesExplicitAndGrayRouting 固定显式版本优先与 FNV 灰度选路协议。
func TestKeyRegistryPreservesExplicitAndGrayRouting(t *testing.T) {
	// 三个版本分别覆盖稳定、灰度和显式非当前版本选路。
	stableSigner := &registryTestSigner{id: "stable"}
	graySigner := &registryTestSigner{id: "gray"}
	nonCurrentSigner := &registryTestSigner{id: "non-current"}
	stableCryptor := &registryTestCryptor{id: "stable"}
	grayCryptor := &registryTestCryptor{id: "gray"}
	registry := NewKeyRegistry(KeyRoute{
		AppID:         "site-a",
		StableVersion: "v1",
		GrayVersion:   "v2",
		GrayPercent:   50,
		GraySalt:      "release-salt",
		SignEnabled:   true,
		CryptoEnabled: true,
	}, map[string]KeyVersion{
		"v1": {HMACSigner: stableSigner, AESCryptor: stableCryptor},
		"v2": {HMACSigner: graySigner, AESCryptor: grayCryptor},
		"v3": {HMACSigner: nonCurrentSigner},
	})

	// 显式版本优先于灰度键，未指定版本才按 FNV 分桶。
	signer, version, err := registry.Signer("site-a", "v3", "ignored", SignatureTypeHMAC)
	if err != nil || version != "v3" || signer != nonCurrentSigner {
		t.Fatalf("explicit Signer() = %T, %q, %v", signer, version, err)
	}
	grayKey, stableKey := registryGrayKeys("release-salt", 50)
	signer, version, err = registry.Signer("site-a", "", grayKey, SignatureTypeHMAC)
	if err != nil || version != "v2" || signer != graySigner {
		t.Fatalf("gray Signer() = %T, %q, %v", signer, version, err)
	}
	cryptor, version, err := registry.Cryptor("site-a", "", stableKey, CryptoTypeAES)
	if err != nil || version != "v1" || cryptor != stableCryptor {
		t.Fatalf("stable Cryptor() = %T, %q, %v", cryptor, version, err)
	}
	// 未知版本和错误 AppID 都必须返回明确选路错误。
	if _, _, err = registry.Signer("site-a", "missing", "", SignatureTypeHMAC); err == nil || !strings.Contains(err.Error(), "未找到版本: missing") {
		t.Fatalf("unknown version error = %v", err)
	}
	if _, _, err = registry.Signer("other-site", "", "", SignatureTypeHMAC); err == nil || !strings.Contains(err.Error(), "AppID未命中") {
		t.Fatalf("wrong AppID error = %v", err)
	}
}

// TestKeyRegistryPreservesGrayBoundaries 固定 0% 走稳定版、100% 走灰度版的边界。
func TestKeyRegistryPreservesGrayBoundaries(t *testing.T) {
	stableSigner := &registryTestSigner{id: "stable"}
	graySigner := &registryTestSigner{id: "gray"}
	versions := map[string]KeyVersion{
		"v1": {HMACSigner: stableSigner},
		"v2": {HMACSigner: graySigner},
	}
	route := KeyRoute{AppID: "site-a", StableVersion: "v1", GrayVersion: "v2"}

	registry := NewKeyRegistry(route, versions)
	signer, version, err := registry.Signer("site-a", "", "", SignatureTypeHMAC)
	if err != nil || version != "v1" || signer != stableSigner {
		t.Fatalf("0%% Signer() = %T, %q, %v", signer, version, err)
	}
	route.GrayPercent = 100
	registry = NewKeyRegistry(route, versions)
	signer, version, err = registry.Signer("site-a", "", "", SignatureTypeHMAC)
	if err != nil || version != "v2" || signer != graySigner {
		t.Fatalf("100%% Signer() = %T, %q, %v", signer, version, err)
	}
}

// TestKeyRegistryCopiesVersionIndex 确保调用方修改输入 map 后不会替换运行期对象。
func TestKeyRegistryCopiesVersionIndex(t *testing.T) {
	original := &registryTestSigner{id: "original"}
	versions := map[string]KeyVersion{"v1": {HMACSigner: original}}
	route := KeyRoute{AppID: "site-a", StableVersion: "v1"}
	registry := NewKeyRegistry(route, versions)

	versions["v1"] = KeyVersion{HMACSigner: &registryTestSigner{id: "changed"}}
	delete(versions, "v1")
	route.StableVersion = "changed"
	signer, version, err := registry.Signer("site-a", "", "", SignatureTypeHMAC)
	if err != nil || version != "v1" || signer != original {
		t.Fatalf("Signer() after input mutation = %T, %q, %v", signer, version, err)
	}
	resolvedRoute, err := registry.Route("site-a")
	if err != nil || resolvedRoute.StableVersion != "v1" {
		t.Fatalf("Route() after input mutation = %#v, %v", resolvedRoute, err)
	}
}

// TestKeyRegistrySharesCryptoObjectsConcurrently 验证注册表中的对称与非对称实现可被并发请求只读复用。
func TestKeyRegistrySharesCryptoObjectsConcurrently(t *testing.T) {
	hmacSigner, err := NewHMACSigner("1234567890123456")
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	aesCryptor, err := NewAESGCMCipher("1234567890123456")
	if err != nil {
		t.Fatalf("NewAESGCMCipher() error = %v", err)
	}
	_, serverPrivatePEM, serverPublicPEM := testRSAKeyPair(t)
	_, clientPrivatePEM, clientPublicPEM := testRSAKeyPair(t)
	serverSigner, err := NewRSASigner(serverPrivatePEM, clientPublicPEM)
	if err != nil {
		t.Fatalf("NewRSASigner(server) error = %v", err)
	}
	clientSigner, err := NewRSASigner(clientPrivatePEM, serverPublicPEM)
	if err != nil {
		t.Fatalf("NewRSASigner(client) error = %v", err)
	}
	serverCryptor, err := NewRSACipher(serverPrivatePEM, clientPublicPEM)
	if err != nil {
		t.Fatalf("NewRSACipher(server) error = %v", err)
	}
	clientCryptor, err := NewRSACipher(clientPrivatePEM, serverPublicPEM)
	if err != nil {
		t.Fatalf("NewRSACipher(client) error = %v", err)
	}
	registry := NewKeyRegistry(KeyRoute{AppID: "site-a", StableVersion: "v1"}, map[string]KeyVersion{
		"v1": {HMACSigner: hmacSigner, RSASigner: serverSigner, AESCryptor: aesCryptor, RSACryptor: serverCryptor},
	})

	var group sync.WaitGroup
	for index := range 32 {
		group.Go(func() {
			value := "payload-" + strconv.Itoa(index)
			// 对称签名与加密对象在并发请求间只读共享，每次调用仍创建独立运算状态。
			resolvedHMAC, _, lookupErr := registry.Signer("site-a", "", "", SignatureTypeHMAC)
			if lookupErr != nil {
				t.Errorf("Signer(HMAC) error = %v", lookupErr)
				return
			}
			signature, signErr := resolvedHMAC.Sign(value)
			if signErr != nil {
				t.Errorf("HMAC Sign() error = %v", signErr)
				return
			}
			verified, verifyErr := resolvedHMAC.Verify(value, signature)
			if verifyErr != nil || !verified {
				t.Errorf("HMAC Verify() = %t, %v", verified, verifyErr)
				return
			}
			resolvedAES, _, lookupErr := registry.Cryptor("site-a", "", "", CryptoTypeAES)
			if lookupErr != nil {
				t.Errorf("Cryptor(AES) error = %v", lookupErr)
				return
			}
			ciphertext, encryptErr := resolvedAES.Encrypt(value)
			if encryptErr != nil {
				t.Errorf("AES Encrypt() error = %v", encryptErr)
				return
			}
			plain, decryptErr := resolvedAES.Decrypt(ciphertext)
			if decryptErr != nil || plain != value {
				t.Errorf("AES Decrypt() = %q, %v", plain, decryptErr)
				return
			}

			// 两组独立 RSA 密钥覆盖客户端请求和服务端响应两个真实方向。
			resolvedRSA, _, lookupErr := registry.Signer("site-a", "", "", SignatureTypeRSA)
			if lookupErr != nil {
				t.Errorf("Signer(RSA) error = %v", lookupErr)
				return
			}
			requestSignature, signErr := clientSigner.Sign(value)
			if signErr != nil {
				t.Errorf("client Sign() error = %v", signErr)
				return
			}
			verified, verifyErr = resolvedRSA.Verify(value, requestSignature)
			if verifyErr != nil || !verified {
				t.Errorf("server Verify() = %t, %v", verified, verifyErr)
				return
			}
			responseSignature, signErr := resolvedRSA.Sign(value)
			if signErr != nil {
				t.Errorf("server Sign() error = %v", signErr)
				return
			}
			verified, verifyErr = clientSigner.Verify(value, responseSignature)
			if verifyErr != nil || !verified {
				t.Errorf("client Verify() = %t, %v", verified, verifyErr)
				return
			}

			resolvedRSACryptor, _, lookupErr := registry.Cryptor("site-a", "", "", CryptoTypeRSA)
			if lookupErr != nil {
				t.Errorf("Cryptor(RSA) error = %v", lookupErr)
				return
			}
			requestCiphertext, encryptErr := clientCryptor.Encrypt(value)
			if encryptErr != nil {
				t.Errorf("client Encrypt() error = %v", encryptErr)
				return
			}
			plain, decryptErr = resolvedRSACryptor.Decrypt(requestCiphertext)
			if decryptErr != nil || plain != value {
				t.Errorf("server Decrypt() = %q, %v", plain, decryptErr)
				return
			}
			responseCiphertext, encryptErr := resolvedRSACryptor.Encrypt(value)
			if encryptErr != nil {
				t.Errorf("server Encrypt() error = %v", encryptErr)
				return
			}
			plain, decryptErr = clientCryptor.Decrypt(responseCiphertext)
			if decryptErr != nil || plain != value {
				t.Errorf("client Decrypt() = %q, %v", plain, decryptErr)
			}
		})
	}
	group.Wait()
}

// registryGrayKeys 返回当前比例下各一个灰度和稳定分桶键，避免把实现结果写死为偶然字符串。
func registryGrayKeys(salt string, percent int) (string, string) {
	grayKey := ""
	stableKey := ""
	for index := range 1000 {
		candidate := "user-" + strconv.Itoa(index)
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(salt + ":" + candidate))
		if int(hash.Sum32()%100) < percent {
			grayKey = candidate
		} else {
			stableKey = candidate
		}
		if grayKey != "" && stableKey != "" {
			return grayKey, stableKey
		}
	}
	return grayKey, stableKey
}
