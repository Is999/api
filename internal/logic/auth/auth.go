package auth

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	codes "api/common/codes"
	i18n "api/common/i18n"
	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/common/secureid"
	"api/internal/config"
	"api/internal/infra/loggerx"
	corelogic "api/internal/logic"
	userlogic "api/internal/logic/user"
	"api/internal/model"
	"api/internal/requestctx"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
	"github.com/golang-jwt/jwt/v4"
	"github.com/zeromicro/go-zero/core/logx"
	"golang.org/x/crypto/bcrypt"
)

// 认证入口限流动作名称。
const (
	authRateLimitActionLoginIP       = "login_ip"       // 登录 IP 维度
	authRateLimitActionLoginIdentity = "login_identity" // 登录身份维度
	authRateLimitActionRegisterIP    = "register_ip"    // 注册 IP 维度
)

const (
	// authLoginDummyPasswordHash 用于不存在账号的等时 bcrypt 校验，避免通过响应耗时枚举用户账号。
	authLoginDummyPasswordHash = "$2y$10$ory3FZfUy1VExaUHmEkeluYtVtP/4CiCCfeSPfD12T9dbpWqO52Eq"
)

// 前台用户 ID 生成命名空间。
const (
	userIDNamespace = "user" // api/admin 写同一用户表必须使用同一业务命名空间
)

// 前台用户会话保护边界。
const (
	maxUserSessions                = 8               // 单个用户最多保留的有效会话数，超出时原子淘汰最早过期会话
	authRuntimeCleanupTimeout      = 3 * time.Second // Lua 结果不确定或数据库写入失败时执行 Redis 补偿的独立超时
	registrationSessionWaitTimeout = 2 * time.Second // 注册创建 Redis 会话的等待上限，超时后不进入数据库写入
	// 撤销标记覆盖历史配置与并发刷新签发的最长 JWT，再留一秒覆盖秒级到期边界。
	userSessionRevocationTTLSeconds = config.MaxJWTExpiresInSeconds + 1
)

// 认证与会话内部错误哨兵。
var (
	// ErrAuthRateLimited 表示认证入口触发限流。
	ErrAuthRateLimited = errors.New("认证入口触发限流")
	// ErrSessionStale 表示刷新使用的旧会话已被消费或失效。
	ErrSessionStale = errors.New("用户会话已失效")
	// ErrAuthVersionMismatch 表示 Redis 认证版本已领先于调用方数据库快照。
	ErrAuthVersionMismatch = errors.New("用户认证版本不一致")
	// ErrAuthRedisUnavailable 标记认证会话或限流所依赖的 Redis 不可用。
	ErrAuthRedisUnavailable = errors.New("认证 Redis 不可用")
)

// AuthLogic 承载前台注册、登录和会话刷新逻辑。
type AuthLogic struct {
	*corelogic.BaseLogic // 复用上下文、日志、数据库和缓存等公共能力
}

// NewAuthLogic 创建前台认证逻辑对象。
func NewAuthLogic(ctx context.Context, svcCtx *svc.ServiceContext) *AuthLogic {
	return &AuthLogic{BaseLogic: corelogic.NewBaseLogicWithContext(ctx, svcCtx)}
}

