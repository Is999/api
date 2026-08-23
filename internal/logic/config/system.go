package config

import (
	"context"
	"strings"

	codes "api/common/codes"
	i18n "api/common/i18n"
	corelogic "api/internal/logic"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
)

// SystemLogic 承载前台 API 框架运行态管理能力。
type SystemLogic struct {
	*corelogic.BaseLogic // 复用上下文、日志和 ServiceContext 访问能力
}

// NewSystemLogic 创建系统运行态 logic。
func NewSystemLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SystemLogic {
	return &SystemLogic{BaseLogic: corelogic.NewBaseLogicWithContext(ctx, svcCtx)}
}

// ConfigReloadStatus 查询 config.yaml 热加载运行状态。
func (l *SystemLogic) ConfigReloadStatus() *types.BizResult {
	if l.Svc == nil {
		return types.NewBizResult(codes.InternalError).
			SetI18nMessage(i18n.MsgKeyInternalError).
			WithError(errors.New("ServiceContext 未初始化"))
	}
	return types.NewBizResult(codes.FetchSuccess).
		SetI18nMessage(i18n.MsgKeyFetchSuccess).
		WithData(l.configReloadStatusResp(l.Svc.CurrentHotReloadStatus()))
}

// RunConfigReload 手动触发一次 config.yaml 重载，并返回最新状态。
func (l *SystemLogic) RunConfigReload() *types.BizResult {
	if l.Svc == nil || l.Svc.ConfigReload == nil {
		return types.NewBizResult(codes.ServiceBusy).
			SetI18nMessage(i18n.MsgKeyServiceBusy).
			WithError(errors.New("配置热加载未启用或未绑定执行器"))
	}
	// 等待本次重载结束再读取状态，响应不能提前沿用上一次执行结果。
	if err := l.Svc.ConfigReload.ReloadConfig(l.Ctx, "manual_api"); err != nil {
		return types.ServerError(i18n.MsgKeyInternalError, err, "SystemLogic.RunConfigReload").ToBizResult()
	}
	return l.ConfigReloadStatus()
}

// configReloadStatusResp 转换热加载状态快照为接口响应。
func (l *SystemLogic) configReloadStatusResp(status svc.HotReloadStatus) *types.ConfigReloadStatusResp {
	return &types.ConfigReloadStatusResp{
		Enabled:                status.Enabled,
		Watching:               status.Watching,
		ConfigFile:             status.ConfigFile,
		CheckIntervalSeconds:   status.CheckIntervalSeconds,
		ConfigVersion:          status.ConfigVersion,
		ConfigSummary:          status.ConfigSummary,
		RestartRequired:        status.RestartRequired,
		RestartReason:          status.RestartReason,
		LastStatus:             status.LastStatus,
		LastMessage:            l.messageFromKeyOrRaw(status.LastMessageKey, status.LastMessage),
		LastTriggerSource:      status.LastTriggerSource,
		LastFailureCategory:    status.LastFailureCategory,
		LastCheckedAt:          status.LastCheckedAt,
		LastReloadAt:           status.LastReloadAt,
		LastSuccessAt:          status.LastSuccessAt,
		LastFailureAt:          status.LastFailureAt,
		ReloadCount:            status.ReloadCount,
		SuppressedFailureCount: status.SuppressedFailureCount,
	}
}

// messageFromKeyOrRaw 优先使用状态源头保存的 key 翻译，动态错误说明保留原文。
func (l *SystemLogic) messageFromKeyOrRaw(messageKey, fallback string) string {
	messageKey = strings.TrimSpace(messageKey)
	if messageKey != "" {
		return l.Message(messageKey)
	}
	return strings.TrimSpace(fallback)
}
