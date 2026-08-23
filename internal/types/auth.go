package types

import (
	"strings"
	"unicode/utf8"

	"github.com/Is999/go-utils/errors"
)

// 前台认证请求字段边界。
const (
	authUsernameMinLength      = 3   // 用户名最小长度
	authUsernameMaxLength      = 32  // 用户名最大长度
	authNicknameMaxLength      = 64  // 昵称最大长度
	authEmailMaxLength         = 128 // 邮箱最大长度
	authPhoneMaxLength         = 32  // 手机号最大长度
	authIdentityValueMaxLength = 191 // 登录身份值最大长度
	authPasswordMaxBytes       = 72  // bcrypt 登录密码最大字节数
)

// 前台密码登录支持的身份类型。
const (
	LoginIdentityTypeUsername = "username" // 自定义账号登录
	LoginIdentityTypeEmail    = "email"    // 邮箱登录
	LoginIdentityTypePhone    = "phone"    // 手机号登录
)

// RegisterReq 表示前台用户注册请求。
type RegisterReq struct {
	Username string `json:"username"`          // 裁剪首尾空白后为 3-32 个 Unicode 字符
	Password string `json:"password"`          // 原文参与 bcrypt，不裁剪空白；最大 72 字节
	Nickname string `json:"nickname,optional"` // 昵称，最大 64 字符
	Email    string `json:"email,optional"`    // 邮箱，最大 128 字符
	Phone    string `json:"phone,optional"`    // 手机号，最大 32 字符
}

// Validate 校验并归一化前台用户注册请求。
func (r *RegisterReq) Validate() error {
	if r == nil {
		return errors.New("请求不能为空")
	}
	r.Username = strings.TrimSpace(r.Username)
	r.Nickname = strings.TrimSpace(r.Nickname)
	r.Email = strings.TrimSpace(r.Email)
	r.Phone = strings.TrimSpace(r.Phone)
	// 用户可见名称按字符计长；密码另按 bcrypt 字节上限检查。
	usernameLength := utf8.RuneCountInString(r.Username)
	if usernameLength < authUsernameMinLength || usernameLength > authUsernameMaxLength {
		return errors.Errorf("用户名长度必须为 %d-%d 位", authUsernameMinLength, authUsernameMaxLength)
	}
	// 仅用裁剪结果判断空密码，后续 bcrypt 仍接收原文字节。
	if strings.TrimSpace(r.Password) == "" {
		return errors.New("密码不能为空")
	}
	if len(r.Password) > authPasswordMaxBytes {
		return errors.Errorf("密码不能超过 %d 字节", authPasswordMaxBytes)
	}
	if utf8.RuneCountInString(r.Nickname) > authNicknameMaxLength {
		return errors.Errorf("昵称不能超过 %d 个字符", authNicknameMaxLength)
	}
	if utf8.RuneCountInString(r.Email) > authEmailMaxLength {
		return errors.Errorf("邮箱不能超过 %d 个字符", authEmailMaxLength)
	}
	if utf8.RuneCountInString(r.Phone) > authPhoneMaxLength {
		return errors.Errorf("手机号不能超过 %d 个字符", authPhoneMaxLength)
	}
	return nil
}

// LoginReq 表示前台用户登录请求。
type LoginReq struct {
	IdentityType  string `json:"identityType"`  // 登录身份类型：username/email/phone
	IdentityValue string `json:"identityValue"` // 登录身份值
	Password      string `json:"password"`      // 原文参与校验，不裁剪空白；最大 72 字节
}

// Validate 校验并归一化前台用户登录请求。
func (r *LoginReq) Validate() error {
	if r == nil {
		return errors.New("请求不能为空")
	}
	r.IdentityValue = strings.TrimSpace(r.IdentityValue)
	if err := validateLoginIdentity(r.IdentityType, r.IdentityValue); err != nil {
		return errors.Tag(err)
	}
	// 登录与注册保留相同密码字节语义，不能通过 TrimSpace 改变凭据。
	if strings.TrimSpace(r.Password) == "" {
		return errors.New("密码不能为空")
	}
	if len(r.Password) > authPasswordMaxBytes {
		return errors.Errorf("密码不能超过 %d 字节", authPasswordMaxBytes)
	}
	return nil
}

// validateLoginIdentity 校验密码登录身份类型和值。
func validateLoginIdentity(identityType string, identityValue string) error {
	if identityType == "" {
		return errors.New("登录身份类型不能为空")
	}
	if identityValue == "" {
		return errors.New("登录身份不能为空")
	}
	// 同一规范身份只计长一次，通用上限与具体身份规则均按 Unicode 字符判断。
	identityLength := utf8.RuneCountInString(identityValue)
	if identityLength > authIdentityValueMaxLength {
		return errors.Errorf("登录身份不能超过 %d 个字符", authIdentityValueMaxLength)
	}
	switch identityType {
	case LoginIdentityTypeUsername:
		if identityLength < authUsernameMinLength || identityLength > authUsernameMaxLength {
			return errors.Errorf("用户名长度必须为 %d-%d 位", authUsernameMinLength, authUsernameMaxLength)
		}
	case LoginIdentityTypeEmail:
		if identityLength > authEmailMaxLength {
			return errors.Errorf("邮箱不能超过 %d 个字符", authEmailMaxLength)
		}
	case LoginIdentityTypePhone:
		if identityLength > authPhoneMaxLength {
			return errors.Errorf("手机号不能超过 %d 个字符", authPhoneMaxLength)
		}
	default:
		return errors.Errorf("不支持的登录身份类型[%s]", identityType)
	}
	return nil
}

// AuthTokenResp 表示登录或刷新后的令牌响应。
type AuthTokenResp struct {
	Token     string       `json:"token"`     // Bearer 访问令牌
	ExpiresAt int64        `json:"expiresAt"` // 过期时间戳，单位秒
	User      *UserProfile `json:"user"`      // 当前用户资料
}
