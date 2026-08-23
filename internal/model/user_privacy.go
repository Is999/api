package model

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Is999/go-utils/errors"
)

const (
	userContactKeyVersion    = "app_key_v1"             // userContactKeyVersion 表示当前联系方式静态加密密钥版本。
	userContactCipherPurpose = "user.contact.cipher.v1" // userContactCipherPurpose 用于从 app_key 派生 AES-GCM 密钥。
	userContactHashPurpose   = "user.contact.lookup.v1" // userContactHashPurpose 用于从 app_key 派生 HMAC 查询密钥。
	userContactNonceSize     = 12                       // userContactNonceSize 是 AES-GCM 推荐 nonce 字节数。
	userContactHashHexSize   = sha256.Size * 2          // userContactHashHexSize 表示 HMAC-SHA256 十六进制长度。
)

// ProtectUserContacts 将用户联系方式明文转换为库内密文、查询哈希和脱敏值。
func ProtectUserContacts(user *User, secret string) error {
	if user == nil {
		return nil
	}
	emailFields, err := buildUserContactFields(UserIdentityTypeEmail, user.Email, secret)
	if err != nil {
		return errors.Tag(err)
	}
	phoneFields, err := buildUserContactFields(UserIdentityTypePhone, user.Phone, secret)
	if err != nil {
		return errors.Tag(err)
	}
	// 两种联系方式均派生成功后再回填，失败时不会留下半份安全字段。
	user.Email = normalizeUserContact(UserIdentityTypeEmail, user.Email)
	user.Phone = normalizeUserContact(UserIdentityTypePhone, user.Phone)
	user.EmailCiphertext = emailFields.ciphertext
	user.EmailHash = emailFields.hash
	user.EmailMasked = emailFields.masked
	user.EmailKeyVersion = emailFields.keyVersion
	user.PhoneCiphertext = phoneFields.ciphertext
	user.PhoneHash = phoneFields.hash
	user.PhoneMasked = phoneFields.masked
	user.PhoneKeyVersion = phoneFields.keyVersion
	return nil
}

// ProtectUserProfileUpdates 把资料更新中的联系方式明文字段替换为安全落库字段。
func ProtectUserProfileUpdates(updates map[string]any, secret string) (map[string]any, error) {
	if len(updates) == 0 {
		return updates, nil
	}
	// 在独立映射中派生密文和索引，失败不改写调用方持有的更新参数。
	next := make(map[string]any, len(updates)+6)
	for key, value := range updates {
		switch key {
		case "email":
			fields, err := buildUserContactFields(UserIdentityTypeEmail, fmt.Sprint(value), secret)
			if err != nil {
				return nil, errors.Tag(err)
			}
			next["email_ciphertext"] = fields.ciphertext
			next["email_hash"] = fields.hash
			next["email_masked"] = fields.masked
			next["email_key_version"] = fields.keyVersion
		case "phone":
			fields, err := buildUserContactFields(UserIdentityTypePhone, fmt.Sprint(value), secret)
			if err != nil {
				return nil, errors.Tag(err)
			}
			next["phone_ciphertext"] = fields.ciphertext
			next["phone_hash"] = fields.hash
			next["phone_masked"] = fields.masked
			next["phone_key_version"] = fields.keyVersion
		default:
			next[key] = value
		}
	}
	return next, nil
}

// UserContactIdentityHash 返回邮箱或手机号精确查询使用的 HMAC 哈希。
func UserContactIdentityHash(identityType string, identityValue string, secret string) (string, error) {
	switch identityType {
	case UserIdentityTypeEmail, UserIdentityTypePhone:
	default:
		return "", errors.Errorf("身份类型[%s]不支持联系方式哈希", identityType)
	}
	value := normalizeUserContact(identityType, identityValue)
	if value == "" {
		return "", nil
	}
	return hmacUserContact(identityType, value, secret)
}

// userContactFields 表示联系方式明文派生出的安全落库字段。
type userContactFields struct {
	ciphertext string // ciphertext 表示 AES-GCM 密文。
	hash       string // hash 表示精确查询 HMAC。
	masked     string // masked 表示默认展示的脱敏值。
	keyVersion string // keyVersion 表示加密密钥版本。
}

