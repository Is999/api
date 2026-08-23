package auth

import (
	_ "embed"

	"api/common/embedasset"

	"github.com/redis/go-redis/v9"
)

var (
	// userSessionCreateScript 原子创建会话、推进认证版本并执行每用户会话上限。
	userSessionCreateScript = redis.NewScript(embedasset.StripLeadingLineComments(userSessionCreateScriptText, "--"))
	// userSessionRollbackScript 在数据库提交失败时按认证版本栅栏回滚新会话并恢复本次淘汰项。
	userSessionRollbackScript = redis.NewScript(embedasset.StripLeadingLineComments(userSessionRollbackScriptText, "--"))
	// userSessionRotateScript 在稳定 sid 下原子 CAS 完整旧 token。
	userSessionRotateScript = redis.NewScript(embedasset.StripLeadingLineComments(userSessionRotateScriptText, "--"))
	// userSessionInvalidateScript 按已提交的数据库认证版本原子失效全部会话。
	userSessionInvalidateScript = redis.NewScript(embedasset.StripLeadingLineComments(userSessionInvalidateScriptText, "--"))
	// userSessionDeleteScript 原子删除单个 sid 会话及其索引成员。
	userSessionDeleteScript = redis.NewScript(embedasset.StripLeadingLineComments(userSessionDeleteScriptText, "--"))
)

// userSessionCreateScriptText 保存会话创建 Lua 资产。
//
//go:embed assets/user_session_create.lua
var userSessionCreateScriptText string

// userSessionRollbackScriptText 保存会话创建失败补偿 Lua 资产。
//
//go:embed assets/user_session_rollback.lua
var userSessionRollbackScriptText string

// userSessionRotateScriptText 保存会话轮换 Lua 资产。
//
//go:embed assets/user_session_rotate.lua
var userSessionRotateScriptText string

// userSessionInvalidateScriptText 保存会话全量失效 Lua 资产。
//
//go:embed assets/user_session_invalidate.lua
var userSessionInvalidateScriptText string

// userSessionDeleteScriptText 保存单会话删除 Lua 资产。
//
//go:embed assets/user_session_delete.lua
var userSessionDeleteScriptText string
