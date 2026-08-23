package validators

import (
	"crypto/rsa"
	"strconv"
	"strings"

	"api/internal/config"
	"api/internal/security"

	"github.com/Is999/go-utils/errors"
)

// 安全配置校验边界常量。
const (
	maxSecurityKeyVersionLength = 64 // 秘钥版本号最大长度，保持请求头和配置一致
)

// securitySecretKeyVersionItem 绑定秘钥版本配置和来源路径，便于精准报错。
type securitySecretKeyVersionItem struct {
	source string                                // source 表示配置来源路径，便于启动报错定位
	value  config.SecuritySecretKeyVersionConfig // value 表示待校验的单版本秘钥材料
}

// CompileSecurityRegistry 从安全配置编译不可变密钥注册表，空安全配置返回 nil。
func CompileSecurityRegistry(c config.Config) (*security.KeyRegistry, error) {
	secretCfg := c.Security.SecretKey
	if configSecuritySecretKeyIsEmpty(secretCfg) {
		return nil, nil
	}
	if c.AppID == "" {
		return nil, errors.Errorf("security.secret_key 已配置时 app_id 不能为空")
	}
	production := config.IsProductionMode(c.Mode)
	versionItems, err := validateSecuritySecretKeyRoute(secretCfg, production)
	if err != nil {
		return nil, errors.Tag(err)
	}

	signEnabled := secretCfg.SignStatus == 1
	cryptoEnabled := secretCfg.CryptoStatus == 1
	versions := make(map[string]security.KeyVersion, len(versionItems))
	// 所有版本在启动期一次编译，避免请求期读取或解析密钥。
	for _, item := range versionItems {
		compiled, err := compileSecuritySecretKeyVersion(item, signEnabled, cryptoEnabled, production)
		if err != nil {
			return nil, errors.Tag(err)
		}
		versions[item.value.KeyVersion] = compiled
	}
	return security.NewKeyRegistry(security.KeyRoute{
		AppID:         c.AppID,
		StableVersion: secretCfg.StableVersion,
		GrayVersion:   secretCfg.GrayVersion,
		GrayPercent:   secretCfg.GrayPercent,
		GraySalt:      secretCfg.GraySalt,
		SignEnabled:   signEnabled,
		CryptoEnabled: cryptoEnabled,
	}, versions), nil
}

// validateSecuritySecretKeyRoute 校验稳定版本、灰度版本和链路开关是否自洽。
func validateSecuritySecretKeyRoute(secretCfg config.SecuritySecretKeyConfig, production bool) ([]securitySecretKeyVersionItem, error) {
	if err := validateSecuritySwitch("security.secret_key.sign_status", secretCfg.SignStatus); err != nil {
		return nil, errors.Tag(err)
	}
	if err := validateSecuritySwitch("security.secret_key.crypto_status", secretCfg.CryptoStatus); err != nil {
		return nil, errors.Tag(err)
	}
	if secretCfg.CryptoStatus == 1 && secretCfg.SignStatus != 1 {
		return nil, errors.Errorf("security.secret_key.crypto_status=1 时 sign_status 必须为 1，密文必须在解密前先验签")
	}
	if secretCfg.GrayPercent < 0 || secretCfg.GrayPercent > 100 {
		return nil, errors.Errorf("security.secret_key.gray_percent 必须在 0-100 之间")
	}

	// 稳定版本承接未携带显式版本的请求，必须先确认其材料完整存在。
	stableVersion := secretCfg.StableVersion
	if stableVersion == "" {
		return nil, errors.Errorf("security.secret_key.stable_version 不能为空")
	}
	if err := validateSecurityKeyVersion("security.secret_key.stable_version", stableVersion); err != nil {
		return nil, errors.Tag(err)
	}

	versionItems, err := configSecurityVersionItems(secretCfg)
	if err != nil {
		return nil, errors.Tag(err)
	}
	versionByName := make(map[string]securitySecretKeyVersionItem, len(versionItems))
	for _, item := range versionItems {
		version := item.value.KeyVersion
		if err := validateSecurityKeyVersion(item.source+".key_version", version); err != nil {
			return nil, errors.Tag(err)
		}
		if _, exists := versionByName[version]; exists {
			return nil, errors.Errorf("security.secret_key 版本[%s]重复配置", version)
		}
		versionByName[version] = item
	}
	if _, ok := versionByName[stableVersion]; !ok {
		return nil, errors.Errorf("security.secret_key 稳定版本[%s]不存在", stableVersion)
	}

	// 灰度仅影响未指定版本的自动选路，显式请求仍可使用任一已配置版本。
	grayVersion := secretCfg.GrayVersion
	if grayVersion != "" {
		if err := validateSecurityKeyVersion("security.secret_key.gray_version", grayVersion); err != nil {
			return nil, errors.Tag(err)
		}
		if _, ok := versionByName[grayVersion]; !ok {
			return nil, errors.Errorf("security.secret_key 灰度版本[%s]不存在", grayVersion)
		}
	}
	if secretCfg.GrayPercent > 0 {
		if grayVersion == "" {
			return nil, errors.Errorf("security.secret_key.gray_percent 大于 0 时必须配置 gray_version")
		}
		if grayVersion == stableVersion {
			return nil, errors.Errorf("security.secret_key.gray_version 不能与稳定版本相同")
		}
		if production && strings.TrimSpace(secretCfg.GraySalt) == "" {
			return nil, errors.Errorf("生产环境启用秘钥灰度时必须配置 security.secret_key.gray_salt")
		}
	}
	return versionItems, nil
}

