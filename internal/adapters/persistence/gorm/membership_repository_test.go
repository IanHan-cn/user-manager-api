package gorm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membership"
	"gorm.io/gorm"
)

// 与 user_repository_test.go 同一手法：不连数据库，
// 只跑"不触碰 *gorm.DB 就能判定"的分支，因此可以直接用零值 &MembershipRepository{}。
// 一旦哪天这些分支改成先查库，用例会以 panic 的形式立刻失败，
// 而不是悄悄退化成依赖环境的集成测试。

// newTestMembership 造一条领域层认可的成员关系，供错误翻译用例取用标识。
func newTestMembership(t *testing.T) *membership.Membership {
	t.Helper()
	m, err := membership.New(uuid.New(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	return m
}

// TestNewMembershipRepository缺少数据库句柄返回错误
func TestNewMembershipRepository缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewMembershipRepository(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// Test写操作遇到空实体返回ErrNilMembership
//
// 空实体是调用方 bug：必须在触碰数据库之前就被拦下，
// 且不得混入业务分类，否则上层会把它当成一次正常的 4xx。
func Test写操作遇到空实体返回ErrNilMembership(t *testing.T) {
	r := &MembershipRepository{}
	ctx := context.Background()

	cases := map[string]func() error{
		"Create": func() error { return r.Create(ctx, nil) },
		"Update": func() error { return r.Update(ctx, nil) },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, membership.ErrNilMembership) {
				t.Fatalf("err = %v，期望 ErrNilMembership", err)
			}
			if !errors.Is(err, domain.ErrNilEntity) {
				t.Fatalf("err = %v，应归类到 domain.ErrNilEntity", err)
			}
			for _, base := range []error{
				domain.ErrInvalidKey, domain.ErrConflict,
				domain.ErrNotFound, domain.ErrInvalidState,
			} {
				if errors.Is(err, base) {
					t.Fatalf("err = %v，不应归类到 %v", err, base)
				}
			}
		})
	}
}

// Test成员关系Update遇到空标识返回记录不存在
//
// id 与 tenant_id 都是 UUID 列：空串绑上去换来的只是一次类型错误，
// 而按端口口径这就是"要改的行不存在"。放任它进 SQL，
// 一次本该精确可判的 ErrMembershipNotFound 会变成不可归类的原生错误。
// 句柄为 nil：这段判定必须发生在任何数据库访问之前。
func Test成员关系Update遇到空标识返回记录不存在(t *testing.T) {
	ctx := context.Background()

	cases := map[string]func(m *membership.Membership){
		"主键为空":   func(m *membership.Membership) { m.ID = "" },
		"租户标识为空": func(m *membership.Membership) { m.TenantID = "  " },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := newTestMembership(t)
			mutate(m)

			err := (&MembershipRepository{}).Update(ctx, m)
			if !errors.Is(err, membership.ErrMembershipNotFound) {
				t.Fatalf("err = %v，期望 ErrMembershipNotFound", err)
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
			}
		})
	}
}

// Test查询方法遇到零值标识不查库
//
// 句柄是 nil：只要真的发起查询就会 panic。
// uuid.Nil 是"没有依据可查"，不是一条待查的数据——
// 把它打进 UUID 列只会换来一次不知所云的类型转换错误。
func Test查询方法遇到零值标识不查库(t *testing.T) {
	r := &MembershipRepository{}
	ctx := context.Background()

	t.Run("单条查询返回 (nil, nil)", func(t *testing.T) {
		cases := map[string]func() (*membership.Membership, error){
			"GetByID": func() (*membership.Membership, error) {
				return r.GetByID(ctx, uuid.Nil())
			},
			"GetByTenantAndUser 租户为零值": func() (*membership.Membership, error) {
				return r.GetByTenantAndUser(ctx, uuid.Nil(), uuid.New())
			},
			"GetByTenantAndUser 用户为零值": func() (*membership.Membership, error) {
				return r.GetByTenantAndUser(ctx, uuid.New(), uuid.Nil())
			},
		}
		for name, call := range cases {
			t.Run(name, func(t *testing.T) {
				got, err := call()
				if got != nil || err != nil {
					t.Fatalf("应返回 (nil, nil)，实际 (%v, %v)", got, err)
				}
			})
		}
	})

	t.Run("列表查询返回空切片", func(t *testing.T) {
		byTenant, err := r.GetByTenantID(ctx, uuid.Nil())
		if err != nil {
			t.Fatalf("GetByTenantID 不应报错：%v", err)
		}
		if byTenant == nil {
			t.Fatal("GetByTenantID 无匹配时应返回空切片，而不是 nil")
		}

		byUser, err := r.GetByUserID(ctx, uuid.Nil())
		if err != nil {
			t.Fatalf("GetByUserID 不应报错：%v", err)
		}
		if byUser == nil {
			t.Fatal("GetByUserID 无匹配时应返回空切片，而不是 nil")
		}
	})
}

