package shared

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Is999/go-utils/errors"

	codes "api/common/codes"
	"api/internal/httpresp"
	"api/internal/infra/loggerx"
	"api/internal/requestctx"
	"api/internal/svc"
	"api/internal/types"

	"github.com/zeromicro/go-zero/rest/httpx"
)

// standardBodyMaxBytes 与框架默认请求上限一致，同时约束未知长度的 chunked 请求体。
const standardBodyMaxBytes = 8 << 20

// handlerFunc 是无泛型请求解析后的统一业务处理函数。
type handlerFunc func(r *http.Request) *types.BizResult

// RespExec 定义标准 handler 执行函数。
type RespExec[Req any] func(*http.Request, *svc.ServiceContext, *Req) *types.BizResult

// RespHandler 泛型封装，简化普通接口 handler 模板代码。
func RespHandler[Req any](exec RespExec[Req]) func(*svc.ServiceContext) http.HandlerFunc {
	return func(sCtx *svc.ServiceContext) http.HandlerFunc {
		return respHandler(func(r *http.Request) *types.BizResult {
			var req Req
			if err := ParseStandardRequest(r, &req); err != nil {
				return types.ParamErrorResult(err)
			}
			resp := exec(r, sCtx, &req)
			if resp == nil {
				return types.NewBizResult(codes.ServerError).WithError(errors.New("业务响应为空"))
			}
			resp.WithReq(&req)
			return resp
		})
	}
}

// ParseStandardRequest 规范化 JSON/表单媒体类型后复用框架的路径、查询、字段映射和 Validate。
func ParseStandardRequest(r *http.Request, value any) error {
	if r == nil {
		return errors.New("HTTP 请求不能为空")
	}
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return errors.Tag(httpx.Parse(r, value))
	}
	// 按 MIME 语法识别类型，不能让合法的大小写变体或畸形头被框架静默忽略。
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(r.Header.Get("Content-Type")))
	if err != nil {
		return errors.Wrap(err, "请求体 Content-Type 格式错误")
	}
	if mediaType != "application/json" && mediaType != "application/x-www-form-urlencoded" {
		return errors.New("请求体仅支持 application/json 或 application/x-www-form-urlencoded")
	}
	if r.ContentLength > standardBodyMaxBytes {
		return errors.Errorf("请求体不能超过%d字节", standardBodyMaxBytes)
	}
	// 读取真实长度后恢复请求体，框架才能解析 ContentLength=-1 的 JSON。
	originalBody := r.Body
	body, readErr := io.ReadAll(io.LimitReader(originalBody, standardBodyMaxBytes+1))
	closeErr := originalBody.Close()
	if readErr != nil {
		return errors.Wrap(readErr, "读取请求体失败")
	}
	if closeErr != nil {
		return errors.Wrap(closeErr, "关闭请求体失败")
	}
	if len(body) > standardBodyMaxBytes {
		return errors.Errorf("请求体不能超过%d字节", standardBodyMaxBytes)
	}
	if mediaType == "application/json" && len(bytes.TrimSpace(body)) > 0 && !json.Valid(body) {
		return errors.New("JSON 请求体必须包含一个完整 JSON 值")
	}
	// 只规范化媒体类型；API 仍保留原有 URL-encoded 表单契约。
	r.Header.Set("Content-Type", mediaType)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return errors.Tag(httpx.Parse(r, value))
}

// respHandler 把业务结果写入统一 JSON 响应。
func respHandler(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		WriteBizResponse(w, r, fn(r))
	}
}

// WriteBizResponse 在 handler 最外层统一输出响应和错误日志。
func WriteBizResponse(w http.ResponseWriter, r *http.Request, resp *types.BizResult) {
	if resp == nil {
		resp = types.NewBizResult(codes.ServerError).WithError(errors.New("业务响应为空"))
	}
	message := resp.ResolveMessage(requestctx.Locale(r.Context()))
	if resp.IsFailure() {
		jsonResp := httpresp.NewJSONResp(r.Context(), w).SetCode(resp.Code)
		if resp.Error != nil && !errors.Is(resp.Error, types.Nil) {
			jsonResp = jsonResp.SetError(resp.Error)
		}
		jsonResp.Fail(message)
		// Fail 先把最终 HTTP 状态和业务码写回 request meta，错误日志才能记录真实响应结果。
		if resp.Error != nil && !errors.Is(resp.Error, types.Nil) {
			loggerx.Errorw(r.Context(), "请求 业务处理失败", resp.Error)
		}
		return
	}
	httpresp.NewJSONResp(r.Context(), w).SetCode(resp.Code).SetMessage(message).Success(resp.Data)
}
