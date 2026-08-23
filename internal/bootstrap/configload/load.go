package configload

import (
	"api/internal/bootstrap/configload/runtimefile"
	"api/internal/bootstrap/configload/validators"
	"api/internal/config"
	"api/internal/security"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/conf"
)

// Load 返回同轮配置、包指纹和编译的只读密钥；版本不能另行读取文件计算。
func Load(file string) (config.Config, string, *security.KeyRegistry, error) {
	c, fingerprint, err := loadBaseConfig(file)
	if err != nil {
		return config.Config{}, "", nil, errors.Tag(err)
	}
	// config_files 在默认值补齐前合并，全部来源共用一次最终校验。
	includeFingerprint, err := runtimefile.Apply(file, &c)
	if err != nil {
		return config.Config{}, "", nil, errors.Tag(err)
	}
	if includeFingerprint != "" {
		fingerprint += "\n" + includeFingerprint
	}
	Normalize(&c)
	// 默认值固定后再编译密钥，返回配置与注册表保持同一快照。
	securityKeys, err := validateAndCompileSecurity(c)
	if err != nil {
		return config.Config{}, "", nil, errors.Tag(err)
	}
	return c, fingerprint, securityKeys, nil
}

// Normalize 补齐运行默认值，避免启动期依赖拿到空参数。
func Normalize(c *config.Config) {
	if c == nil {
		return
	}
	if c.Name == "" {
		c.Name = "api"
	}
	// 未指定观测服务名时继承服务名称，环境只能来自顶层 Mode。
	if c.Observability.ServiceName == "" {
		c.Observability.ServiceName = c.Name
	}
	c.Observability.Environment = c.Mode
	if c.JwtExpiresIn == 0 {
		c.JwtExpiresIn = config.DefaultJWTExpiresInSeconds
	}
	if c.Auth.Issuer == "" {
		c.Auth.Issuer = c.Name
	}
	// 登录态默认与JWT同寿命，避免有效Token先遇到已清理的会话。
	if c.Auth.SessionTTLSeconds == 0 {
		c.Auth.SessionTTLSeconds = c.JwtExpiresIn
	}
	if c.Auth.ProfileCacheTTLSeconds == 0 {
		c.Auth.ProfileCacheTTLSeconds = config.DefaultProfileCacheTTLSeconds
	}
	// 最小密码长度为 0 时采用 8 个 Unicode 字符，字节上限由请求校验约束。
	if c.Auth.PasswordMinLength == 0 {
		c.Auth.PasswordMinLength = 8
	}
	if c.User.RouteShardCount == 0 {
		c.User.RouteShardCount = defaultUserRouteShardCount
	}
}

// loadBaseConfig 只读取主配置文件，不处理 config_files 引用。
func loadBaseConfig(file string) (config.Config, string, error) {
	content, fingerprint, err := runtimefile.ReadSource(file)
	if err != nil {
		return config.Config{}, "", errors.Tag(err)
	}
	// 严格字段校验与 go-zero 默认值解码必须使用同一份内容。
	if err = validators.ValidateKnownYAMLFields(content, config.Config{}); err != nil {
		return config.Config{}, fingerprint, errors.Wrapf(err, "配置字段校验失败 file=%s", file)
	}
	var c config.Config
	if err = conf.LoadFromYamlBytes(content, &c); err != nil {
		return config.Config{}, fingerprint, errors.Tag(err)
	}
	return c, fingerprint, nil
}