// Register 注册前台用户并创建登录态。
func (l *AuthLogic) Register(req *types.RegisterReq) *types.BizResult {
	if err := req.Validate(); err != nil {
		return types.ParamErrorResult(err).
			WithError(err)
	}
	cfg := l.Svc.CurrentConfig()
	if !cfg.Auth.RegisterEnabled {
		return types.NewBizResult(codes.RegisterDisabled).
			SetI18nMessage(i18n.MsgKeyRegisterDisabled).
			WithError(errors.New("AuthLogic.Register 注册入口未开放"))
	}
	if utf8.RuneCountInString(req.Password) < l.passwordMinLength() {
		err := errors.Errorf("密码长度不能少于 %d 位", l.passwordMinLength())
		return types.ParamErrorResult(err).
			WithError(err)
	}
	// 先限制注册来源，再执行身份查询和密码哈希，防止未认证流量占满数据库与 CPU。
	if err := l.checkAuthRateLimit(authRateLimitActionRegisterIP, l.ClientIP(), cfg.Auth.RegisterRateLimit); err != nil {
		if errors.Is(err, ErrAuthRateLimited) {
			l.emitAuthEvent(AuthEventInput{
				Action:   AuthEventActionRateLimited,
				Identity: model.UserIdentitySubject(model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, req.Username),
				Reason:   AuthEventReasonRegisterIPRateLimited,
			})
		}
		return authRateLimitResult(err)
	}
	// 前置身份查询只用于快速拒绝，最终唯一性仍由数据库约束保证。
	exists, err := model.FindUserIdentity(l.Svc.WriteDB(svc.DatabaseMain), model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, req.Username, cfg.AppKey)
	if err != nil {
		return mysqlUnavailableResult(err, "AuthLogic.Register 查询本地用户名身份").ToBizResult()
	}
	if exists != nil {
		return types.NewBizResult(codes.UserAlreadyExists).
			SetI18nMessage(i18n.MsgKeyUserAlreadyExists).
			WithError(errors.New("AuthLogic.Register 账号标识已被占用"))
	}
	// 密码哈希放在事务外计算，避免高成本 CPU 运算占用数据库连接。
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return types.ServerError(i18n.MsgKeyInternalError, err, "AuthLogic.Register 生成密码哈希失败").ToBizResult()
	}
	// 用户 ID 在事务外生成，避免号段或租约访问拉长事务时间。
	userID, err := idgen.NextID(userIDNamespace)
	if err != nil {
		return types.ServerError(i18n.MsgKeyInternalError, err, "AuthLogic.Register 生成用户 ID失败").ToBizResult()
	}
	now := time.Now()
	user := &model.User{
		ID:           userID,
		ShardNo:      idgen.ShardNo(userID),
		Username:     req.Username,
		Nickname:     req.Nickname,
		PasswordHash: string(passwordHash),
		Email:        req.Email,
		Phone:        req.Phone,
		Status:       model.UserStatusEnabled,
		AuthVersion:  1,
		LastLoginAt:  now,
		LastLoginIP:  l.ClientIP(),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if user.Nickname == "" {
		user.Nickname = user.Username
	}
	// 会话先于数据库写入创建，避免网络等待占用数据库连接。
	sessionCtx, cancel := context.WithTimeout(l.Ctx, registrationSessionWaitTimeout)
	created, err := NewAuthLogic(sessionCtx, l.Svc).createSession(user, sessionRollbackUncommittedRegistration)
	cancel()
	if err != nil {
		return authSessionFailureResult(err, "AuthLogic.Register 创建用户 ID[%d]会话失败", user.ID)
	}

	db := l.Svc.WriteDB(svc.DatabaseMain)
	err = model.CreateUserWithIdentities(db, user, cfg.User.RouteShardCount, cfg.AppKey)
	if err != nil {
		// 数据库失败时撤销预创建会话，并恢复本次容量淘汰的旧会话。
		if cleanupErr := l.rollbackCreatedSessionWithIndependentTimeout(user.ID, user.AuthVersion, created, sessionRollbackUncommittedRegistration); cleanupErr != nil {
			return authSessionFailureResult(errors.Join(err, cleanupErr),
				"AuthLogic.Register 创建用户 ID[%d]失败且会话回滚失败", user.ID)
		}
		if corelogic.IsMySQLDuplicateEntryError(err) {
			return types.NewBizResult(codes.UserAlreadyExists).
				SetI18nMessage(i18n.MsgKeyUserAlreadyExists).
				WithError(errors.New("AuthLogic.Register 账号标识已被占用"))
		}
		return mysqlUnavailableResult(err, "AuthLogic.Register 创建用户 ID[%d]", user.ID).ToBizResult()
	}
	// 用户和身份表提交后再组装响应，认证流程不回写全量资料缓存。
	created.Response.User = userlogic.BuildUserProfile(user)
	l.emitAuthEvent(AuthEventInput{
		Action:    AuthEventActionRegisterSuccess,
		UserID:    user.ID,
		Identity:  model.UserIdentitySubject(model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, user.Username),
		SessionID: created.SessionID,
		Reason:    AuthEventReasonSessionCreated,
	})
	return types.NewBizResult(codes.CreateSuccess).
		SetI18nMessage(i18n.MsgKeyCreateSuccess).
		WithData(created.Response)
}

