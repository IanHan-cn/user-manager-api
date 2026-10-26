package user

import (
	"errors"
	"strings"
	"testing"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// TestNew_只有外部标识时构造成功
//
// 本地用户是 ZITADEL 用户的业务侧映射，外部标识是唯一必填项：
// 用户名、邮箱、手机号在 ZITADEL 侧都可以缺省，缺任何一个都不影响本地行成立。
func TestNew_只有外部标识时构造成功(t *testing.T) {
	u, err := New("zitadel-uid-1")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if u.ID == "" {
		t.Fatal("主键不应为空")
	}
	if u.ZitadelUserID != "zitadel-uid-1" {
		t.Fatalf("ZitadelUserID = %q，期望 %q", u.ZitadelUserID, "zitadel-uid-1")
	}
	if u.Status != UserStatusActive {
		t.Fatalf("初始状态 = %q，期望 %q", u.Status, UserStatusActive)
	}
	if u.EmailVerified {
		t.Fatal("未经验证的邮箱，EmailVerified 应为 false")
	}
	if u.Username != "" || u.DisplayName != "" || u.Email != "" || u.Phone != "" || u.AvatarURL != "" {
		t.Fatalf("未传入的可选字段应为空，实际 %+v", u)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Fatal("创建时间与更新时间不应为零值")
	}
}

// TestNew_外部标识非法
func TestNew_外部标识非法(t *testing.T) {
	cases := map[string]string{
		"空串":   "",
		"纯空白":  "   ",
		"超长标识": strings.Repeat("a", maxZitadelUserIDLen+1),
	}

	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			u, err := New(id)
			if !errors.Is(err, ErrInvalidZitadelUserID) {
				t.Fatalf("err = %v，期望 ErrInvalidZitadelUserID", err)
			}
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
			}
			if u != nil {
				t.Fatalf("构造失败时不应返回实体，实际 %+v", u)
			}
		})
	}
}

// TestNew_外部标识去空白
func TestNew_外部标识去空白(t *testing.T) {
	u, err := New("  zitadel-uid-1\n")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if u.ZitadelUserID != "zitadel-uid-1" {
		t.Fatalf("ZitadelUserID = %q，期望去掉首尾空白", u.ZitadelUserID)
	}
}

// TestNew_可选项按需填充并归一化
func TestNew_可选项按需填充并归一化(t *testing.T) {
	u, err := New("zitadel-uid-1",
		WithUsername("  Alice_Chen "),
		WithDisplayName(" 张三 "),
		WithEmail(" Alice@Example.COM "),
		WithPhone(" +8613800000000 "),
		WithAvatarURL("https://cdn.example.com/avatar.png"),
		WithStatus(UserStatusInactive),
	)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if u.Username != "alice_chen" {
		t.Fatalf("Username = %q，期望归一化后的 %q", u.Username, "alice_chen")
	}
	if u.DisplayName != "张三" {
		t.Fatalf("DisplayName = %q，期望去掉首尾空白", u.DisplayName)
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("Email = %q，期望归一化后的小写邮箱", u.Email)
	}
	if u.Phone != "+8613800000000" {
		t.Fatalf("Phone = %q，期望去掉首尾空白并保留前导 +", u.Phone)
	}
	if u.AvatarURL != "https://cdn.example.com/avatar.png" {
		t.Fatalf("AvatarURL = %q，期望原样保留", u.AvatarURL)
	}
	if u.Status != UserStatusInactive {
		t.Fatalf("Status = %q，期望被 WithStatus 覆写为 %q", u.Status, UserStatusInactive)
	}
}

