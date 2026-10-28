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
	r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "分配理由")
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

	r, err := New(tenantID, membershipID, roleID, assignedBy, time.Now(), time.Time{}, "  分配理由  ")
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
				return New(uuid.Nil(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "")
			},
			domain.ErrInvalidTenantID,
		},
		{
			"成员关系标识为零值",
			func() (*MembershipRole, error) {
				return New(uuid.New(), uuid.Nil(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "")
			},
			domain.ErrInvalidMembershipID,
		},
		{
			"角色标识为零值",
			func() (*MembershipRole, error) {
				return New(uuid.New(), uuid.New(), uuid.Nil(), uuid.New(), time.Now(), time.Time{}, "")
			},
			domain.ErrInvalidRoleID,
		},
		{
			"分配人为零值",
			func() (*MembershipRole, error) {
				return New(uuid.New(), uuid.New(), uuid.New(), uuid.Nil(), time.Now(), time.Time{}, "")
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

// Test构造函数接受未来到期时间并按数据库精度对齐
//
// 时间戳一律对齐到 TIMESTAMPTZ 的微秒精度（见 alignToPG）：
// 纳秒部分留着没有意义——落库时会被截断，而领域层的比较却用它，
// 于是"应用层判定了、数据库不认"的裂缝就出现了。
func Test构造函数接受未来到期时间并按数据库精度对齐(t *testing.T) {
	now := time.Now()
	expiresAt := now.Add(time.Hour)

	r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), now, expiresAt, "")
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	want := expiresAt.Truncate(time.Microsecond)
	if !r.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v，期望对齐到微秒后的 %v", r.ExpiresAt, want)
	}
	// 同一批时间戳用的是同一个来源，且都已对齐。
	for _, got := range []time.Time{r.AssignedAt, r.EffectiveAt, r.CreatedAt, r.UpdatedAt} {
		if !got.Equal(now.Truncate(time.Microsecond)) {
			t.Fatalf("时间戳 = %v，期望对齐到微秒后的 %v", got, now.Truncate(time.Microsecond))
		}
	}
}

// Test到期余量不足一微秒时被拒绝
//
// 这是 alignToPG 存在的理由：余量小于数据库精度时，
// 领域层在纳秒粒度上会判定"晚于"并放行，
// 落库截断后两者相等，chk_membership_roles_expires_after_effective 随即拒绝——
// 一次本该在入口拦下的入参错误变成一次数据库冲突。
func Test到期余量不足一微秒时被拒绝(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	for _, offset := range []time.Duration{0, 1, 500 * time.Nanosecond} {
		_, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), now, now.Add(offset), "")
		if !errors.Is(err, ErrInvalidEffectiveRange) {
			t.Fatalf("余量 %v 应被拒绝，实际 err = %v", offset, err)
		}
	}

	// 刚好一个精度单位就必须放行，否则正常的短期授权会被误拒。
	if _, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), now, now.Add(time.Microsecond), ""); err != nil {
		t.Fatalf("余量 1µs 应被接受，实际 err = %v", err)
	}
}

