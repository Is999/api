package bootstrap

import (
	"crypto/tls"
	"crypto/x509"
	"os"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/rest"
)

// newInternalServer 创建只承载内网路由的独立 HTTP Server。
func newInternalServer(c config.Config) (*rest.Server, error) {
	restConf := c.RestConf
	restConf.Name = c.Name + "-internal"
	restConf.Host = c.InternalServer.Host
	restConf.Port = c.InternalServer.Port
	restConf.CertFile = c.InternalServer.CertFile
	restConf.KeyFile = c.InternalServer.KeyFile
	restConf.Middlewares.Log = false
	// 内网同样由项目 Trace 继承请求链路，避免框架提前覆盖 X-Trace-Id。
	restConf.Middlewares.Trace = false

	options := make([]rest.RunOption, 0, 1)
	if c.InternalServer.ClientCAFile != "" {
		// 证书在监听前读取校验，文件错误由 bootstrap 返回而不是延迟到 TLS 启动。
		if _, err := tls.LoadX509KeyPair(restConf.CertFile, restConf.KeyFile); err != nil {
			return nil, errors.Wrap(err, "加载内网服务端证书失败")
		}
		tlsConfig, err := internalServerTLSConfig(c.InternalServer.ClientCAFile)
		if err != nil {
			return nil, errors.Tag(err)
		}
		options = append(options, rest.WithTLSConfig(tlsConfig))
	}
	server, err := rest.NewServer(restConf, options...)
	if err != nil {
		return nil, errors.Wrapf(err, "创建内网 HTTP 服务失败 host=%s port=%d", restConf.Host, restConf.Port)
	}
	return server, nil
}

// internalServerTLSConfig 加载客户端 CA，并强制校验 mTLS 客户端证书。
func internalServerTLSConfig(clientCAFile string) (*tls.Config, error) {
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, errors.Wrap(err, "读取内网客户端 CA 文件失败")
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("内网客户端 CA 文件不包含有效证书")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  clientCAs,
	}, nil
}
