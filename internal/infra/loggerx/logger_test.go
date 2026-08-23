package loggerx

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"api/internal/requestctx"

	goerrors "github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
)

// TestInfowSkipUsesOuterCallSite 验证调用方可以按需补充封装层数。
func TestInfowSkipUsesOuterCallSite(t *testing.T) {
	// 本包用例替换进程级 logx writer，不能并行执行；结束后恢复原 writer。
	var buffer bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(wrapLogWriter(logx.NewWriter(&buffer)))
	logx.SetLevel(logx.InfoLevel)
	t.Cleanup(func() {
		logx.SetWriter(previousWriter)
		logx.SetLevel(logx.InfoLevel)
	})

	expected := logInfoCallerSkipOuterProbe(context.Background())
	output := buffer.String()
	if !strings.Contains(output, expected) {
		t.Fatalf("caller should point to outer call site %s, got %s", expected, output)
	}
}

// logInfoCallerSkipOuterProbe 返回期望的外层业务调用点。
func logInfoCallerSkipOuterProbe(ctx context.Context) string {
	_, file, line, _ := runtime.Caller(0)
	expected := shortCaller(file, line+2)
	logInfoCallerSkipInnerProbe(ctx)
	return expected
}

// logInfoCallerSkipInnerProbe 模拟业务侧再包一层 loggerx 的调用。
func logInfoCallerSkipInnerProbe(ctx context.Context) {
	InfowSkip(ctx, 1, "loggerx caller skip probe")
}

// TestGoUtilsLoggerCallerUsesSourceCallSite 验证 go-utils 适配器 caller 落在日志产生处。
func TestGoUtilsLoggerCallerUsesSourceCallSite(t *testing.T) {
	var buffer bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(wrapLogWriter(logx.NewWriter(&buffer)))
	logx.SetLevel(logx.InfoLevel)
	t.Cleanup(func() {
		logx.SetWriter(previousWriter)
		logx.SetLevel(logx.InfoLevel)
	})

	expected := logGoUtilsInfoCallerProbe()
	output := buffer.String()
	if !strings.Contains(output, expected) {
		t.Fatalf("go-utils caller should point to source call site %s, got %s", expected, output)
	}
	if strings.Contains(output, "loggerx/logger.go:") {
		t.Fatalf("go-utils caller should not point to loggerx wrapper, got %s", output)
	}
}

// logGoUtilsInfoCallerProbe 返回 go-utils 适配器应定位的日志产生点。
func logGoUtilsInfoCallerProbe() string {
	_, file, line, _ := runtime.Caller(0)
	expected := shortCaller(file, line+2)
	newGoUtilsLogger(nil).Info("go-utils caller probe")
	return expected
}

// TestInfowMovesDetailFieldsToContent 验证非公共字段进入 content，不再扩散成日志平台顶层字段。
func TestInfowMovesDetailFieldsToContent(t *testing.T) {
	// 临时接管全局 writer，并在用例结束后恢复日志环境。
	var buffer bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(wrapLogWriter(logx.NewWriter(&buffer)))
	logx.SetLevel(logx.InfoLevel)
	t.Cleanup(func() {
		logx.SetWriter(previousWriter)
		logx.SetLevel(logx.InfoLevel)
	})

	// 上下文同时包含公共索引字段和不得提升或泄漏的详情字段。
	ctx := requestctx.WithMeta(context.Background(), &requestctx.Meta{
		TraceID:    "trace-1",
		Route:      "user.detail",
		Method:     "GET",
		Path:       "/api/users/7",
		ClientIP:   "127.0.0.1",
		UserID:     7,
		UserName:   "tester",
		BizMessage: "成功",
	})
	Infow(ctx, "字段收口",
		logx.Field(fieldHTTPStatus, 200),
		logx.Field("detail", "select * from demo"),
		logx.Field("bytes", 128),
	)

	// 公共字段保留为顶层索引，其余详情收口到 content。
	entry := decodeLogEntry(t, buffer.String())
	assertEntryValue(t, entry, fieldTraceID, "trace-1")
	assertEntryValue(t, entry, fieldRoute, "user.detail")
	assertEntryValue(t, entry, fieldHTTPMethod, "GET")
	assertEntryValue(t, entry, fieldHTTPStatus, "200")
	assertEntryValue(t, entry, fieldUserID, "7")
	assertEntryMissing(t, entry, "detail")
	assertEntryMissing(t, entry, "bytes")
	assertEntryMissing(t, entry, fieldPath)
	assertEntryMissing(t, entry, fieldUserName)
	assertEntryMissing(t, entry, fieldBizMessage)
	assertEntryMissing(t, entry, fieldLogCaller)
	content := fmt.Sprint(entry["content"])
	for _, want := range []string{`detail="select * from demo"`, "bytes=128", "path=/api/users/7", "biz_message=成功"} {
		if !strings.Contains(content, want) {
			t.Fatalf("content should contain %q, got %s", want, content)
		}
	}
	// 原始 IP 和用户名属于敏感信息，整个日志文本都不得出现。
	for _, forbidden := range []string{"127.0.0.1", "tester", "user_name"} {
		if strings.Contains(buffer.String(), forbidden) {
			t.Fatalf("日志不应包含原始身份字段 %q，实际=%s", forbidden, buffer.String())
		}
	}
}