// Test失效时间不晚于生效时间被拒绝
//
// 数据库有 chk_membership_roles_expires_after_effective 兜底，
// 但等到撞约束才失败就已经太晚：那是一次 500 而不是一次可归类的入参错误。
func Test失效时间不晚于生效时间被拒绝(t *testing.T) {
	past := time.Now().Add(-time.Hour)

	r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), past, "")
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
		_, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{},
			strings.Repeat("理", maxReasonLen+1))
		if !errors.Is(err, ErrInvalidReason) {
			t.Fatalf("err = %v，期望 ErrInvalidReason", err)
		}
	})

	t.Run("纯空白视为未填写", func(t *testing.T) {
		r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "   ")
		if err != nil {
			t.Fatalf("空白原因不该报错：%v", err)
		}
		if r.Reason != "" {
			t.Fatalf("Reason = %q，应归一为空串（落库为 NULL）", r.Reason)
		}
	})

	t.Run("恰好达到上限", func(t *testing.T) {
		r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{},
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
			return NewSystem(uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "")
		},
		"NewSystemPending": func() (*MembershipRole, error) {
			return NewSystemPending(uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "")
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
	r, err := NewPending(uuid.New(), uuid.New(), uuid.New(), uuid.New(), time.Now(), time.Time{}, "")
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
	if !errors.Is(err, ErrAlreadyExpired) {
		t.Fatalf("err = %v，期望 ErrAlreadyExpired", err)
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

	// 长期有效是"查询条件写错了"，未到期是"时机未到"：
	// 两者分开之后上层才能分清该改查询还是该再等一会儿。
	t.Run("长期有效的授权不可判为到期", func(t *testing.T) {
		r := newInStatus(t, StatusActive)
		if err := r.Expire(fixedNow); !errors.Is(err, ErrNoExpiry) {
			t.Fatalf("err = %v，期望 ErrNoExpiry", err)
		}
		if r.Status != StatusActive {
			t.Fatalf("失败后状态不应改变：%q", r.Status)
		}
	})

	t.Run("未到期不可判为到期", func(t *testing.T) {
		r := newExpiring(t, futureExpiry)
		if err := r.Expire(futureExpiry.Add(-time.Minute)); !errors.Is(err, ErrNotYetExpired) {
			t.Fatalf("err = %v，期望 ErrNotYetExpired", err)
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

// Test状态取值的封闭性判定
//
// 未知状态一律判非法（fail closed）：滚动发布时旧版本会读到新版本写入的状态，
// 那时宁可报错，也不能把它当成"未失效"继续参与鉴权。
func Test状态取值的封闭性判定(t *testing.T) {
	cases := map[Status]bool{
		StatusPending:   true,
		StatusActive:    true,
		StatusRevoked:   true,
		StatusExpired:   true,
		Status(""):      false,
		Status("weird"): false,
		// 大小写不同就是另一个取值：词表是逐字比较的。
		Status("PENDING"): false,
	}

	for status, want := range cases {
		if got := status.Valid(); got != want {
			t.Errorf("Status(%q).Valid() = %v，期望 %v", status, got, want)
		}
	}
}

// TestValidate拦下绕过构造函数的非法取值
//
// 实体的字段是导出的，调用方可以不走构造函数直接改写它们。
// Validate 是写路径上的入口校验：少了它，非法取值只能等数据库 CHECK 兜底，
// 上层拿到的是记录级错误而不是可归类的入参错误。
func TestValidate拦下绕过构造函数的非法取值(t *testing.T) {
	valid := func() *MembershipRole {
		r, err := New(uuid.New(), uuid.New(), uuid.New(), uuid.New(), fixedNow, futureExpiry, "分配理由")
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		return r
	}

	cases := []struct {
		name     string
		mutate   func(r *MembershipRole)
		want     error
		wantBase error
	}{
		{"主键非法", func(r *MembershipRole) { r.ID = "not-a-uuid" }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"主键为零值", func(r *MembershipRole) { r.ID = uuid.Nil().String() }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		// uuid.Parse 接受但 PostgreSQL 的 uuid 输入函数不接受的几种形态：
		// 只校验"能解析"会留下"领域层判合法、落库时数据库报 22P02"的裂缝。
		{"主键带 urn 前缀", func(r *MembershipRole) { r.ID = "urn:uuid:" + r.ID }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"主键带花括号", func(r *MembershipRole) { r.ID = "{" + r.ID + "}" }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"主键无连字符", func(r *MembershipRole) { r.ID = strings.ReplaceAll(r.ID, "-", "") }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"主键为大写", func(r *MembershipRole) { r.ID = strings.ToUpper(r.ID) }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"主键首尾有空白", func(r *MembershipRole) { r.ID = " " + r.ID + " " }, ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"租户标识非法", func(r *MembershipRole) { r.TenantID = "" }, domain.ErrInvalidTenantID, domain.ErrInvalidKey},
		{"成员关系标识非法", func(r *MembershipRole) { r.MembershipID = "" }, domain.ErrInvalidMembershipID, domain.ErrInvalidKey},
		{"角色标识非法", func(r *MembershipRole) { r.RoleID = "" }, domain.ErrInvalidRoleID, domain.ErrInvalidKey},
		{"分配人为纯空白", func(r *MembershipRole) { r.AssignedBy = "   " }, domain.ErrInvalidActor, domain.ErrInvalidKey},
		{"状态未知", func(r *MembershipRole) { r.Status = Status("weird") }, ErrInvalidStatusValue, domain.ErrInvalidKey},
		{"状态为空", func(r *MembershipRole) { r.Status = Status("") }, ErrInvalidStatusValue, domain.ErrInvalidKey},
		{"原因为纯空白", func(r *MembershipRole) { r.Reason = "   " }, ErrInvalidReason, domain.ErrInvalidKey},
		{"原因超长", func(r *MembershipRole) { r.Reason = strings.Repeat("理", maxReasonLen+1) }, ErrInvalidReason, domain.ErrInvalidKey},
		{"失效不晚于生效", func(r *MembershipRole) { r.ExpiresAt = r.EffectiveAt }, ErrInvalidEffectiveRange, domain.ErrInvalidKey},
		{"expired 无到期时间", func(r *MembershipRole) { r.Status = StatusExpired; r.ExpiresAt = time.Time{} }, ErrInvalidMembershipRoleRecord, domain.ErrInvalidState},
		{"生效时间为零值", func(r *MembershipRole) { r.EffectiveAt = time.Time{} }, ErrInvalidMembershipRoleRecord, domain.ErrInvalidState},
		{"版本号为零", func(r *MembershipRole) { r.Version = 0 }, ErrInvalidMembershipRoleRecord, domain.ErrInvalidState},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid()
			tc.mutate(r)
			err := r.Validate()
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v，期望 %v", err, tc.want)
			}
			if !errors.Is(err, tc.wantBase) {
				t.Fatalf("err = %v，应归类到 %v", err, tc.wantBase)
			}
		})
	}
}

// TestValidate放行构造函数产出的实体与状态转换后的实体
//
// 反向用例：入口校验不能误伤正常路径。
func TestValidate放行构造函数产出的实体与状态转换后的实体(t *testing.T) {
	for _, status := range []Status{StatusPending, StatusActive} {
		r, err := newMembershipRole(uuid.New(), uuid.New(), uuid.New(), domain.SystemCreator, status, fixedNow, futureExpiry, "理由")
		if err != nil {
			t.Fatalf("构造失败：%v", err)
		}
		if err := r.Validate(); err != nil {
			t.Fatalf("状态 %q 的实体应能通过 Validate，实际 err = %v", status, err)
		}

		// 终态也要能落库：撤销与到期之后都要写回去。
		if status == StatusActive {
			if err := r.Revoke("不再需要"); err != nil {
				t.Fatalf("撤销失败：%v", err)
			}
			if err := r.Validate(); err != nil {
				t.Fatalf("撤销后的实体应能通过 Validate，实际 err = %v", err)
			}
		}
	}
}

// TestValidate对空实体返回ErrNilMembershipRole
func TestValidate对空实体返回ErrNilMembershipRole(t *testing.T) {
	var r *MembershipRole

	err := r.Validate()
	if !errors.Is(err, ErrNilMembershipRole) {
		t.Fatalf("err = %v，期望 ErrNilMembershipRole", err)
	}
	if !errors.Is(err, domain.ErrNilEntity) {
		t.Fatalf("err = %v，应归类到 domain.ErrNilEntity", err)
	}
}

// Test各领域错误的基类归属稳定
//
// 上层只需对 domain 的基类做 errors.Is 判定即可统一归类，
// 不必逐个枚举本包的错误——前提是每一个错误的基类都稳定。
// 这张表把归属固化下来：改任何一个错误的基类都会让这里变红。
func Test各领域错误的基类归属稳定(t *testing.T) {
	cases := []struct {
		name string
		err  error
		base error
	}{
		{"ErrInvalidStatusTransition", ErrInvalidStatusTransition, domain.ErrInvalidState},
		{"ErrInvalidStatusValue", ErrInvalidStatusValue, domain.ErrInvalidKey},
		{"ErrNoExpiry", ErrNoExpiry, domain.ErrInvalidState},
		{"ErrNotYetExpired", ErrNotYetExpired, domain.ErrInvalidState},
		{"ErrAlreadyExpired", ErrAlreadyExpired, domain.ErrInvalidState},
		{"ErrNilMembershipRole", ErrNilMembershipRole, domain.ErrNilEntity},
		{"ErrInvalidMembershipRoleRecord", ErrInvalidMembershipRoleRecord, domain.ErrInvalidState},
		{"ErrInvalidReason", ErrInvalidReason, domain.ErrInvalidKey},
		{"ErrInvalidEffectiveRange", ErrInvalidEffectiveRange, domain.ErrInvalidKey},
		{"ErrInvalidMembershipRoleID", ErrInvalidMembershipRoleID, domain.ErrInvalidKey},
		{"ErrMembershipRoleExists", ErrMembershipRoleExists, domain.ErrAlreadyExists},
		{"ErrMembershipRoleNotFound", ErrMembershipRoleNotFound, domain.ErrNotFound},
		{"ErrMembershipRoleConflict", ErrMembershipRoleConflict, domain.ErrConflict},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, tc.base) {
				t.Fatalf("%s 未归类到 %v", tc.name, tc.base)
			}
		})
	}
}

// Test三个到期错误互不串类
//
// 拆成三个而不是共用一个的理由就在这里：
// 上层要能分清"该改查询条件"（NoExpiry）、"该再等等"（NotYetExpired）
// 与"早该判了"（AlreadyExpired）。可辨识就等价于互不命中。
func Test三个到期错误互不串类(t *testing.T) {
	expiryErrors := map[string]error{
		"ErrNoExpiry":       ErrNoExpiry,
		"ErrNotYetExpired":  ErrNotYetExpired,
		"ErrAlreadyExpired": ErrAlreadyExpired,
	}

	for name, err := range expiryErrors {
		t.Run(name, func(t *testing.T) {
			for otherName, other := range expiryErrors {
				if otherName == name {
					continue
				}
				if errors.Is(err, other) {
					t.Fatalf("%s 不应同时命中 %s", name, otherName)
				}
			}
		})
	}
}
