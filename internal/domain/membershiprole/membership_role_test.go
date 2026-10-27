package membershiprole

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// fixedNow 是测试中的时间基准：状态转换的时间由调用方传入，
// 因此可以直接断言相等，无需依赖真实时钟。
var fixedNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

// futureExpiry 是一个晚于 fixedNow 的到期时间，供构造长期有效之外的授权使用。
var futureExpiry = fixedNow.Add(24 * time.Hour)

// newInStatus 构造处于指定状态的授权，用于直接测试各条状态转换路径。
// 状态由构造函数固定为 active / pending，这里只是覆写，不绕过任何校验。
func newInStatus(t *testing.T, status Status) *MembershipRole {
	t.Helper()
	r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Time{}, "分配理由")
	if err != nil {
		t.Fatalf("构造授权失败：%v", err)
	}
	r.Status = status
	return r
}

// newExpiring 构造一条带到期时间的 active 授权，用于测试到期推进。
func newExpiring(t *testing.T, expiresAt time.Time) *MembershipRole {
	t.Helper()
	return newExpiringInStatus(t, expiresAt, StatusActive)
}

// newExpiringInStatus 构造一条带到期时间、处于指定状态的授权。
// 构造函数要求 expires_at 晚于此刻，因此这里直接覆写字段，不走校验。
func newExpiringInStatus(t *testing.T, expiresAt time.Time, status Status) *MembershipRole {
	t.Helper()
	r := newInStatus(t, status)
	r.ExpiresAt = expiresAt
	return r
}

// statusName 把状态转成可读的用例名。空状态没有可打印文本，
// 单独给一个名字，避免子测试名变成空字符串后难以定位。
func statusName(s Status) string {
	if s == "" {
		return "空状态"
	}
	return string(s)
}

// 非法来源状态的用例集：除各方法明确不允许的状态外，
// 一并覆盖未知状态与空状态——空状态是从数据库读到的更现实的脏数据场景。
var illegalForApprove = []Status{StatusActive, StatusRevoked, StatusExpired, Status("weird"), Status("")}

func TestNew_必填UUID齐全时构造成功(t *testing.T) {
	tenantID := uuid.New()
	membershipID := uuid.New()
	roleID := uuid.New()
	assignedBy := uuid.New()

	r, err := New(tenantID, membershipID, roleID, assignedBy, time.Time{}, "  分配理由  ")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if r.TenantID != tenantID.String() {
		t.Fatalf("TenantID = %q，期望 %q", r.TenantID, tenantID.String())
	}
	if r.MembershipID != membershipID.String() {
		t.Fatalf("MembershipID = %q，期望 %q", r.MembershipID, membershipID.String())
	}
	if r.RoleID != roleID.String() {
		t.Fatalf("RoleID = %q，期望 %q", r.RoleID, roleID.String())
	}
	if r.AssignedBy != assignedBy.String() {
		t.Fatalf("AssignedBy = %q，期望 %q", r.AssignedBy, assignedBy.String())
	}
	if r.Status != StatusActive {
		t.Fatalf("初始状态 = %q，期望 %q", r.Status, StatusActive)
	}
	if r.ID == "" {
		t.Fatal("主键不应为空")
	}
	if !r.ExpiresAt.IsZero() {
		t.Fatalf("未传到期时间时 ExpiresAt 应为零值，实际 %v", r.ExpiresAt)
	}
	if r.Reason != "分配理由" {
		t.Fatalf("Reason = %q，应已去两侧空白", r.Reason)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
		t.Fatal("创建时间与更新时间不应为零值")
	}
	// 立即生效的授权：生效时间就是授权动作发生的时刻，两者不应漂移。
	if !r.EffectiveAt.Equal(r.AssignedAt) {
		t.Fatalf("EffectiveAt = %v，应与 AssignedAt %v 一致", r.EffectiveAt, r.AssignedAt)
	}
}

