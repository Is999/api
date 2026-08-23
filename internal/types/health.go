package types

// HealthDependencyStatus 表示 ready 检查中的单个依赖状态。
type HealthDependencyStatus struct {
	Name    string `json:"name"`              // 依赖名称，如 mysql、redis
	Status  string `json:"status"`            // 依赖状态：ok/error；未启用且无探测错误的组件也返回 ok。
	Code    int    `json:"code,omitempty"`    // 异常时对应的业务状态码
	Message string `json:"message,omitempty"` // 公开稳定说明，不包含驱动错误、地址或凭证
}

// HealthStatusResp 表示服务健康检查响应。
type HealthStatusResp struct {
	Status       string                   `json:"status"`       // 服务整体状态：ok/error
	Mode         string                   `json:"mode"`         // 当前进程启动模式
	Node         string                   `json:"node"`         // 当前节点名称
	Version      string                   `json:"version"`      // 当前配置版本指纹
	Dependencies []HealthDependencyStatus `json:"dependencies"` // readiness 按组件注册顺序返回；liveness 不探测依赖，值为 null
}
