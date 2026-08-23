package types

import (
	"strings"
	"time"

	"github.com/Is999/go-utils/errors"
)

// ConfigReloadStatusResp 表示 config.yaml 热加载运行状态。
type ConfigReloadStatusResp struct {
	Enabled                bool      `json:"enabled"`                // 是否启用热加载
	Watching               bool      `json:"watching"`               // 当前是否正在监听配置文件
	ConfigFile             string    `json:"configFile"`             // 当前监听的配置文件路径
	CheckIntervalSeconds   int       `json:"checkIntervalSeconds"`   // 轮询间隔，单位秒
	ConfigVersion          string    `json:"configVersion"`          // 当前配置版本指纹
	ConfigSummary          string    `json:"configSummary"`          // 当前配置摘要
	RestartRequired        bool      `json:"restartRequired"`        // 是否需要重启才能完全生效
	RestartReason          string    `json:"restartReason"`          // 需要重启的原因摘要
	LastStatus             string    `json:"lastStatus"`             // 最近一次重载状态：idle、success 或 failed
	LastMessage            string    `json:"lastMessage"`            // 最近一次重载结果的可读说明
	LastTriggerSource      string    `json:"lastTriggerSource"`      // 最近一次触发来源
	LastFailureCategory    string    `json:"lastFailureCategory"`    // 最近一次失败分类
	LastCheckedAt          time.Time `json:"lastCheckedAt"`          // 最近一次检查时间
	LastReloadAt           time.Time `json:"lastReloadAt"`           // 最近一次重载时间
	LastSuccessAt          time.Time `json:"lastSuccessAt"`          // 最近一次成功时间
	LastFailureAt          time.Time `json:"lastFailureAt"`          // 最近一次失败时间
	ReloadCount            int64     `json:"reloadCount"`            // 累计成功加载次数
	SuppressedFailureCount int64     `json:"suppressedFailureCount"` // 限频压制的重复失败日志次数
}

// ConfigItemQueryReq 表示运行态配置项查询请求。
// keyword 只匹配路径、类型和已脱敏展示值，避免通过搜索反推出敏感原文。
type ConfigItemQueryReq struct {
	Keyword       string `json:"keyword,optional" form:"keyword,optional"`             // 配置路径或展示值关键字
	SensitiveOnly bool   `json:"sensitiveOnly,optional" form:"sensitiveOnly,optional"` // 是否只返回敏感配置项
	Page          int    `json:"page,optional" form:"page,optional"`                   // 页码，从 1 开始
	PageSize      int    `json:"pageSize,optional" form:"pageSize,optional"`           // 每页条数，最大 100
}

// Validate 校验并归一化运行态配置项查询参数。
func (r *ConfigItemQueryReq) Validate() error {
	if r == nil {
		return errors.Errorf("配置项查询请求不能为空")
	}
	r.Keyword = strings.TrimSpace(r.Keyword)
	if len([]rune(r.Keyword)) > 128 {
		return errors.Errorf("keyword 不能超过 128 个字符")
	}
	r.Page, r.PageSize = normalizePage(r.Page, r.PageSize, 20)
	return nil
}

// ConfigItem 表示一条已脱敏的运行态配置项。
type ConfigItem struct {
	Path      string `json:"path"`      // 扁平化配置路径
	Value     string `json:"value"`     // 展示值；敏感项只保留首尾少量字符
	ValueType string `json:"valueType"` // 值类型：string/number/bool/list/object/null
	Sensitive bool   `json:"sensitive"` // 是否按敏感数据处理过
}

// ConfigSectionStat 表示顶层配置分组统计。
type ConfigSectionStat struct {
	Name           string `json:"name"`           // 顶层配置段名称
	Total          int    `json:"total"`          // 分组内配置项数量
	SensitiveTotal int    `json:"sensitiveTotal"` // 分组内敏感配置项数量
}

// ConfigSourceMeta 表示运行态配置快照来源。
type ConfigSourceMeta struct {
	Source            string `json:"source"`            // 快照来源，固定为 runtime_snapshot
	ConfigFile        string `json:"configFile"`        // 当前监听的主配置文件
	RuntimeFile       string `json:"runtimeFile"`       // 运行期外部配置文件
	ConfigVersion     string `json:"configVersion"`     // 当前生效配置版本指纹
	LastStatus        string `json:"lastStatus"`        // 最近一次热加载状态
	LastTriggerSource string `json:"lastTriggerSource"` // 最近一次触发来源
	LastReloadAt      string `json:"lastReloadAt"`      // 最近一次触发重载时间
	LastSuccessAt     string `json:"lastSuccessAt"`     // 最近一次成功加载时间
	RestartRequired   bool   `json:"restartRequired"`   // 是否仍需重启进程才能完全生效
	RestartReason     string `json:"restartReason"`     // 需要重启才能完全生效的原因摘要
}

// ConfigItemQueryResp 表示运行态配置项查询结果。
type ConfigItemQueryResp struct {
	Keyword        string              `json:"keyword,omitempty"` // 当前查询关键字
	SensitiveOnly  bool                `json:"sensitiveOnly"`     // 是否只返回敏感项
	Page           int                 `json:"page"`              // 当前页码
	PageSize       int                 `json:"pageSize"`          // 当前页大小
	Total          int64               `json:"total"`             // 命中总数
	TotalItems     int                 `json:"totalItems"`        // 当前快照配置项总数
	SensitiveTotal int                 `json:"sensitiveTotal"`    // 当前快照敏感配置项总数
	Sections       []ConfigSectionStat `json:"sections"`          // 顶层配置分组统计
	Source         ConfigSourceMeta    `json:"source"`            // 当前运行态快照来源
	SnapshotYAML   string              `json:"snapshotYaml"`      // 脱敏 YAML 快照，保留数字零值与 false
	RuntimeYAML    string              `json:"runtimeYaml"`       // 运行期外部配置的脱敏 YAML 视图
	Items          []ConfigItem        `json:"items"`             // 当前页配置项
}
