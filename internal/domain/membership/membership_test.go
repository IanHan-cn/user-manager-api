package membership

import (
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// fixedNow 是测试中的时间基准：状态转换的时间戳由调用方传入，
// 因此可以直接断言相等，无需依赖真实时钟。
var fixedNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// newInStatus 构造处于指定状态的成员关系，用于直接测试各条状态转换路径。
// 状态由构造函数固定为 invited，这里只是覆写，不绕过任何校验。
func newInStatus(t *testing.T, status Status) *Membership {
	t.Helper()
	m, err := New(uuid.New(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("构造成员关系失败：%v", err)
	}
	m.Status = status
	return m
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
var (
	illegalForActivate = []Status{StatusActive, StatusLeft, Status("weird"), Status("")}
	illegalForSuspend  = []Status{StatusInvited, StatusSuspended, StatusLeft, Status("weird"), Status("")}
)

func TestNew_必填UUID齐全时构造成功(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	invitedBy := uuid.New()

	m, err := New(tenantID, userID, invitedBy)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if m.TenantID != tenantID.String() {
		t.Fatalf("TenantID = %q，期望 %q", m.TenantID, tenantID.String())
	}
	if m.UserID != userID.String() {
		t.Fatalf("UserID = %q，期望 %q", m.UserID, userID.String())
	}
	if m.InvitedBy != invitedBy.String() {
		t.Fatalf("InvitedBy = %q，期望 %q", m.InvitedBy, invitedBy.String())
	}
	if m.Status != StatusInvited {
		t.Fatalf("初始状态 = %q，期望 %q", m.Status, StatusInvited)
	}
	if m.ID == "" {
		t.Fatal("主键不应为空")
	}
	if m.CreatedAt.IsZero() || m.UpdatedAt.IsZero() {
		t.Fatal("创建时间与更新时间不应为零值")
	}
	if !m.JoinedAt.IsZero() {
		t.Fatalf("新建成员关系尚未加入，JoinedAt 应为零值，实际 %v", m.JoinedAt)
	}
}

// TestNew_零值入参被拒绝
//
// 主体 ID 收窄为 uuid.UUID 后，"非法 ID"在入口就被拦住，
// 这三条用例锁定的正是这个收窄行为。
func TestNew_零值入参被拒绝(t *testing.T) {
	cases := []struct {
		name string
		call func() (*Membership, error)
		want error
	}{
		{
			"租户标识为零值",
			func() (*Membership, error) { return New(uuid.Nil(), uuid.New(), uuid.New()) },
			domain.ErrInvalidTenantID,
		},
		{
			"用户标识为零值",
			func() (*Membership, error) { return New(uuid.New(), uuid.Nil(), uuid.New()) },
			domain.ErrInvalidUserID,
		},
		{
			"邀请人为零值",
			func() (*Membership, error) { return New(uuid.New(), uuid.New(), uuid.Nil()) },
			domain.ErrInvalidActor,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := c.call()
			if !errors.Is(err, c.want) {
				t.Fatalf("错误 = %v，期望 %v", err, c.want)
			}
			if !errors.Is(err, domain.ErrInvalidKey) {
				t.Fatalf("错误 %v 未归类到 domain.ErrInvalidKey", err)
			}
			if m != nil {
				t.Fatal("出错时不应返回实体")
			}
		})
	}
}

func TestNewSystem_邀请人固定为系统标识(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()

	m, err := NewSystem(tenantID, userID)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}
	if m.InvitedBy != domain.SystemCreator {
		t.Fatalf("InvitedBy = %q，期望 %q", m.InvitedBy, domain.SystemCreator)
	}
	if m.TenantID != tenantID.String() || m.UserID != userID.String() {
		t.Fatalf("主体 ID = %q / %q，期望 UUID 字符串", m.TenantID, m.UserID)
	}
	if m.Status != StatusInvited {
		t.Fatalf("初始状态 = %q，期望 %q", m.Status, StatusInvited)
	}
}

// TestNewSystem_零值入参被拒绝
//
// 系统邀请不走 invitedBy，但主体 ID 的校验与 New 完全一致。
func TestNewSystem_零值入参被拒绝(t *testing.T) {
	if _, err := NewSystem(uuid.Nil(), uuid.New()); !errors.Is(err, domain.ErrInvalidTenantID) {
		t.Fatalf("租户标识零值错误 = %v，期望 domain.ErrInvalidTenantID", err)
	}
	if _, err := NewSystem(uuid.New(), uuid.Nil()); !errors.Is(err, domain.ErrInvalidUserID) {
		t.Fatalf("用户标识零值错误 = %v，期望 domain.ErrInvalidUserID", err)
	}
}

// TestActivate_首次加入写入加入时间
func TestActivate_首次加入写入加入时间(t *testing.T) {
	m := newInStatus(t, StatusInvited)
	before := m.UpdatedAt

	if err := m.Activate(fixedNow); err != nil {
		t.Fatalf("Activate 失败：%v", err)
	}
	if m.Status != StatusActive {
		t.Fatalf("激活后状态 = %q，期望 %q", m.Status, StatusActive)
	}
	if !m.JoinedAt.Equal(fixedNow) {
		t.Fatalf("JoinedAt = %v，期望 %v", m.JoinedAt, fixedNow)
	}
	if !m.UpdatedAt.Equal(before) {
		t.Fatalf("状态转换不应改写 UpdatedAt：%v → %v", before, m.UpdatedAt)
	}
}

// TestActivate_暂停后恢复保留首次加入时间
func TestActivate_暂停后恢复保留首次加入时间(t *testing.T) {
	m := newInStatus(t, StatusInvited)
	if err := m.Activate(fixedNow); err != nil {
		t.Fatalf("首次激活失败：%v", err)
	}
	if err := m.Suspend(fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("暂停失败：%v", err)
	}
	if err := m.Activate(fixedNow.Add(2 * time.Hour)); err != nil {
		t.Fatalf("恢复失败：%v", err)
	}

	if m.Status != StatusActive {
		t.Fatalf("恢复后状态 = %q，期望 %q", m.Status, StatusActive)
	}
	if !m.JoinedAt.Equal(fixedNow) {
		t.Fatalf("恢复后 JoinedAt = %v，期望保留首次加入时间 %v", m.JoinedAt, fixedNow)
	}
	if !m.SuspendedAt.IsZero() {
		t.Fatalf("恢复后 SuspendedAt = %v，期望清零", m.SuspendedAt)
	}
}

// TestActivate_非法来源状态被拒绝
//
// 拒绝时必须保持状态不变：失败的状态转换不能留下半截修改。
func TestActivate_非法来源状态被拒绝(t *testing.T) {
	for _, from := range illegalForActivate {
		t.Run(statusName(from), func(t *testing.T) {
			m := newInStatus(t, from)
			err := m.Activate(fixedNow)
			if !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("错误 = %v，期望 ErrInvalidStatusTransition", err)
			}
			if !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("错误 %v 未归类到 domain.ErrInvalidState", err)
			}
			if m.Status != from {
				t.Fatalf("失败后状态被改写为 %q，应保持 %q", m.Status, from)
			}
		})
	}
}

