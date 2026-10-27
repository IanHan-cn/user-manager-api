package role

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// equalIDs 比较两个权限标识序列是否逐项相同。
// 本仓不引入 testify 之类的断言库（见 go.mod），比较逻辑就地写清楚。
func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNormalizePermissionIDs_去重并保持首次出现顺序
//
// 去重是为了不让"语义上相同"的入参升级成一次写失败：数据库唯一索引会拒绝
// 同一 (role_id, permission_id) 出现两次，而重复项在业务上毫无含义；
// 保留首次出现顺序则让同一份入参每次都得到同一份写入序列，便于比对与排障。
func TestNormalizePermissionIDs_去重并保持首次出现顺序(t *testing.T) {
	a := uuid.New().String()
	b := uuid.New().String()
	c := uuid.New().String()

	got, err := NormalizePermissionIDs([]string{a, b, a, c, b, a})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}

	want := []string{a, b, c}
	if !equalIDs(got, want) {
		t.Fatalf("结果 = %v，期望 %v（去重且保持首次出现顺序）", got, want)
	}
}

// TestNormalizePermissionIDs_去空白并跳过空串
//
// 空串等同于"这一项没传"，不是"传了一个非法标识"，因此跳过而不是报错：
// 上层不必为了少一个空串就先自己过滤一遍。
func TestNormalizePermissionIDs_去空白并跳过空串(t *testing.T) {
	a := uuid.New().String()
	b := uuid.New().String()

	got, err := NormalizePermissionIDs([]string{"", "  " + a + "  ", "", b, "   ", ""})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}

	want := []string{a, b}
	if !equalIDs(got, want) {
		t.Fatalf("结果 = %v，期望 %v", got, want)
	}
}

// TestNormalizePermissionIDs_空结果返回空切片而非nil
//
// 权限被清空是 ReplacePermissions 的合法结果，返回空切片让调用方
// 统一用 len() 判断即可，不必再判 nil。
func TestNormalizePermissionIDs_空结果返回空切片而非nil(t *testing.T) {
	cases := []struct {
		name string
		in   []string
	}{
		{"nil 入参", nil},
		{"空切片", []string{}},
		{"全是空串", []string{"", "  "}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizePermissionIDs(c.in)
			if err != nil {
				t.Fatalf("归一化失败：%v", err)
			}
			if got == nil {
				t.Fatal("结果应为空切片，而不是 nil")
			}
			if len(got) != 0 {
				t.Fatalf("结果 = %v，期望为空", got)
			}
		})
	}
}

// TestNormalizePermissionIDs_非法标识被拒绝
//
// 口径与 membership.New 拒绝 uuid.Nil() 一致：权限标识引用的是 permissions 表里
// 已存在的行，非法标识必须在进入写路径之前就失败。
// 静默丢弃会让一次调用悄悄少写一部分权限，而调用方从返回值上看不出来。
func TestNormalizePermissionIDs_非法标识被拒绝(t *testing.T) {
	valid := uuid.New().String()

	cases := []struct {
		name string
		in   []string
	}{
		{"非 UUID 字符串", []string{valid, "not-a-uuid"}},
		{"零值 UUID", []string{valid, uuid.Nil().String()}},
		{"残缺 UUID", []string{valid, "550e8400-e29b"}},
		{"多余尾巴", []string{valid, valid + "-extra"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := NormalizePermissionIDs(c.in)
			if !errors.Is(err, domain.ErrInvalidPermissionID) {
				t.Fatalf("错误 = %v，期望归入 domain.ErrInvalidPermissionID", err)
			}
			// 归类检查：上层只按基类判定也能正确归类。
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("错误 %v 未归类到 domain.ErrInvalidKey", err)
			}
			// 反向断言：防误归类到同层的其它基类上。
			if errors.Is(err, domain.ErrInvalidState) || errors.Is(err, domain.ErrConflict) {
				t.Fatalf("错误 %v 被误归类：入参不合法，既不是状态问题也不是写冲突", err)
			}
			if got != nil {
				t.Fatalf("出错时不应返回结果，实际 %v", got)
			}
		})
	}
}

// TestNormalizePermissionIDs_错误信息带上原始取值
//
// 排障时"哪一项坏了"比"某项坏了"有用得多，因此错误必须带上原始入参。
func TestNormalizePermissionIDs_错误信息带上原始取值(t *testing.T) {
	bad := "not-a-uuid"

	_, err := NormalizePermissionIDs([]string{bad})
	if err == nil {
		t.Fatal("期望报错，实际通过")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Fatalf("错误信息 %q 未包含原始取值 %q", err.Error(), bad)
	}
}

// TestNormalizePermissionIDs_不同书写形式归一到同一形态
//
// uuid.Parse 同时接受大写与花括号写法（RFC 4122），两种写法指向同一条权限。
// 归一到小写无括号的规范形态，去重才不会被绕过去：
// 否则同一个权限会以两种文本形态各留一份。
func TestNormalizePermissionIDs_不同书写形式归一到同一形态(t *testing.T) {
	valid := uuid.New().String()

	got, err := NormalizePermissionIDs([]string{
		strings.ToUpper(valid),
		"{" + valid + "}",
		valid,
	})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}

	want := []string{valid}
	if !equalIDs(got, want) {
		t.Fatalf("结果 = %v，期望 %v", got, want)
	}
}

// TestNormalizePermissionIDs_重复归一化结果不变
//
// ListPermissionIDs 读出来的东西要能原样写回去：两次归一化必须得到同一结果，
// 否则"读-改-写"会因为没有实际变化而白白触发一次写操作。
func TestNormalizePermissionIDs_重复归一化结果不变(t *testing.T) {
	a := uuid.New().String()
	b := uuid.New().String()

	first, err := NormalizePermissionIDs([]string{b, a, b})
	if err != nil {
		t.Fatalf("首次归一化失败：%v", err)
	}
	second, err := NormalizePermissionIDs(first)
	if err != nil {
		t.Fatalf("二次归一化失败：%v", err)
	}
	if !equalIDs(first, second) {
		t.Fatalf("二次归一化 = %v，与首次结果 %v 不一致", second, first)
	}
}
