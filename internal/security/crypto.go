package security

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"

	"github.com/Is999/go-utils/errors"
)

// 安全算法请求头短码常量。
const (
	// SignatureTypeHMAC 表示 HMAC-SHA256 签名方式，对应 X-Signature=H。
	SignatureTypeHMAC = "H"
	// SignatureTypeRSA 表示 RSA-SHA256 签名方式，对应 X-Signature=R。
	SignatureTypeRSA = "R"

	// CryptoTypeAES 表示 AES-256-GCM 加解密方式，对应 X-Crypto=A。
	CryptoTypeAES = "A"
	// CryptoTypeRSA 表示 RSA-OAEP-SHA256 加解密方式，对应 X-Crypto=R。
	CryptoTypeRSA = "R"

	// MinRSAKeyBits 是签名与字段加密允许的最小 RSA 模数位数，低于该值的配置会在启动期被拒绝。
	MinRSAKeyBits = 2048

	symmetricEncryptionKeyInfo = "field-encryption:aes-gcm:v1"    // AES-GCM 子密钥用途，防止同一主密钥跨算法复用。
	symmetricSigningKeyInfo    = "field-signature:hmac-sha256:v1" // HMAC 子密钥用途，与加密子密钥保持域隔离。
	rsaOAEPLabel               = "field-encryption:rsa-oaep:v1"   // OAEP 标签属于密文协议，客户端必须使用相同字节串。
)

var aesGCMAdditionalData = []byte("field-encryption:aes-gcm:v1") // 固定附加数据用于拒绝跨协议重放密文。

// ResolveSignatureType 返回请求声明的签名短码，空值按协议默认使用 RSA。
func ResolveSignatureType(value string) string {
	if value == "" {
		return SignatureTypeRSA
	}
	return value
}

// ResolveCryptoType 返回请求声明的加解密短码，空值按协议默认使用 AES-GCM。
func ResolveCryptoType(value string) string {
	if value == "" {
		return CryptoTypeAES
	}
	return value
}

// Signer 定义签名与验签能力。
type Signer interface {
	Sign(data string) (string, error)       // Sign 对待签名字符串生成签名值
	Verify(data, sign string) (bool, error) // Verify 校验待签名字符串与签名值是否匹配
}

// Cryptor 定义字段级认证加密能力。
type Cryptor interface {
	Encrypt(data string) (string, error) // Encrypt 输出包含随机 nonce 或 OAEP 随机量的 base64 密文
	Decrypt(data string) (string, error) // Decrypt 仅返回通过认证的明文，畸形密文使用统一错误
}

// AESGCMCipher 使用随机 nonce 的 AES-GCM 保护字段机密性与完整性。
type AESGCMCipher struct {
	aead cipher.AEAD // aead 由主密钥派生的独立加密子密钥构造，可安全并发调用。
}

// NewAESGCMCipher 从配置主密钥派生 AES-256-GCM 子密钥。
func NewAESGCMCipher(masterKey string) (*AESGCMCipher, error) {
	// 加密与签名使用不同 HKDF 用途标签，不能直接复用主密钥。
	key, err := deriveSymmetricKey(masterKey, symmetricEncryptionKeyInfo)
	if err != nil {
		return nil, errors.Tag(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.Wrap(err, "初始化AES-GCM失败")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.Wrap(err, "初始化AES-GCM认证模式失败")
	}
	return &AESGCMCipher{aead: aead}, nil
}

// Encrypt 为每个字段生成独立随机 nonce，并把 nonce、密文和认证标签一起编码。
func (c *AESGCMCipher) Encrypt(data string) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("AES-GCM加密器未初始化")
	}
	// 每次字段加密独立取随机 nonce，同一主密钥下不得重复。
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.Wrap(err, "生成AES-GCM nonce失败")
	}
	// nonce 前置便于客户端拆包，附加数据绑定当前字段加密协议。
	sealed := c.aead.Seal(nonce, nonce, []byte(data), aesGCMAdditionalData)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt 对 base64、nonce 长度和 GCM 标签使用同一个外部错误，避免暴露解析阶段。
func (c *AESGCMCipher) Decrypt(data string) (string, error) {
	if c == nil || c.aead == nil {
		return "", errors.New("AES-GCM加密器未初始化")
	}
	ciphertext, ok := decodeCanonicalBase64(data)
	if !ok || len(ciphertext) < c.aead.NonceSize()+c.aead.Overhead() {
		return "", errors.New("AES-GCM密文认证失败")
	}
	nonce := ciphertext[:c.aead.NonceSize()]
	// 认证成功前不向业务层暴露任何明文；标签错误与编码错误保持相同外部结果。
	plaintext, err := c.aead.Open(nil, nonce, ciphertext[c.aead.NonceSize():], aesGCMAdditionalData)
	if err != nil {
		return "", errors.New("AES-GCM密文认证失败")
	}
	return string(plaintext), nil
}

