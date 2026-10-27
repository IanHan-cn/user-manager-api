package gorm

import (
	"context"
	"errors"
	"testing"

	"github.com/memories-cn/user-management-api/internal/domain/permission"
)

// 与 user_repository_test.go 同一手法：不连数据库，
// 只跑"不触碰 *gorm.DB 就能判定"的分支。

// TestNewPermissionWriter缺少数据库句柄返回错误
func TestNewPermissionWriter缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewPermissionWriter(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// TestEnsureCatalog非法目录在写库前被拒绝
//
// 零值 &PermissionWriter{} 没有数据库句柄：
// 只要校验被挪到查库之后，用例就会 panic。
// 这条顺序是必须的——非法目录若进到 SQL 里，
// 抛回来的是 CHECK 约束的原生错误，上层看不懂。
func TestEnsureCatalog非法目录在写库前被拒绝(t *testing.T) {
	entries := []permission.Entry{{Resource: "tenant", Action: "read", Code: "member:read"}}

	got, err := (&PermissionWriter{}).EnsureCatalog(context.Background(), entries)
	if !errors.Is(err, permission.ErrInvalidCode) {
		t.Fatalf("错误 = %v，期望归入 permission.ErrInvalidCode", err)
	}
	if got != (permission.SeedResult{}) {
		t.Fatalf("出错时不该返回结果，实际 %+v", got)
	}
}

// TestEnsureCatalog空目录直接返回不查库
func TestEnsureCatalog空目录直接返回不查库(t *testing.T) {
	got, err := (&PermissionWriter{}).EnsureCatalog(context.Background(), nil)
	if err != nil {
		t.Fatalf("空目录不应报错：%v", err)
	}
	if got.Total() != 0 {
		t.Fatalf("结果 = %+v，期望全为零", got)
	}
	if got.Changed() {
		t.Fatal("没有写任何东西时 Changed 应为 false")
	}
}

// Test官方权限目录可以直接喂给写入端口
//
// 目录是系统唯一的受控词表，本用例把它与 ValidateCatalog 对齐：
// 目录本身必须过得了同步前的那道校验。
func Test官方权限目录可以直接喂给写入端口(t *testing.T) {
	if err := permission.ValidateCatalog(permission.All()); err != nil {
		t.Fatalf("目录校验失败：%v", err)
	}
}

// Test新建权限行由存储层生成主键与时间
//
// 目录项只描述"要有什么权限"，主键与落库时间属于存储层，
// 不该出现在 domain/permission.Entry 里，因此在这里生成。
func Test新建权限行由存储层生成主键与时间(t *testing.T) {
	e := permission.Entry{Resource: "tenant", Action: "read", Code: "tenant:read", Description: "查看租户信息"}

	rec := newPermissionRecord(e)
	if rec.ID == "" {
		t.Fatal("主键不应为空")
	}
	if rec.CreatedAt.IsZero() {
		t.Fatal("落库时间不应为零值")
	}
	if rec.Code != e.Code || rec.Resource != e.Resource || rec.Action != e.Action {
		t.Fatalf("行 = %+v，与目录项 %+v 不一致", rec, e)
	}
}

// diffCatalogTestEntries 是差分测试用的三个目录项。
func diffCatalogTestEntries() []permission.Entry {
	return []permission.Entry{
		{Resource: "tenant", Action: "read", Code: "tenant:read", Description: "查看租户信息"},
		{Resource: "tenant", Action: "update", Code: "tenant:update", Description: "修改租户信息"},
		{Resource: "member", Action: "invite", Code: "member:invite", Description: "邀请成员加入租户"},
	}
}

// Test目录同步_全新目录全部计为新增
//
// 三项计数互斥且必须覆盖全部目录项：
// 漏计一项，"这次到底改了什么"的汇报就会失真，
// 而 cmd/seed 正是靠它判断是否真的写入了数据。
func Test目录同步_全新目录全部计为新增(t *testing.T) {
	entries := diffCatalogTestEntries()

	result, toWrite := diffCatalog(entries, map[string]string{})

	if result.Inserted != len(entries) {
		t.Fatalf("新增 = %d，期望 %d", result.Inserted, len(entries))
	}
	if result.Updated != 0 || result.Unchanged != 0 {
		t.Fatalf("统计 = %+v，期望更新与未变均为零", result)
	}
	if result.Total() != len(entries) {
		t.Fatalf("总数 = %d，期望等于目录项数 %d", result.Total(), len(entries))
	}
	if len(toWrite) != len(entries) {
		t.Fatalf("待写 = %d 项，期望 %d 项", len(toWrite), len(entries))
	}
}

// Test目录同步_文案变化计为更新
func Test目录同步_文案变化计为更新(t *testing.T) {
	entries := diffCatalogTestEntries()

	current := map[string]string{
		"tenant:read":   "查看租户信息",
		"tenant:update": "修改租户（文案已过期）",
		"member:invite": "邀请成员加入租户",
	}

	result, toWrite := diffCatalog(entries, current)

	if result.Updated != 1 {
		t.Fatalf("更新 = %d，期望 1", result.Updated)
	}
	if result.Inserted != 0 || result.Unchanged != 2 {
		t.Fatalf("统计 = %+v，期望新增为零、未变两项", result)
	}
	if result.Total() != len(entries) {
		t.Fatalf("总数 = %d，期望等于目录项数 %d", result.Total(), len(entries))
	}
	if len(toWrite) != 1 || toWrite[0].Code != "tenant:update" {
		t.Fatalf("待写 = %+v，期望只有 tenant:update", toWrite)
	}
}

// Test目录同步_完全一致时全部计为未变且不产生写入
//
// "什么都没做"必须能被认出来：Seeder 每次部署都会跑，
// 把它跑成一个可以判定为 no-op 的操作，重复执行才是安全的。
func Test目录同步_完全一致时全部计为未变且不产生写入(t *testing.T) {
	entries := diffCatalogTestEntries()

	current := map[string]string{}
	for _, e := range entries {
		current[e.Code] = e.Description
	}

	result, toWrite := diffCatalog(entries, current)

	if result.Unchanged != len(entries) {
		t.Fatalf("未变 = %d，期望 %d", result.Unchanged, len(entries))
	}
	if result.Inserted != 0 || result.Updated != 0 {
		t.Fatalf("统计 = %+v，期望新增与更新均为零", result)
	}
	if result.Changed() {
		t.Fatal("没有任何改动时 Changed 应为 false")
	}
	if len(toWrite) != 0 {
		t.Fatalf("待写 = %+v，期望为空", toWrite)
	}
}

// Test目录同步_二次执行不再产生新增与更新
//
// 端口契约的硬要求："重复调用既不报错也不产生重复数据，
// 且第二次调用必须得到 Inserted=0、Updated=0 的结果"。
// 这里用"先按首次结果构造现状、再跑一次"来模拟第二次执行。
func Test目录同步_二次执行不再产生新增与更新(t *testing.T) {
	entries := diffCatalogTestEntries()

	first, _ := diffCatalog(entries, map[string]string{})
	if first.Inserted != len(entries) {
		t.Fatalf("首次新增 = %d，期望 %d", first.Inserted, len(entries))
	}

	// 首次执行之后，库里就有了这三条记录，文案与目录一致。
	current := map[string]string{}
	for _, e := range entries {
		current[e.Code] = e.Description
	}

	second, toWrite := diffCatalog(entries, current)
	if second.Inserted != 0 || second.Updated != 0 {
		t.Fatalf("二次统计 = %+v，期望新增与更新均为零", second)
	}
	if second.Unchanged != len(entries) {
		t.Fatalf("二次未变 = %d，期望 %d", second.Unchanged, len(entries))
	}
	if len(toWrite) != 0 {
		t.Fatalf("二次待写 = %+v，期望为空", toWrite)
	}
}

// Test目录同步_库中多余的权限不计入任何一项
//
// Seeder 只做写入不做清理：库里存在但不在目录里的权限可能正被既有角色引用着，
// 删除会牵连历史数据。三项计数只覆盖本次目录项，
// 因此库中的多余项不应出现在统计里。
func Test目录同步_库中多余的权限不计入任何一项(t *testing.T) {
	entries := diffCatalogTestEntries()

	current := map[string]string{
		"tenant:read":     "查看租户信息",
		"legacy:removed":  "早已下线的权限",
		"member:invite":   "邀请成员加入租户",
		"tenant:update":   "修改租户信息",
	}

	result, toWrite := diffCatalog(entries, current)

	if result.Total() != len(entries) {
		t.Fatalf("总数 = %d，期望等于目录项数 %d", result.Total(), len(entries))
	}
	if result.Unchanged != len(entries) {
		t.Fatalf("未变 = %d，期望 %d", result.Unchanged, len(entries))
	}
	if len(toWrite) != 0 {
		t.Fatalf("待写 = %+v，期望为空", toWrite)
	}
}