// TestNew_零值入参被拒绝
//
// 主体 ID 收窄为 uuid.UUID 后，"非法 ID"在入口就被拦住，
// 这四条用例锁定的正是这个收窄行为。
func TestNew_零值入参被拒绝(t *testing.T) {
	cases := []struct {
		name string
		call func() (*MembershipRole, error)
		want error
	}{
		{
			"租户标识为零值",
			func() (*MembershipRole, error) {
				return New(uuid.Nil(), uuid.New(), uuid.New(), uuid.New(), time.Time{}, "")
			},
			domain.ErrInvalidTenantID,
		},
		{
			"成员关系标识为零值",
			func() (*MembershipRole, error) {
				return New(uuid.New(), uuid.Nil(), uuid.New(), uuid.New(), time.Time{}, "")
			},
			ErrInvalidMembershipID,
		},
		{
			"角色标识为零值",
			func() (*MembershipRole, error) {
				return New(uuid.New(), uuid.New(), uuid.Nil(), uuid.New(), time.Time{}, "")
			},
			domain.ErrInvalidRoleID,
		},
		{
			"分配人为零值",
			func() (*MembershipRole, error) {
				return New(uuid.New(), uuid.New(), uuid.New(), uuid.Nil(), time.Time{}, "")
			},
			domain.ErrInvalidActor,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.call()
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v，期望 %v", err, tc.want)
			}
			// 标识类错误统一归到 ErrInvalidKey，上层才能按基类归类为 400。
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
			}
		})
	}
}

// Test构造函数接受未来到期时间并保留取值
func Test构造函数接受未来到期时间并保留取值(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), expiresAt, "")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if !r.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v，期望 %v", r.ExpiresAt, expiresAt)
	}
}

// Test失效时间不晚于生效时间被拒绝
//
// 数据库有 chk_membership_roles_expires_after_effective 兜底，
// 但等到撞约束才失败就已经太晚：那是一次 500 而不是一次可归类的入参错误。
func Test失效时间不晚于生效时间被拒绝(t *testing.T) {
	past := time.Now().Add(-time.Hour)

	r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), past, "")
	if !errors.Is(err, ErrInvalidEffectiveRange) {
		t.Fatalf("err = %v，期望 ErrInvalidEffectiveRange", err)
	}
	if r != nil {
		t.Fatalf("失败时不应返回实体：%+v", r)
	}
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", err)
	}
}

// Test原因超长被拒绝而空白视为未填写
func Test原因超长被拒绝而空白视为未填写(t *testing.T) {
	t.Run("超长", func(t *testing.T) {
		_, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Time{},
			strings.Repeat("理", maxReasonLen+1))
		if !errors.Is(err, ErrInvalidReason) {
			t.Fatalf("err = %v，期望 ErrInvalidReason", err)
		}
	})

	t.Run("纯空白视为未填写", func(t *testing.T) {
		r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Time{}, "   ")
		if err != nil {
			t.Fatalf("空白原因不该报错：%v", err)
		}
		if r.Reason != "" {
			t.Fatalf("Reason = %q，应归一为空串（落库为 NULL）", r.Reason)
		}
	})

	t.Run("恰好达到上限", func(t *testing.T) {
		r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Time{},
			strings.Repeat("理", maxReasonLen))
		if err != nil {
			t.Fatalf("达到上限的原因应被接受：%v", err)
		}
		// 上限按字符数计（与数据库的 length(reason) 同口径），中文原因因此不会被误判超长。
		if n := utf8.RuneCountInString(r.Reason); n != maxReasonLen {
			t.Fatalf("Reason 字符数 = %d，期望 %d", n, maxReasonLen)
		}
	})
}

// Test系统身份构造函数的分配人为SystemCreator
//
// 系统初始化给 owner 分配角色时没有自然人分配人，
// 各聚合一律引用 domain.SystemCreator，不得自行书写 "system" 字面量。
func Test系统身份构造函数的分配人为SystemCreator(t *testing.T) {
	cases := map[string]func() (*MembershipRole, error){
		"NewSystem": func() (*MembershipRole, error) {
			return NewSystem(uuid.New(), uuid.New(), uuid.New(), time.Time{}, "")
		},
		"NewSystemPending": func() (*MembershipRole, error) {
			return NewSystemPending(uuid.New(), uuid.New(), uuid.New(), time.Time{}, "")
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			r, err := call()
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			if r.AssignedBy != domain.SystemCreator {
				t.Fatalf("AssignedBy = %q，期望 %q", r.AssignedBy, domain.SystemCreator)
			}
		})
	}
}

