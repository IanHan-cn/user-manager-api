package gorm

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membershiprole"
	"gorm.io/gorm"
)

// 与 membership_repository_test.go 同一手法：不连数据库，
// 只跑"不触碰 *gorm.DB 就能判定"的分支，因此可以直接用零值 &MembershipRoleRepository{}。
// 一旦哪天这些分支改成先查库，用例会以 panic 的形式立刻失败，
// 而不是悄悄退化成依赖环境的集成测试。

// newTestMembershipRole 造一条领域层认可的角色授权，供错误翻译用例取用标识。
func newTestMembershipRole(t *testing.T) *membershiprole.MembershipRole {
	t.Helper()
	r, err := membershiprole.New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "分配理由")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	return r
}

// TestNewMembershipRoleRepository缺少数据库句柄返回错误
func TestNewMembershipRoleRepository缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewMembershipRoleRepository(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// Test成员角色写操作遇到空实体返回ErrNilMembershipRole
//
// 空实体是调用方 bug：必须在触碰数据库之前就被拦下，
// 且不得混入业务分类，否则上层会把它当成一次正常的 4xx。
func Test成员角色写操作遇到空实体返回ErrNilMembershipRole(t *testing.T) {
	r := &MembershipRoleRepository{}
	ctx := context.Background()

	cases := map[string]func() error{
		"Create": func() error { return r.Create(ctx, nil) },
		"Update": func() error { return r.Update(ctx, nil) },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, membershiprole.ErrNilMembershipRole) {
				t.Fatalf("err = %v，期望 ErrNilMembershipRole", err)
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

// Test成员角色Update遇到空标识返回记录不存在
//
// id 与 tenant_id 都是 UUID 列：空串绑上去换来的只是一次类型错误，
// 而按端口口径这就是"要改的行不存在"。放任它进 SQL，
// 一次本该精确可判的 ErrMembershipRoleNotFound 会变成不可归类的原生错误。
// 句柄为 nil：这段判定必须发生在任何数据库访问之前。
func Test成员角色Update遇到空标识返回记录不存在(t *testing.T) {
	ctx := context.Background()

	cases := map[string]func(r *membershiprole.MembershipRole){
		"主键为空":   func(r *membershiprole.MembershipRole) { r.ID = "" },
		"租户标识为空": func(r *membershiprole.MembershipRole) { r.TenantID = "  " },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := newTestMembershipRole(t)
			mutate(m)

			err := (&MembershipRoleRepository{}).Update(ctx, m)
			if !errors.Is(err, membershiprole.ErrMembershipRoleNotFound) {
				t.Fatalf("err = %v，期望 ErrMembershipRoleNotFound", err)
			}
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
			}
		})
	}
}