// HMACSigner 使用与 AES-GCM 分离的派生子密钥生成 HMAC-SHA256。
type HMACSigner struct {
	key []byte // key 是 HKDF 派生的 32 字节签名子密钥，不直接保存配置主密钥。
}

// NewHMACSigner 从配置主密钥派生 HMAC-SHA256 子密钥。
func NewHMACSigner(masterKey string) (*HMACSigner, error) {
	key, err := deriveSymmetricKey(masterKey, symmetricSigningKeyInfo)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return &HMACSigner{key: key}, nil
}

// Sign 生成固定 32 字节摘要并输出规范 base64。
func (s *HMACSigner) Sign(data string) (string, error) {
	if s == nil || len(s.key) == 0 {
		return "", errors.New("HMAC签名器未初始化")
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Verify 严格解码签名并使用常量时间比较，畸形值按验签失败处理。
func (s *HMACSigner) Verify(data, sign string) (bool, error) {
	if s == nil || len(s.key) == 0 {
		return false, errors.New("HMAC签名器未初始化")
	}
	actual, ok := decodeCanonicalBase64(sign)
	if !ok || len(actual) != sha256.Size {
		return false, nil
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(data))
	return hmac.Equal(actual, mac.Sum(nil)), nil
}

// RSASigner 实现 RSA SHA256 签名与验签能力。
type RSASigner struct {
	privateKey *rsa.PrivateKey // 服务端私钥只读共享，用于响应签名
	publicKey  *rsa.PublicKey  // 用户公钥只读共享，用于请求验签
}

// NewRSASigner 创建 RSA 签名器。
func NewRSASigner(privatePEM string, publicPEM string) (*RSASigner, error) {
	var privateKey *rsa.PrivateKey
	if privatePEM != "" {
		parsed, err := ParseRSAPrivateKey(privatePEM)
		if err != nil {
			return nil, errors.Wrap(err, "校验RSA签名私钥失败")
		}
		privateKey = parsed
	}
	var publicKey *rsa.PublicKey
	if publicPEM != "" {
		parsed, err := ParseRSAPublicKey(publicPEM)
		if err != nil {
			return nil, errors.Wrap(err, "校验RSA验签公钥失败")
		}
		publicKey = parsed
	}
	return NewRSASignerFromKeys(privateKey, publicKey), nil
}

// NewRSASignerFromKeys 复用启动期已校验的 RSA 密钥，避免再次解析 PEM。
func NewRSASignerFromKeys(privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey) *RSASigner {
	return &RSASigner{privateKey: privateKey, publicKey: publicKey}
}

// Sign 使用服务端私钥对待签名字符串做 RSA-SHA256 签名。
func (s *RSASigner) Sign(data string) (string, error) {
	if s == nil || s.privateKey == nil {
		return "", errors.New("RSA私钥未配置")
	}
	digest := sha256.Sum256([]byte(data))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.Tag(err)
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// Verify 使用用户公钥校验 RSA-SHA256 签名。
func (s *RSASigner) Verify(data, sign string) (bool, error) {
	if s == nil || s.publicKey == nil {
		return false, errors.New("RSA公钥未配置")
	}
	signature, err := base64.StdEncoding.DecodeString(sign)
	if err != nil {
		return false, errors.Tag(err)
	}
	digest := sha256.Sum256([]byte(data))
	if err := rsa.VerifyPKCS1v15(s.publicKey, crypto.SHA256, digest[:], signature); err != nil {
		// 正常签名不匹配属于 false，而非依赖故障；格式损坏和其他异常仍保留 error。
		if errors.Is(err, rsa.ErrVerification) {
			return false, nil
		}
		return false, errors.Tag(err)
	}
	return true, nil
}

// RSACipher 使用 RSA-OAEP-SHA256 分段保护字段密文。
type RSACipher struct {
	privateKey *rsa.PrivateKey // 服务端私钥仅用于解密请求字段
	publicKey  *rsa.PublicKey  // 用户公钥仅用于加密响应字段
}

// NewRSACipher 创建使用 OAEP-SHA256 的 RSA 加解密器，不接受其它密文协议。
func NewRSACipher(privatePEM string, publicPEM string) (*RSACipher, error) {
	var privateKey *rsa.PrivateKey
	if privatePEM != "" {
		parsed, err := ParseRSAPrivateKey(privatePEM)
		if err != nil {
			return nil, errors.Wrap(err, "初始化RSA-OAEP解密私钥失败")
		}
		privateKey = parsed
	}
	var publicKey *rsa.PublicKey
	if publicPEM != "" {
		parsed, err := ParseRSAPublicKey(publicPEM)
		if err != nil {
			return nil, errors.Wrap(err, "初始化RSA-OAEP加密公钥失败")
		}
		publicKey = parsed
	}
	return NewRSACipherFromKeys(privateKey, publicKey), nil
}

// NewRSACipherFromKeys 复用启动期已校验的 RSA 密钥，避免再次解析 PEM。
func NewRSACipherFromKeys(privateKey *rsa.PrivateKey, publicKey *rsa.PublicKey) *RSACipher {
	return &RSACipher{privateKey: privateKey, publicKey: publicKey}
}

// Encrypt 使用 OAEP-SHA256 按密钥容量分段，密文块保持固定长度便于严格解码。
func (c *RSACipher) Encrypt(data string) (string, error) {
	if c == nil || c.publicKey == nil {
		return "", errors.New("RSA公钥未配置")
	}
	plaintext := []byte(data)
	if len(plaintext) == 0 {
		return "", errors.New("RSA加密明文不能为空")
	}
	// OAEP 的两份 SHA-256 摘要和固定开销占用密钥容量，按剩余字节切块。
	maxChunk := c.publicKey.Size() - 2*sha256.Size - 2
	if maxChunk <= 0 {
		return "", errors.New("RSA密钥长度不足以使用OAEP-SHA256")
	}
	encrypted := make([]byte, 0, ((len(plaintext)+maxChunk-1)/maxChunk)*c.publicKey.Size())
	for offset := 0; offset < len(plaintext); offset += maxChunk {
		end := min(offset+maxChunk, len(plaintext))
		chunk, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, c.publicKey, plaintext[offset:end], []byte(rsaOAEPLabel))
		if err != nil {
			return "", errors.Wrap(err, "RSA-OAEP加密失败")
		}
		encrypted = append(encrypted, chunk...)
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}

// Decrypt 严格按私钥块长解码 OAEP，任何格式或认证失败都返回同一错误。
func (c *RSACipher) Decrypt(data string) (string, error) {
	if c == nil || c.privateKey == nil {
		return "", errors.New("RSA私钥未配置")
	}
	ciphertext, ok := decodeCanonicalBase64(data)
	blockSize := c.privateKey.Size()
	if !ok || len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return "", errors.New("RSA-OAEP密文认证失败")
	}
	plaintext := make([]byte, 0, len(ciphertext))
	// 所有块都解密成功后才交付完整明文，后续坏块不能让前面已解出的片段进入业务层。
	for offset := 0; offset < len(ciphertext); offset += blockSize {
		chunk, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, c.privateKey, ciphertext[offset:offset+blockSize], []byte(rsaOAEPLabel))
		if err != nil {
			return "", errors.New("RSA-OAEP密文认证失败")
		}
		plaintext = append(plaintext, chunk...)
	}
	return string(plaintext), nil
}

// ParseRSAPrivateKey 解析 PKCS#1 或 PKCS#8 私钥 PEM。
func ParseRSAPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.Errorf("RSA私钥PEM格式不合法")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return prepareRSAPrivateKey(key)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.Tag(err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.Errorf("PEM内容不是RSA私钥")
	}
	return prepareRSAPrivateKey(key)
}

