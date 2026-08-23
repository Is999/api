package bootstrap

import (
	"context"
	"strings"
	"time"

	i18n "api/common/i18n"
	"api/internal/bootstrap/configload"
	"api/internal/bootstrap/hotreload"
	"api/internal/config"
	"api/internal/infra/loggerx"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
)

// ReloadConfig 手动触发一次配置重载，供 handler/logic 通过接口调用。
func (a *App) ReloadConfig(ctx context.Context, source string) error {
	_, err := a.reloadConfigFile(ctx, source, a.boundConfigFile(), configload.Load)
	return errors.Tag(err)
}

// startConfigHotReload 在启用时启动后台配置轮询协程。
func (a *App) startConfigHotReload() {
	if a == nil || a.ServiceContext == nil {
		return
	}
	cfg := a.ServiceContext.CurrentConfig()
	interval := hotreload.CheckInterval(cfg.HotReload.CheckIntervalSeconds)
	configFile := a.boundConfigFile()
	// 未启用或未绑定文件也发布状态，便于区分关闭与启动失败。
	a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
		status.Enabled = cfg.HotReload.Enabled
		status.Watching = false
		status.ConfigFile = configFile
		status.CheckIntervalSeconds = int(interval / time.Second)
		status.ConfigVersion = a.ServiceContext.CurrentVersion()
		status.ConfigSummary = hotreload.Summary(cfg)
		if status.LastStatus == "" {
			status.LastStatus = "idle"
			status.LastMessage = "热加载监听尚未启动"
			status.LastMessageKey = i18n.MsgKeyHotReloadWatcherNotStarted
		}
		return status
	})
	if configFile == "" || !cfg.HotReload.Enabled {
		return
	}
	// StartWatcher 只允许一个后台轮询协程进入运行态。
	if !a.hotReload.StartWatcher(func(ctx context.Context) {
		a.watchConfigFile(ctx, configFile)
	}) {
		return
	}
	loggerx.Infow(context.Background(), "配置 热加载已启用",
		logx.Field("file", configFile),
		logx.Field(loggerx.FieldIntervalSeconds, int(interval/time.Second)),
	)
}

// stopConfigHotReload 停止配置热加载后台协程。
func (a *App) stopConfigHotReload(ctx context.Context) error {
	if a == nil {
		return nil
	}
	return errors.Tag(a.hotReload.StopWatcher(ctx))
}

// isConfigHotReloadRunning 返回当前是否已有热加载 watcher 在运行。
func (a *App) isConfigHotReloadRunning() bool {
	if a == nil {
		return false
	}
	return a.hotReload.WatcherRunning()
}

// watchConfigFile 轮询配置文件指纹，检测到变化后重新解析并刷新配置快照。
func (a *App) watchConfigFile(ctx context.Context, configFile string) {
	interval := hotreload.CheckInterval(a.ServiceContext.CurrentConfig().HotReload.CheckIntervalSeconds)
	// watcher 启动后立即发布运行状态，避免首轮定时检查前显示为未启动。
	a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
		status.Enabled = true
		status.Watching = true
		status.ConfigFile = configFile
		status.CheckIntervalSeconds = int(interval / time.Second)
		status.ConfigVersion = a.ServiceContext.CurrentVersion()
		status.ConfigSummary = hotreload.Summary(a.ServiceContext.CurrentConfig())
		status.LastTriggerSource = "startup"
		if status.LastStatus == "" || status.LastStatus == "idle" {
			status.LastStatus = "idle"
			status.LastMessage = "热加载监听运行中"
			status.LastMessageKey = i18n.MsgKeyHotReloadWatcherRunning
		}
		return status
	})
	// 首轮立即加载当前文件，避免把装配期间的新内容误记为已应用版本。
	lastFingerprint := ""
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			// 退出只清除运行标记，最后一次加载结果继续保留给状态接口。
			a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
				status.Watching = false
				if status.LastMessage == "" {
					status.LastMessage = "热加载监听已停止"
					status.LastMessageKey = i18n.MsgKeyHotReloadWatcherStopped
				}
				return status
			})
			return
		case <-timer.C:
			now := time.Now()
			a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
				status.LastCheckedAt = now
				status.CheckIntervalSeconds = int(hotreload.CheckInterval(a.ServiceContext.CurrentConfig().HotReload.CheckIntervalSeconds) / time.Second)
				return status
			})
			currentFingerprint, statErr := configload.BundleFingerprint(configFile)
			if statErr != nil {
				a.markHotReloadFailure(i18n.MsgKeyHotReloadFileStatusReadFailed, "读取配置文件状态失败", statErr, "", "watcher", "fingerprint", configFile)
				timer.Reset(hotreload.CheckInterval(a.ServiceContext.CurrentConfig().HotReload.CheckIntervalSeconds))
				continue
			}
			// 重载失败不推进指纹，使下一轮继续尝试同一版本。
			if lastFingerprint == "" || currentFingerprint != lastFingerprint {
				if reloadedFingerprint, reloadErr := a.reloadConfigFile(ctx, "watcher", configFile, configload.Load); reloadErr == nil {
					lastFingerprint = reloadedFingerprint
				}
			}
			// 配置关闭热加载后结束当前 watcher，后续重新启用时再创建协程。
			if !a.ServiceContext.CurrentConfig().HotReload.Enabled {
				a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
					status.Enabled = false
					status.Watching = false
					status.LastStatus = "idle"
					status.LastMessage = "热加载监听已关闭"
					status.LastMessageKey = i18n.MsgKeyHotReloadWatcherClosed
					return status
				})
				return
			}
			// 每轮读取最新间隔，使已发布的热加载配置在下一轮生效。
			timer.Reset(hotreload.CheckInterval(a.ServiceContext.CurrentConfig().HotReload.CheckIntervalSeconds))
		}
	}
}