// validateSecuritySwitch 校验安全链路开关只能使用 0 或 1。
func validateSecuritySwitch(name string, value int) error {
	if value != 0 && value != 1 {
		return errors.Errorf("%s 只能是 0 或 1", name)
	}
	return nil
}

// validateSecurityKeyVersion 校验秘钥版本号格式，避免请求头选路出现歧义。
func validateSecurityKeyVersion(name string, version string) error {
	if version == "" {
		return errors.Errorf("%s 不能为空", name)
	}
	if len(version) > maxSecurityKeyVersionLength {
		return errors.Errorf("%s 长度不能超过 %d", name, maxSecurityKeyVersionLength)
	}
	if strings.ContainsAny(version, " \t\r\n") || strings.ContainsAny(version, "*?") {
		return errors.Errorf("%s 不能包含空白或通配符", name)
	}
	return nil
}

// compileSecuritySecretKeyVersion 单次读取并预编译一个版本的全部安全实现。
func compileSecuritySecretKeyVersion(item securitySecretKeyVersionItem, signEnabled bool, cryptoEnabled bool, production bool) (security.KeyVersion, error) {
	if !signEnabled && !cryptoEnabled {
		return security.KeyVersion{}, nil
	}
	versionCfg := item.value
	// 任一安全链启用时都在启动期读取并校验对称主密钥。
	key, err := resolveSecuritySecretText(versionCfg.AESKey, versionCfg.AESKeyRef, item.source+".aes_key", production)
	if err != nil {
		return security.KeyVersion{}, errors.Wrap(err, "对称主密钥不可用")
	}
	if len(key) != 16 && len(key) != 24 && len(key) != 32 {
		return security.KeyVersion{}, errors.Errorf("%s 对称主密钥长度必须是16、24或32字节", item.source)
	}
	// AES-GCM 与 HMAC 构造器分别派生用途隔离的子密钥。
	aesCryptor, err := security.NewAESGCMCipher(key)
	if err != nil {
		return security.KeyVersion{}, errors.Wrap(err, "AES-GCM加解密器初始化失败")
	}
	hmacSigner, err := security.NewHMACSigner(key)
	if err != nil {
		return security.KeyVersion{}, errors.Wrap(err, "HMAC-SHA256签名器初始化失败")
	}
	// 用户公钥只用于请求验签和响应加密。
	userPublicKey, err := resolveSecurityRSAPublicKey(versionCfg.RSAPublicKeyUser, versionCfg.RSAPublicKeyUserRef, item.source+".rsa_public_key_user")
	if err != nil {
		return security.KeyVersion{}, errors.Wrap(err, "用户 RSA公钥不可用")
	}
	// 服务端私钥只用于请求解密和响应签名。
	serverPrivateKey, err := resolveSecurityRSAPrivateKey(versionCfg.RSAPrivateKeyServer, versionCfg.RSAPrivateKeyServerRef, item.source+".rsa_private_key_server")
	if err != nil {
		return security.KeyVersion{}, errors.Wrap(err, "服务端 RSA私钥不可用")
	}
	if hasSecuritySecretValue(versionCfg.RSAPublicKeyServer, versionCfg.RSAPublicKeyServerRef) {
		// 可选服务端公钥只用于校验发布材料与私钥是否配对。
		serverPublicKey, err := resolveSecurityRSAPublicKey(versionCfg.RSAPublicKeyServer, versionCfg.RSAPublicKeyServerRef, item.source+".rsa_public_key_server")
		if err != nil {
			return security.KeyVersion{}, errors.Wrap(err, "服务端 RSA公钥不可用")
		}
		if serverPublicKey.E != serverPrivateKey.PublicKey.E || serverPublicKey.N.Cmp(serverPrivateKey.PublicKey.N) != 0 {
			return security.KeyVersion{}, errors.Errorf("%s 服务端 RSA公钥与私钥不匹配", item.source)
		}
	}
	return security.KeyVersion{
		HMACSigner: hmacSigner,
		RSASigner:  security.NewRSASignerFromKeys(serverPrivateKey, userPublicKey),
		AESCryptor: aesCryptor,
		RSACryptor: security.NewRSACipherFromKeys(serverPrivateKey, userPublicKey),
	}, nil
}

