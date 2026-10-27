package gorm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/permission"
)

// 与 user_repository_test.go 同一手法：不连数据库，
// 只跑"不触碰 *gorm.DB 就能判定"的分支，因此可以直接用零值 &PermissionRepository{}。

// TestNewPermissionRepository缺少数据库句柄返回错误
func TestNewPermissionRepository缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewPermissionRepository(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// TestFindByCode遇到空入参或非法代码不查库
//
// 句柄是 nil：只要真的发起查询就会 panic。
// 权限代码是业务 key 而不是 UUID，因此空值与格式错误都按"未命中"处理，
// 而不是抛回一类调用方错误——口径与 RoleRepository.FindByKey 一致。
func TestFindByCode遇到空入参或非法代码不查库(t *testing.T) {
	r := &PermissionRepository{}
	ctx := context.Background()

	cases := map[string]string{
		"空串":    "",
		"纯空白":   "   ",
		"缺冒号":   "tenantread",
		"缺操作":   "tenant:",
		"缺资源":   ":read",
		"以数字开头": "1tenant:read",
		"含大写以外": "tenant:read:extra",
	}

	for name, code := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := r.FindByCode(ctx, code)
			if got != nil || err != nil {
				t.Fatalf("code=%q 应返回 (nil, nil)，实际 (%v, %v)", code, got, err)
			}
		})
	}
}

// TestFindByCodes遇到空入参直接返回不查库
func TestFindByCodes遇到空入参直接返回不查库(t *testing.T) {
	got, err := (&PermissionRepository{}).FindByCodes(context.Background(), nil)
	if err != nil {
		t.Fatalf("空入参不应报错：%v", err)
	}
	if got == nil {
		t.Fatal("无匹配时应返回空切片，而不是 nil")
	}
	if len(got) != 0 {
		t.Fatalf("返回 %d 项，期望 0 项", len(got))
	}
}

// TestFindByCodes遇到非法代码在查库前被拒绝
//
// 批量查询的语义是"我就要这几项"，任一项写错都必须报出来：
// 静默丢弃会让调用方以为权限取齐了，实际少了几项，
// 而少掉的那项会顺着角色绑定一路静默下去。
func TestFindByCodes遇到非法代码在查库前被拒绝(t *testing.T) {
	got, err := (&PermissionRepository{}).FindByCodes(
		context.Background(), []string{"tenant:read", "NOT_A_CODE"})
	if !errors.Is(err, permission.ErrInvalidCode) {
		t.Fatalf("err = %v，期望归入 permission.ErrInvalidCode", err)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
	}
	if got != nil {
		t.Fatalf("出错时不应返回结果，实际 %v", got)
	}
}

// Test归一化权限代码去重且保持首次出现的顺序
func Test归一化权限代码去重且保持首次出现的顺序(t *testing.T) {
	got, err := normalizePermissionCodes([]string{
		"Tenant:Read", "member:invite", "tenant:read", "  tenant:read  ", "audit:read",
	})
	if err != nil {
		t.Fatalf("归一化失败：%v", err)
	}

	want := []string{"tenant:read", "member:invite", "audit:read"}
	if len(got) != len(want) {
		t.Fatalf("归一化结果 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

// Test官方权限目录可以直接喂给批量查询
//
// 目录是系统唯一的受控词表：它必须过得了批量查询入口的那道归一化，
// 否则角色预置（platformsetup）一次性带上全量目录时就会在写库前被拦下。
func Test官方权限目录可以直接喂给批量查询(t *testing.T) {
	entries := permission.All()
	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}

	got, err := normalizePermissionCodes(codes)
	if err != nil {
		t.Fatalf("官方目录归一化失败：%v", err)
	}
	if len(got) != len(codes) {
		t.Fatalf("归一化后 %d 项，期望 %d 项（目录内不应有重复代码）", len(got), len(codes))
	}
}

// Test按入参顺序还原查询结果
//
// 顺序一致是端口明写的契约：先查后写的调用方按位置对应语义，
// 顺序一乱就会把权限挂到别的语义上。
func Test按入参顺序还原查询结果(t *testing.T) {
	recs := []permissionRecord{
		{ID: "id-1", Code: "tenant:read", Resource: "tenant", Action: "read"},
		{ID: "id-2", Code: "member:invite", Resource: "member", Action: "invite"},
		{ID: "id-3", Code: "audit:read", Resource: "audit", Action: "read"},
	}
	indexed := indexPermissionRecords(recs)

	got, err := orderPermissionsByCodes(
		[]string{"audit:read", "tenant:read", "member:invite"}, indexed)
	if err != nil {
		t.Fatalf("还原失败：%v", err)
	}
	if len(got) != 3 {
		t.Fatalf("返回 %d 项，期望 3 项", len(got))
	}
	wantIDs := []string{"id-3", "id-1", "id-2"}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Fatalf("第 %d 项 ID = %q，期望 %q", i, got[i].ID, id)
		}
	}
}

// Test批量查询缺项时报ErrUnknownCode
//
// 缺项与"格式错"是两回事：能走到还原这一步的代码格式都已校验过，
// 缺的那一项是目录与数据库不同步（seed 没跑或跑漏了），
// 排障方向完全不同，因此不能笼统报 ErrInvalidCode。
func Test批量查询缺项时报ErrUnknownCode(t *testing.T) {
	indexed := indexPermissionRecords([]permissionRecord{
		{ID: "id-1", Code: "tenant:read", Resource: "tenant", Action: "read"},
	})

	got, err := orderPermissionsByCodes([]string{"tenant:read", "audit:read"}, indexed)
	if !errors.Is(err, permission.ErrUnknownCode) {
		t.Fatalf("err = %v，期望 permission.ErrUnknownCode", err)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
	}
	if got != nil {
		t.Fatalf("出错时不应返回结果，实际 %v", got)
	}
}

// Test权限行还原成领域实体
//
// permissions 表没有 updated_at（见 migrations/000004），
// 因此实体里也不该凭空多出一个更新时间——本用例锁住字段映射的完整集合。
func Test权限行还原成领域实体(t *testing.T) {
	createdAt := time.Now()
	rec := permissionRecord{
		ID:          "id-1",
		Code:        "member:invite",
		Resource:    "member",
		Action:      "invite",
		Description: "邀请成员加入租户",
		CreatedAt:   createdAt,
	}

	got := rec.toEntity()
	if got.ID != "id-1" || got.Code != "member:invite" {
		t.Fatalf("标识映射错误：%+v", got)
	}
	if got.Resource != "member" || got.Action != "invite" {
		t.Fatalf("资源/操作映射错误：%+v", got)
	}
	if got.Description != "邀请成员加入租户" {
		t.Fatalf("描述映射错误：%+v", got)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("创建时间映射错误：%+v", got)
	}
}

// Test空结果转成空切片而不是nil
func Test空结果转成空切片而不是nil(t *testing.T) {
	got := permissionEntities(nil)
	if got == nil {
		t.Fatal("空输入应返回空切片，而不是 nil")
	}
	if len(got) != 0 {
		t.Fatalf("返回 %d 项，期望 0 项", len(got))
	}
}