func TestSuspend_仅已激活可暂停(t *testing.T) {
	m := newInStatus(t, StatusActive)
	if err := m.Suspend(fixedNow); err != nil {
		t.Fatalf("Suspend 失败：%v", err)
	}
	if m.Status != StatusSuspended {
		t.Fatalf("暂停后状态 = %q，期望 %q", m.Status, StatusSuspended)
	}
	if !m.SuspendedAt.Equal(fixedNow) {
		t.Fatalf("SuspendedAt = %v，期望 %v", m.SuspendedAt, fixedNow)
	}
}

// TestSuspend_非法来源状态被拒绝
func TestSuspend_非法来源状态被拒绝(t *testing.T) {
	for _, from := range illegalForSuspend {
		t.Run(statusName(from), func(t *testing.T) {
			m := newInStatus(t, from)
			err := m.Suspend(fixedNow)
			if !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("错误 = %v，期望 ErrInvalidStatusTransition", err)
			}
			if m.Status != from {
				t.Fatalf("失败后状态被改写为 %q，应保持 %q", m.Status, from)
			}
		})
	}
}

// TestLeave_进入终态并记录离开时间
func TestLeave_进入终态并记录离开时间(t *testing.T) {
	for _, from := range []Status{StatusInvited, StatusActive, StatusSuspended} {
		t.Run(string(from), func(t *testing.T) {
			m := newInStatus(t, from)
			if err := m.Leave(fixedNow); err != nil {
				t.Fatalf("Leave 失败：%v", err)
			}
			if m.Status != StatusLeft {
				t.Fatalf("离开后状态 = %q，期望 %q", m.Status, StatusLeft)
			}
			if !m.LeftAt.Equal(fixedNow) {
				t.Fatalf("LeftAt = %v，期望 %v", m.LeftAt, fixedNow)
			}
		})
	}
}