// resolveSecuritySecretText 读取明文或文件引用秘钥，并按调用方要求拒绝普通文本占位值。
func resolveSecuritySecretText(value string, ref string, name string, rejectPlaceholder bool) (string, error) {
	if value != "" && ref != "" {
		return "", errors.Errorf("%s 只能配置明文或文件引用之一", name)
	}
	if value != "" {
		if rejectPlaceholder && isPlaceholderSecret(value) {
			return "", errors.Errorf("生产环境 %s 不能使用占位值", name)
		}
		return value, nil
	}
	if ref == "" {
		return "", errors.Errorf("%s 未配置", name)
	}
	if strings.TrimSpace(ref) != ref {
		return "", errors.Errorf("%s 文件引用不能包含首尾空白", name)
	}
	// 文件引用只在启动编译阶段读取。
	body, err := security.ReadSecretFile(ref)
	if err != nil {
		return "", errors.Wrapf(err, "读取 %s 文件失败", name)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return "", errors.Errorf("%s 文件内容为空", name)
	}
	if rejectPlaceholder && isPlaceholderSecret(text) {
		return "", errors.Errorf("生产环境 %s 不能使用占位值", name)
	}
	return text, nil
}

// resolveSecurityRSAPublicKey 读取 RSA 公钥并校验格式和最低安全位数。
func resolveSecurityRSAPublicKey(value string, ref string, name string) (*rsa.PublicKey, error) {
	// PEM 的随机 Base64 不参与普通文本占位词扫描。
	text, err := resolveSecuritySecretText(value, ref, name, false)
	if err != nil {
		return nil, errors.Tag(err)
	}
	key, err := security.ParseRSAPublicKey(text)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return key, nil
}

// resolveSecurityRSAPrivateKey 读取 RSA 私钥并校验格式、完整性和最低安全位数。
func resolveSecurityRSAPrivateKey(value string, ref string, name string) (*rsa.PrivateKey, error) {
	// PEM 的随机 Base64 不参与普通文本占位词扫描。
	text, err := resolveSecuritySecretText(value, ref, name, false)
	if err != nil {
		return nil, errors.Tag(err)
	}
	key, err := security.ParseRSAPrivateKey(text)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return key, nil
}

// configSecurityVersionItems 绑定版本材料及其配置路径，供启动错误精确定位。
func configSecurityVersionItems(secretCfg config.SecuritySecretKeyConfig) ([]securitySecretKeyVersionItem, error) {
	items := make([]securitySecretKeyVersionItem, 0, len(secretCfg.Versions))
	for index, versionCfg := range secretCfg.Versions {
		items = append(items, securitySecretKeyVersionItem{
			source: "security.secret_key.versions[" + strconv.Itoa(index) + "]",
			value:  versionCfg,
		})
	}
	if len(items) == 0 {
		return nil, errors.Errorf("security.secret_key 至少需要一个秘钥版本")
	}
	return items, nil
}

// configSecuritySecretKeyIsEmpty 判断配置文件秘钥段是否完全未填写。
func configSecuritySecretKeyIsEmpty(secretCfg config.SecuritySecretKeyConfig) bool {
	return secretCfg.SignStatus == 0 &&
		secretCfg.CryptoStatus == 0 &&
		secretCfg.StableVersion == "" &&
		secretCfg.GrayVersion == "" &&
		secretCfg.GrayPercent == 0 &&
		secretCfg.GraySalt == "" &&
		len(secretCfg.Versions) == 0
}

// hasSecuritySecretValue 判断一个秘钥字段是否配置了明文或文件引用。
func hasSecuritySecretValue(value string, ref string) bool {
	return value != "" || ref != ""
}