// Test成员角色查询方法遇到零值标识不查库
//
// 句柄是 nil：只要真的发起查询就会 panic。
// uuid.Nil 是"没有依据可查"，不是一条待查的数据——
// 把它打进 UUID 列只会换来一次不知所云的类型转换错误。
func Test成员角色查询方法遇到零值标识不查库(t *testing.T) {
	r := &MembershipRoleRepository{}
	ctx := context.Background()

	t.Run("单条查询返回 (nil, nil)", func(t *testing.T) {
		cases := map[string]func() (*membershiprole.MembershipRole, error){
			"GetByID 主键为零值": func() (*membershiprole.MembershipRole, error) {
				return r.GetByID(ctx, uuid.New(), uuid.Nil())
			},
			"GetByID 租户为零值": func() (*membershiprole.MembershipRole, error) {
				return r.GetByID(ctx, uuid.Nil(), uuid.New())
			},
			"GetByMembershipAndRole 租户为零值": func() (*membershiprole.MembershipRole, error) {
				return r.GetByMembershipAndRole(ctx, uuid.Nil(), uuid.New(), uuid.New(), time.Now())
			},
			"GetByMembershipAndRole 成员关系为零值": func() (*membershiprole.MembershipRole, error) {
				return r.GetByMembershipAndRole(ctx, uuid.New(), uuid.Nil(), uuid.New(), time.Now())
			},
			"GetByMembershipAndRole 角色为零值": func() (*membershiprole.MembershipRole, error) {
				return r.GetByMembershipAndRole(ctx, uuid.New(), uuid.New(), uuid.Nil(), time.Now())
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
		cases := map[string]func() ([]*membershiprole.MembershipRole, error){
			"ListByMembershipID 租户为零值": func() ([]*membershiprole.MembershipRole, error) {
				return r.ListByMembershipID(ctx, uuid.Nil(), uuid.New())
			},
			"ListByMembershipID 成员关系为零值": func() ([]*membershiprole.MembershipRole, error) {
				return r.ListByMembershipID(ctx, uuid.New(), uuid.Nil())
			},
			"ListActiveByMembershipID 租户为零值": func() ([]*membershiprole.MembershipRole, error) {
				return r.ListActiveByMembershipID(ctx, uuid.Nil(), uuid.New(), time.Now())
			},
			"ListByRoleID 角色为零值": func() ([]*membershiprole.MembershipRole, error) {
				return r.ListByRoleID(ctx, uuid.New(), uuid.Nil())
			},
			"ListDueForExpiry 租户为零值": func() ([]*membershiprole.MembershipRole, error) {
				return r.ListDueForExpiry(ctx, uuid.Nil(), time.Now(), 100)
			},
		}
		for name, call := range cases {
			t.Run(name, func(t *testing.T) {
				got, err := call()
				if err != nil {
					t.Fatalf("%s 不应报错：%v", name, err)
				}
				if got == nil {
					t.Fatalf("%s 无匹配时应返回空切片，而不是 nil", name)
				}
			})
		}
	})
}

// Test空串与NULL互转
//
// 领域层用空串表达"没填原因"，库里用 NULL
// （见 migrations/000009 的 reason 列注释）。互转写反的后果不是显示偏差，
// 而是把空串写进列里、撞上 chk_membership_roles_reason。
func Test空串与NULL互转(t *testing.T) {
	if got := toNullableString(""); got != nil {
		t.Fatalf("空串应转成 NULL，实际 %v", got)
	}

	got := toNullableString("离职")
	if got == nil || *got != "离职" {
		t.Fatalf("非空串不应转成 NULL，实际 %v", got)
	}

	if fromNullableString(nil) != "" {
		t.Fatal("NULL 应还原成空串")
	}
	if fromNullableString(got) != "离职" {
		t.Fatal("非空指针应还原成原串")
	}
}

// Test成员角色往返转换保留有效期与原因
//
// 状态转换改写的正是这两列：漏掉任何一列，
// 撤销之后库里就会留着上一次的到期时间与分配理由。
func Test成员角色往返转换保留有效期与原因(t *testing.T) {
	r := newTestMembershipRole(t)
	expiresAt := time.Now().Add(24 * time.Hour)
	r.ExpiresAt = expiresAt
	if err := r.Revoke("离职"); err != nil {
		t.Fatalf("撤销失败：%v", err)
	}

	got, err := toMembershipRoleRecord(r).toEntity()
	if err != nil {
		t.Fatalf("还原实体失败：%v", err)
	}
	if got.Status != membershiprole.StatusRevoked {
		t.Fatalf("状态 = %q，期望 revoked", got.Status)
	}
	if got.Reason != "离职" {
		t.Fatalf("Reason = %q，期望被撤销理由覆写", got.Reason)
	}
	if !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v，期望 %v", got.ExpiresAt, expiresAt)
	}
	if got.TenantID != r.TenantID || got.MembershipID != r.MembershipID || got.RoleID != r.RoleID {
		t.Fatalf("业务键丢失：%+v", got)
	}
	if got.AssignedBy != r.AssignedBy || !got.AssignedAt.Equal(r.AssignedAt) {
		t.Fatalf("授权动作不可被改写：%+v", got)
	}
}

// Test成员角色长期有效与未填原因落库为NULL
func Test成员角色长期有效与未填原因落库为NULL(t *testing.T) {
	r := newTestMembershipRole(t)
	r.ExpiresAt = time.Time{}
	r.Reason = ""

	rec := toMembershipRoleRecord(r)
	if rec.ExpiresAt != nil {
		t.Fatalf("ExpiresAt 应为 NULL，实际 %v", rec.ExpiresAt)
	}
	if rec.Reason != nil {
		t.Fatalf("Reason 应为 NULL，实际 %v", rec.Reason)
	}
}

// Test部分唯一索引冲突归入ErrMembershipRoleExists
//
// 端口要求"撞上 idx_membership_roles_membership_role_active 必须返回
// ErrMembershipRoleExists，不得静默吞掉后返回 nil"：
// 授权流程正是靠它区分"已经持有该角色"与"授权已生效"。
//
// 错误按生产环境的样子构造：GORM 的翻译把驱动错误包在错误链的第二个位置上，
// 约束名只能靠 errors.As 取（见 classifyWriteError）。
func Test部分唯一索引冲突归入ErrMembershipRoleExists(t *testing.T) {
	r := newTestMembershipRole(t)
	err := pgConstraintError("23505", "idx_membership_roles_membership_role_active", gorm.ErrDuplicatedKey)

	got := translateMembershipRoleWriteError(err, r)
	if !errors.Is(got, membershiprole.ErrMembershipRoleExists) {
		t.Fatalf("err = %v，期望 ErrMembershipRoleExists", got)
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

// Test成员角色取值不自洽归入ErrInvalidMembershipRoleRecord
//
// 撞上 chk_membership_roles_status 说明状态取值与领域层已经漂移
// （通常是有人绕过了构造函数与状态转换方法直接改字段）。
// 它必须变成可归类的领域错误，否则只能以驱动原生错误的形式冒到上层。
//
// 注意这条约束仍归记录级错误：领域层没有同口径的字段级错误，
// 详见 membershipRoleWriteTargets 里的分流说明。
func Test成员角色取值不自洽归入ErrInvalidMembershipRoleRecord(t *testing.T) {
	r := newTestMembershipRole(t)
	err := pgConstraintError("23514", "chk_membership_roles_status", gorm.ErrCheckConstraintViolated)

	got := translateMembershipRoleWriteError(err, r)
	if !errors.Is(got, membershiprole.ErrInvalidMembershipRoleRecord) {
		t.Fatalf("err = %v，期望 ErrInvalidMembershipRoleRecord", got)
	}
	if !errors.Is(got, domain.ErrInvalidState) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidState", got)
	}
	if errors.Is(got, domain.ErrConflict) || errors.Is(got, domain.ErrNotFound) {
		t.Fatalf("err = %v，不应归类到写操作结局类", got)
	}
}

// Test成员角色遇到未识别的数据库错误原样上抛
//
// 翻译只覆盖已知的两类：连接失败、超时等必须原样冒泡，
// 不得被误判成"已持有该角色"或"记录不合法"。
func Test成员角色遇到未识别的数据库错误原样上抛(t *testing.T) {
	boom := errors.New("connection reset")
	got := translateMembershipRoleWriteError(boom, newTestMembershipRole(t))
	if !errors.Is(got, boom) {
		t.Fatalf("err = %v，期望原样返回底层错误", got)
	}
}

// Test成员角色Update命中零行时的三种结局
//
// Update 的 WHERE 带着版本号与"非终态"两个前提，命中 0 行有三种原因：
// 目标行不存在（调用方 bug，404）、目标行已是终态（改它等于复活，409 状态类）、
// 目标行存在且非终态但版本号对不上（已被他人改过，409 冲突类）。
// 这条判定必须能脱离数据库单独验证——命中 0 行之后的分支
// 在集成测试里要靠 Docker 才能跑到。
func Test成员角色Update命中零行时的三种结局(t *testing.T) {
	t.Run("目标行不存在", func(t *testing.T) {
		err := classifyMembershipRoleUpdate(false, "", "some-id")
		if !errors.Is(err, membershiprole.ErrMembershipRoleNotFound) {
			t.Fatalf("err = %v，期望 ErrMembershipRoleNotFound", err)
		}
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
		}
	})

	t.Run("库里是不认识的状态", func(t *testing.T) {
		err := classifyMembershipRoleUpdate(true, "weird", "some-id")
		// 取值问题，不是"终态不可改"：两者的处置方向完全不同。
		if !errors.Is(err, membershiprole.ErrInvalidStatusValue) {
			t.Fatalf("err = %v，期望 ErrInvalidStatusValue", err)
		}
		if !errors.Is(err, domain.ErrInvalidKey) {
			t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
		}
		if errors.Is(err, membershiprole.ErrInvalidStatusTransition) {
			t.Fatalf("err = %v，未知状态不应被误报为终态", err)
		}
	})

	t.Run("目标行已是终态", func(t *testing.T) {
		err := classifyMembershipRoleUpdate(true, string(membershiprole.StatusRevoked), "some-id")
		// 终态是历史，改它等于复活一次已撤销的授权：
		// 报状态类错误，不报冲突类——后者会让调用方"重新读取再重试"，
		// 而终态行重试多少次都不会成功。
		if !errors.Is(err, membershiprole.ErrInvalidStatusTransition) {
			t.Fatalf("err = %v，期望 ErrInvalidStatusTransition", err)
		}
		if !errors.Is(err, domain.ErrInvalidState) {
			t.Fatalf("err = %v，应归类到 domain.ErrInvalidState", err)
		}
		if errors.Is(err, domain.ErrConflict) {
			t.Fatalf("err = %v，终态不应归类到 domain.ErrConflict", err)
		}
	})

	t.Run("目标行已被他人改过", func(t *testing.T) {
		err := classifyMembershipRoleUpdate(true, string(membershiprole.StatusActive), "some-id")
		if !errors.Is(err, membershiprole.ErrMembershipRoleConflict) {
			t.Fatalf("err = %v，期望 ErrMembershipRoleConflict", err)
		}
		if !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("err = %v，应归类到 domain.ErrConflict", err)
		}
		// 冲突与不存在必须能区分：前者让调用方重新读取，后者是调用方 bug。
		if errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v，不应归类到 domain.ErrNotFound", err)
		}
	})
}