// TestLeave_终态不可再转出
//
// 这是状态机的关键不变量：left 是终态，任何转换都必须被拒绝。
func TestLeave_终态不可再转出(t *testing.T) {
	m := newInStatus(t, StatusInvited)
	if err := m.Leave(fixedNow); err != nil {
		t.Fatalf("Leave 失败：%v", err)
	}

	if err := m.Activate(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("终态激活错误 = %v，期望 ErrInvalidStatusTransition", err)
	}
	if err := m.Suspend(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("终态暂停错误 = %v，期望 ErrInvalidStatusTransition", err)
	}
	if err := m.Leave(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
		t.Fatalf("终态离开错误 = %v，期望 ErrInvalidStatusTransition", err)
	}
	if m.Status != StatusLeft {
		t.Fatalf("终态被改写为 %q", m.Status)
	}
}

// Test状态转换_未知状态一律拒绝
//
// fail closed：从数据库读取到未知状态值（含空值）时，任何转换都不应被放行。
// 空状态比 "weird" 更现实：列允许为空、或历史数据缺列时都会出现。
func Test状态转换_未知状态一律拒绝(t *testing.T) {
	for _, from := range []Status{Status("weird"), Status("")} {
		t.Run(statusName(from), func(t *testing.T) {
			m := newInStatus(t, from)
			if err := m.Activate(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("激活错误 = %v", err)
			}
			if err := m.Suspend(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("暂停错误 = %v", err)
			}
			if err := m.Leave(fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("离开错误 = %v", err)
			}
			if err := m.Reinvite(uuid.New(), fixedNow); !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("重新邀请错误 = %v", err)
			}
		})
	}
}

// Test状态转换_nil 接收者返回错误而不 panic
//
// 空实体是调用方 bug，必须归到 domain.ErrNilEntity：
// 一旦归到 domain.ErrInvalidState，上层按状态类返回 409
// 就会把程序缺陷伪装成一次正常的业务冲突。
func Test状态转换_nil接收者返回错误而不panic(t *testing.T) {
	var m *Membership

	cases := map[string]error{
		"Activate": m.Activate(fixedNow),
		"Suspend":  m.Suspend(fixedNow),
		"Leave":    m.Leave(fixedNow),
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, ErrNilMembership) {
				t.Fatalf("%s 的错误 = %v，期望 ErrNilMembership", name, err)
			}
			if !errors.Is(err, domain.ErrNilEntity) {
				t.Fatalf("%s 的错误未归类到 domain.ErrNilEntity", name)
			}
			if errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("%s 的错误不应归类到 domain.ErrInvalidState", name)
			}
		})
	}
}

// TestReinvite_离开后可重新邀请
//
// 这是"终态与唯一键冲突"的修复点：(tenant_id, user_id) 全表唯一，
// 用户离开后必须复用同一行回到 invited，而不是再插一条新记录撞唯一约束。
func TestReinvite_离开后可重新邀请(t *testing.T) {
	m := newInStatus(t, StatusInvited)
	if err := m.Activate(fixedNow); err != nil {
		t.Fatalf("激活失败：%v", err)
	}
	if err := m.Leave(fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("离开失败：%v", err)
	}
	createdAt := m.CreatedAt

	inviter := uuid.New()
	if err := m.Reinvite(inviter, fixedNow.Add(2*time.Hour)); err != nil {
		t.Fatalf("重新邀请失败：%v", err)
	}

	if m.Status != StatusInvited {
		t.Fatalf("重新邀请后状态 = %q，期望 %q", m.Status, StatusInvited)
	}
	if m.InvitedBy != inviter.String() {
		t.Fatalf("InvitedBy = %q，期望 %q", m.InvitedBy, inviter.String())
	}
	if !m.InvitedAt.Equal(fixedNow.Add(2 * time.Hour)) {
		t.Fatalf("InvitedAt = %v，期望本次邀请时间", m.InvitedAt)
	}
	// 上一周期的时间戳必须清空，否则状态与时间戳会漂移。
	if !m.JoinedAt.IsZero() {
		t.Fatalf("重新邀请后 JoinedAt = %v，期望清零", m.JoinedAt)
	}
	if !m.SuspendedAt.IsZero() {
		t.Fatalf("重新邀请后 SuspendedAt = %v，期望清零", m.SuspendedAt)
	}
	if !m.LeftAt.IsZero() {
		t.Fatalf("重新邀请后 LeftAt = %v，期望清零", m.LeftAt)
	}
	// 复用同一行：主键与创建时间不变，UpdatedAt 仍由数据库触发器维护。
	if !m.CreatedAt.Equal(createdAt) {
		t.Fatalf("重新邀请不应改写 CreatedAt：%v → %v", createdAt, m.CreatedAt)
	}
}

