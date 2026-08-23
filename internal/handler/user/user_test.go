package user

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api/common/codes"
	"api/common/runtimecfg"
	"api/internal/config"
	"api/internal/svc"
	"api/internal/types"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/rest/router"
)

// TestRuntimeSyncUsesPathID 验证正文和查询参数不能改变路径指定的用户及其缓存副作用。
func TestRuntimeSyncUsesPathID(t *testing.T) {
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "runtime-sync-path"})
	t.Cleanup(func() { runtimecfg.Restore(previous) })

	for _, test := range []struct {
		name  string // 区分路径、正文和查询参数发生冲突的位置。
		query string // 故意携带另一用户 ID；路径仍为 42。
		body  string // 正常请求与带冲突 ID 的 JSON 都应保留路径对象。
	}{
		{name: "path only", body: `{"profile":true}`},
		{name: "body cannot override", body: `{"id":43,"profile":true}`},
		{name: "query cannot override", query: "?id=43", body: `{"profile":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			// 只隔离 Redis 存储，不替换路由解析、Handler、Logic 和统一响应。
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			service := svc.NewServiceContext(config.Config{AppID: "runtime-sync-path"}, "test", svc.Dependencies{Rds: client})
			const targetKey = "app:runtime-sync-path:user:profile:42"
			const otherKey = "app:runtime-sync-path:user:profile:43"
			for _, key := range []string{targetKey, otherKey} {
				if err := client.Set(context.Background(), key, "profile", time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
			}

			// 路由器负责 pathvar；此用例不替代内网来源与 Ops HMAC 的中间件验证。
			routes := router.NewRouter()
			if err := routes.Handle(http.MethodPost, "/internal/users/:id/runtime-sync", UserRuntimeSyncHandler(service)); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/internal/users/42/runtime-sync"+test.query, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, request)

			var result struct {
				Code int                       `json:"code"` // 统一响应应保留更新成功业务码。
				Data types.UserRuntimeSyncResp `json:"data"` // 回执对象必须与实际失效对象一致。
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || result.Code != codes.UpdateSuccess || result.Data.UserID != 42 {
				t.Fatalf("runtime sync response: HTTP %d %s", response.Code, response.Body.String())
			}
			if server.Exists(targetKey) || !server.Exists(otherKey) {
				t.Fatalf("wrong cache invalidation: target=%t other=%t", server.Exists(targetKey), server.Exists(otherKey))
			}
		})
	}
}
