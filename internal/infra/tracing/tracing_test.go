package tracing

import (
	"context"
	"testing"

	"api/internal/config"

	"go.opentelemetry.io/otel"
)

// TestSetupDisabledSkipsExporter 验证关闭 trace 时不会连接合法但不可达的 OTLP 端点。
func TestSetupDisabledSkipsExporter(t *testing.T) {
	shutdown, err := Setup(context.Background(), config.ObservabilityConfig{
		TraceEnabled: false,
		OTLPProtocol: "grpc",
		OTLPEndpoint: "blackhole.invalid:4317",
		SampleRatio:  1,
	})
	if err != nil {
		t.Fatalf("Setup(disabled) error = %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
}

// TestSetupRejectsNonCanonicalEndpoint 确保 URL、路径和空白地址不会形成多套 endpoint 语义。
func TestSetupRejectsNonCanonicalEndpoint(t *testing.T) {
	for _, endpoint := range []string{" http://127.0.0.1:4318 ", "http://127.0.0.1:4318", "127.0.0.1:4318/v1/traces", "127.0.0.1"} {
		if _, err := Setup(context.Background(), config.ObservabilityConfig{OTLPEndpoint: endpoint}); err == nil {
			t.Fatalf("Setup(endpoint=%q) expected error", endpoint)
		}
	}
}

// TestSetupPreservesZeroSampleRatio 验证显式零采样不会被静默改成全量采样。
func TestSetupPreservesZeroSampleRatio(t *testing.T) {
	shutdown, err := Setup(context.Background(), config.ObservabilityConfig{
		TraceEnabled: true,
		SampleRatio:  0,
	})
	if err != nil {
		t.Fatalf("Setup(sample=0) error = %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	_, span := otel.Tracer("test").Start(context.Background(), "zero-sample")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("sample_ratio=0 should not record spans")
	}
}

// TestSetupRejectsOTLPProtocolAlias 确保直接装配也只接受 grpc/http 规范值。
func TestSetupRejectsOTLPProtocolAlias(t *testing.T) {
	if _, err := Setup(context.Background(), config.ObservabilityConfig{
		TraceEnabled: true,
		OTLPProtocol: "http/protobuf",
		OTLPEndpoint: "127.0.0.1:4318",
		SampleRatio:  1,
	}); err == nil {
		t.Fatal("Setup() 应拒绝 OTLP 协议别名")
	}
}