// Login 校验账号密码并创建登录态。
func (l *AuthLogic) Login(req *types.LoginReq) *types.BizResult {
	if err := req.Validate(); err != nil {
		return types.ParamErrorResult(err).
			WithError(err)
	}
	cfg := l.Svc.CurrentConfig()
	if err := l.checkAuthRateLimit(authRateLimitActionLoginIP, l.ClientIP(), cfg.Auth.LoginRateLimit); err != nil {
		if errors.Is(err, ErrAuthRateLimited) {
			l.emitAuthEvent(AuthEventInput{
				Action:   AuthEventActionRateLimited,
				Identity: loginIdentitySubject(req),
				Reason:   AuthEventReasonLoginIPRateLimited,
			})
		}
		return authRateLimitResult(err)
	}
	// IP 限制阻止单源撞库，身份限制阻止跨 IP 持续尝试同一账号。
	identitySubject := loginIdentitySubject(req)
	if err := l.checkAuthRateLimit(authRateLimitActionLoginIdentity, identitySubject, cfg.Auth.LoginRateLimit); err != nil {
		if errors.Is(err, ErrAuthRateLimited) {
			l.emitAuthEvent(AuthEventInput{
				Action:   AuthEventActionRateLimited,
				Identity: identitySubject,
				Reason:   AuthEventReasonLoginIdentityRateLimited,
			})
		}
		return authRateLimitResult(err)
	}
	user, err := model.FindUserByIdentity(l.Svc.WriteDB(svc.DatabaseMain), req.IdentityType, model.UserIdentityProviderLocal, req.IdentityValue, cfg.AppKey, cfg.User.RouteShardCount)
	if err != nil {
		return mysqlUnavailableResult(err, "AuthLogic.Login 查询登录身份类型[%s]", req.IdentityType).ToBizResult()
	}
	if user == nil {
		// 用户不存在时仍执行固定哈希校验，使失败路径耗时接近，避免通过响应时间枚举账号。
		_ = bcrypt.CompareHashAndPassword([]byte(authLoginDummyPasswordHash), []byte(req.Password))
		l.emitAuthEvent(AuthEventInput{
			Action:   AuthEventActionLoginFailed,
			Identity: identitySubject,
			Reason:   AuthEventReasonInvalidPassword,
		})
		return invalidPasswordResult(errors.Errorf("AuthLogic.Login 登录身份类型[%s]不存在", req.IdentityType))
	}
	if err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		l.emitAuthEvent(AuthEventInput{
			Action:   AuthEventActionLoginFailed,
			UserID:   user.ID,
			Identity: identitySubject,
			Reason:   AuthEventReasonInvalidPassword,
		})
		return invalidPasswordResult(errors.Errorf("AuthLogic.Login 登录身份类型[%s]密码错误", req.IdentityType))
	}
	// 密码通过后才判断禁用状态，避免未持有密码的请求探测账号状态。
	if user.Status != model.UserStatusEnabled {
		l.emitAuthEvent(AuthEventInput{
			Action:   AuthEventActionLoginFailed,
			UserID:   user.ID,
			Identity: identitySubject,
			Reason:   AuthEventReasonUserDisabled,
		})
		return invalidPasswordResult(errors.Errorf("AuthLogic.Login 用户 ID[%d]已禁用", user.ID))
	}
	now := time.Now()
	user.LastLoginAt = now
	user.LastLoginIP = l.ClientIP()
	user.UpdatedAt = now
	// Redis 会话先创建，失败时不更新最后登录信息。
	created, err := l.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		return authSessionFailureResult(err, "AuthLogic.Login 创建用户 ID[%d]会话失败", user.ID)
	}
	if err = model.UpdateUser(l.Svc.WriteDB(svc.DatabaseMain), user.ID, map[string]any{
		"last_login_at": now,
		"last_login_ip": l.ClientIP(),
		"updated_at":    now,
	}, cfg.User.RouteShardCount); err != nil {
		// 登录信息未提交则撤销本次会话；独立超时保证请求取消后仍能执行补偿。
		if cleanupErr := l.rollbackCreatedSessionWithIndependentTimeout(user.ID, user.AuthVersion, created, sessionRollbackExistingUser); cleanupErr != nil {
			err = errors.Wrapf(err, "更新最后登录信息失败且清理本次会话失败 cleanup_error=%v", cleanupErr)
		}
		return mysqlUnavailableResult(err, "AuthLogic.Login 更新用户 ID[%d]登录信息", user.ID).ToBizResult()
	}
	// 登录信息提交后只删除资料缓存，避免旧快照覆盖并发资料更新。
	if cacheErr := userlogic.NewUserLogic(l.Ctx, l.Svc).DeleteUserProfileCache(user.ID); cacheErr != nil {
		loggerx.Errorw(l.Ctx, "登录 资料缓存失效失败", cacheErr, logx.Field("user_id", user.ID))
	}
	// 登录成功后清除失败计数，清理失败只计指标。
	if err := l.clearAuthRateLimit(authRateLimitActionLoginIP, l.ClientIP()); err != nil {
		recordAuthRateLimitCleanupFailure(authRateLimitActionLoginIP)
	}
	if err := l.clearAuthRateLimit(authRateLimitActionLoginIdentity, identitySubject); err != nil {
		recordAuthRateLimitCleanupFailure(authRateLimitActionLoginIdentity)
	}
	l.emitAuthEvent(AuthEventInput{
		Action:    AuthEventActionLoginSuccess,
		UserID:    user.ID,
		Identity:  identitySubject,
		SessionID: created.SessionID,
		Reason:    AuthEventReasonSessionCreated,
	})
	return types.NewBizResult(codes.Success).
		SetI18nMessage(i18n.MsgKeySuccess).
		WithData(created.Response)
}

