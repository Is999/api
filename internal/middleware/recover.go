package middleware

import (
	"net/http"
	"runtime/debug"

	codes "api/common/codes"
	i18n "api/common/i18n"
	"api/internal/httpresp"
	"api/internal/infra/loggerx"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
)

// RecoverMiddleware 在最外层兜底捕获 panic。
type RecoverMiddleware struct{}

// NewRecoverMiddleware 创建 panic 保护中间件。
func NewRecoverMiddleware() *RecoverMiddleware {
	return &RecoverMiddleware{}
}

// Handle 捕获 panic 并写入统一响应。
func (m *RecoverMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				ctx := r.Context()
				panicErr := errors.Errorf("请求 panic: %v", err)
				loggerx.Errorw(ctx, "请求 发生异常", panicErr, logx.Field("stacktrace", string(debug.Stack())))
				// 将 panic 原因写入请求元数据，供统一日志与 trace 采集。
				httpresp.NewJSONResp(ctx, w).
					SetHTTPStatus(http.StatusInternalServerError).
					SetCode(codes.InternalError).
					SetError(panicErr).
					Fail(i18n.MsgKeyInternalError)
			}
		}()
		next(w, r)
	}
}
