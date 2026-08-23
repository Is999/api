package tracing

import (
	"context"
	"net"
	"strconv"
	"strings"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Setup 初始化 OpenTelemetry provider。
func Setup(ctx context.Context, cfg config.ObservabilityConfig) (func(context.Context) error, error) {
	serviceName := cfg.ServiceName
	if serviceName != strings.TrimSpace(serviceName) {
		return nil, errors.Errorf("observability.service_name 不能包含首尾空白")
	}
	if serviceName == "" {
		serviceName = "api"
	}
	environment := cfg.Environment
	if environment != strings.TrimSpace(environment) {
		return nil, errors.Errorf("observability.environment 不能包含首尾空白")
	}
	if environment == "" {
		environment = "unknown"
	}
	protocol := cfg.OTLPProtocol
	if protocol == "" {
		protocol = "grpc"
	}
	if protocol != "grpc" && protocol != "http" {
		return nil, errors.Errorf("不支持的 otlp_protocol: %s", cfg.OTLPProtocol)
	}
	if err := validateOTLPEndpoint(cfg.OTLPEndpoint); err != nil {
		return nil, errors.Tag(err)
	}

	resource, err := sdkresource.New(ctx,
		sdkresource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("deployment.environment", environment),
		),
	)
	if err != nil {
		return nil, errors.Wrap(err, "构建 OTEL 资源失败")
	}

	sampleRatio := cfg.SampleRatio
	if sampleRatio < 0 || sampleRatio > 1 {
		return nil, errors.Errorf("observability.sample_ratio 必须在 0-1 之间")
	}
	if !cfg.TraceEnabled {
		// 关闭 tracing 时仍注册 provider，但强制使用零采样率。
		sampleRatio = 0
	}

	options := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(sampleRatio)),
		sdktrace.WithResource(resource),
	}
	// 未配置端点时保留本地 span 上下文，不创建网络导出器。
	if cfg.TraceEnabled && cfg.OTLPEndpoint != "" {
		switch protocol {
		case "grpc":
			exporterOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint)}
			if cfg.OTLPInsecure {
				exporterOpts = append(exporterOpts, otlptracegrpc.WithInsecure())
			}
			exporter, err := otlptracegrpc.New(ctx, exporterOpts...)
			if err != nil {
				return nil, errors.Wrap(err, "初始化 OTLP 导出器失败")
			}
			options = append(options, sdktrace.WithBatcher(exporter))
		case "http":
			exporterOpts := []otlptracehttp.Option{
				otlptracehttp.WithEndpoint(cfg.OTLPEndpoint),
				otlptracehttp.WithURLPath("/v1/traces"),
			}
			if cfg.OTLPInsecure {
				exporterOpts = append(exporterOpts, otlptracehttp.WithInsecure())
			}
			exporter, err := otlptracehttp.New(ctx, exporterOpts...)
			if err != nil {
				return nil, errors.Wrap(err, "初始化 OTLP 导出器失败")
			}
			options = append(options, sdktrace.WithBatcher(exporter))
		}
	}

	// exporter 初始化成功后再替换全局 provider，避免失败启动污染进程状态。
	tp := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	// 调用方在资源关闭阶段执行 Shutdown，刷出尚未导出的批量 span。
	return tp.Shutdown, nil
}

// validateOTLPEndpoint 校验直接装配也只能使用唯一的 host:port 地址形态。
func validateOTLPEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	if endpoint != strings.TrimSpace(endpoint) {
		return errors.Errorf("observability.otlp_endpoint 不能包含首尾空白")
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return errors.Errorf("observability.otlp_endpoint 必须使用 host:port 形态")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.Errorf("observability.otlp_endpoint 端口必须在 1-65535 之间")
	}
	return nil
}