// Refresh 使用鉴权中间件已确认的用户快照原子轮换当前会话 token。
func (l *AuthLogic) Refresh() *types.BizResult {
	ctxUser := l.GetCtxUser()
	if ctxUser == nil || ctxUser.ID <= 0 {
		return types.NewBizResult(codes.Unauthorized).
			SetI18nMessage(i18n.MsgKeyUnauthorizedText).
			WithError(errors.New("AuthLogic.Refresh 当前请求未登录"))
	}
	// 复用鉴权中间件确认的用户快照，避免同一请求重复查库。
	user := requestctx.AuthUserFromContext(l.Ctx)
	if user == nil || user.Profile.ID != ctxUser.ID {
		return types.NewBizResult(codes.Unauthorized).
			SetI18nMessage(i18n.MsgKeyUnauthorizedText).
			WithError(errors.New("AuthLogic.Refresh 当前请求缺少鉴权用户快照"))
	}
	sessionID := ""
	if meta := l.Meta(); meta != nil {
		sessionID = meta.SessionID
	}
	if sessionID == "" {
		return types.NewBizResult(codes.TokenInvalid).
			SetI18nMessage(i18n.MsgKeyTokenInvalid).
			WithError(errors.New("AuthLogic.Refresh 当前 token 缺少 sid"))
	}
	resp, err := l.rotateSession(user, sessionID, l.AccessToken())
	if err != nil {
		if errors.Is(err, ErrSessionStale) || errors.Is(err, ErrAuthVersionMismatch) {
			return types.NewBizResult(codes.SessionExpired).
				SetI18nMessage(i18n.MsgKeySessionExpired).
				WithError(corelogic.WrapLogicError(err, "AuthLogic.Refresh 用户 ID[%d]原会话已失效", ctxUser.ID))
		}
		return authSessionFailureResult(err, "AuthLogic.Refresh 用户 ID[%d]轮换会话", ctxUser.ID)
	}
	l.emitAuthEvent(AuthEventInput{
		Action:    AuthEventActionRefreshSuccess,
		UserID:    user.Profile.ID,
		Identity:  model.UserIdentitySubject(model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, user.Profile.Username),
		SessionID: sessionID,
		Reason:    AuthEventReasonSessionRotated,
	})
	return types.NewBizResult(codes.Success).
		SetI18nMessage(i18n.MsgKeySuccess).
		WithData(resp)
}

// Logout 清理当前用户登录态。
func (l *AuthLogic) Logout() *types.BizResult {
	ctxUser := l.GetCtxUser()
	if ctxUser == nil || ctxUser.ID <= 0 {
		return types.NewBizResult(codes.Unauthorized).
			SetI18nMessage(i18n.MsgKeyUnauthorizedText).
			WithError(errors.New("AuthLogic.Logout 当前请求未登录"))
	}
	sessionID := ""
	if meta := l.Meta(); meta != nil {
		sessionID = meta.SessionID
	}
	if sessionID == "" {
		return types.NewBizResult(codes.TokenInvalid).
			SetI18nMessage(i18n.MsgKeyTokenInvalid).
			WithError(errors.New("AuthLogic.Logout 当前 token 缺少 sid"))
	}
	// 按稳定 sid 注销，使并发刷新生成的新 token 也随本次退出失效。
	if err := l.deleteUserSession(ctxUser.ID, sessionID); err != nil {
		return authSessionFailureResult(err, "AuthLogic.Logout 用户 ID[%d]清理会话", ctxUser.ID)
	}
	l.emitAuthEvent(AuthEventInput{
		Action:    AuthEventActionLogoutSuccess,
		UserID:    ctxUser.ID,
		Identity:  model.UserIdentitySubject(model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, ctxUser.Name),
		SessionID: sessionID,
		Reason:    AuthEventReasonCurrentSessionDeleted,
	})
	return types.NewBizResult(codes.Success).
		SetI18nMessage(i18n.MsgKeyLogoutSuccess)
}

// createdSession 表示已写入 Redis 的新会话。
type createdSession struct {
	Response  *types.AuthTokenResp // Response 表示返回给客户端的 token 数据
	SessionID string               // SessionID 表示一次登录会话内保持稳定的会话 ID
	Evicted   []evictedSession     // Evicted 保存本次会话上限淘汰项，仅供数据库失败补偿恢复
}

// evictedSession 保存创建新会话时被原子淘汰的旧会话快照。
type evictedSession struct {
	SessionID   string // SessionID 是待恢复旧会话的稳定 sid
	Token       string // Token 是待恢复旧会话的完整访问令牌，不得写入日志
	ExpiresAtMS int64  // ExpiresAtMS 是 Redis ZSET 使用的 Unix 毫秒过期时间
}

// sessionRollbackMode 区分已落库用户与尚未提交的注册补偿。
type sessionRollbackMode uint8

const (
	sessionRollbackExistingUser            sessionRollbackMode = iota // 保留空版本栅栏，阻止并发旧快照倒退版本
	sessionRollbackUncommittedRegistration                            // 删除空版本栅栏，避免未落库用户遗留 Redis Key
)

