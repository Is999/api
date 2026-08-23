package i18n

import codes "api/common/codes"

// 多语言消息 key，按通用、认证、用户和依赖模块分段维护。
const (
	// MsgKeyUndefined 表示未知业务状态的通用文案 key。
	MsgKeyUndefined = codes.MsgKeyUndefined
	// MsgKeySuccess 表示通用成功响应文案 key。
	MsgKeySuccess = codes.MsgKeySuccess
	// MsgKeyFail 表示通用失败响应文案 key。
	MsgKeyFail = codes.MsgKeyFail
	// MsgKeyOK 表示 HTTP OK 语义的文案 key。
	MsgKeyOK = codes.MsgKeyOK
	// MsgKeyBadRequest 表示请求参数或格式错误的 HTTP 文案 key。
	MsgKeyBadRequest = codes.MsgKeyBadRequest
	// MsgKeyUnauthorized 表示未授权访问的 HTTP 文案 key。
	MsgKeyUnauthorized = codes.MsgKeyUnauthorized
	// MsgKeyForbidden 表示无权限访问的 HTTP 文案 key。
	MsgKeyForbidden = codes.MsgKeyForbidden
	// MsgKeyNotFound 表示资源未找到的 HTTP 文案 key。
	MsgKeyNotFound = codes.MsgKeyNotFound
	// MsgKeyServerError 表示服务端异常的 HTTP 文案 key。
	MsgKeyServerError = codes.MsgKeyServerError
	// MsgKeyServiceBusy 表示服务繁忙或依赖不可用的 HTTP 文案 key。
	MsgKeyServiceBusy = codes.MsgKeyServiceBusy
	// MsgKeyTimeout 表示请求超时的 HTTP 文案 key。
	MsgKeyTimeout = codes.MsgKeyTimeout

	// MsgKeyParamError 表示通用参数错误的业务文案 key。
	MsgKeyParamError = codes.MsgKeyParamError
	// MsgKeyParamErrorFormat 表示参数错误动态详情模板 key。
	MsgKeyParamErrorFormat = "fmt.param_error"
	// MsgKeyAuthFailed 表示认证失败的业务文案 key。
	MsgKeyAuthFailed = codes.MsgKeyAuthFailed
	// MsgKeyRateLimit 表示触发限流保护的业务文案 key。
	MsgKeyRateLimit = codes.MsgKeyRateLimit
	// MsgKeyInternalError 表示内部错误的业务文案 key。
	MsgKeyInternalError = codes.MsgKeyInternalError
	// MsgKeyInternalErrorFormat 表示内部错误动态详情模板 key。
	MsgKeyInternalErrorFormat = "fmt.internal_error"
	// MsgKeyDBError 表示数据库错误的业务文案 key。
	MsgKeyDBError = codes.MsgKeyDBError
	// MsgKeyDBErrorFormat 表示数据库错误动态详情模板 key。
	MsgKeyDBErrorFormat = "fmt.db_error"

	// MsgKeyCreateSuccess 表示创建成功的业务文案 key。
	MsgKeyCreateSuccess = codes.MsgKeyCreateSuccess
	// MsgKeyCreateFail 表示创建失败的业务文案 key。
	MsgKeyCreateFail = codes.MsgKeyCreateFail
	// MsgKeySaveSuccess 表示保存成功的业务文案 key。
	MsgKeySaveSuccess = codes.MsgKeySaveSuccess
	// MsgKeySaveFail 表示保存失败的业务文案 key。
	MsgKeySaveFail = codes.MsgKeySaveFail
	// MsgKeyUpdateSuccess 表示更新成功的业务文案 key。
	MsgKeyUpdateSuccess = codes.MsgKeyUpdateSuccess
	// MsgKeyUpdateFail 表示更新失败的业务文案 key。
	MsgKeyUpdateFail = codes.MsgKeyUpdateFail
	// MsgKeyDeleteSuccess 表示删除成功的业务文案 key。
	MsgKeyDeleteSuccess = codes.MsgKeyDeleteSuccess
	// MsgKeyDeleteFail 表示删除失败的业务文案 key。
	MsgKeyDeleteFail = codes.MsgKeyDeleteFail
	// MsgKeyFetchSuccess 表示获取成功的业务文案 key。
	MsgKeyFetchSuccess = codes.MsgKeyFetchSuccess
	// MsgKeyFetchFail 表示获取失败的业务文案 key。
	MsgKeyFetchFail = codes.MsgKeyFetchFail

	// MsgKeyUnauthorizedText 表示需要登录或重新登录的认证文案 key。
	MsgKeyUnauthorizedText = "auth.unauthorized_text"
	// MsgKeyTokenExpired 表示登录 token 已过期的认证文案 key。
	MsgKeyTokenExpired = codes.MsgKeyTokenExpired
	// MsgKeyTokenInvalid 表示登录 token 无效的认证文案 key。
	MsgKeyTokenInvalid = codes.MsgKeyTokenInvalid
	// MsgKeySessionExpired 表示服务端会话已失效的认证文案 key。
	MsgKeySessionExpired = codes.MsgKeySessionExpired
	// MsgKeyInvalidPassword 表示账号或密码错误的认证文案 key。
	MsgKeyInvalidPassword = codes.MsgKeyInvalidPassword
	// MsgKeyLogoutSuccess 表示登出成功的认证文案 key。
	MsgKeyLogoutSuccess = "auth.logout_success"
	// MsgKeyRegisterDisabled 表示注册入口已关闭的认证文案 key。
	MsgKeyRegisterDisabled = codes.MsgKeyRegisterDisabled
	// MsgKeySecurityAppIDInvalid 表示安全链路 AppID 无效的文案 key。
	MsgKeySecurityAppIDInvalid = codes.MsgKeySecurityAppIDInvalid
	// MsgKeySecurityKeyUnavailable 表示安全链路秘钥不可用的文案 key。
	MsgKeySecurityKeyUnavailable = codes.MsgKeySecurityKeyUnavailable
	// MsgKeySecurityRequestRejected 表示请求未通过统一安全校验的文案 key。
	MsgKeySecurityRequestRejected = codes.MsgKeySecurityRequestRejected
	// MsgKeySecurityPayloadTooLarge 表示安全字段超过限制的文案 key。
	MsgKeySecurityPayloadTooLarge = codes.MsgKeySecurityPayloadTooLarge
	// MsgKeySecurityCryptoDisabled 表示加解密链路未启用的文案 key。
	MsgKeySecurityCryptoDisabled = codes.MsgKeySecurityCryptoDisabled
	// MsgKeySecurityResponseSignFailed 表示响应签名处理失败的文案 key。
	MsgKeySecurityResponseSignFailed = codes.MsgKeySecurityResponseSignFailed
	// MsgKeySecurityResponseEncryptFailed 表示响应加密处理失败的文案 key。
	MsgKeySecurityResponseEncryptFailed = codes.MsgKeySecurityResponseEncryptFailed

	// MsgKeyUserNotFound 表示用户不存在的文案 key。
	MsgKeyUserNotFound = codes.MsgKeyUserNotFound
	// MsgKeyUserAlreadyExists 对账号标识冲突统一提示，不披露已存在的联系方式。
	MsgKeyUserAlreadyExists = codes.MsgKeyUserAlreadyExists
	// MsgKeyUserDisabled 表示账号被禁用的文案 key。
	MsgKeyUserDisabled = codes.MsgKeyUserDisabled

	// MsgKeyDependencyUnavailable 表示核心依赖不可用的文案 key。
	MsgKeyDependencyUnavailable = codes.MsgKeyDependencyUnavailable
	// MsgKeyMySQLUnavailable 表示 MySQL 不可用的文案 key。
	MsgKeyMySQLUnavailable = codes.MsgKeyMySQLUnavailable
	// MsgKeyRedisUnavailable 表示 Redis 不可用的文案 key。
	MsgKeyRedisUnavailable = codes.MsgKeyRedisUnavailable

	// MsgKeyHotReloadFailed 表示配置热加载失败状态说明 key。
	MsgKeyHotReloadFailed = "hot_reload.failed"
	// MsgKeyHotReloadFingerprintInitFailed 表示初始化配置指纹失败状态说明 key。
	MsgKeyHotReloadFingerprintInitFailed = "hot_reload.fingerprint_init_failed"
	// MsgKeyHotReloadFingerprintReadFailed 表示读取配置指纹失败状态说明 key。
	MsgKeyHotReloadFingerprintReadFailed = "hot_reload.fingerprint_read_failed"
	// MsgKeyHotReloadFileStatusReadFailed 表示读取配置文件状态失败说明 key。
	MsgKeyHotReloadFileStatusReadFailed = "hot_reload.file_status_read_failed"
	// MsgKeyHotReloadNotBound 表示配置热加载未绑定文件说明 key。
	MsgKeyHotReloadNotBound = "hot_reload.not_bound"
	// MsgKeyHotReloadCancelled 表示配置热加载被取消说明 key。
	MsgKeyHotReloadCancelled = "hot_reload.cancelled"
	// MsgKeyHotReloadSuccess 表示配置热加载成功说明 key。
	MsgKeyHotReloadSuccess = "hot_reload.success"
	// MsgKeyHotReloadSuccessRestart 表示热加载成功但需重启说明 key。
	MsgKeyHotReloadSuccessRestart = "hot_reload.success_restart"
	// MsgKeyHotReloadUnchanged 表示配置无变化说明 key。
	MsgKeyHotReloadUnchanged = "hot_reload.unchanged"
	// MsgKeyHotReloadWatcherNotStarted 表示热加载 watcher 未启动说明 key。
	MsgKeyHotReloadWatcherNotStarted = "hot_reload.watcher_not_started"
	// MsgKeyHotReloadWatcherRunning 表示热加载 watcher 运行中说明 key。
	MsgKeyHotReloadWatcherRunning = "hot_reload.watcher_running"
	// MsgKeyHotReloadWatcherClosed 表示热加载 watcher 已关闭说明 key。
	MsgKeyHotReloadWatcherClosed = "hot_reload.watcher_closed"
	// MsgKeyHotReloadWatcherStopped 表示热加载 watcher 已停止说明 key。
	MsgKeyHotReloadWatcherStopped = "hot_reload.watcher_stopped"
)