// Test成员角色可写列白名单排除业务键与授权动作
//
// 一次授权的业务键换了任何一个都已是另一次授权，不是"改了一下"；
// assigned_by / assigned_at 记录授权动作本身，允许覆写就等于允许事后改写
// "谁授的权、什么时候授的"；created_at 不可变、updated_at 归触发器维护。
//
// expires_at 也在禁止之列：它在白名单里时，一次没带 ExpiresAt 的 Update
// 会把库里的到期时间静默写成 NULL，一条有期限的授权就此变成永久有效。
func Test成员角色可写列白名单排除业务键与授权动作(t *testing.T) {
	forbidden := []string{
		"id", "tenant_id", "membership_id", "role_id",
		"assigned_by", "assigned_at", "created_at", "updated_at",
		"expires_at",
	}
	for _, col := range forbidden {
		for _, allowed := range membershipRoleUpdatableColumns {
			if col == allowed {
				t.Fatalf("可写列白名单不应包含 %q", col)
			}
		}
	}

	want := []string{"status", "reason", "effective_at", "version"}
	if len(membershipRoleUpdatableColumns) != len(want) {
		t.Fatalf("白名单 = %v，期望 %v", membershipRoleUpdatableColumns, want)
	}
	for i := range want {
		if membershipRoleUpdatableColumns[i] != want[i] {
			t.Fatalf("白名单第 %d 项 = %q，期望 %q", i, membershipRoleUpdatableColumns[i], want[i])
		}
	}
}