// createSession 生成独立 sid 与 jti，并原子写入 Redis 会话。
func (l *AuthLogic) createSession(user *model.User, rollbackMode sessionRollbackMode) (*createdSession, error) {
	if user == nil {
		return nil, errors.New("用户为空")
	}
	if user.AuthVersion == 0 {
		return nil, errors.New("用户认证版本不能为空")
	}
	sessionID, err := newTokenID()
	if err != nil {
		return nil, errors.Wrap(err, "生成用户会话 ID 失败")
	}
	jti, err := newTokenID()
	if err != nil {
		return nil, errors.Wrap(err, "生成 JWT ID 失败")
	}
	token, expiresAt, err := l.generateJWT(user.ID, user.Username, user.AuthVersion, sessionID, jti)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if l.Redis() == nil {
		return nil, authRedisUnavailableError(errors.New("Redis 未初始化"), "创建用户会话")
	}
	now := time.Now()
	ttlSeconds := l.sessionTTL()
	sessionExpiresAtMS := now.Add(time.Duration(ttlSeconds) * time.Second).UnixMilli()
	// 失败补偿只持有本次 sid 和 token，不恢复无法确认的容量淘汰项。
	pendingSession := &createdSession{
		SessionID: sessionID,
		Response:  &types.AuthTokenResp{Token: token},
	}
	// Lua 同时校验认证版本、写入新会话并返回淘汰项，数据库失败时可原样恢复旧会话。
	result, err := userSessionCreateScript.Run(
		l.Ctx,
		l.Redis(),
		keys.UserSessionKeys(user.ID),
		now.UnixMilli(),
		user.AuthVersion,
		sessionID,
		token,
		sessionExpiresAtMS,
		maxUserSessions,
	).Slice()
	if err != nil {
		createErr := authRedisUnavailableError(err, "原子创建用户会话失败 user_id=%d sid=%s", user.ID, sessionID)
		return nil, l.compensateSessionCreateFailure(user.ID, user.AuthVersion, pendingSession, rollbackMode, createErr)
	}
	evicted, err := parseSessionCreateResult(result)
	if err != nil {
		if errors.Is(err, ErrAuthVersionMismatch) {
			return nil, err
		}
		parseErr := authRedisUnavailableError(err, "解析用户会话创建结果失败 user_id=%d sid=%s", user.ID, sessionID)
		return nil, l.compensateSessionCreateFailure(user.ID, user.AuthVersion, pendingSession, rollbackMode, parseErr)
	}
	pendingSession.Evicted = evicted
	pendingSession.Response.ExpiresAt = expiresAt
	pendingSession.Response.User = userlogic.BuildUserProfile(user)
	return pendingSession, nil
}

// compensateSessionCreateFailure 合并会话创建失败与独立补偿结果。
func (l *AuthLogic) compensateSessionCreateFailure(userID int64, authVersion uint64, pending *createdSession, rollbackMode sessionRollbackMode, createErr error) error {
	cleanupErr := l.rollbackCreatedSessionWithIndependentTimeout(userID, authVersion, pending, rollbackMode)
	return errors.Join(createErr, cleanupErr)
}

// parseSessionCreateResult 解析 Lua 返回的淘汰快照，拒绝不完整结果以免补偿时误恢复会话。
func parseSessionCreateResult(result []any) ([]evictedSession, error) {
	if len(result) == 0 {
		return nil, errors.New("用户会话创建结果为空")
	}
	count, ok := result[0].(int64)
	if !ok {
		return nil, errors.Errorf("用户会话创建结果数量类型非法: %T", result[0])
	}
	if count == -1 {
		return nil, ErrAuthVersionMismatch
	}
	if count < 0 || count > int64((len(result)-1)/3) || len(result) != 1+int(count)*3 {
		return nil, errors.Errorf("用户会话创建结果长度非法 count=%d length=%d", count, len(result))
	}
	evicted := make([]evictedSession, 0, int(count))
	// Lua 每个淘汰项固定返回 sid、token、毫秒截止时间三个连续值。
	for index := range int(count) {
		offset := 1 + index*3
		sessionID, sessionOK := result[offset].(string)
		token, tokenOK := result[offset+1].(string)
		expiresText, expiresOK := result[offset+2].(string)
		expiresAtMS, expiresErr := strconv.ParseInt(expiresText, 10, 64)
		if !sessionOK || !tokenOK || !expiresOK || sessionID == "" || token == "" || expiresErr != nil || expiresAtMS <= 0 {
			return nil, errors.Errorf("用户会话创建结果第%d个淘汰项非法", index)
		}
		evicted = append(evicted, evictedSession{SessionID: sessionID, Token: token, ExpiresAtMS: expiresAtMS})
	}
	return evicted, nil
}