// TestBindContextKeepsOnlyPublicFields 验证原始 logx context 绑定不会重新扩散非公共字段。
func TestBindContextKeepsOnlyPublicFields(t *testing.T) {
	var buffer bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(wrapLogWriter(logx.NewWriter(&buffer)))
	logx.SetLevel(logx.InfoLevel)
	t.Cleanup(func() {
		logx.SetWriter(previousWriter)
		logx.SetLevel(logx.InfoLevel)
	})

	ctx := requestctx.WithMeta(context.Background(), &requestctx.Meta{
		TraceID:    "trace-1",
		Route:      "user.detail",
		Method:     "GET",
		Path:       "/api/users/7",
		ClientIP:   "127.0.0.1",
		UserID:     7,
		UserName:   "tester",
		BizMessage: "成功",
	})
	logx.WithContext(BindContext(ctx)).Info("原始 logx 绑定字段")

	entry := decodeLogEntry(t, buffer.String())
	assertEntryValue(t, entry, fieldTraceID, "trace-1")
	assertEntryValue(t, entry, fieldRoute, "user.detail")
	assertEntryValue(t, entry, fieldHTTPMethod, "GET")
	assertEntryValue(t, entry, fieldUserID, "7")
	assertEntryMissing(t, entry, fieldUID)
	assertEntryMissing(t, entry, fieldPath)
	assertEntryMissing(t, entry, fieldUserName)
	assertEntryMissing(t, entry, fieldBizMessage)
	if strings.Contains(buffer.String(), "127.0.0.1") || strings.Contains(buffer.String(), "tester") {
		t.Fatalf("绑定上下文不应输出原始 IP 或用户名，实际=%s", buffer.String())
	}
}

// TestTraceAttributesExcludeRawIdentity 验证 trace 只保留稳定用户 ID，不写入原始 IP 或用户名。
func TestTraceAttributesExcludeRawIdentity(t *testing.T) {
	attrs := TraceAttributesFromMeta(&requestctx.Meta{ClientIP: "127.0.0.1", UserID: 7, UserName: "tester"})
	seenUserID := false
	for _, attr := range attrs {
		key := string(attr.Key)
		if key == "enduser.id" {
			seenUserID = true
		}
		if key == "client.address" || key == "app.client_ip" || key == "enduser.name" || key == "app.user_name" {
			t.Fatalf("trace 不应包含原始身份属性: %s", key)
		}
	}
	if !seenUserID {
		t.Fatal("trace 应保留稳定用户 ID")
	}
}

// TestExplicitFieldsOverrideContextFields 验证调用点字段优先于上下文默认值。
func TestExplicitFieldsOverrideContextFields(t *testing.T) {
	var buffer bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(wrapLogWriter(logx.NewWriter(&buffer)))
	logx.SetLevel(logx.InfoLevel)
	t.Cleanup(func() {
		logx.SetWriter(previousWriter)
		logx.SetLevel(logx.InfoLevel)
	})

	ctx := requestctx.WithMeta(context.Background(), &requestctx.Meta{
		TraceID:    "trace-1",
		HTTPStatus: 200,
		BizCode:    1,
	})
	Errorw(ctx, "显式字段覆盖", stderrors.New("invalid sign"),
		logx.Field(fieldHTTPStatus, 401),
		logx.Field(fieldBizCode, 20001),
	)

	entry := decodeLogEntry(t, buffer.String())
	assertEntryValue(t, entry, fieldHTTPStatus, "401")
	assertEntryValue(t, entry, fieldBizCode, "20001")
	assertEntryValue(t, entry, fieldError, "invalid sign")
	assertEntryContains(t, entry, fieldErrorChain, "invalid sign")
	assertEntryMissing(t, entry, fieldLogCaller)
}

// TestDedupeLogCallerFieldsKeepsDifferentCaller 验证错误源和打印点不同的时候保留 log_caller。
func TestDedupeLogCallerFieldsKeepsDifferentCaller(t *testing.T) {
	fields := dedupeLogCallerFields([]logx.LogField{
		logx.Field(fieldCaller, "logic/user.go:10"),
		logx.Field(fieldLogCaller, "middleware/recover.go:20"),
	})

	got := fieldsToMap(fields)
	assertFieldValue(t, got, fieldCaller, "logic/user.go:10")
	assertFieldValue(t, got, fieldLogCaller, "middleware/recover.go:20")
}