// TestNew_可选项非法时整体失败
//
// 任一选项不合法就返回错误：半填充的实体若被放出去，
// 上层可能把它当成完整用户继续走同步流程。
func TestNew_可选项非法时整体失败(t *testing.T) {
	cases := map[string]struct {
		opt  Option
		want error
	}{
		"用户名含空格":     {WithUsername("bad name"), ErrInvalidUsername},
		"用户名含大写以外符号": {WithUsername("alice#1"), ErrInvalidUsername},
		"用户名超长":      {WithUsername(strings.Repeat("a", maxUsernameLen+1)), ErrInvalidUsername},
		"邮箱缺少@":      {WithEmail("alice.example.com"), ErrInvalidEmail},
		"邮箱域名不带点":    {WithEmail("alice@localhost"), ErrInvalidEmail},
		"邮箱带显示名":     {WithEmail("张三 <alice@example.com>"), ErrInvalidEmail},
		"邮箱超长":       {WithEmail(strings.Repeat("a", maxEmailLen-5) + "@x.com"), ErrInvalidEmail},
		"手机号位数不足":    {WithPhone("138"), ErrInvalidPhone},
		"手机号含字母":     {WithPhone("1380000abcd"), ErrInvalidPhone},
		"手机号超长":      {WithPhone("+" + strings.Repeat("1", maxPhoneLen)), ErrInvalidPhone},
		"头像非http协议":  {WithAvatarURL("ftp://cdn.example.com/a.png"), ErrInvalidAvatarURL},
		"头像伪协议":      {WithAvatarURL("javascript:alert(1)"), ErrInvalidAvatarURL},
		"头像不是URL":    {WithAvatarURL("cdn.example.com/a.png"), ErrInvalidAvatarURL},
		"头像超长":       {WithAvatarURL("https://x.com/" + strings.Repeat("a", maxAvatarURLLen)), ErrInvalidAvatarURL},
		"展示名超长":      {WithDisplayName(strings.Repeat("名", maxDisplayNameLen+1)), domain.ErrInvalidName},
		"状态不在词表内":    {WithStatus(UserStatus("weird")), ErrInvalidUserStatus},
		"状态大小写不符":    {WithStatus(UserStatus("ACTIVE")), ErrInvalidUserStatus},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u, err := New("zitadel-uid-1", tc.opt)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v，期望 %v", err, tc.want)
			}
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
			}
			if u != nil {
				t.Fatalf("构造失败时不应返回实体，实际 %+v", u)
			}
		})
	}
}

// TestNew_空值选项视为未设置
//
// 空串与纯空白都表示"ZITADEL 侧没有这一项"，不得被判为格式非法。
func TestNew_空值选项视为未设置(t *testing.T) {
	u, err := New("zitadel-uid-1",
		WithUsername("   "),
		WithDisplayName(""),
		WithEmail("  "),
		WithPhone(""),
		WithAvatarURL("   "),
	)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if u.Username != "" || u.DisplayName != "" || u.Email != "" || u.Phone != "" || u.AvatarURL != "" {
		t.Fatalf("空值选项应保持零值，实际 %+v", u)
	}
}

// TestNormalizeEmail_统一小写并去空白
func TestNormalizeEmail_统一小写并去空白(t *testing.T) {
	cases := map[string]string{
		"  Alice@Example.COM ": "alice@example.com",
		"alice@example.com":    "alice@example.com",
		"":                     "",
	}

	for input, want := range cases {
		if got := NormalizeEmail(input); got != want {
			t.Fatalf("NormalizeEmail(%q) = %q，期望 %q", input, got, want)
		}
	}
}

// TestUserStatus_Valid_未知状态一律非法
//
// 从数据库读到的行也要能走这条判定：未知状态必须判为非法（fail closed），
// 不能被当成合法状态继续流转。
func TestUserStatus_Valid_未知状态一律非法(t *testing.T) {
	for _, s := range []UserStatus{UserStatusActive, UserStatusInactive, UserStatusSuspended} {
		if !s.Valid() {
			t.Fatalf("状态 %q 应合法", s)
		}
	}
	for _, s := range []UserStatus{"", "ACTIVE", "deleted", "weird"} {
		if s.Valid() {
			t.Fatalf("状态 %q 应非法", s)
		}
	}
}