// Test零值时间与NULL互转
//
// 领域层用 time.Time 零值表达"尚未发生"，库里用 NULL
// （见 migrations/000007 的列注释）。互转写反的后果不是显示偏差，
// 而是把 0001-01-01 写进列里、撞上 chk_memberships_status_timestamps。
func Test零值时间与NULL互转(t *testing.T) {
	if got := toNullableTime(time.Time{}); got != nil {
		t.Fatalf("零值时间应转成 NULL，实际 %v", got)
	}

	now := time.Now()
	got := toNullableTime(now)
	if got == nil {
		t.Fatal("非零时间不应转成 NULL")
	}
	if !got.Equal(now) {
		t.Fatalf("时间被改写：%v，期望 %v", got, now)
	}

	if !fromNullableTime(nil).IsZero() {
		t.Fatal("NULL 应还原成零值时间")
	}
	if !fromNullableTime(got).Equal(now) {
		t.Fatal("非空指针应还原成原时间")
	}
}

// Test成员关系往返转换保留业务时间戳
//
// 状态转换方法改写的正是这三个时间戳：左开右合的转换一旦漏掉某一列，
// Reinvite 之后库里就会留着上一周期的 joined_at / left_at。
func Test成员关系往返转换保留业务时间戳(t *testing.T) {
	m := newTestMembership(t)
	if err := m.Activate(time.Now()); err != nil {
		t.Fatalf("激活失败：%v", err)
	}
	suspendedAt := time.Now()
	if err := m.Suspend(suspendedAt); err != nil {
		t.Fatalf("暂停失败：%v", err)
	}

	got := toMembershipRecord(m).toEntity()
	if got.Status != membership.StatusSuspended {
		t.Fatalf("状态 = %q，期望 suspended", got.Status)
	}
	if got.JoinedAt.IsZero() {
		t.Fatal("JoinedAt 不应丢失")
	}
	if !got.SuspendedAt.Equal(suspendedAt) {
		t.Fatalf("SuspendedAt = %v，期望 %v", got.SuspendedAt, suspendedAt)
	}
	if !got.LeftAt.IsZero() {
		t.Fatalf("LeftAt 应为零值，实际 %v", got.LeftAt)
	}
	if got.TenantID != m.TenantID || got.UserID != m.UserID {
		t.Fatalf("业务键丢失：%+v", got)
	}
}

// Test唯一约束冲突归入ErrMembershipExists
//
// 端口要求"撞上 (tenant_id, user_id) 唯一约束必须返回 ErrMembershipExists，
// 不得静默吞掉后返回 nil"：邀请流程正是靠它区分"已在租户里"与"邀请已送达"。
func Test唯一约束冲突归入ErrMembershipExists(t *testing.T) {
	m := newTestMembership(t)
	err := fmt.Errorf("duplicate key value violates unique constraint %q: %w",
		"idx_memberships_tenant_user", gorm.ErrDuplicatedKey)

	got := translateMembershipWriteError(err, m)
	if !errors.Is(got, membership.ErrMembershipExists) {
		t.Fatalf("err = %v，期望 ErrMembershipExists", got)
	}
	if !errors.Is(got, domain.ErrConflict) {
		t.Fatalf("err = %v，应归类到 domain.ErrConflict", got)
	}
	if errors.Is(got, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，不应归类到 domain.ErrInvalidKey", got)
	}
	// 底层错误必须保留：真正被违反的约束名只有驱动知道。
	if !errors.Is(got, gorm.ErrDuplicatedKey) {
		t.Fatalf("err = %v，应保留底层错误以便定位约束名", got)
	}
}

// Test状态与时间戳不自洽归入ErrInvalidMembershipRecord
//
// 撞上 chk_memberships_status_timestamps 说明状态与业务时间戳已经漂移
// （通常是有人绕过了四个状态转换方法直接改字段）。
// 它必须变成可归类的领域错误，否则只能以驱动原生错误的形式冒到上层。
func Test状态与时间戳不自洽归入ErrInvalidMembershipRecord(t *testing.T) {
	m := newTestMembership(t)
	err := fmt.Errorf("new row violates check constraint %q: %w",
		"chk_memberships_status_timestamps", gorm.ErrCheckConstraintViolated)

	got := translateMembershipWriteError(err, m)
	if !errors.Is(got, membership.ErrInvalidMembershipRecord) {
		t.Fatalf("err = %v，期望 ErrInvalidMembershipRecord", got)
	}
	if !errors.Is(got, domain.ErrInvalidState) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidState", got)
	}
	if errors.Is(got, domain.ErrConflict) || errors.Is(got, domain.ErrNotFound) {
		t.Fatalf("err = %v，不应归类到写操作结局类", got)
	}
}