// rotateSession 在稳定 sid 下原子比较完整旧 token 并写入新 token。
func (l *AuthLogic) rotateSession(user *requestctx.AuthUser, sessionID string, previousToken string) (*types.AuthTokenResp, error) {
	sessionID = strings.TrimSpace(sessionID)
	if user == nil || user.Profile.ID <= 0 {
		return nil, errors.New("用户为空")
	}
	if user.AuthVersion == 0 {
		return nil, errors.New("用户认证版本不能为空")
	}
	if sessionID == "" || previousToken == "" {
		return nil, errors.New("原会话标识不能为空")
	}
	if l.Redis() == nil {
		return nil, authRedisUnavailableError(errors.New("Redis 未初始化"), "轮换用户会话")
	}
	newJTI, err := newTokenID()
	if err != nil {
		return nil, errors.Wrap(err, "生成 JWT ID 失败")
	}
	newToken, expiresAt, err := l.generateJWT(user.Profile.ID, user.Profile.Username, user.AuthVersion, sessionID, newJTI)
	if err != nil {
		return nil, errors.Tag(err)
	}
	now := time.Now()
	// Lua 同时比较认证版本、sid 和完整旧 token，保证多实例并发刷新只有一次成功。
	result, err := userSessionRotateScript.Run(
		l.Ctx,
		l.Redis(),
		keys.UserSessionKeys(user.Profile.ID),
		now.UnixMilli(),
		user.AuthVersion,
		sessionID,
		previousToken,
		newToken,
		now.Add(time.Duration(l.sessionTTL())*time.Second).UnixMilli(),
	).Int64()
	if err != nil {
		return nil, authRedisUnavailableError(err, "原子轮换用户会话失败 user_id=%d sid=%s", user.Profile.ID, sessionID)
	}
	switch result {
	case -1:
		return nil, ErrAuthVersionMismatch
	case 0:
		return nil, ErrSessionStale
	}
	// 响应使用本次鉴权快照，禁止回写可能已过期的共享资料缓存。
	profile := new(user.Profile)
	return &types.AuthTokenResp{Token: newToken, ExpiresAt: expiresAt, User: profile}, nil
}

// generateJWT 生成包含用户、站点、稳定 sid 和唯一 jti 的访问令牌。
func (l *AuthLogic) generateJWT(userID int64, username string, authVersion uint64, sessionID string, jti string) (string, int64, error) {
	sessionID = strings.TrimSpace(sessionID)
	jti = strings.TrimSpace(jti)
	if userID <= 0 || authVersion == 0 || sessionID == "" || jti == "" {
		return "", 0, errors.New("用户或认证版本非法")
	}
	cfg := l.Svc.CurrentConfig()
	now := time.Now()
	expiresAt := now.Add(time.Duration(config.JWTExpiresInSeconds(cfg.JwtExpiresIn)) * time.Second).Unix()
	claims := jwt.MapClaims{
		"sub":          strconv.FormatInt(userID, 10), // 雪花 ID 使用字符串，避免 JSON 浮点精度损失
		"username":     username,                      // 仅供身份展示，授权以主库状态和认证版本为准
		"sid":          sessionID,                     // 一次登录内稳定，刷新不新增会话
		"jti":          jti,                           // 每次签发独立生成，保证 token 可做完整值 CAS
		"iss":          cfg.Auth.Issuer,               // 启动配置约定的令牌发行方
		"app_id":       cfg.AppID,                     // 绑定站点，不能跨应用复用令牌
		"auth_version": authVersion,                   // 主库认证版本，敏感变更后旧版本立即失效
		"iat":          now.Unix(),                    // 签发时间，Unix 秒
		"exp":          expiresAt,                     // 过期时间，Unix 秒；JWT 到期后即使 Redis 尚未清理也拒绝鉴权
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(cfg.JwtSecret))
	return tokenString, expiresAt, errors.Tag(err)
}

// deleteUserSession 删除指定 sid 对应的当前登录会话。
func (l *AuthLogic) deleteUserSession(userID int64, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if userID <= 0 || sessionID == "" {
		return errors.New("用户会话标识不能为空")
	}
	if l.Redis() == nil {
		return authRedisUnavailableError(errors.New("Redis 未初始化"), "删除用户会话")
	}
	// sid 即使已被并发登录淘汰，也要原子记录撤销，阻止失败补偿恢复它。
	_, err := userSessionDeleteScript.Run(
		l.Ctx,
		l.Redis(),
		[]string{keys.UserSessionHashKey(userID), keys.UserSessionIndexKey(userID), keys.UserSessionRevokedKey(userID, sessionID)},
		sessionID,
		userSessionRevocationTTLSeconds,
	).Int64()
	if err != nil {
		return authRedisUnavailableError(err, "删除用户会话失败 user_id=%d sid=%s", userID, sessionID)
	}
	return nil
}

// rollbackCreatedSessionWithIndependentTimeout 使用独立短超时执行会话补偿。
func (l *AuthLogic) rollbackCreatedSessionWithIndependentTimeout(userID int64, authVersion uint64, created *createdSession, rollbackMode sessionRollbackMode) error {
	// 原请求超时后仍需撤销预创建会话，独立截止时间限制补偿占用。
	cleanupCtx, cancel := context.WithTimeout(context.Background(), authRuntimeCleanupTimeout)
	defer cancel()
	return errors.Tag(NewAuthLogic(cleanupCtx, l.Svc).rollbackCreatedSession(userID, authVersion, created, rollbackMode))
}

