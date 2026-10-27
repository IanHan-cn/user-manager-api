// Package membership 定义用户与租户之间的成员关系实体、状态机与持久化端口。
//
// 分层约束：本包属于 Domain，不得依赖 Gin、GORM、Redis 或 Casbin。
// 成员关系只刻画业务侧的成员资格：身份的唯一来源仍是 ZITADEL，
// 本地不保存任何认证凭据，只保存成员关系本身。
package membership

import (
	"fmt"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// Status 表示成员关系的状态。
//
// 状态机（合法转换由 Activate / Suspend / Leave / Reinvite 四个方法守护，
// 非法转换一律返回 ErrInvalidStatusTransition）：
//
//	invited   --Activate-->  active
//	active    --Suspend-->  suspended
//	suspended --Activate-->  active     （恢复，保留首次加入时间）
//	invited / active / suspended --Leave--> left
//	left      --Reinvite--> invited     （重新邀请，回到初始状态）
//
// left 不能再通过 Activate / Suspend / Leave 转出，唯一出路是 Reinvite：
// 一条成员关系只刻画"某用户在某租户中的当前成员资格"，
// (tenant_id, user_id) 全局唯一（见 Repository 端口注释），
// 因此用户离开后再次加入必须复用同一行，而不是再插一条新记录去撞唯一约束。
type Status string

// 成员关系的全部状态取值。
const (
	StatusInvited   Status = "invited"   // 已邀请，尚未正式加入
	StatusActive    Status = "active"    // 正常成员
	StatusSuspended Status = "suspended" // 成员被暂停
	StatusLeft      Status = "left"      // 已离开租户
)

// Membership 表示用户与租户之间的成员关系。
//
// 一条记录只刻画"某用户在某租户中的成员资格"，因此 TenantID 与 UserID 共同决定
// 它的业务唯一性。Status 与业务时间戳（InvitedAt、JoinedAt、SuspendedAt、LeftAt）
// 描述同一件事的两种视角：状态由 Activate / Suspend / Leave 维护，
// 时间戳不得绕过这三个方法单独改写，否则两者会出现漂移。
//
// TenantID 与 UserID 是 UUIDv7 字符串，与 role.Role、tenant.Tenant 保持一致；
// 构造时以 uuid.UUID 类型的入参收窄，避免非法 ID 写进实体。
//
// UpdatedAt 由数据库触发器维护（见 migrations/000001），应用层不写它，
// 因此任何状态转换之后，内存实例里的 UpdatedAt 都是转换前的旧值：
// 不可用于乐观锁，也不可回写，需要最新值必须重新查询。
type Membership struct {
	ID          string    //	成员关系主键 UUIDv7
	TenantID    string    //	所属租户  UUIDv7
	UserID      string    //	对应的用户  UUIDv7
	Status      Status    //	成员关系状态
	InvitedBy   string    //	邀请人的用户 ID（UUID 字符串）；系统邀请时为 domain.SystemCreator
	InvitedAt   time.Time //	邀请时间
	JoinedAt    time.Time //	正式加入时间
	SuspendedAt time.Time //	成员被暂停的时间
	LeftAt      time.Time //	成员离开时间
	CreatedAt   time.Time //	关系创建时间
	UpdatedAt   time.Time //	最后更新时间
}

// 租户标识、用户标识、操作者标识（InvitedBy）的错误不定义在本包：
// 它们是跨聚合共用的概念，统一引用 domain.ErrInvalidTenantID、
// domain.ErrInvalidUserID、domain.ErrInvalidActor。

// ErrInvalidStatusTransition 表示当前状态不允许执行该状态转换。
//
// 归类到 domain.ErrInvalidState 而不是 ErrInvalidKey：
// 状态机拒绝的是"当下不能做这个操作"，入参本身并没有问题。
var ErrInvalidStatusTransition = fmt.Errorf("%w: 成员关系状态转换", domain.ErrInvalidState)

// ErrNilMembership 表示在一个空成员关系上执行了操作。
//
// 既覆盖本包的状态转换方法，也覆盖适配层的写操作
// （Create / Update 收到 nil 实体）：两种情形是同一个缺陷
// ——该查没查到、或查到却没判空。
//
// 以 domain.ErrNilEntity 而不是 ErrInvalidStatusTransition 为基类：
// 空实体是调用方 bug，不是"状态不允许"。
// 归到状态类会让上层按 ErrInvalidState 返回 409，
// 把一次程序缺陷伪装成正常的业务冲突。
var ErrNilMembership = fmt.Errorf("%w: 成员关系", domain.ErrNilEntity)

// ErrInvalidMembershipRecord 表示写入的值被数据库的 CHECK 约束拒绝。
//
// 它是兜底而不是主防线：正常路径上两条 CHECK 在领域层都有对应校验
// （状态转换由四个方法守护、邀请人由构造函数校验），
// 走到这里说明两侧口径已经漂移，或调用方绕过构造函数直接改了字段。
//
// 归入 domain.ErrInvalidState 而不是 ErrInvalidKey：被拒绝的是
// chk_memberships_status_timestamps，即"状态与业务时间戳不自洽"，
// 是当下这批取值组合不成立，而不是某一个字段格式不合法。
var ErrInvalidMembershipRecord = fmt.Errorf("%w: 成员关系记录", domain.ErrInvalidState)

// 写操作的结局错误：二者都以 domain 的基类包装，
// 由适配层按 Repository 端口的约定返回，
// 调用方既能按本聚合精确判定，也能按基类统一归类。
var (
	// ErrMembershipExists 表示该用户已经有一条本租户的成员关系。
	// Create 撞上 (tenant_id, user_id) 唯一约束时必须返回它，
	// 不得静默吞掉后返回 nil。
	// 用户离开后想再次加入，走 Reinvite 复用同一行，不要再次 Create。
	ErrMembershipExists = fmt.Errorf("%w: 该用户已是该租户成员", domain.ErrAlreadyExists)

	// ErrMembershipNotFound 表示要改动的成员关系不存在。
	// Update 写入不存在的行时返回它：未命中属于调用方 bug，
	// 静默成功会让上层误以为更新生效。
	// 查询类方法未命中按端口约定返回 (nil, nil)，不使用本错误。
	ErrMembershipNotFound = fmt.Errorf("%w: 成员关系", domain.ErrNotFound)
)

// New 构造一条成员关系。
//
// tenantID、userID、invitedBy 必须是有效的 UUID，不接受 uuid.Nil()：
// 三者都引用本系统已存在的实体，非法 ID 应当在调用方解析入参时就失败，
// 而不是一路带到写入数据库才发现。实体字段仍存 UUIDv7 字符串，
// 与 role.NewCustomRole 的既有写法保持一致。
//
// 新建成员关系处于 invited 状态，JoinedAt 为零值；
// 正式加入由调用方随后调用 Activate 完成，构造函数不隐式激活。
//
// 同一 (tenant_id, user_id) 只允许存在一条成员关系：
// 若该用户在本租户已有记录（哪怕已经 left），不得再调用本构造函数新建，
// 否则 Create 会撞上唯一约束并返回 ErrMembershipExists；
// 重新加入走 Reinvite 复用同一行。
func New(tenantID, userID, invitedBy uuid.UUID) (*Membership, error) {
	if invitedBy == uuid.Nil() {
		return nil, domain.ErrInvalidActor
	}
	return newMembership(tenantID, userID, invitedBy.String())
}

// NewSystem 以系统身份构造一条成员关系，tenantID、userID 不接受 uuid.Nil()。
//
// 用于系统初始化、数据修复等没有自然人邀请人的场景，
// 邀请人固定记为 domain.SystemCreator（"system"）——它不是 UUID，
// 因此走本构造函数而不是 New。
func NewSystem(tenantID, userID uuid.UUID) (*Membership, error) {
	return newMembership(tenantID, userID, domain.SystemCreator)
}

// newMembership 校验必填字段并生成初始实体，供上面两个构造函数共用。
//
// invitedBy 保持字符串：它既可能是自然人的 UUID 字符串，
// 也可能是 domain.SystemCreator 这类系统伪标识，无法收窄为 uuid.UUID。
func newMembership(tenantID, userID uuid.UUID, invitedBy string) (*Membership, error) {
	if tenantID == uuid.Nil() {
		return nil, domain.ErrInvalidTenantID
	}
	if userID == uuid.Nil() {
		return nil, domain.ErrInvalidUserID
	}
	if invitedBy == "" {
		return nil, domain.ErrInvalidActor
	}

	now := time.Now()
	return &Membership{
		ID:        uuid.NewV7().String(),
		TenantID:  tenantID.String(),
		UserID:    userID.String(),
		Status:    StatusInvited,
		InvitedBy: invitedBy,
		InvitedAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// canTransition 校验一次状态转换是否被允许，不允许时返回包装了
// ErrInvalidStatusTransition 的错误。
//
// 四个转换方法统一走这里，收益有三：
//   - 每个方法允许的来源状态集中在一行里，不会出现某个方法漏掉终态检查；
//   - 空实体与非法状态在同一处判定，空实体返回 ErrNilMembership，
//     不会被误归类到 ErrInvalidState（见 ErrNilMembership 的注释）；
//   - 未知状态（含从数据库读到的空值）不在 allowed 内，一律拒绝（fail closed）。
func canTransition(m *Membership, action string, allowed ...Status) error {
	if m == nil {
		return ErrNilMembership
	}
	for _, s := range allowed {
		if m.Status == s {
			return nil
		}
	}
	return fmt.Errorf("成员关系当前状态 %q 不能%s：%w", m.Status, action, ErrInvalidStatusTransition)
}

// Activate 将成员关系置为已加入状态。
//
// 允许的来源状态：
//   - invited：首次正式加入，写入 JoinedAt；
//   - suspended：暂停后恢复，保留首次加入时间，并清空 SuspendedAt。
//
// now 由调用方给出，保证同一批业务操作使用同一时间基准，也便于测试。
// UpdatedAt 不在此处修改：updated_at 由数据库触发器统一维护（见 migrations/000001），
// 避免应用层与数据库各写一份导致时间不一致。
// 代价是转换后内存实例的 UpdatedAt 是旧值，不可用于乐观锁或回写。
func (m *Membership) Activate(now time.Time) error {
	if err := canTransition(m, "激活", StatusInvited, StatusSuspended); err != nil {
		return err
	}

	// JoinedAt 记录首次正式加入时间，恢复成员身份时不覆盖。
	if m.JoinedAt.IsZero() {
		m.JoinedAt = now
	}
	m.SuspendedAt = time.Time{}
	m.Status = StatusActive
	return nil
}

// Suspend 将成员关系置为暂停状态。
//
// 允许的来源状态：active。暂停时写入 SuspendedAt，
// 恢复时由 Activate 清空，保证 SuspendedAt 非零当且仅当当前状态为 suspended。
func (m *Membership) Suspend(now time.Time) error {
	if err := canTransition(m, "暂停", StatusActive); err != nil {
		return err
	}
	m.SuspendedAt = now
	m.Status = StatusSuspended
	return nil
}

// Leave 将成员关系置为已离开状态。
//
// 允许的来源状态：invited、active、suspended。离开时写入 LeftAt。
// left 是终态，不能再转出，因此对 left 再次调用本方法会返回错误。
func (m *Membership) Leave(now time.Time) error {
	if err := canTransition(m, "离开", StatusInvited, StatusActive, StatusSuspended); err != nil {
		return err
	}
	m.LeftAt = now
	m.Status = StatusLeft
	return nil
}

// Reinvite 重新邀请已离开的成员，成员关系回到 invited 状态。
//
// 允许的来源状态：仅 left。left 不能再通过 Activate / Suspend / Leave 转出，
// 本方法是它唯一的出路：成员关系只刻画"当前成员资格"，(tenant_id, user_id)
// 全局唯一，因此重新邀请必须复用同一行——先由调用方取回原记录
// （GetByTenantAndUser）再走本方法，而不是再插一条新记录去撞唯一约束。
//
// 重新邀请视为一次全新的成员周期：清空 JoinedAt、SuspendedAt、LeftAt，
// 并把 InvitedAt、InvitedBy 更新为本次邀请的时间与邀请人，不保留上一周期的痕迹。
// CreatedAt 保持原值，UpdatedAt 由数据库触发器维护，本方法不改写。
// now 由调用方给出，与其余转换保持一致。
func (m *Membership) Reinvite(invitedBy uuid.UUID, now time.Time) error {
	if invitedBy == uuid.Nil() {
		return domain.ErrInvalidActor
	}
	return m.reinvite(invitedBy.String(), now)
}

// ReinviteBySystem 以系统身份重新邀请已离开的成员，
// 邀请人固定记为 domain.SystemCreator（"system"）——它不是 UUID，
// 因此走本方法而不是 Reinvite。其余语义见 Reinvite。
func (m *Membership) ReinviteBySystem(now time.Time) error {
	return m.reinvite(domain.SystemCreator, now)
}

// reinvite 执行重新邀请，供上面两个方法共用。
// invitedBy 保持字符串：可能是自然人 UUID，也可能是 domain.SystemCreator。
func (m *Membership) reinvite(invitedBy string, now time.Time) error {
	if err := canTransition(m, "重新邀请", StatusLeft); err != nil {
		return err
	}
	if invitedBy == "" {
		return domain.ErrInvalidActor
	}

	m.Status = StatusInvited
	m.InvitedBy = invitedBy
	m.InvitedAt = now
	m.JoinedAt = time.Time{}
	m.SuspendedAt = time.Time{}
	m.LeftAt = time.Time{}
	return nil
}
