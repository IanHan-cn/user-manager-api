package permission

import (
	"errors"
	"strings"
	"testing"
)

// TestCatalog自身校验通过
//
// 这条用例是目录与数据库 CHECK 约束之间的一致性闸门：
// permissions 表要求 code = resource || ':' || action，
// 目录一旦不满足，同步时会被约束拒绝而报错地方却在 SQL 里。
func TestCatalog自身校验通过(t *testing.T) {
	if err := ValidateCatalog(catalog); err != nil {
		t.Fatalf("目录自身校验失败：%v", err)
	}
	if len(catalog) == 0 {
		t.Fatal("系统权限目录不应为空")
	}
}

// TestCatalog每项格式都能通过ParseCode
func TestCatalog每项格式都能通过ParseCode(t *testing.T) {
	for _, e := range catalog {
		resource, action, err := ParseCode(e.Code)
		if err != nil {
			t.Fatalf("目录项 %q 的代码解析失败：%v", e.Code, err)
		}
		if resource != e.Resource || action != e.Action {
			t.Fatalf("目录项 %q 的 resource/action 与代码不一致：%s/%s",
				e.Code, e.Resource, e.Action)
		}
		if e.Description == "" {
			t.Fatalf("目录项 %q 缺少中文说明，无法用于展示", e.Code)
		}
	}
}

// TestValidateCatalog拒绝重复项
func TestValidateCatalog拒绝重复项(t *testing.T) {
	entries := []Entry{
		newEntry("tenant", "read", "查看租户信息"),
		newEntry("tenant", "read", "另一个文案"),
	}

	err := ValidateCatalog(entries)
	if !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("错误 = %v，期望归入 ErrInvalidCode", err)
	}
}

// TestValidateCatalog拒绝不自洽的目录项
//
// Entry 是导出结构体，调用方可以手工构造，
// 因此构造函数保证的 code/resource/action 一致性仍然要在写库前校验一次。
func TestValidateCatalog拒绝不自洽的目录项(t *testing.T) {
	cases := map[string]Entry{
		"代码与 resource/action 不符": {Resource: "tenant", Action: "read", Code: "member:read"},
		"代码格式非法":                 {Resource: "tenant", Action: "read", Code: "TENANT-READ"},
		"缺少 action":              {Resource: "tenant", Action: "", Code: "tenant:"},
	}

	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCatalog([]Entry{e}); !errors.Is(err, ErrInvalidCode) {
				t.Fatalf("错误 = %v，期望归入 ErrInvalidCode", err)
			}
		})
	}
}

// TestValidateCatalog空目录合法
//
// 空集合不是错误：调用方可能只是想先校验一批，
// 真正要不要写东西由上层决定。
func TestValidateCatalog空目录合法(t *testing.T) {
	if err := ValidateCatalog(nil); err != nil {
		t.Fatalf("空目录不应报错：%v", err)
	}
}

// TestValidateCatalog拒绝超长的资源或操作
//
// permissions 表给 resource 与 action 各留了 VARCHAR(64)，
// 而 code 自身的上限是 VARCHAR(128)——只校验 code 总长会放过
// "resource 70 字符 + action 短"这种 code 总长合格、单列却超限的取值，
// 写库时才会被列长度拒绝（而那正是 Writer 端口契约禁止的原生错误）。
func TestValidateCatalog拒绝超长的资源或操作(t *testing.T) {
	long := strings.Repeat("a", maxResourceLen+1)

	cases := map[string]Entry{
		"资源超长": {Resource: long, Action: "read", Code: BuildCode(long, "read")},
		"操作超长": {Resource: "tenant", Action: long, Code: BuildCode("tenant", long)},
	}

	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCatalog([]Entry{e}); !errors.Is(err, ErrInvalidCode) {
				t.Fatalf("错误 = %v，期望归入 ErrInvalidCode", err)
			}
		})
	}
}

// TestParseCode拒绝单列超长的代码
//
// code 总长合法不代表两列都合法：
// ParseCode 是格式校验的第一道闸门，必须与数据库的单列长度对齐。
func TestParseCode拒绝单列超长的代码(t *testing.T) {
	long := strings.Repeat("a", maxResourceLen+1)

	if _, _, err := ParseCode(BuildCode(long, "read")); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("错误 = %v，期望归入 ErrInvalidCode", err)
	}
	if _, _, err := ParseCode(BuildCode("tenant", long)); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("错误 = %v，期望归入 ErrInvalidCode", err)
	}
}

// TestValidateResource与ValidateAction
//
// 单列校验是目录校验与写库前校验共用的同一把尺子，
// 保证"目录里的项"与"能写进库的项"永远是同一个范围。
func TestValidateResource与ValidateAction(t *testing.T) {
	if err := ValidateResource("tenant"); err != nil {
		t.Fatalf("合法资源标识被拒绝：%v", err)
	}
	if err := ValidateAction("read"); err != nil {
		t.Fatalf("合法操作标识被拒绝：%v", err)
	}

	cases := map[string]error{
		"空资源":   ValidateResource(""),
		"空操作":   ValidateAction(""),
		"含连字符":   ValidateResource("tenant-admin"),
		"大写开头":   ValidateResource("Tenant"),
		"超长资源":   ValidateResource(strings.Repeat("a", maxResourceLen+1)),
		"超长操作":   ValidateAction(strings.Repeat("a", maxActionLen+1)),
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, ErrInvalidCode) {
				t.Fatalf("错误 = %v，期望归入 ErrInvalidCode", err)
			}
		})
	}
}

// TestLookup命中与未命中
func TestLookup命中与未命中(t *testing.T) {
	first := catalog[0]

	got, ok := Lookup(first.Code)
	if !ok {
		t.Fatalf("Lookup(%q) 未命中，期望命中", first.Code)
	}
	if got.Code != first.Code || got.Description != first.Description {
		t.Fatalf("返回 %+v，期望 %+v", got, first)
	}

	if _, ok := Lookup(":nonexistent:op"); ok {
		t.Fatal("不在目录里的代码不应命中")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("空代码不应命中")
	}
}

// TestLookup大小写与空白被忽略
//
// 权限代码可能来自配置或 HTTP 参数，
// 查不到正确的一项会让守卫莫名其妙地拒绝合法请求。
func TestLookup大小写与空白被忽略(t *testing.T) {
	first := catalog[0]

	if _, ok := Lookup("  " + first.Code + "  "); !ok {
		t.Fatalf("带空白的代码 %q 应当命中", first.Code)
	}
	if _, ok := Lookup(upperSafe(first.Code)); !ok {
		t.Fatalf("大写代码应当命中：%q", first.Code)
	}
}

// TestAll返回副本而不是原切片
//
// catalog 是包级变量，一旦调用方能直接改到它，
// 后续所有人看到的目录就都被改了。
func TestAll返回副本而不是原切片(t *testing.T) {
	first := All()[0]

	got := All()
	got[0].Description = "被改写过的说明"

	if All()[0].Description != first.Description {
		t.Fatalf("返回值被修改影响到了目录本身：%q，期望 %q",
			All()[0].Description, first.Description)
	}
	if len(All()) != len(catalog) {
		t.Fatalf("All() 返回 %d 项，期望 %d 项", len(All()), len(catalog))
	}
}

// upperSafe 把 ASCII 字母转大写，用于构造大小写变体。
// 目录代码只含小写字母、数字与冒号下划线，无需考虑 Unicode。
func upperSafe(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'a' && out[i] <= 'z' {
			out[i] -= 'a' - 'A'
		}
	}
	return string(out)
}