// rollbackCreatedSession 在认证版本未推进时原子删除新会话，并按容量恢复本次被淘汰的有效旧会话。
func (l *AuthLogic) rollbackCreatedSession(userID int64, authVersion uint64, created *createdSession, rollbackMode sessionRollbackMode) error {
	if userID <= 0 || authVersion == 0 || created == nil || created.Response == nil || created.SessionID == "" || created.Response.Token == "" {
		return errors.New("待回滚用户会话参数非法")
	}
	if l.Redis() == nil {
		return authRedisUnavailableError(errors.New("Redis 未初始化"), "回滚用户会话")
	}
	// 撤销键与淘汰快照逐项对应，同一次 Lua 只恢复未退出、未被其它失败登录撤销的 sid。
	sessionKeys := append(keys.UserSessionKeys(userID), keys.UserSessionRevokedKey(userID, created.SessionID))
	args := make([]any, 0, 7+len(created.Evicted)*3)
	args = append(args, time.Now().UnixMilli(), authVersion, created.SessionID, created.Response.Token, maxUserSessions, int(rollbackMode), userSessionRevocationTTLSeconds)
	for _, evicted := range created.Evicted {
		sessionKeys = append(sessionKeys, keys.UserSessionRevokedKey(userID, evicted.SessionID))
		args = append(args, evicted.SessionID, evicted.Token, evicted.ExpiresAtMS)
	}
	result, err := userSessionRollbackScript.Run(l.Ctx, l.Redis(), sessionKeys, args...).Int64()
	if err != nil {
		return authRedisUnavailableError(err, "原子回滚用户会话失败 user_id=%d sid=%s", userID, created.SessionID)
	}
	// 认证版本已经推进时，新旧会话均已由失效脚本清除，禁止恢复旧版本登录态。
	if result == -1 {
		return nil
	}
	if result < 0 {
		return errors.Errorf("用户会话回滚结果非法 result=%d", result)
	}
	return nil
}

// InvalidateUserSessions 按已提交的数据库认证版本原子删除全部登录态。
func (l *AuthLogic) InvalidateUserSessions(userID int64, authVersion uint64) error {
	if userID <= 0 || authVersion == 0 {
		return errors.New("用户 ID 和认证版本不能为空")
	}
	if l.Redis() == nil {
		return authRedisUnavailableError(errors.New("Redis 未初始化"), "失效用户会话")
	}
	invalidatedCount, err := userSessionInvalidateScript.Run(
		l.Ctx,
		l.Redis(),
		keys.UserSessionKeys(userID),
		authVersion,
		l.authVersionFenceTTL(),
	).Int64()
	if err != nil {
		return authRedisUnavailableError(err, "原子失效用户会话失败 user_id=%d auth_version=%d", userID, authVersion)
	}
	if invalidatedCount < 0 {
		return ErrAuthVersionMismatch
	}
	// Redis 栅栏推进成功后才记录会话失效事件。
	l.emitAuthEvent(AuthEventInput{
		Action: AuthEventActionSessionInvalidateAll,
		UserID: userID,
		Reason: AuthEventReasonUserSessionsInvalidated,
		Count:  int(invalidatedCount),
	})
	return nil
}

// sessionTTL 限制会话配置时长不超过 JWT 时长，实际鉴权仍同时检查 JWT 和 Redis 截止时间。
func (l *AuthLogic) sessionTTL() int64 {
	cfg := l.Svc.CurrentConfig()
	jwtTTL := config.JWTExpiresInSeconds(cfg.JwtExpiresIn)
	if cfg.Auth.SessionTTLSeconds > 0 && cfg.Auth.SessionTTLSeconds < jwtTTL {
		return cfg.Auth.SessionTTLSeconds
	}
	return jwtTTL
}

// authVersionFenceTTL 返回认证版本栅栏 TTL，覆盖 JWT 最长存活期。
func (l *AuthLogic) authVersionFenceTTL() int64 {
	return config.JWTExpiresInSeconds(l.Svc.CurrentConfig().JwtExpiresIn)
}

// checkAuthRateLimit 校验认证入口在 Redis 中的限流状态。
func (l *AuthLogic) checkAuthRateLimit(action, subject string, cfg config.AuthRateLimitConfig) error {
	cfg = normalizeAuthRateLimitConfig(action, cfg)
	if !cfg.Enabled {
		return nil
	}
	if l.Redis() == nil {
		return errors.Errorf("认证限流 Redis 未初始化 action=%s", action)
	}
	countKey, lockKey := l.authRateLimitKeys(action, subject)
	// Lua 原子更新计数与锁定状态，负数表示仍在锁定期。
	result, err := authRateLimitScript.Run(
		l.Ctx,
		l.Redis(),
		[]string{countKey, lockKey},
		cfg.WindowSeconds,
		cfg.MaxAttempts,
		cfg.LockSeconds,
	).Int64()
	if err != nil {
		return errors.Wrapf(err, "原子更新认证限流失败 action=%s", action)
	}
	if result < 0 {
		return ErrAuthRateLimited
	}
	return nil
}

