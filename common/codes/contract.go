package codes

// 默认响应文案 key，按业务码契约分段维护，i18n 包据此加载语言包。
const (
	// MsgKeyUndefined 表示未知业务状态的通用文案 key。
	MsgKeyUndefined = "common.undefined"
	// MsgKeySuccess 表示通用成功响应文案 key。
	MsgKeySuccess = "common.success"
	// MsgKeyFail 表示通用失败响应文案 key。
	MsgKeyFail = "common.fail"
	// MsgKeyOK 表示 HTTP OK 语义的文案 key。
	MsgKeyOK = "http.ok"
	// MsgKeyBadRequest 表示请求参数或格式错误的 HTTP 文案 key。
	MsgKeyBadRequest = "http.bad_request"
	// MsgKeyUnauthorized 表示未授权访问的 HTTP 文案 key。
	MsgKeyUnauthorized = "http.unauthorized"
	// MsgKeyForbidden 表示无权限访问的 HTTP 文案 key。
	MsgKeyForbidden = "http.forbidden"
	// MsgKeyNotFound 表示资源未找到的 HTTP 文案 key。
	MsgKeyNotFound = "http.not_found"
	// MsgKeyServerError 表示服务端异常的 HTTP 文案 key。
	MsgKeyServerError = "http.server_error"
	// MsgKeyServiceBusy 表示服务繁忙或依赖不可用的 HTTP 文案 key。
	MsgKeyServiceBusy = "http.service_busy"
	// MsgKeyTimeout 表示请求超时的 HTTP 文案 key。
	MsgKeyTimeout = "http.timeout"

	// MsgKeyParamError 表示通用参数错误的业务文案 key。
	MsgKeyParamError = "biz.param_error"
	// MsgKeyAuthFailed 表示认证失败的业务文案 key。
	MsgKeyAuthFailed = "biz.auth_failed"
	// MsgKeyRateLimit 表示触发限流保护的业务文案 key。
	MsgKeyRateLimit = "biz.rate_limit"
	// MsgKeyInternalError 表示内部错误的业务文案 key。
	MsgKeyInternalError = "biz.internal_error"
	// MsgKeyDBError 表示数据库错误的业务文案 key。
	MsgKeyDBError = "biz.db_error"

	// MsgKeyCreateSuccess 表示创建成功的业务文案 key。
	MsgKeyCreateSuccess = "biz.create_success"
	// MsgKeyCreateFail 表示创建失败的业务文案 key。
	MsgKeyCreateFail = "biz.create_fail"
	// MsgKeySaveSuccess 表示保存成功的业务文案 key。
	MsgKeySaveSuccess = "biz.save_success"
	// MsgKeySaveFail 表示保存失败的业务文案 key。
	MsgKeySaveFail = "biz.save_fail"
	// MsgKeyUpdateSuccess 表示更新成功的业务文案 key。
	MsgKeyUpdateSuccess = "biz.update_success"
	// MsgKeyUpdateFail 表示更新失败的业务文案 key。
	MsgKeyUpdateFail = "biz.update_fail"
	// MsgKeyDeleteSuccess 表示删除成功的业务文案 key。
	MsgKeyDeleteSuccess = "biz.delete_success"
	// MsgKeyDeleteFail 表示删除失败的业务文案 key。
	MsgKeyDeleteFail = "biz.delete_fail"
	// MsgKeyFetchSuccess 表示获取成功的业务文案 key。
	MsgKeyFetchSuccess = "biz.fetch_success"
	// MsgKeyFetchFail 表示获取失败的业务文案 key。
	MsgKeyFetchFail = "biz.fetch_fail"

	// MsgKeyTokenExpired 表示登录 token 已过期的认证文案 key。
	MsgKeyTokenExpired = "auth.token_expired"
	// MsgKeyTokenInvalid 表示登录 token 无效的认证文案 key。
	MsgKeyTokenInvalid = "auth.token_invalid"
	// MsgKeySessionExpired 表示服务端会话已失效的认证文案 key。
	MsgKeySessionExpired = "auth.session_expired"
	// MsgKeyInvalidPassword 表示账号或密码错误的认证文案 key。
	MsgKeyInvalidPassword = "auth.invalid_password"
	// MsgKeyRegisterDisabled 表示注册入口已关闭的认证文案 key。
	MsgKeyRegisterDisabled = "auth.register_disabled"
	// MsgKeySecurityAppIDInvalid 表示安全链路 AppID 无效的文案 key。
	MsgKeySecurityAppIDInvalid = "security.app_id_invalid"
	// MsgKeySecurityKeyUnavailable 表示安全链路秘钥不可用的文案 key。
	MsgKeySecurityKeyUnavailable = "security.key_unavailable"
	// MsgKeySecurityRequestRejected 表示请求未通过统一安全校验的文案 key。
	MsgKeySecurityRequestRejected = "security.request_rejected"
	// MsgKeySecurityPayloadTooLarge 表示安全字段超过限制的文案 key。
	MsgKeySecurityPayloadTooLarge = "security.payload_too_large"
	// MsgKeySecurityCryptoDisabled 表示加解密链路未启用的文案 key。
	MsgKeySecurityCryptoDisabled = "security.crypto_disabled"
	// MsgKeySecurityResponseSignFailed 表示响应签名处理失败的文案 key。
	MsgKeySecurityResponseSignFailed = "security.response_sign_failed"
	// MsgKeySecurityResponseEncryptFailed 表示响应加密处理失败的文案 key。
	MsgKeySecurityResponseEncryptFailed = "security.response_encrypt_failed"

	// MsgKeyUserNotFound 表示用户不存在的文案 key。
	MsgKeyUserNotFound = "user.not_found"
	// MsgKeyUserAlreadyExists 对账号标识冲突统一提示，不披露已存在的联系方式。
	MsgKeyUserAlreadyExists = "user.already_exists"
	// MsgKeyUserDisabled 表示账号被禁用的文案 key。
	MsgKeyUserDisabled = "user.disabled"

	// MsgKeyDependencyUnavailable 表示核心依赖不可用的文案 key。
	MsgKeyDependencyUnavailable = "dependency.unavailable"
	// MsgKeyMySQLUnavailable 表示 MySQL 不可用的文案 key。
	MsgKeyMySQLUnavailable = "dependency.mysql_unavailable"
	// MsgKeyRedisUnavailable 表示 Redis 不可用的文案 key。
	MsgKeyRedisUnavailable = "dependency.redis_unavailable"
)

