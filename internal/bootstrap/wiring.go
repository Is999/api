package bootstrap

import (
	"context"

	"api/internal/bootstrap/configload"
	"api/internal/config"
	"api/internal/security"

	"github.com/Is999/go-utils/errors"
)

// LoadConfig 读取配置、生成版本，并返回同轮校验编译的安全密钥快照。
func LoadConfig(file string) (config.Config, string, *security.KeyRegistry, error) {
	c, fingerprint, securityKeys, err := configload.Load(file)
	if err != nil {
		return config.Config{}, "", nil, errors.Tag(err)
	}
	return c, configload.Version(fingerprint), securityKeys, nil
}

// Wire 作为应用装配入口，统一负责读取配置并构建 App。
func Wire(ctx context.Context, configFile string) (*App, error) {
	cfg, version, securityKeys, err := LoadConfig(configFile)
	if err != nil {
		return nil, errors.Tag(err)
	}
	app, err := New(ctx, cfg, version, securityKeys)
	if err != nil {
		return nil, errors.Tag(err)
	}
	app.ConfigFile = configFile
	return app, nil
}