// Test离开后重新邀请可再次激活
//
// 锁定"离开 → 重新邀请 → 激活"这条完整路径：left 不再是死路。
func Test离开后重新邀请可再次激活(t *testing.T) {
	m := newInStatus(t, StatusInvited)
	if err := m.Activate(fixedNow); err != nil {
		t.Fatalf("激活失败：%v", err)
	}
	if err := m.Leave(fixedNow.Add(time.Hour)); err != nil {
		t.Fatalf("离开失败：%v", err)
	}
	if err := m.Reinvite(uuid.New(), fixedNow.Add(2*time.Hour)); err != nil {
		t.Fatalf("重新邀请失败：%v", err)
	}

	joinAt := fixedNow.Add(3 * time.Hour)
	if err := m.Activate(joinAt); err != nil {
		t.Fatalf("再次激活失败：%v", err)
	}
	if m.Status != StatusActive {
		t.Fatalf("再次激活后状态 = %q，期望 %q", m.Status, StatusActive)
	}
	if !m.JoinedAt.Equal(joinAt) {
		t.Fatalf("JoinedAt = %v，期望本次加入时间 %v", m.JoinedAt, joinAt)
	}
}

func TestReinviteBySystem_邀请人固定为系统标识(t *testing.T) {
	m := newInStatus(t, StatusLeft)
	if err := m.ReinviteBySystem(fixedNow); err != nil {
		t.Fatalf("系统重新邀请失败：%v", err)
	}
	if m.InvitedBy != domain.SystemCreator {
		t.Fatalf("InvitedBy = %q，期望 %q", m.InvitedBy, domain.SystemCreator)
	}
	if m.Status != StatusInvited {
		t.Fatalf("状态 = %q，期望 %q", m.Status, StatusInvited)
	}
}

// TestReinvite_仅已离开可重新邀请
//
// 其余状态走的是"已经在该租户里"，重新邀请没有意义，一律拒绝。
func TestReinvite_仅已离开可重新邀请(t *testing.T) {
	// left 是本方法唯一允许的来源状态，其余（含未知状态与空状态）一律拒绝。
	for _, from := range []Status{StatusInvited, StatusActive, StatusSuspended, Status("weird"), Status("")} {
		t.Run(statusName(from), func(t *testing.T) {
			m := newInStatus(t, from)
			err := m.Reinvite(uuid.New(), fixedNow)
			if !errors.Is(err, ErrInvalidStatusTransition) {
				t.Fatalf("错误 = %v，期望 ErrInvalidStatusTransition", err)
			}
			if !errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("错误 %v 未归类到 domain.ErrInvalidState", err)
			}
			if m.Status != from {
				t.Fatalf("失败后状态被改写为 %q，应保持 %q", m.Status, from)
			}
		})
	}
}

// TestReinvite_零值邀请人被拒绝
func TestReinvite_零值邀请人被拒绝(t *testing.T) {
	m := newInStatus(t, StatusLeft)
	if err := m.Reinvite(uuid.Nil(), fixedNow); !errors.Is(err, domain.ErrInvalidActor) {
		t.Fatalf("错误 = %v，期望 domain.ErrInvalidActor", err)
	}
	if m.Status != StatusLeft {
		t.Fatalf("失败后状态被改写为 %q，应保持 %q", m.Status, StatusLeft)
	}
}

// TestReinvite_nil接收者返回错误而不panic
//
// 与其余转换保持一致：空实体归 ErrNilMembership，不归类到 ErrInvalidState。
func TestReinvite_nil接收者返回错误而不panic(t *testing.T) {
	var m *Membership

	cases := map[string]error{
		"Reinvite":         m.Reinvite(uuid.New(), fixedNow),
		"ReinviteBySystem": m.ReinviteBySystem(fixedNow),
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, ErrNilMembership) {
				t.Fatalf("%s 的错误 = %v，期望 ErrNilMembership", name, err)
			}
			if errors.Is(err, domain.ErrInvalidState) {
				t.Fatalf("%s 的错误不应归类到 domain.ErrInvalidState", name)
			}
		})
	}
}