const (
	// statusPayloadTooLarge 表示安全字段超过处理上限时建议返回的 HTTP 状态码。
	statusPayloadTooLarge = 413
	// statusTooManyRequests 表示触发限流保护时建议返回的 HTTP 状态码。
	statusTooManyRequests = 429
)

// CodeContract 描述业务码的默认响应契约。
type CodeContract struct {
	Code       int    // Code 是唯一业务码。
	HTTPStatus int    // HTTPStatus 是该业务码建议返回的 HTTP 状态码。
	Success    bool   // Success 表示统一响应是否按成功结果处理。
	MessageKey string // MessageKey 是默认多语言文案 key。
}

// codeSpec 是内部响应码契约源，元素按通用、认证、用户和依赖分段维护。
type codeSpec struct {
	code       int    // code 表示唯一业务码。
	httpStatus int    // httpStatus 表示建议返回的 HTTP 状态码。
	success    bool   // success 表示统一响应是否按成功处理。
	messageKey string // messageKey 表示默认多语言文案 key。
}

// defaultCodeSpecs 是业务码默认契约源，派生成功码集合、HTTP 状态和默认文案 key。
var defaultCodeSpecs = []codeSpec{
	{code: Undefined, httpStatus: ServerError, messageKey: MsgKeyUndefined},   // 未定义业务码按服务端异常和未知状态文案处理。
	{code: Success, httpStatus: OK, success: true, messageKey: MsgKeySuccess}, // 通用成功码按成功响应处理。
	{code: Fail, httpStatus: ServerError, messageKey: MsgKeyFail},             // 通用失败码按服务端异常处理。

	{code: OK, httpStatus: OK, success: true, messageKey: MsgKeyOK},                // HTTP OK 语义按成功响应处理。
	{code: BadRequest, httpStatus: BadRequest, messageKey: MsgKeyBadRequest},       // 错误请求返回 HTTP 400。
	{code: Unauthorized, httpStatus: Unauthorized, messageKey: MsgKeyUnauthorized}, // 未授权返回 HTTP 401。
	{code: Forbidden, httpStatus: Forbidden, messageKey: MsgKeyForbidden},          // 禁止访问返回 HTTP 403。
	{code: NotFound, httpStatus: NotFound, messageKey: MsgKeyNotFound},             // 资源不存在返回 HTTP 404。
	{code: ServerError, httpStatus: ServerError, messageKey: MsgKeyServerError},    // 服务端异常返回 HTTP 500。
	{code: ServiceBusy, httpStatus: ServiceBusy, messageKey: MsgKeyServiceBusy},    // 服务繁忙返回 HTTP 503。
	{code: Timeout, httpStatus: Timeout, messageKey: MsgKeyTimeout},                // 请求超时返回 HTTP 504。

	{code: ParamError, httpStatus: BadRequest, messageKey: MsgKeyParamError},              // 参数错误返回 HTTP 400。
	{code: AuthFailed, httpStatus: Unauthorized, messageKey: MsgKeyAuthFailed},            // 认证失败返回 HTTP 401。
	{code: RateLimit, httpStatus: statusTooManyRequests, messageKey: MsgKeyRateLimit},     // 请求过多返回 HTTP 429。
	{code: InternalError, httpStatus: ServerError, messageKey: MsgKeyInternalError},       // 内部错误返回 HTTP 500。
	{code: DBError, httpStatus: ServerError, messageKey: MsgKeyDBError},                   // 数据库错误返回 HTTP 500。
	{code: CreateSuccess, httpStatus: OK, success: true, messageKey: MsgKeyCreateSuccess}, // 创建成功按成功响应处理。
	{code: CreateFail, httpStatus: ServerError, messageKey: MsgKeyCreateFail},             // 创建失败返回 HTTP 500。
	{code: SaveSuccess, httpStatus: OK, success: true, messageKey: MsgKeySaveSuccess},     // 保存成功按成功响应处理。
	{code: SaveFail, httpStatus: ServerError, messageKey: MsgKeySaveFail},                 // 保存失败返回 HTTP 500。
	{code: UpdateSuccess, httpStatus: OK, success: true, messageKey: MsgKeyUpdateSuccess}, // 更新成功按成功响应处理。
	{code: UpdateFail, httpStatus: ServerError, messageKey: MsgKeyUpdateFail},             // 更新失败返回 HTTP 500。
	{code: DeleteSuccess, httpStatus: OK, success: true, messageKey: MsgKeyDeleteSuccess}, // 删除成功按成功响应处理。
	{code: DeleteFail, httpStatus: ServerError, messageKey: MsgKeyDeleteFail},             // 删除失败返回 HTTP 500。
	{code: FetchSuccess, httpStatus: OK, success: true, messageKey: MsgKeyFetchSuccess},   // 获取成功按成功响应处理。
	{code: FetchFail, httpStatus: ServerError, messageKey: MsgKeyFetchFail},               // 获取失败返回 HTTP 500。

	{code: InvalidPassword, httpStatus: BadRequest, messageKey: MsgKeyInvalidPassword},                              // 账号或密码错误返回 HTTP 400。
	{code: TokenExpired, httpStatus: Unauthorized, messageKey: MsgKeyTokenExpired},                                  // 访问令牌过期返回 HTTP 401。
	{code: TokenInvalid, httpStatus: Unauthorized, messageKey: MsgKeyTokenInvalid},                                  // 访问令牌无效返回 HTTP 401。
	{code: SessionExpired, httpStatus: Unauthorized, messageKey: MsgKeySessionExpired},                              // 服务端会话失效返回 HTTP 401。
	{code: RegisterDisabled, httpStatus: Forbidden, messageKey: MsgKeyRegisterDisabled},                             // 关闭注册返回 HTTP 403。
	{code: SecurityAppIDInvalid, httpStatus: BadRequest, messageKey: MsgKeySecurityAppIDInvalid},                    // 安全 AppID 无效返回 HTTP 400。
	{code: SecurityKeyUnavailable, httpStatus: ServerError, messageKey: MsgKeySecurityKeyUnavailable},               // 安全秘钥不可用返回 HTTP 500。
	{code: SecurityRequestRejected, httpStatus: Unauthorized, messageKey: MsgKeySecurityRequestRejected},            // 请求安全校验失败返回 HTTP 401，不暴露具体阶段。
	{code: SecurityPayloadTooLarge, httpStatus: statusPayloadTooLarge, messageKey: MsgKeySecurityPayloadTooLarge},   // 安全字段超限返回 HTTP 413。
	{code: SecurityCryptoDisabled, httpStatus: Forbidden, messageKey: MsgKeySecurityCryptoDisabled},                 // 加解密链路未启用返回 HTTP 403。
	{code: SecurityResponseSignFailed, httpStatus: ServerError, messageKey: MsgKeySecurityResponseSignFailed},       // 响应回签失败返回 HTTP 500。
	{code: SecurityResponseEncryptFailed, httpStatus: ServerError, messageKey: MsgKeySecurityResponseEncryptFailed}, // 响应加密失败返回 HTTP 500。
	{code: UserNotFound, httpStatus: NotFound, messageKey: MsgKeyUserNotFound},                                      // 用户不存在返回 HTTP 404。
	{code: UserAlreadyExists, httpStatus: BadRequest, messageKey: MsgKeyUserAlreadyExists},                          // 账号标识已被占用返回 HTTP 400。
	{code: UserDisabled, httpStatus: Unauthorized, messageKey: MsgKeyUserDisabled},                                  // 账号禁用返回 HTTP 401。

	{code: DependencyUnavailable, httpStatus: ServiceBusy, messageKey: MsgKeyDependencyUnavailable}, // 核心依赖不可用返回 HTTP 503。
	{code: MySQLUnavailable, httpStatus: ServiceBusy, messageKey: MsgKeyMySQLUnavailable},           // MySQL 不可用返回 HTTP 503。
	{code: RedisUnavailable, httpStatus: ServiceBusy, messageKey: MsgKeyRedisUnavailable},           // Redis 不可用返回 HTTP 503。
}