// reloadConfigFile 串行执行重载；load 隔离文件读取，真实调用方统一传入 configload.Load。
func (a *App) reloadConfigFile(ctx context.Context, source string, configFile string, load func(string) (config.Config, string, *security.KeyRegistry, error)) (string, error) {
	if a == nil || a.ServiceContext == nil {
		return "", errors.Errorf("应用实例为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if configFile == "" || configFile != strings.TrimSpace(configFile) {
		notBoundErr := errors.Errorf("未绑定配置文件路径")
		a.markHotReloadFailure(i18n.MsgKeyHotReloadNotBound, "配置热加载未绑定文件", notBoundErr, "", source, "not_bound", configFile)
		return "", notBoundErr
	}
	// watcher 与手动触发共用串行锁，避免并发发布不同配置快照。
	a.hotReload.LockExec()
	reconcileWatcher := false
	reconcileEnabled := false
	defer func() {
		// 先释放执行锁，避免停止 watcher 时等待当前重载造成死锁。
		a.hotReload.UnlockExec()
		if !reconcileWatcher {
			return
		}
		if reconcileEnabled && !a.isConfigHotReloadRunning() {
			// 手动开启热加载后补建唯一 watcher。
			a.startConfigHotReload()
		}
		if !reconcileEnabled && hotreload.Source(source) != "watcher" {
			// 手动关闭热加载时等待原 watcher 退出。
			_ = a.stopConfigHotReload(context.Background())
		}
	}()
	select {
	case <-ctx.Done():
		cancelErr := errors.Tag(ctx.Err())
		a.markHotReloadFailure(i18n.MsgKeyHotReloadCancelled, "配置热加载已取消", cancelErr, "", source, "cancelled", configFile)
		return "", cancelErr
	default:
	}

	beforeCfg := a.ServiceContext.CurrentConfig()
	previousVersion := a.ServiceContext.CurrentVersion()
	currentFingerprint, err := configload.BundleFingerprint(configFile)
	if err != nil {
		a.markHotReloadFailure(i18n.MsgKeyHotReloadFingerprintReadFailed, "读取配置文件指纹失败", err, "", source, "fingerprint", configFile)
		return "", errors.Tag(err)
	}
	// 重载仍完整校验启动期密钥，非法材料不得进入状态快照。
	cfg, loadedFingerprint, _, err := load(configFile)
	if err != nil {
		a.markHotReloadFailure(i18n.MsgKeyHotReloadFailed, "配置热加载失败", err, currentFingerprint, source, "load", configFile)
		return "", errors.Tag(err)
	}
	// 生效版本和成功水位只能来自本轮实际解码的内容，不能使用预检时的文件状态。
	currentFingerprint = loadedFingerprint
	version := configload.Version(loadedFingerprint)
	if previousVersion != "" && version == previousVersion {
		a.markHotReloadUnchanged(configFile, source, version)
		return currentFingerprint, nil
	}
	restartRequired, restartReason := configload.DetectReloadRestartImpact(beforeCfg, cfg)
	effectiveCfg := cfg
	if restartRequired {
		// 启动期字段只登记重启需求，运行快照继续沿用旧值。
		effectiveCfg = configload.BuildReloadEffectiveConfig(beforeCfg, cfg)
	}
	// 全部校验完成后再发布运行快照，失败路径继续使用旧配置。
	publishRuntimeConfig(effectiveCfg)
	a.ServiceContext.UpdateConfig(effectiveCfg)
	a.ServiceContext.UpdateVersion(version)
	a.updateRuntimeAlertConfig(effectiveCfg)
	reconcileWatcher = true
	reconcileEnabled = effectiveCfg.HotReload.Enabled
	now := time.Now()
	message := "配置热加载成功"
	messageKey := i18n.MsgKeyHotReloadSuccess
	if restartRequired {
		message = "配置热加载成功，部分启动期配置需重启后生效"
		messageKey = i18n.MsgKeyHotReloadSuccessRestart
	}
	// 运行快照发布后才标记成功，避免状态接口提前暴露新版本。
	a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
		status.Enabled = effectiveCfg.HotReload.Enabled
		status.ConfigFile = configFile
		status.CheckIntervalSeconds = int(hotreload.CheckInterval(effectiveCfg.HotReload.CheckIntervalSeconds) / time.Second)
		status.ConfigVersion = version
		status.ConfigSummary = hotreload.Summary(effectiveCfg)
		status.RestartRequired = restartRequired
		status.RestartReason = restartReason
		status.LastStatus = "success"
		status.LastMessage = message
		status.LastMessageKey = messageKey
		status.LastTriggerSource = hotreload.Source(source)
		status.LastFailureCategory = ""
		status.LastReloadAt = now
		status.LastSuccessAt = now
		status.ReloadCount++
		return status
	})
	a.hotReload.ResetFailureLog()
	loggerx.Infow(ctx, "配置 热加载成功",
		logx.Field("file", configFile),
		logx.Field("from_version", previousVersion),
		logx.Field("to_version", version),
		logx.Field("restart_required", restartRequired),
		logx.Field("restart_reason", restartReason),
	)
	return currentFingerprint, nil
}