// TestPending构造函数的初始状态为待审批
func TestPending构造函数的初始状态为待审批(t *testing.T) {
	r, err := NewPending(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Time{}, "")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if r.Status != StatusPending {
		t.Fatalf("初始状态 = %q，期望 %q", r.Status, StatusPending)
	}
}

// TestApprove_审批通过写入生效时间
//
// pending 期间 effective_at 只是构造时刻的占位，
// 审批通过的时刻才是授权真正开始生效的时刻——这里锁定的正是这个改写。
func TestApprove_审批通过写入生效时间(t *testing.T) {
	r := newInStatus(t, StatusPending)
	before := r.EffectiveAt

	if err := r.Approve(fixedNow); err != nil {
		t.Fatalf("审批失败：%v", err)
	}
	if r.Status != StatusActive {
		t.Fatalf("状态 = %q，期望 %q", r.Status, StatusActive)
	}
	if !r.EffectiveAt.Equal(fixedNow) {
		t.Fatalf("EffectiveAt = %v，期望 %v", r.EffectiveAt, fixedNow)
	}
	if !r.AssignedAt.Equal(before) {
		t.Fatalf("AssignedAt 不应被改写：%v，期望 %v", r.AssignedAt, before)
	}
}

// TestApprove_审批时刻不早于到期时间时被拒绝
//
// 此刻该授权已经过期，正确处置是 Expire 而不是先批准再立刻失效。
// 少了这一校验，写进去的值会撞上 chk_membership_roles_expires_after_effective，
// 一次本该精确可判的入参错误会变成 500。
func TestApprove_审批时刻不早于到期时间时被拒绝(t *testing.T) {
	r := newExpiringInStatus(t, futureExpiry, StatusPending)

	err := r.Approve(futureExpiry.Add(time.Hour))
	if !errors.Is(err, ErrNotExpirable) {
		t.Fatalf("err = %v，期望 ErrNotExpirable", err)
	}
	if r.Status != StatusPending {
		t.Fatalf("失败后状态不应改变：%q", r.Status)
	}
}

// Test状态转换拒绝非法来源状态
func Test状态转换拒绝非法来源状态(t *testing.T) {
	cases := []struct {
		name string
		call func(r *MembershipRole) error
		from []Status
	}{
		{
			"Approve",
			func(r *MembershipRole) error { return r.Approve(fixedNow) },
			illegalForApprove,
		},
		{
			"Revoke",
			func(r *MembershipRole) error { return r.Revoke("不再需要") },
			[]Status{StatusRevoked, StatusExpired, Status("weird"), Status("")},
		},
		{
			"Expire",
			func(r *MembershipRole) error { return r.Expire(fixedNow) },
			[]Status{StatusRevoked, StatusExpired, Status("weird"), Status("")},
		},
	}

	for _, tc := range cases {
		for _, from := range tc.from {
			t.Run(tc.name+"_"+statusName(from), func(t *testing.T) {
				r := newInStatus(t, from)
				err := tc.call(r)
				if !errors.Is(err, ErrInvalidStatusTransition) {
					t.Fatalf("err = %v，期望 ErrInvalidStatusTransition", err)
				}
				// 状态类错误的归类必须稳定，否则上层无法统一映射 409。
				if !errors.Is(err, domain.ErrInvalidState) {
					t.Fatalf("err = %v，应归类到 domain.ErrInvalidState", err)
				}
				if errors.Is(err, domain.ErrInvalidKey) {
					t.Fatalf("err = %v，不应归类到 domain.ErrInvalidKey", err)
				}
				if r.Status != from {
					t.Fatalf("失败后状态不应改变：%q，期望 %q", r.Status, from)
				}
			})
		}
	}
}

// TestRevoke_撤销覆写原因且拒绝空原因
func TestRevoke_撤销覆写原因且拒绝空原因(t *testing.T) {
	t.Run("覆写原因", func(t *testing.T) {
		r := newInStatus(t, StatusActive)
		if err := r.Revoke("  离职  "); err != nil {
			t.Fatalf("撤销失败：%v", err)
		}
		if r.Status != StatusRevoked {
			t.Fatalf("状态 = %q，期望 %q", r.Status, StatusRevoked)
		}
		if r.Reason != "离职" {
			t.Fatalf("Reason = %q，应被撤销理由覆写并去空白", r.Reason)
		}
	})

	// 撤销是敏感操作：没有理由的撤销会让事后审计无从下手。
	t.Run("空原因被拒绝", func(t *testing.T) {
		r := newInStatus(t, StatusActive)
		err := r.Revoke("   ")
		if !errors.Is(err, ErrInvalidReason) {
			t.Fatalf("err = %v，期望 ErrInvalidReason", err)
		}
		if r.Status != StatusActive {
			t.Fatalf("失败后状态不应改变：%q", r.Status)
		}
	})
}