// Test成员关系遇到未识别的数据库错误原样上抛
//
// 翻译只覆盖已知的两类：连接失败、超时等必须原样冒泡，
// 不得被误判成"已是成员"或"记录不合法"。
func Test成员关系遇到未识别的数据库错误原样上抛(t *testing.T) {
	boom := errors.New("connection reset")
	got := translateMembershipWriteError(boom, newTestMembership(t))
	if !errors.Is(got, boom) {
		t.Fatalf("err = %v，期望原样返回底层错误", got)
	}
}

// Test成员关系Update结果判定的三条分支
func Test成员关系Update结果判定的三条分支(t *testing.T) {
	m := newTestMembership(t)

	if err := translateMembershipUpdateResult(&gorm.DB{RowsAffected: 1}, m); err != nil {
		t.Fatalf("改到一行时应返回 nil，实际 %v", err)
	}

	// 没改到任何行即目标不存在：静默成功会让上层误以为更新生效。
	err := translateMembershipUpdateResult(&gorm.DB{RowsAffected: 0}, m)
	if !errors.Is(err, membership.ErrMembershipNotFound) {
		t.Fatalf("err = %v，期望 ErrMembershipNotFound", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
	}

	// 出错时走同一套翻译，不得绕过。
	err = translateMembershipUpdateResult(&gorm.DB{Error: gorm.ErrDuplicatedKey}, m)
	if !errors.Is(err, membership.ErrMembershipExists) {
		t.Fatalf("err = %v，期望 ErrMembershipExists", err)
	}
}

// Test可写列白名单排除业务键与时间戳
//
// 一条成员关系只刻画"某用户在某租户中的当前成员资格"，
// 换了 tenant_id 或 user_id 就已经是另一条关系，不是"改了一下"；
// created_at 不可变、updated_at 归触发器维护。
func Test可写列白名单排除业务键与时间戳(t *testing.T) {
	forbidden := []string{"id", "tenant_id", "user_id", "created_at", "updated_at"}
	for _, col := range forbidden {
		for _, allowed := range membershipUpdatableColumns {
			if col == allowed {
				t.Fatalf("可写列白名单不应包含 %q", col)
			}
		}
	}

	want := []string{"status", "invited_by", "invited_at", "joined_at", "suspended_at", "left_at"}
	if len(membershipUpdatableColumns) != len(want) {
		t.Fatalf("白名单 = %v，期望 %v", membershipUpdatableColumns, want)
	}
	for i := range want {
		if membershipUpdatableColumns[i] != want[i] {
			t.Fatalf("白名单第 %d 项 = %q，期望 %q", i, membershipUpdatableColumns[i], want[i])
		}
	}
}

// Test更新列值对的键与白名单一致
//
// 两处一旦漂移，GORM 的 Select 会静默忽略白名单之外的键，
// 于是"改了却没生效"，而且没有任何报错。
func Test更新列值对的键与白名单一致(t *testing.T) {
	m := newTestMembership(t)
	values := toMembershipRecord(m).toUpdateMap()

	if len(values) != len(membershipUpdatableColumns) {
		t.Fatalf("列值对有 %d 项，白名单有 %d 项", len(values), len(membershipUpdatableColumns))
	}
	for _, col := range membershipUpdatableColumns {
		if _, ok := values[col]; !ok {
			t.Fatalf("列值对缺少白名单中的列 %q", col)
		}
	}
}

// Test清空暂停时间会被写成NULL
//
// 恢复成员身份（Activate）会把 SuspendedAt 清零，
// 若按结构体更新，零值会被 GORM 当成"未设置"跳过，该列就留在库里不清。
// 本用例锁住"用 map 更新"这一手法仍然成立。
func Test清空暂停时间会被写成NULL(t *testing.T) {
	m := newTestMembership(t)
	if err := m.Activate(time.Now()); err != nil {
		t.Fatalf("激活失败：%v", err)
	}
	if err := m.Suspend(time.Now()); err != nil {
		t.Fatalf("暂停失败：%v", err)
	}
	if err := m.Activate(time.Now()); err != nil {
		t.Fatalf("恢复失败：%v", err)
	}

	rec := toMembershipRecord(m)
	if rec.SuspendedAt != nil {
		t.Fatalf("恢复后 SuspendedAt 应为 NULL，实际 %v", *rec.SuspendedAt)
	}
	if got, ok := rec.toUpdateMap()["suspended_at"].(*time.Time); !ok || got != nil {
		t.Fatalf("更新值应为 NULL 指针，实际 %v", rec.toUpdateMap()["suspended_at"])
	}
}
