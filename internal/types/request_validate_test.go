package types

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zeromicro/go-zero/core/validation"
	"github.com/zeromicro/go-zero/rest/httpx"
)

var (
	_ validation.Validator = (*RegisterReq)(nil)        // _ 校验 RegisterReq 实现 validation.Validator。
	_ validation.Validator = (*LoginReq)(nil)           // _ 校验 LoginReq 实现 validation.Validator。
	_ validation.Validator = (*ConfigItemQueryReq)(nil) // _ 校验 ConfigItemQueryReq 实现 validation.Validator。
	_ validation.Validator = (*UserRuntimeSyncReq)(nil) // _ 校验 UserRuntimeSyncReq 实现 validation.Validator。
)

// TestAuthReqValidate 验证认证请求基础校验和字段归一化。
func TestAuthReqValidate(t *testing.T) {
	registerReq := &RegisterReq{
		Username: " demo_user ",
		Password: "secret123",
		Nickname: " Demo ",
		Email:    " demo@example.com ",
		Phone:    " 13800138000 ",
	}
	if err := registerReq.Validate(); err != nil {
		t.Fatalf("RegisterReq.Validate() error = %v", err)
	}
	if registerReq.Username != "demo_user" || registerReq.Nickname != "Demo" || registerReq.Email != "demo@example.com" || registerReq.Phone != "13800138000" {
		t.Fatalf("RegisterReq.Validate() did not trim fields: %+v", registerReq)
	}
	// 用户名按字符计数，密码受 bcrypt 的 72 字节限制，两者不能共用长度口径。
	if err := (&RegisterReq{Username: "测试用户", Password: "secret123"}).Validate(); err != nil {
		t.Fatalf("RegisterReq.Validate() should count a multibyte username by characters: %v", err)
	}

	cases := []struct {
		name string       // 标明本次越界的字段，失败时可直接定位限制项
		req  *RegisterReq // 仅保留一个非法字段，避免其他校验提前遮蔽目标分支
	}{
		{name: "用户名过短", req: &RegisterReq{Username: "ab", Password: "secret123"}},
		{name: "多字节用户名过长", req: &RegisterReq{Username: strings.Repeat("用", authUsernameMaxLength+1), Password: "secret123"}},
		{name: "密码为空", req: &RegisterReq{Username: "demo_user", Password: "   "}},
		{name: "密码超过bcrypt字节上限", req: &RegisterReq{Username: "demo_user", Password: strings.Repeat("密", 25)}},
		{name: "昵称过长", req: &RegisterReq{Username: "demo_user", Password: "secret123", Nickname: strings.Repeat("名", authNicknameMaxLength+1)}},
		{name: "邮箱过长", req: &RegisterReq{Username: "demo_user", Password: "secret123", Email: strings.Repeat("a", authEmailMaxLength+1)}},
		{name: "手机号过长", req: &RegisterReq{Username: "demo_user", Password: "secret123", Phone: strings.Repeat("1", authPhoneMaxLength+1)}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("RegisterReq.Validate() should reject invalid request")
			}
		})
	}

	loginReq := &LoginReq{IdentityType: LoginIdentityTypeUsername, IdentityValue: " demo_user ", Password: "secret123"}
	if err := loginReq.Validate(); err != nil {
		t.Fatalf("LoginReq.Validate() error = %v", err)
	}
	if loginReq.IdentityType != LoginIdentityTypeUsername || loginReq.IdentityValue != "demo_user" {
		t.Fatalf("LoginReq.Validate() identity = %s:%s, want username:demo_user", loginReq.IdentityType, loginReq.IdentityValue)
	}
	for _, identityType := range []string{" username ", "USERNAME", "Email"} {
		if err := (&LoginReq{IdentityType: identityType, IdentityValue: "demo_user", Password: "secret123"}).Validate(); err == nil {
			t.Fatalf("LoginReq.Validate() 应拒绝非规范 identityType=%q", identityType)
		}
	}
	if err := (&LoginReq{IdentityType: LoginIdentityTypeUsername, IdentityValue: "测试用户", Password: "secret123"}).Validate(); err != nil {
		t.Fatalf("LoginReq.Validate() should count a multibyte username by characters: %v", err)
	}
	if err := (&LoginReq{IdentityType: LoginIdentityTypeUsername, IdentityValue: strings.Repeat("用", authUsernameMaxLength+1), Password: "secret123"}).Validate(); err == nil {
		t.Fatal("LoginReq.Validate() should reject a multibyte username over 32 characters")
	}
	if err := (&LoginReq{IdentityType: LoginIdentityTypeUsername, IdentityValue: "demo_user", Password: " "}).Validate(); err == nil {
		t.Fatal("LoginReq.Validate() should reject blank password")
	}
	if err := (&LoginReq{IdentityType: LoginIdentityTypeUsername, IdentityValue: "demo_user", Password: strings.Repeat("a", authPasswordMaxBytes)}).Validate(); err != nil {
		t.Fatalf("LoginReq.Validate() should accept a 72-byte password: %v", err)
	}
	if err := (&LoginReq{IdentityType: LoginIdentityTypeUsername, IdentityValue: "demo_user", Password: strings.Repeat("a", authPasswordMaxBytes+1)}).Validate(); err == nil {
		t.Fatal("LoginReq.Validate() should reject a 73-byte password")
	}
	if err := (&LoginReq{IdentityType: "oauth", IdentityValue: "demo", Password: "secret123"}).Validate(); err == nil {
		t.Fatal("LoginReq.Validate() should reject oauth password login")
	}
}