// clearAuthRateLimit 删除当前主体的限流计数和锁定状态。
func (l *AuthLogic) clearAuthRateLimit(action, subject string) error {
	if l == nil || l.Redis() == nil {
		return errors.Errorf("清理认证限流时 Redis 未初始化 action=%s", action)
	}
	countKey, lockKey := l.authRateLimitKeys(action, subject)
	if err := l.Redis().Del(l.Ctx, countKey, lockKey).Err(); err != nil {
		return errors.Wrapf(err, "清理认证限流失败 action=%s", action)
	}
	return nil
}

// authRateLimitKeys 生成认证入口限流计数和锁定 Redis Key。
func (l *AuthLogic) authRateLimitKeys(action, subject string) (string, string) {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "unknown"
	}
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		subject = "unknown"
	}
	// HMAC 隐藏邮箱、手机号和客户端 IP，避免 Redis Key 暴露可枚举原文。
	subjectHash := authSensitiveValueHash(l.Svc.CurrentConfig(), subject)
	return l.AppRedisKey(fmt.Sprintf(keys.AuthRateLimitCount, action, subjectHash)),
		l.AppRedisKey(fmt.Sprintf(keys.AuthRateLimitLock, action, subjectHash))
}

// normalizeAuthRateLimitConfig 补齐认证限流默认值。
func normalizeAuthRateLimitConfig(action string, cfg config.AuthRateLimitConfig) config.AuthRateLimitConfig {
	if cfg.WindowSeconds <= 0 {
		cfg.WindowSeconds = 60
	}
	if cfg.LockSeconds <= 0 {
		cfg.LockSeconds = cfg.WindowSeconds
	}
	if cfg.MaxAttempts <= 0 {
		switch action {
		case authRateLimitActionRegisterIP:
			cfg.MaxAttempts = 3
		default:
			cfg.MaxAttempts = 5
		}
	}
	return cfg
}

// passwordMinLength 返回注册密码最小长度，未配置时使用 8 位。
func (l *AuthLogic) passwordMinLength() int {
	cfg := l.Svc.CurrentConfig()
	if cfg.Auth.PasswordMinLength > 0 {
		return cfg.Auth.PasswordMinLength
	}
	return 8
}

// loginIdentitySubject 返回密码登录限流和风控使用的身份主体。
func loginIdentitySubject(req *types.LoginReq) string {
	if req == nil {
		return ""
	}
	return model.UserIdentitySubject(req.IdentityType, model.UserIdentityProviderLocal, req.IdentityValue)
}

// invalidPasswordResult 返回统一账号或密码错误，避免暴露账号存在性。
func invalidPasswordResult(err error) *types.BizResult {
	return types.NewBizResult(codes.InvalidPassword).
		SetI18nMessage(i18n.MsgKeyInvalidPassword).
		WithError(err)
}

// authRateLimitResult 返回统一限流或内部错误响应。
func authRateLimitResult(err error) *types.BizResult {
	if errors.Is(err, ErrAuthRateLimited) {
		return types.NewBizResult(codes.RateLimit).
			SetI18nMessage(i18n.MsgKeyRateLimit).
			WithError(err)
	}
	return redisUnavailableResult(err, "AuthLogic.RateLimit").ToBizResult()
}

// authSessionFailureResult 把会话依赖故障与进程内部错误映射到不同响应，避免把 Redis 故障伪装成 HTTP 500。
func authSessionFailureResult(err error, format string, args ...any) *types.BizResult {
	if errors.Is(err, ErrAuthRedisUnavailable) {
		return redisUnavailableResult(err, format, args...).ToBizResult()
	}
	return types.ServerError(i18n.MsgKeyInternalError, err, append([]any{format}, args...)...).ToBizResult()
}

// mysqlUnavailableResult 构造 MySQL 依赖不可用响应，错误详情只进入服务端日志。
func mysqlUnavailableResult(err error, format string, args ...any) *types.Error {
	return types.Errorf(codes.MySQLUnavailable, i18n.MsgKeyMySQLUnavailable, err, format, args...)
}

// redisUnavailableResult 构造 Redis 依赖不可用响应，错误详情只进入服务端日志。
func redisUnavailableResult(err error, format string, args ...any) *types.Error {
	return types.Errorf(codes.RedisUnavailable, i18n.MsgKeyRedisUnavailable, err, format, args...)
}

// authRedisUnavailableError 同时保留依赖分类与底层错误链，供响应映射和日志排障使用。
func authRedisUnavailableError(err error, format string, args ...any) error {
	return errors.Join(ErrAuthRedisUnavailable, errors.Wrapf(err, format, args...))
}

// newTokenID 生成不含分隔符的随机 token 标识。
func newTokenID() (string, error) {
	return secureid.NewHex(16)
}