// TestExpire_到期推进与两条前置校验
//
// Expire 由定时任务调用，因此两条前置校验缺一不可：
// 否则一次越权的定时任务就能把长期有效的授权批量判死，或提前收回权限。
func TestExpire_到期推进与两条前置校验(t *testing.T) {
	t.Run("到期后推进", func(t *testing.T) {
		r := newExpiring(t, futureExpiry)
		if err := r.Expire(futureExpiry.Add(time.Minute)); err != nil {
			t.Fatalf("判为到期失败：%v", err)
		}
		if r.Status != StatusExpired {
			t.Fatalf("状态 = %q，期望 %q", r.Status, StatusExpired)
		}
	})

	t.Run("恰好到期即可推进", func(t *testing.T) {
		r := newExpiring(t, futureExpiry)
		if err := r.Expire(futureExpiry); err != nil {
			t.Fatalf("到期时间到达时应当可以推进：%v", err)
		}
	})

	t.Run("长期有效的授权不可判为到期", func(t *testing.T) {
		r := newInStatus(t, StatusActive)
		if err := r.Expire(fixedNow); !errors.Is(err, ErrNotExpirable) {
			t.Fatalf("err = %v，期望 ErrNotExpirable", err)
		}
		if r.Status != StatusActive {
			t.Fatalf("失败后状态不应改变：%q", r.Status)
		}
	})

	t.Run("未到期不可判为到期", func(t *testing.T) {
		r := newExpiring(t, futureExpiry)
		if err := r.Expire(futureExpiry.Add(-time.Minute)); !errors.Is(err, ErrNotExpirable) {
			t.Fatalf("err = %v，期望 ErrNotExpirable", err)
		}
		if r.Status != StatusActive {
			t.Fatalf("失败后状态不应改变：%q", r.Status)
		}
	})

	// pending 授权在过期前仍未审批，同样由定时任务判死。
	t.Run("待审批的授权也会到期", func(t *testing.T) {
		r := newExpiringInStatus(t, futureExpiry, StatusPending)
		if err := r.Expire(futureExpiry.Add(time.Hour)); err != nil {
			t.Fatalf("err = %v，待审批的授权到期后应可判定", err)
		}
		if r.Status != StatusExpired {
			t.Fatalf("状态 = %q，期望 %q", r.Status, StatusExpired)
		}
	})
}

// Test空实体上的状态转换归入ErrNilEntity
//
// 空实体是调用方 bug，必须落在 domain.ErrNilEntity 上（HTTP 层映射 500），
// 不得混入业务分类——否则一次程序缺陷会被当成正常的 4xx。
func Test空实体上的状态转换归入ErrNilEntity(t *testing.T) {
	var r *MembershipRole

	cases := map[string]func() error{
		"Approve": func() error { return r.Approve(fixedNow) },
		"Revoke":  func() error { return r.Revoke("原因") },
		"Expire":  func() error { return r.Expire(fixedNow) },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, ErrNilMembershipRole) {
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

// Test终态不再转出
//
// revoked / expired 是终态：再次授予同一角色应新建一行，
// 而不是把历史行改回 active——那样会抹掉上一次授权的痕迹。
func Test终态不再转出(t *testing.T) {
	for _, from := range []Status{StatusRevoked, StatusExpired} {
		t.Run(statusName(from), func(t *testing.T) {
			r := newInStatus(t, from)
			if err := r.Approve(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("Approve err = %v，期望 ErrInvalidStatusTransition", err)
			}
			if err := r.Revoke("原因"); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("Revoke err = %v，期望 ErrInvalidStatusTransition", err)
			}
			if r.Status != from {
				t.Fatalf("状态被改写为 %q，期望保持 %q", r.Status, from)
			}
		})
	}
}
