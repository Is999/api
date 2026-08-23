package configload

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"api/internal/bootstrap/configload/runtimefile"

	"github.com/Is999/go-utils/errors"
)

// BundleFingerprint 读取主文件及其声明的外置文件，供 watcher 判断是否需要完整重载。
func BundleFingerprint(file string) (string, error) {
	cfg, mainFingerprint, err := loadBaseConfig(file)
	if err != nil {
		if mainFingerprint != "" {
			// 非法内容仍有指纹，由完整 Load 记录具体错误；读取失败则直接返回。
			return mainFingerprint, nil
		}
		return "", errors.Tag(err)
	}
	parts := []string{mainFingerprint}
	// 外置路径必须来自上述主文件字节，不能再次解析另一版本的主配置。
	for _, include := range runtimefile.IncludePaths(file, cfg.ConfigFiles) {
		_, fingerprint, innerErr := runtimefile.ReadSource(include)
		if innerErr != nil {
			return "", errors.Wrapf(innerErr, "读取外部配置文件指纹失败 file=%s", include)
		}
		parts = append(parts, fingerprint)
	}
	return strings.Join(parts, "\n"), nil
}

// Version 只压缩已读取的配置包指纹，不再访问文件，避免旧配置绑定新文件版本。
func Version(fingerprint string) string {
	sum := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(sum[:8])
}