// TestGoZeroParseCallsValidate 验证 go-zero 解析请求后会调用 Validate。
func TestGoZeroParseCallsValidate(t *testing.T) {
	// 在内存请求上调用框架解析器，不经过路由或认证中间件。
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"identityType":"username","identityValue":"demo_user","password":"   "}`))
	req.Header.Set("Content-Type", "application/json")

	var parsed LoginReq
	if err := httpx.Parse(req, &parsed); err == nil || !strings.Contains(err.Error(), "密码不能为空") {
		t.Fatalf("httpx.Parse() error = %v, want password validation error", err)
	}
}

// TestConfigItemQueryReqValidate 验证运行态配置查询参数默认值和边界。
func TestConfigItemQueryReqValidate(t *testing.T) {
	req := &ConfigItemQueryReq{
		Keyword:       " security ",
		SensitiveOnly: true,
		Page:          -1,
		PageSize:      1000,
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("ConfigItemQueryReq.Validate() error = %v", err)
	}
	if req.Keyword != "security" || req.Page != 1 || req.PageSize != 100 {
		t.Fatalf("ConfigItemQueryReq.Validate() got %+v, want trimmed keyword and bounded page", req)
	}
	if err := (&ConfigItemQueryReq{Keyword: strings.Repeat("a", 129)}).Validate(); err == nil {
		t.Fatal("ConfigItemQueryReq.Validate() should reject long keyword")
	}
}

// TestUserRuntimeSyncReqValidate 验证内网用户运行态同步参数默认值和边界。
func TestUserRuntimeSyncReqValidate(t *testing.T) {
	req := &UserRuntimeSyncReq{ID: 42, Reason: " manual sync "}
	if err := req.Validate(); err != nil {
		t.Fatalf("UserRuntimeSyncReq.Validate() error = %v", err)
	}
	if !req.Profile || req.Sessions || req.Reason != "manual sync" {
		t.Fatalf("UserRuntimeSyncReq.Validate() got %+v, want profile default true and trimmed reason", req)
	}
	if err := (&UserRuntimeSyncReq{ID: 0}).Validate(); err == nil {
		t.Fatal("UserRuntimeSyncReq.Validate() should reject empty user ID")
	}
	if err := (&UserRuntimeSyncReq{ID: 42, Reason: strings.Repeat("a", userRuntimeSyncReasonMaxLength+1)}).Validate(); err == nil {
		t.Fatal("UserRuntimeSyncReq.Validate() should reject long reason")
	}
	if err := (&UserRuntimeSyncReq{ID: 42, Sessions: true}).Validate(); err == nil {
		t.Fatal("UserRuntimeSyncReq.Validate() should require authVersion for session invalidation")
	}
}