// buildUserContactFields 生成单个联系方式的安全字段。
func buildUserContactFields(identityType string, identityValue string, secret string) (userContactFields, error) {
	value := normalizeUserContact(identityType, identityValue)
	if value == "" {
		return userContactFields{}, nil
	}
	ciphertext, err := encryptUserContact(value, secret)
	if err != nil {
		return userContactFields{}, errors.Tag(err)
	}
	hashValue, err := hmacUserContact(identityType, value, secret)
	if err != nil {
		return userContactFields{}, errors.Tag(err)
	}
	return userContactFields{
		ciphertext: ciphertext,
		hash:       hashValue,
		masked:     maskUserContact(identityType, value),
		keyVersion: userContactKeyVersion,
	}, nil
}

// normalizeUserContact 归一化联系方式，保证加密、哈希和登录查找输入一致。
func normalizeUserContact(identityType string, value string) string {
	value = strings.TrimSpace(value)
	switch identityType {
	case UserIdentityTypeEmail:
		return strings.ToLower(value)
	case UserIdentityTypePhone:
		return value
	default:
		return value
	}
}

// encryptUserContact 使用 AES-GCM 加密联系方式明文。
func encryptUserContact(value string, secret string) (string, error) {
	key, err := deriveUserContactKey(secret, userContactCipherPurpose)
	if err != nil {
		return "", errors.Tag(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", errors.Wrap(err, "初始化用户联系方式加密器失败")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", errors.Wrap(err, "初始化用户联系方式GCM失败")
	}
	nonce := make([]byte, userContactNonceSize)
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", errors.Wrap(err, "生成用户联系方式nonce失败")
	}
	// 存储格式为 nonce + 密文 + GCM 标签；密钥版本作为附加认证数据绑定。
	payload := aead.Seal(nonce, nonce, []byte(value), []byte(userContactKeyVersion))
	return base64.RawStdEncoding.EncodeToString(payload), nil
}

// hmacUserContact 生成不可逆的联系方式精确查询哈希。
func hmacUserContact(identityType string, value string, secret string) (string, error) {
	key, err := deriveUserContactKey(secret, userContactHashPurpose)
	if err != nil {
		return "", errors.Tag(err)
	}
	mac := hmac.New(sha256.New, key)
	// 类型和零字节分隔符隔离邮箱、手机号查询域，密文的随机 nonce 不参与索引。
	_, _ = mac.Write([]byte(identityType))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// deriveUserContactKey 从 app_key 派生固定用途的 32 字节密钥。
func deriveUserContactKey(secret string, purpose string) ([]byte, error) {
	if secret == "" || secret != strings.TrimSpace(secret) {
		return nil, errors.New("user 联系方式加密需要配置 app_key")
	}
	sum := sha256.Sum256([]byte(purpose + "\x00" + secret))
	return sum[:], nil
}

// maskUserContact 返回联系方式默认展示值。
func maskUserContact(identityType string, value string) string {
	switch identityType {
	case UserIdentityTypeEmail:
		return maskUserEmail(value)
	case UserIdentityTypePhone:
		return maskUserPhone(value)
	default:
		return ""
	}
}

// maskUserEmail 脱敏邮箱本地部分，域名保留便于人工识别。
func maskUserEmail(value string) string {
	value = strings.TrimSpace(value)
	at := strings.LastIndex(value, "@")
	if at <= 0 {
		return maskMiddle(value, 2, 2)
	}
	return maskMiddle(value[:at], 2, 1) + value[at:]
}

// maskUserPhone 脱敏手机号，保留前三后四。
func maskUserPhone(value string) string {
	return maskMiddle(strings.TrimSpace(value), 3, 4)
}

// maskMiddle 对字符串中段打码，按 rune 处理避免截断多字节字符。
func maskMiddle(value string, left int, right int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	total := utf8.RuneCountInString(value)
	if total <= left+right {
		// 短值不能被“保留前后缀”规则完整暴露，最多留下首字符。
		if total <= 1 {
			return "*"
		}
		left = 1
		right = 0
	}
	runes := []rune(value)
	maskedCount := max(total-left-right, 3)
	if right <= 0 {
		return string(runes[:left]) + strings.Repeat("*", maskedCount)
	}
	return string(runes[:left]) + strings.Repeat("*", maskedCount) + string(runes[total-right:])
}
