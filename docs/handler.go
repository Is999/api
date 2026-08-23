package docs

import (
	"io/fs"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
)

const (
	internalDocsPathPrefix  = "/internal/docs" // API 内网文档资源路由前缀
	internalDocsDefaultPath = "文档首页.md"        // 直接访问入口时返回的文档中心首页
	docsPageCacheHeader     = "no-cache"       // Markdown 文档不做强缓存
)

// Handler 返回 API 内网文档资源处理器；调用方仍需在路由层挂 OpsMiddleware。
func Handler() http.HandlerFunc {
	// 始终使用随二进制打包的资源，工作目录的残缺文档不能覆盖发布内容。
	sub, initErr := fs.Sub(FS, "site")
	fileServer := http.FileServer(http.FS(sub))

	return func(w http.ResponseWriter, r *http.Request) {
		if initErr != nil {
			http.Error(w, "文档资源初始化失败", http.StatusInternalServerError)
			return
		}
		docsPath, ok := internalDocsAssetPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Cache-Control", docsPageCacheHeader)
		// 保留原请求路径供外层运维鉴权和访问日志使用。
		req := r.Clone(r.Context())
		req.URL.Path = "/" + docsPath
		req.URL.RawPath = ""
		fileServer.ServeHTTP(w, req)
	}
}

// internalDocsAssetPath 清洗内网文档路径，并只放行可展示给后台的文档站资源。
func internalDocsAssetPath(requestPath string) (string, bool) {
	// 额外解码一次以识别双重编码点段；必须在 Clean 抹去点段前拒绝穿越。
	if text, err := url.PathUnescape(strings.TrimSpace(requestPath)); err == nil {
		requestPath = text
	}
	if hasDocsPathTraversal(requestPath) {
		return "", false
	}
	cleanPath := pathpkg.Clean("/" + strings.TrimLeft(strings.TrimSpace(requestPath), "/"))
	if cleanPath == internalDocsPathPrefix {
		return internalDocsDefaultPath, true
	}
	if !strings.HasPrefix(cleanPath, internalDocsPathPrefix+"/") {
		return "", false
	}
	docsPath := strings.TrimPrefix(cleanPath, internalDocsPathPrefix+"/")
	if !allowedInternalDocsAsset(docsPath) {
		return "", false
	}
	return docsPath, true
}

// hasDocsPathTraversal 判断请求路径是否包含显式穿越片段。
func hasDocsPathTraversal(requestPath string) bool {
	for _, part := range strings.Split(requestPath, "/") {
		if part == "." || part == ".." {
			return true
		}
	}
	return false
}

// allowedInternalDocsAsset 限定后台可代理的 API 文档范围，避免暴露安全清单等机器资产。
func allowedInternalDocsAsset(docsPath string) bool {
	docsPath = strings.Trim(strings.TrimSpace(docsPath), "/")
	if docsPath == "_sidebar.md" {
		return true
	}
	if docsPath == internalDocsDefaultPath {
		return true
	}
	if !strings.HasSuffix(docsPath, ".md") {
		return false
	}
	return strings.HasPrefix(docsPath, "接口文档/") || strings.HasPrefix(docsPath, "角色文档/")
}