// prepareRSAPrivateKey 在启动期完成私钥完整性校验和 CRT 预计算，请求期只读复用结果。
func prepareRSAPrivateKey(key *rsa.PrivateKey) (*rsa.PrivateKey, error) {
	if key == nil {
		return nil, errors.New("RSA私钥未初始化")
	}
	key.Precompute()
	if err := key.Validate(); err != nil {
		return nil, errors.Tag(err)
	}
	if key.N.BitLen() < MinRSAKeyBits {
		return nil, errors.Errorf("RSA私钥长度不能低于%d位", MinRSAKeyBits)
	}
	return key, nil
}

// ParseRSAPublicKey 解析 PKIX 或 PKCS#1 公钥 PEM。
func ParseRSAPublicKey(pemText string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.Errorf("RSA公钥PEM格式不合法")
	}
	if parsed, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PublicKey)
		if !ok {
			return nil, errors.Errorf("PEM内容不是RSA公钥")
		}
		if key.N.BitLen() < MinRSAKeyBits {
			return nil, errors.Errorf("RSA公钥长度不能低于%d位", MinRSAKeyBits)
		}
		return key, nil
	}
	key, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if key.N.BitLen() < MinRSAKeyBits {
		return nil, errors.Errorf("RSA公钥长度不能低于%d位", MinRSAKeyBits)
	}
	return key, nil
}

// deriveSymmetricKey 校验主密钥长度并通过 HKDF-SHA256 生成独立 32 字节子密钥。
func deriveSymmetricKey(masterKey string, info string) ([]byte, error) {
	if len(masterKey) != 16 && len(masterKey) != 24 && len(masterKey) != 32 {
		return nil, errors.New("对称主密钥长度必须是16、24或32字节")
	}
	key, err := hkdf.Key(sha256.New, []byte(masterKey), nil, info, 32)
	if err != nil {
		return nil, errors.Wrap(err, "派生安全子密钥失败")
	}
	return key, nil
}

// decodeCanonicalBase64 只接受无首尾空白且可往返的标准 base64，避免多种文本表示。
func decodeCanonicalBase64(value string) ([]byte, bool) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	return decoded, err == nil && base64.StdEncoding.EncodeToString(decoded) == value
}