var (
	// successCodeSet 由 defaultCodeSpecs 派生统一响应可识别为成功的业务码集合。
	successCodeSet = buildSuccessCodeSet(defaultCodeSpecs)
	// codeHTTPStatusMap 由 defaultCodeSpecs 派生业务码到 HTTP 状态码的建议映射。
	codeHTTPStatusMap = buildCodeHTTPStatusMap(defaultCodeSpecs)
	// codeMessageKeyMap 由 defaultCodeSpecs 派生业务码到默认多语言 key 的映射。
	codeMessageKeyMap = buildCodeMessageKeyMap(defaultCodeSpecs)
)

// DefaultCodeContracts 返回业务码默认响应契约快照，调用方不能修改内部源表。
func DefaultCodeContracts() []CodeContract {
	contracts := make([]CodeContract, 0, len(defaultCodeSpecs))
	for _, spec := range defaultCodeSpecs {
		contracts = append(contracts, CodeContract{
			Code:       spec.code,
			HTTPStatus: spec.httpStatus,
			Success:    spec.success,
			MessageKey: spec.messageKey,
		})
	}
	return contracts
}

// MessageKey 返回业务码默认多语言文案 key，未知业务码返回 false。
func MessageKey(code int) (string, bool) {
	key, ok := codeMessageKeyMap[code]
	return key, ok
}

