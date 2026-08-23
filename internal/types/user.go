package types

import (
	"strings"

	"github.com/Is999/go-utils/errors"
)

// userRuntimeSyncReasonMaxLength 表示内网同步原因最大字符数。
const userRuntimeSyncReasonMaxLength = 128

// UserProfile 表示业务用户公开资料。
type UserProfile struct {
	ID          int64  `json:"id,string"`   // 用户 ID，JSON 以字符串返回，避免前端丢失精度
	ShardNo     int    `json:"shardNo"`     // ID 哈希分片，来源 CRC32(id字符串)%1024，便于分表和分片游标查询
	Username    string `json:"username"`    // 用户名
	Nickname    string `json:"nickname"`    // 昵称
	Email       string `json:"email"`       // 邮箱脱敏展示值，不返回明文
	Phone       string `json:"phone"`       // 手机号脱敏展示值，不返回明文
	Avatar      string `json:"avatar"`      // 头像
	Status      int    `json:"status"`      // 状态：1 正常，0 禁用
	LastLoginAt string `json:"lastLoginAt"` // 按存储时区输出年月日时分秒，未登录时为空字符串
	LastLoginIP string `json:"lastLoginIp"` // 最后登录 IP
	CreatedAt   string `json:"createdAt"`   // 创建时间，按存储时区输出年月日时分秒
	UpdatedAt   string `json:"updatedAt"`   // 更新时间，按存储时区输出年月日时分秒
}

// UserRuntimeSyncReq 表示内网同步单个业务用户运行态缓存的请求。
type UserRuntimeSyncReq struct {
	ID          int64  `path:"id" json:"-" form:"-"` // 用户 ID 只取路径，正文和查询参数不能改变同步对象。
	Profile     bool   `json:"profile,optional"`     // 是否删除用户资料缓存
	Sessions    bool   `json:"sessions,optional"`    // 是否失效该用户全部登录态
	AuthVersion uint64 `json:"authVersion,optional"` // admin 已在业务用户表提交的新认证版本
	Reason      string `json:"reason,optional"`      // 触发同步的后台操作原因
}

// Validate 校验并归一化内网用户运行态同步请求。
func (r *UserRuntimeSyncReq) Validate() error {
	if r == nil {
		return errors.New("请求不能为空")
	}
	if r.ID <= 0 {
		return errors.New("用户 ID 不能为空")
	}
	if !r.Profile && !r.Sessions {
		// 未指定同步动作只清资料，不隐式撤销用户登录态。
		r.Profile = true
	}
	// 会话撤销只接受后台已提交版本，不能由同步请求自行递增认证版本。
	if r.Sessions && r.AuthVersion == 0 {
		return errors.New("失效登录态时认证版本不能为空")
	}
	r.Reason = strings.TrimSpace(r.Reason)
	if len([]rune(r.Reason)) > userRuntimeSyncReasonMaxLength {
		return errors.Errorf("同步原因不能超过 %d 个字符", userRuntimeSyncReasonMaxLength)
	}
	return nil
}

// UserRuntimeSyncResp 表示业务用户运行态同步结果。
type UserRuntimeSyncResp struct {
	UserID                  int64  `json:"userId,string"`           // 用户 ID，JSON 以字符串返回，避免前端丢失精度
	ProfileCacheInvalidated bool   `json:"profileCacheInvalidated"` // 是否已处理资料缓存
	SessionsInvalidated     bool   `json:"sessionsInvalidated"`     // 是否已处理全部登录态
	AuthVersion             uint64 `json:"authVersion"`             // 本次登录态失效使用的已提交认证版本
	Reason                  string `json:"reason"`                  // 后台传入的同步原因
}
