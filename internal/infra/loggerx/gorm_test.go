package loggerx

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zeromicro/go-zero/core/logx"
	gormlogger "gorm.io/gorm/logger"
)

// TestGormLoggerParamsFilterRemovesSensitiveValues 验证 GORM 只把占位 SQL 交给日志格式化器。
func TestGormLoggerParamsFilterRemovesSensitiveValues(t *testing.T) {
	logger := &GormLogger{}
	sql, params := logger.ParamsFilter(context.Background(), "SELECT * FROM user WHERE password_hash = ?", "secret-hash")
	if sql != "SELECT * FROM user WHERE password_hash = ?" || len(params) != 0 {
		t.Fatalf("ParamsFilter() sql=%q params=%v", sql, params)
	}
}

// TestGormLoggerTraceRespectsLogMode 验证会话级 Error 抑制成功慢查询，Warn/Info 与真实查询失败仍按各自级别输出。
func TestGormLoggerTraceRespectsLogMode(t *testing.T) {
	cases := []struct {
		name      string              // 当前日志等级与查询状态的组合。
		level     gormlogger.LogLevel // 经 GORM LogMode 配置的会话日志等级。
		threshold time.Duration       // 慢 SQL 阈值；一小时用于确定性模拟普通查询。
		err       error               // 查询结果；非空时必须优先进入错误日志分支。
		want      string              // 应输出的日志类别；空值表示保持静默。
	}{
		{name: "silent_slow", level: gormlogger.Silent, threshold: time.Millisecond},
		{name: "error_slow", level: gormlogger.Error, threshold: time.Millisecond},
		{name: "warn_slow", level: gormlogger.Warn, threshold: time.Millisecond, want: "数据库 慢查询"},
		{name: "info_slow", level: gormlogger.Info, threshold: time.Millisecond, want: "数据库 慢查询"},
		{name: "warn_fast", level: gormlogger.Warn, threshold: time.Hour},
		{name: "info_fast", level: gormlogger.Info, threshold: time.Hour, want: "数据库 查询"},
		{name: "error_failed", level: gormlogger.Error, threshold: time.Millisecond, err: errors.New("query failed"), want: "数据库 查询失败"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// logx writer 为进程共享状态，本组不并行并在每例结束时恢复。
			var output bytes.Buffer
			previousWriter := logx.Reset()
			logx.SetWriter(wrapLogWriter(logx.NewWriter(&output)))
			logx.SetLevel(logx.InfoLevel)
			t.Cleanup(func() {
				logx.SetWriter(previousWriter)
				logx.SetLevel(logx.InfoLevel)
			})

			// 固定一秒耗时而不实际等待，日志等级必须由 GORM 的公开接口设置。
			logger := NewGormLogger(tc.threshold).LogMode(tc.level)
			logger.Trace(t.Context(), time.Now().Add(-time.Second), func() (string, int64) {
				return "SELECT ?", 1
			}, tc.err)
			if tc.want == "" {
				if output.Len() != 0 {
					t.Fatalf("日志等级应抑制查询记录，实际=%s", output.String())
				}
				return
			}
			if !strings.Contains(output.String(), tc.want) {
				t.Fatalf("日志缺少预期类别 %q，实际=%s", tc.want, output.String())
			}
		})
	}
}
