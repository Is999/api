package handler

import (
	"api/internal/handler/shared"
	"api/internal/routealias"
	"api/internal/security"
)

// RouteSecurityManifestItem 描述前后端同步安全策略所需的单路由清单项。
type RouteSecurityManifestItem struct {
	Alias          routealias.Alias          `json:"alias"`          // 路由别名
	Method         string                    `json:"method"`         // HTTP 方法
	Path           string                    `json:"path"`           // HTTP 路径
	Access         shared.RouteAccess        `json:"access"`         // 访问边界
	Chain          shared.RouteSecurityChain `json:"chain"`          // 实际安全链路
	Describe       string                    `json:"describe"`       // 中文业务说明
	RequestSign    []string                  `json:"requestSign"`    // 请求签名字段
	RequestCipher  []string                  `json:"requestCipher"`  // 请求解密字段
	ResponseSign   []string                  `json:"responseSign"`   // 响应回签字段
	ResponseCipher []string                  `json:"responseCipher"` // 响应加密字段
	DocumentPath   string                    `json:"documentPath"`   // 接口文档路径
}

// DefaultRouteSecurityManifest 返回内置路由的安全策略清单，供测试、文档和前端同步复用。
func DefaultRouteSecurityManifest() []RouteSecurityManifestItem {
	securityByAlias := defaultRouteSecurityContractsByAlias()
	contracts := DefaultRouteContracts()
	items := make([]RouteSecurityManifestItem, 0, len(contracts))
	for _, contract := range contracts {
		securityContract := securityByAlias[string(contract.Meta.Alias)]
		policy, _ := security.LookupRoutePolicy(string(contract.Meta.Alias))
		items = append(items, RouteSecurityManifestItem{
			Alias:          contract.Meta.Alias,
			Method:         contract.Method,
			Path:           contract.Path,
			Access:         contract.Meta.Access,
			Chain:          securityContract.Chain,
			Describe:       contract.Meta.Describe,
			RequestSign:    cloneSecurityFields(policy.RequestSign),
			RequestCipher:  cloneSecurityFields(policy.RequestCipher),
			ResponseSign:   cloneSecurityFields(policy.ResponseSign),
			ResponseCipher: cloneSecurityFields(policy.ResponseCipher),
			DocumentPath:   contract.DocumentPath,
		})
	}
	return items
}

// defaultRouteSecurityContractsByAlias 按路由别名索引默认安全链路契约。
func defaultRouteSecurityContractsByAlias() map[string]RouteSecurityContract {
	contracts := DefaultRouteSecurityContracts()
	result := make(map[string]RouteSecurityContract, len(contracts))
	for _, contract := range contracts {
		result[string(contract.Alias)] = contract
	}
	return result
}

// cloneSecurityFields 隔离全局策略切片，同时保留 nil 关闭与空切片仅签基础头的区别。
func cloneSecurityFields(fields []string) []string {
	if fields == nil {
		return nil
	}
	result := make([]string, len(fields))
	copy(result, fields)
	return result
}