// TestCallerDedupWriterRemovesSameLogCaller 验证 Setup 安装的 writer 包装器会去掉重复 log_caller。
func TestCallerDedupWriterRemovesSameLogCaller(t *testing.T) {
	var buffer bytes.Buffer
	writer := wrapLogWriter(logx.NewWriter(&buffer))
	writer.Info("writer caller 去重", logx.Field(fieldCaller, "logic/user.go:10"), logx.Field(fieldLogCaller, "logic/user.go:10"))

	entry := decodeLogEntry(t, buffer.String())
	assertEntryValue(t, entry, fieldCaller, "logic/user.go:10")
	assertEntryMissing(t, entry, fieldLogCaller)
}

// TestErrorFieldsIncludeTraceAndCaller 验证错误字段包含可检索的链路文本和错误源位置。
func TestErrorFieldsIncludeTraceAndCaller(t *testing.T) {
	err := tracedErrorProbe()
	fields := fieldsToMap(ErrorFields(err))

	assertFieldValue(t, fields, fieldError, "boom")
	if !json.Valid([]byte(fields[fieldErrorChain])) {
		t.Fatalf("error_chain should be valid JSON, got %s", fields[fieldErrorChain])
	}
	if !strings.Contains(fields[fieldErrorChain], `"trace"`) {
		t.Fatalf("error_chain should include trace, got %s", fields[fieldErrorChain])
	}
	if !strings.Contains(fields[fieldErrorTrace], "loggerx/logger_test.go:") {
		t.Fatalf("error_trace should include source file, got %s", fields[fieldErrorTrace])
	}
	if !strings.Contains(fields[fieldErrorCaller], "loggerx/logger_test.go:") {
		t.Fatalf("error_caller should include source file, got %s", fields[fieldErrorCaller])
	}
}

// TestErrorCallerFieldsPreferErrorSource 验证错误日志 caller 指向错误源。
func TestErrorCallerFieldsPreferErrorSource(t *testing.T) {
	err := tracedErrorProbe()
	fields := fieldsToMap(appendErrorCallerFields(nil, err, "middleware/signature.go:233"))

	if !strings.Contains(fields[fieldCaller], "loggerx/logger_test.go:") {
		t.Fatalf("caller should prefer error source, got %s", fields[fieldCaller])
	}
	assertFieldValue(t, fields, fieldLogCaller, "middleware/signature.go:233")
}

// tracedErrorProbe 构造带 go-utils trace 的测试错误。
func tracedErrorProbe() error {
	return goerrors.Tag(stderrors.New("boom"))
}

// fieldsToMap 把日志字段转成便于断言的字符串 map。
func fieldsToMap(fields []logx.LogField) map[string]string {
	items := make(map[string]string, len(fields))
	for _, field := range fields {
		items[field.Key] = fmt.Sprint(field.Value)
	}
	return items
}

// assertFieldValue 断言指定日志字段存在且值匹配。
func assertFieldValue(t *testing.T, fields map[string]string, key, want string) {
	t.Helper()
	got, ok := fields[key]
	if !ok {
		t.Fatalf("expected key %q to exist", key)
	}
	if got != want {
		t.Fatalf("expected key %q to be %q, got %q", key, want, got)
	}
}

// decodeLogEntry 解析 go-zero JSON 日志首行。
func decodeLogEntry(t *testing.T, output string) map[string]any {
	t.Helper()
	line := strings.TrimSpace(output)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	if line == "" {
		t.Fatal("expected log output")
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		t.Fatalf("decode log entry failed: %v, output=%s", err, output)
	}
	return entry
}

// assertEntryValue 断言 JSON 日志字段存在且值匹配。
func assertEntryValue(t *testing.T, entry map[string]any, key, want string) {
	t.Helper()
	got, ok := entry[key]
	if !ok {
		t.Fatalf("expected entry key %q to exist", key)
	}
	if fmt.Sprint(got) != want {
		t.Fatalf("expected entry key %q to be %q, got %q", key, want, fmt.Sprint(got))
	}
}

// assertEntryContains 断言 JSON 日志字段存在且包含指定文本。
func assertEntryContains(t *testing.T, entry map[string]any, key, want string) {
	t.Helper()
	got, ok := entry[key]
	if !ok {
		t.Fatalf("expected entry key %q to exist", key)
	}
	if !strings.Contains(fmt.Sprint(got), want) {
		t.Fatalf("expected entry key %q to contain %q, got %q", key, want, fmt.Sprint(got))
	}
}

// assertEntryMissing 断言 JSON 日志没有输出指定顶层字段。
func assertEntryMissing(t *testing.T, entry map[string]any, key string) {
	t.Helper()
	if _, ok := entry[key]; ok {
		t.Fatalf("expected entry key %q to be folded into content, entry=%v", key, entry)
	}
}
