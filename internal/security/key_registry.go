package security

import (
	"hash/fnv"
	"maps"

	"github.com/Is999/go-utils/errors"
)

// KeyRoute 保存当前 AppID 的版本选路和安全链路开关，创建后只读共享。
type KeyRoute struct {
	AppID         string // AppID 是请求必须精确匹配的接入应用标识
	StableVersion string // StableVersion 是未命中灰度时使用的版本
	GrayVersion   string // GrayVersion 为空时关闭灰度选路
	GrayPercent   int    // GrayPercent 是 0-100 的稳定哈希命中比例
	GraySalt      string // GraySalt 隔离不同部署的灰度分桶结果
	SignEnabled   bool   // SignEnabled 控制请求验签和响应回签
	CryptoEnabled bool   // CryptoEnabled 控制请求解密和响应加密
}

// KeyVersion 保存单个版本预编译的四种安全实现，底层对象可并发只读复用。
type KeyVersion struct {
	HMACSigner Signer  // HMACSigner 使用派生后的签名子密钥，不保存主密钥明文
	RSASigner  Signer  // RSASigner 同时持有请求验签公钥和响应签名私钥
	AESCryptor Cryptor // AESCryptor 复用启动期初始化的 AES-GCM 实例
	RSACryptor Cryptor // RSACryptor 同时持有响应加密公钥和请求解密私钥
}

// KeyRegistry 保存启动期编译的密钥快照，运行期不会重新读取配置或文件。
type KeyRegistry struct {
	route    KeyRoute              // route 在创建时按值复制，热加载不会改变当前进程选路
	versions map[string]KeyVersion // versions 的 key 是精确版本号，value 是预编译安全实现
}

// NewKeyRegistry 复制已完成启动校验的版本索引，避免调用方后续修改 map 影响运行快照。
func NewKeyRegistry(route KeyRoute, versions map[string]KeyVersion) *KeyRegistry {
	return &KeyRegistry{route: route, versions: maps.Clone(versions)}
}

// Route 按 AppID 返回只读选路快照，空值和非精确匹配均失败关闭。
func (r *KeyRegistry) Route(appID string) (KeyRoute, error) {
	if r == nil || appID == "" || r.route.AppID == "" || appID != r.route.AppID {
		return KeyRoute{}, errors.Errorf("AppID未命中配置文件秘钥: %s", appID)
	}
	return r.route, nil
}

// Signer 返回命中版本的预编译签名器；显式版本优先于灰度选路。
func (r *KeyRegistry) Signer(appID, versionHint, grayKey, signatureType string) (Signer, string, error) {
	if signatureType != SignatureTypeHMAC && signatureType != SignatureTypeRSA {
		return nil, "", errors.New("签名方式不合法")
	}
	version, item, err := r.resolve(appID, versionHint, grayKey)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	if signatureType == SignatureTypeHMAC {
		if item.HMACSigner == nil {
			return nil, "", errors.New("HMAC签名器未初始化")
		}
		return item.HMACSigner, version, nil
	}
	if item.RSASigner == nil {
		return nil, "", errors.New("RSA签名器未初始化")
	}
	return item.RSASigner, version, nil
}

// Cryptor 返回命中版本的预编译加解密器；同一对象同时服务请求和响应方向。
func (r *KeyRegistry) Cryptor(appID, versionHint, grayKey, cryptoType string) (Cryptor, string, error) {
	if cryptoType != CryptoTypeAES && cryptoType != CryptoTypeRSA {
		return nil, "", errors.New("加密方式不合法")
	}
	version, item, err := r.resolve(appID, versionHint, grayKey)
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	if cryptoType == CryptoTypeAES {
		if item.AESCryptor == nil {
			return nil, "", errors.New("AES-GCM加解密器未初始化")
		}
		return item.AESCryptor, version, nil
	}
	if item.RSACryptor == nil {
		return nil, "", errors.New("RSA加解密器未初始化")
	}
	return item.RSACryptor, version, nil
}

// resolve 按原始请求头精确选择版本，空提示才进入稳定哈希灰度。
func (r *KeyRegistry) resolve(appID, versionHint, grayKey string) (string, KeyVersion, error) {
	if _, err := r.Route(appID); err != nil {
		return "", KeyVersion{}, errors.Tag(err)
	}
	// 显式版本严格按原值命中，未知值不能降级到稳定版本掩盖客户端配置错误。
	version := versionHint
	if version == "" {
		// 缺少显式版本时使用稳定哈希，保证相同灰度键在不同实例得到一致结果。
		version = r.route.StableVersion
		if r.route.GrayVersion != "" && hitKeyGray(grayKey, r.route.GraySalt, r.route.GrayPercent) {
			version = r.route.GrayVersion
		}
	}
	if version == "" {
		return "", KeyVersion{}, errors.New("秘钥版本不能为空")
	}
	item, ok := r.versions[version]
	if !ok {
		return "", KeyVersion{}, errors.Errorf("配置文件security.secret_key.versions未找到版本: %s", version)
	}
	return version, item, nil
}

// hitKeyGray 使用固定 FNV-1a 协议计算 0-99 分桶，空灰度键按 default 处理。
func hitKeyGray(grayKey, salt string, percent int) bool {
	if percent <= 0 {
		return false
	}
	if percent >= 100 {
		return true
	}
	if grayKey == "" {
		grayKey = "default"
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(salt + ":" + grayKey))
	return int(hash.Sum32()%100) < percent
}
