package configload

import (
	"net/netip"
	"strings"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/rest"
)

// validateInternalServer 校验独立内网监听器，防止内网路由重新暴露到公网入口。
func validateInternalServer(public rest.RestConf, internal config.InternalServerConfig, mode string) error {
	host := strings.TrimSpace(internal.Host)
	if host == "" || host != internal.Host {
		return errors.Errorf("internal_server.host 必须配置且不能包含首尾空白")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return errors.Wrap(err, "internal_server.host 必须使用明确 IP")
	}
	addr = addr.Unmap()
	if !addr.IsLoopback() && !addr.IsPrivate() && !addr.IsUnspecified() {
		return errors.Errorf("internal_server.host 只能使用回环或私有 IP")
	}
	if internal.Port <= 0 || internal.Port > 65535 {
		return errors.Errorf("internal_server.port 必须在 1-65535 之间")
	}
	if internal.Port == public.Port {
		return errors.Errorf("internal_server.port 不能与公网 HTTP 端口相同")
	}
	tlsEnabled, err := internalServerTLSEnabled(internal)
	if err != nil {
		return errors.Tag(err)
	}
	if config.IsProductionMode(mode) {
		if addr.IsUnspecified() {
			return errors.Errorf("生产环境 internal_server.host 不能使用通配监听地址")
		}
		if !addr.IsLoopback() && !tlsEnabled {
			return errors.Errorf("生产环境跨主机 internal_server 必须启用 mTLS")
		}
	}
	return nil
}

// internalServerTLSEnabled 校验 mTLS 三个文件必须同时配置。
func internalServerTLSEnabled(cfg config.InternalServerConfig) (bool, error) {
	files := []string{
		cfg.CertFile,
		cfg.KeyFile,
		cfg.ClientCAFile,
	}
	configured := 0
	for index, file := range files {
		if file != strings.TrimSpace(file) {
			return false, errors.Errorf("internal_server TLS 文件路径[%d]不能包含首尾空白", index)
		}
		if file != "" {
			configured++
		}
	}
	if configured != 0 && configured != len(files) {
		return false, errors.Errorf("internal_server.cert_file、key_file、client_ca_file 必须同时配置")
	}
	return configured == len(files), nil
}