// Test成员角色Update的列值对与白名单逐项一致
//
// 两份清单必须同步：Select 用的是白名单切片，Updates 用的是列值对 map。
// 只往一处加列的后果是"该写的字段没写"——静默的数据错误，
// 而只断言白名单内容（上一版就是这么做的）根本发现不了。
func Test成员角色Update的列值对与白名单逐项一致(t *testing.T) {
	updateMap := toMembershipRoleRecord(newTestMembershipRole(t)).toUpdateMap()

	if len(updateMap) != len(membershipRoleUpdatableColumns) {
		t.Fatalf("列值对有 %d 项，白名单有 %d 项，两者必须逐项对应",
			len(updateMap), len(membershipRoleUpdatableColumns))
	}
	for _, col := range membershipRoleUpdatableColumns {
		if _, ok := updateMap[col]; !ok {
			t.Fatalf("列值对里缺少白名单列 %q", col)
		}
	}
}

// Test未失效状态集合与部分唯一索引条件一致
//
// 唯一性只对这批状态成立，查询若用另一套口径，
// 就会出现"库里挡住了、查询却说没有"或反之的自相矛盾。
func Test未失效状态集合与部分唯一索引条件一致(t *testing.T) {
	want := map[string]bool{
		string(membershiprole.StatusPending): true,
		string(membershiprole.StatusActive):  true,
	}

	if len(membershipRoleNotExpiredStatuses) != len(want) {
		t.Fatalf("集合 = %v，期望恰好 %d 项", membershipRoleNotExpiredStatuses, len(want))
	}
	for _, s := range membershipRoleNotExpiredStatuses {
		if !want[s] {
			t.Fatalf("集合包含不该有的状态 %q", s)
		}
	}
}

// Test还原实体时校验状态取值
//
// 滚动发布时旧版本会读到新版本写入的状态。
// 让"读到一个不认识的状态"成为可归类的错误，
// 而不是让它以"未失效"的身份混进鉴权结果。
//
// 注意这条只能单测：chk_membership_roles_status 会挡住非法状态写入，
// 真库里造不出这样的行。
func Test还原实体时校验状态取值(t *testing.T) {
	r := newTestMembershipRole(t)

	rec := toMembershipRoleRecord(r)
	got, err := rec.toEntity()
	if err != nil {
		t.Fatalf("合法状态不应报错：%v", err)
	}
	if got.Status != r.Status {
		t.Fatalf("Status = %q，期望 %q", got.Status, r.Status)
	}

	for _, bad := range []membershiprole.Status{membershiprole.Status("weird"), membershiprole.Status("")} {
		t.Run(string(bad), func(t *testing.T) {
			rec.Status = string(bad)
			got, err := rec.toEntity()
			if got != nil {
				t.Fatalf("非法状态不应返回实体：%+v", got)
			}
			if !errors.Is(err, membershiprole.ErrInvalidStatusValue) {
				t.Fatalf("err = %v，期望 ErrInvalidStatusValue", err)
			}
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
			}
		})
	}
}