// markHotReloadUnchanged 记录一次无配置变更的热加载检查，不刷新运行配置快照。
func (a *App) markHotReloadUnchanged(configFile, source, version string) {
	if a == nil || a.ServiceContext == nil {
		return
	}
	now := time.Now()
	a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
		status.ConfigFile = configFile
		status.ConfigVersion = strings.TrimSpace(version)
		status.ConfigSummary = hotreload.Summary(a.ServiceContext.CurrentConfig())
		status.LastStatus = "success"
		status.LastMessage = "配置无变化"
		status.LastMessageKey = i18n.MsgKeyHotReloadUnchanged
		status.LastTriggerSource = hotreload.Source(source)
		status.LastFailureCategory = ""
		status.LastCheckedAt = now
		return status
	})
}

// boundConfigFile 返回当前 App 绑定的配置文件路径。
func (a *App) boundConfigFile() string {
	if a == nil {
		return ""
	}
	return a.ConfigFile
}

// refreshHotReloadStatus 在当前状态基础上执行原子更新。
func (a *App) refreshHotReloadStatus(mutator func(svc.HotReloadStatus) svc.HotReloadStatus) {
	if a == nil || a.ServiceContext == nil || mutator == nil {
		return
	}
	a.hotReload.UpdateStatus(a.ServiceContext, mutator)
}

// markHotReloadFailure 记录最近一次热加载失败状态，并对重复错误限频。
func (a *App) markHotReloadFailure(messageKey, message string, err error, fingerprint, source, category, configFile string) {
	if a == nil {
		return
	}
	now := time.Now()
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	// 失败状态始终发布，日志限频不能隐藏最新故障。
	a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
		status.LastStatus = "failed"
		status.LastMessageKey = strings.TrimSpace(messageKey)
		if status.LastMessageKey == "" {
			status.LastMessageKey = i18n.MsgKeyHotReloadFailed
		}
		status.LastMessage = strings.TrimSpace(message)
		status.LastReloadAt = now
		status.LastFailureAt = now
		status.LastTriggerSource = hotreload.Source(source)
		status.LastFailureCategory = hotreload.FailureCategory(category)
		// 失败候选只写入日志；状态中的配置版本仍代表最后一次成功加载。
		return status
	})
	errorKey := message + "|" + errText + "|" + source + "|" + category
	// 相同失败在三十秒内只累计压制次数，避免重复日志和告警。
	if a.hotReload.SuppressFailure(errorKey, now, 30*time.Second) {
		a.refreshHotReloadStatus(func(status svc.HotReloadStatus) svc.HotReloadStatus {
			status.SuppressedFailureCount++
			return status
		})
		return
	}
	// 首次或窗口外失败同时写日志并触发运行告警。
	loggerx.ErrorTextw(context.Background(), "配置 热加载失败", errText,
		logx.Field("file", configFile),
		logx.Field("detail", message),
		logx.Field("version", fingerprint),
		logx.Field("source", hotreload.Source(source)),
		logx.Field("category", hotreload.FailureCategory(category)),
	)
	a.notifyConfigReloadFailure(message, err, source, category, configFile)
}