// IsSuccess 判断业务码是否代表成功结果。
func IsSuccess(code int) bool {
	_, ok := successCodeSet[code]
	return ok
}

// HTTPStatus 返回内置业务码的 HTTP 映射；未声明的业务码按 500 处理。
func HTTPStatus(code int) int {
	if status, ok := codeHTTPStatusMap[code]; ok {
		return status
	}
	// 成功码已包含在契约表，未登记业务码统一按内部错误处理。
	return ServerError
}

// buildSuccessCodeSet 从响应码契约派生成功码集合。
func buildSuccessCodeSet(specs []codeSpec) map[int]struct{} {
	result := make(map[int]struct{})
	for _, spec := range specs {
		if spec.success {
			result[spec.code] = struct{}{}
		}
	}
	return result
}

// buildCodeHTTPStatusMap 从响应码契约派生 HTTP 状态码映射。
func buildCodeHTTPStatusMap(specs []codeSpec) map[int]int {
	result := make(map[int]int, len(specs))
	for _, spec := range specs {
		result[spec.code] = spec.httpStatus
	}
	return result
}

// buildCodeMessageKeyMap 从响应码契约派生默认多语言 key 映射。
func buildCodeMessageKeyMap(specs []codeSpec) map[int]string {
	result := make(map[int]string, len(specs))
	for _, spec := range specs {
		if spec.messageKey != "" {
			result[spec.code] = spec.messageKey
		}
	}
	return result
}
