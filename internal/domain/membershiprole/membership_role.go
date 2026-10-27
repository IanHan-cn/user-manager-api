// Package membershiprole 定义成员关系与角色之间的授权关联实体、状态机与持久化端口。
//
// 分层约束：本包属于 Domain，不得依赖 Gin、GORM、Redis 或 Casbin。
//
// 与 role_permissions 的区别：那是一张没有生命周期的纯关联表，
// 只有"整体替换"一种写入方式，因此不配独立聚合；
// 本表的每一次授权都要回答"谁授的、何时生效、何时失效、因何变更"，
// 有 pending / active / revoked / expired 四种状态与自己的转换规则，
// 因此单独成包。
package membershiprole

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// Status 表示一次角色授权的状态。
//
// 状态机（合法转换由 Approve / Revoke / Expire 三个方法守护，
// 非法转换一律返回 ErrInvalidStatusTransition）：
//
//	pending --Approve--> active      （审批通过）
//	pending --Revoke-->  revoked     （审批被拒）
//	pending --Expire-->  expired     （过期前仍未审批，由定时任务推进）
//	active  --Revoke-->  revoked
//	active  --Expire-->  expired     （到期，由定时任务推进）
//
// revoked 与 expired 都是终态，不能再转出：
// 一次授权的生命周期到此结束，需要再次授予同一角色时
// 由调用方新建一行（旧行保留为历史，见 Repository 端口注释的"重新分配"小节），
// 而不是把终态"复活"——那样会抹掉这次授权曾经存在过的痕迹。
//
// pending 的语义是"待审批"，与 effective_at 无关：
// 生效时间不参与等待，审批通过即生效。因此不存在"active 但尚未生效"的中间态，
// 鉴权侧只需判定 status，不必再比对当前时间与 effective_at。
type Status string

// 一次角色授权的全部状态取值。
const (
	StatusPending Status = "pending" // 待审批，尚未生效
	StatusActive  Status = "active"  // 已生效
	StatusRevoked Status = "revoked" // 已被人工撤销
	StatusExpired Status = "expired" // 已到期失效
)

// maxReasonLen 是变更原因的长度上限，与 migrations/000009 的
// chk_membership_roles_reason 保持一致：数据库那条 CHECK 是兜底而不是主防线，
// 真正的拒绝发生在构造与状态转换里。
const maxReasonLen = 512

// MembershipRole 表示一条成员关系被授予某个角色的一次授权。
//
// TenantID 是冗余列（见 migrations/000009 的表注释）：
// 它让租户条件下推到本表的索引，代价是与 memberships.tenant_id 没有外键兜底，
// 一致性由"所有写入都经过本包的构造函数与 Repository"保证。
//
// 四个业务时间戳各司其职，不得互相代替：
//   - AssignedAt：授权动作发生的时间，恒非空；
//   - EffectiveAt：开始生效的时间，恒非空；
//   - ExpiresAt：失效时间，零值表示长期有效；
//   - CreatedAt / UpdatedAt：行本身的创建与修改时间。
//
// UpdatedAt 由数据库触发器维护（见 migrations/000001），应用层不写它，
// 因此任何状态转换之后，内存实例里的 UpdatedAt 都是转换前的旧值：
// 不可用于乐观锁，也不可回写，需要最新值必须重新查询。
type MembershipRole struct {
	ID           string    // 主键 UUIDv7
	TenantID     string    // 所属租户 UUIDv7
	MembershipID string    // 被授权的成员关系 UUIDv7
	RoleID       string    // 授予的角色 UUIDv7
	AssignedBy   string    // 分配人的用户 ID（UUID 字符串）；系统分配时为 domain.SystemCreator
	AssignedAt   time.Time // 授权动作发生的时间
	EffectiveAt  time.Time // 生效时间
	ExpiresAt    time.Time // 失效时间，零值表示长期有效
	Status       Status    // 授权状态
	Reason       string    // 变更原因，由最近一次变更覆写；空串表示未填写
	CreatedAt    time.Time // 创建时间
	UpdatedAt    time.Time // 最后更新时间
}

// 租户标识、成员关系标识、角色标识、操作者标识的错误不定义在本包：
// 它们是跨聚合共用的概念，统一引用 domain.ErrInvalidTenantID、
// domain.ErrInvalidUserID、domain.ErrInvalidRoleID、domain.ErrInvalidActor。
// （MembershipID 引用的是 memberships 里的行，与"用户标识"同属被别处引用的词汇，
// 因此复用 domain.ErrInvalidUserID 的同类口径——见下方 ErrInvalidMembershipID。）

// ErrInvalidStatusTransition 表示当前状态不允许执行该状态转换。
//
// 归类到 domain.ErrInvalidState 而不是 ErrInvalidKey：
// 状态机拒绝的是"当下不能做这个操作"，入参本身并没有问题。
var ErrInvalidStatusTransition = fmt.Errorf("%w: 成员角色状态转换", domain.ErrInvalidState)

// ErrNotExpirable 表示对一条不该失效的授权调用了 Expire。
//
// 两种情形共用本错误，只在文案上区分：
//   - 该授权没有到期时间（ExpiresAt 为零值），永远不会到期；
//   - 到期时间尚未到达，现在还不能判它失效。
//
// 归到 domain.ErrInvalidState 而不是 ErrInvalidKey：
// 被拒绝的是"这次调用发生在错误的时机"，不是某个字段格式不合法。
var ErrNotExpirable = fmt.Errorf("%w: 该授权尚不可判为到期", domain.ErrInvalidState)

// ErrNilMembershipRole 表示在一个空授权上执行了操作。
//
// 既覆盖本包的状态转换方法，也覆盖适配层的写操作
// （Create / Update 收到 nil 实体）：两种情形是同一个缺陷
// ——该查没查到、或查到却没判空。
//
// 以 domain.ErrNilEntity 而不是 ErrInvalidStatusTransition 为基类：
// 空实体是调用方 bug，不是"状态不允许"。
// 归到状态类会让上层按 ErrInvalidState 返回 409，
// 把一次程序缺陷伪装成正常的业务冲突。
var ErrNilMembershipRole = fmt.Errorf("%w: 成员角色授权", domain.ErrNilEntity)

// ErrInvalidMembershipRoleRecord 表示写入的值被数据库的 CHECK 约束拒绝。
//
// 它是兜底而不是主防线：正常路径上几条 CHECK 在领域层都有对应校验
// （状态由三个转换方法守护、有效期与原因由构造函数校验），
// 走到这里说明两侧口径已经漂移，或调用方绕过构造函数直接改了字段。
//
// 归入 domain.ErrInvalidState：被拒绝的是"这批取值组合在一起不成立"
// （status 与 expires_at 不自洽、assigned_by 为空白等），
// 而不是某一个字段的格式不合法。
var ErrInvalidMembershipRoleRecord = fmt.Errorf("%w: 成员角色授权记录", domain.ErrInvalidState)

// 字段级错误：原因与有效期都是"取值不合法"，归到 domain.ErrInvalidKey，
// 与 ErrInvalidMembershipRoleRecord（组合不自洽，归 ErrInvalidState）分属两类。
var (
	// ErrInvalidReason 表示变更原因为纯空白或超过 maxReasonLen。
	ErrInvalidReason = fmt.Errorf("%w: 变更原因", domain.ErrInvalidKey)

	// ErrInvalidEffectiveRange 表示失效时间不晚于生效时间。
	// 等于或早于意味着这条授权从未成立过，与数据库的
	// chk_membership_roles_expires_after_effective 同一口径。
	ErrInvalidEffectiveRange = fmt.Errorf("%w: 授权有效期", domain.ErrInvalidKey)

	// ErrInvalidMembershipID 表示成员关系标识为零值。
	// 它引用的是 memberships 里的行，与本包自己无关，
	// 因此按"被别的聚合引用的标识"处理，以 domain.ErrInvalidKey 为基类。
	ErrInvalidMembershipID = fmt.Errorf("%w: 成员关系标识", domain.ErrInvalidKey)
)

// 写操作的结局错误：二者都以 domain 的基类包装，
// 由适配层按 Repository 端口的约定返回，
// 调用方既能按本聚合精确判定，也能按基类统一归类。
var (
	// ErrMembershipRoleExists 表示该成员关系已经持有一行未失效的同角色授权。
	// Create 撞上 idx_membership_roles_membership_role_active 时必须返回它，
	// 不得静默吞掉后返回 nil。
	// 已有的授权若是 revoked / expired，重新分配走 Create 新插一行，不撞本约束。
	ErrMembershipRoleExists = fmt.Errorf("%w: 该成员已持有该角色", domain.ErrAlreadyExists)

	// ErrMembershipRoleNotFound 表示要改动的授权不存在。
	// Update 写入不存在的行时返回它：未命中属于调用方 bug，
	// 静默成功会让上层误以为更新生效。
	// 查询类方法未命中按端口约定返回 (nil, nil)，不使用本错误。
	ErrMembershipRoleNotFound = fmt.Errorf("%w: 成员角色授权", domain.ErrNotFound)
)

// New 构造一条立即生效的角色授权，status 为 active。
//
// tenantID、membershipID、roleID、assignedBy 必须是有效的 UUID，不接受 uuid.Nil()：
// 四者都引用本系统已存在的实体，非法 ID 应当在调用方解析入参时就失败，
// 而不是一路带到写入数据库才发现。实体字段仍存 UUIDv7 字符串，
// 与 membership.New 的既有写法保持一致。
//
// expiresAt 为零值表示长期有效；非空时必须晚于生效时间（即此刻），
// 否则返回 ErrInvalidEffectiveRange。
// reason 是可选的分配理由：去空白后为空即留空，超过 maxReasonLen 返回 ErrInvalidReason。
func New(tenantID, membershipID, roleID, assignedBy uuid.UUID, expiresAt time.Time, reason string) (*MembershipRole, error) {
	if assignedBy == uuid.Nil() {
		return nil, domain.ErrInvalidActor
	}
	return newMembershipRole(tenantID, membershipID, roleID, assignedBy.String(), StatusActive, expiresAt, reason)
}

// NewPending 构造一条待审批的角色授权，status 为 pending。
//
// 与 New 的唯一差别是初始状态：pending 需经 Approve 才生效，
// 在此之前不参与鉴权（鉴权侧只认 active）。其余入参校验见 New。
func NewPending(tenantID, membershipID, roleID, assignedBy uuid.UUID, expiresAt time.Time, reason string) (*MembershipRole, error) {
	if assignedBy == uuid.Nil() {
		return nil, domain.ErrInvalidActor
	}
	return newMembershipRole(tenantID, membershipID, roleID, assignedBy.String(), StatusPending, expiresAt, reason)
}

// NewSystem 以系统身份构造一条立即生效的角色授权，
// 分配人固定记为 domain.SystemCreator（"system"）——它不是 UUID，
// 因此走本构造函数而不是 New。
//
// 用于系统初始化（例如把 owner 角色授予第一个成员）等没有自然人分配人的场景。
func NewSystem(tenantID, membershipID, roleID uuid.UUID, expiresAt time.Time, reason string) (*MembershipRole, error) {
	return newMembershipRole(tenantID, membershipID, roleID, domain.SystemCreator, StatusActive, expiresAt, reason)
}

// NewSystemPending 以系统身份构造一条待审批的角色授权，
// 分配人固定记为 domain.SystemCreator。其余语义见 NewPending 与 NewSystem。
func NewSystemPending(tenantID, membershipID, roleID uuid.UUID, expiresAt time.Time, reason string) (*MembershipRole, error) {
	return newMembershipRole(tenantID, membershipID, roleID, domain.SystemCreator, StatusPending, expiresAt, reason)
}

// newMembershipRole 校验必填字段并生成初始实体，供上面四个构造函数共用。
//
// assignedBy 保持字符串：它既可能是自然人的 UUID 字符串，
// 也可能是 domain.SystemCreator 这类系统伪标识，无法收窄为 uuid.UUID。
//
// now 是生效时间：当前不提供预约未来生效的入口
// （理由见 Status 类型注释），因此它同时也是分配时间。
func newMembershipRole(
	tenantID, membershipID, roleID uuid.UUID,
	assignedBy string,
	status Status,
	expiresAt time.Time,
	reason string,
) (*MembershipRole, error) {
	if tenantID == uuid.Nil() {
		return nil, domain.ErrInvalidTenantID
	}
	if membershipID == uuid.Nil() {
		return nil, ErrInvalidMembershipID
	}
	if roleID == uuid.Nil() {
		return nil, domain.ErrInvalidRoleID
	}
	if assignedBy == "" {
		return nil, domain.ErrInvalidActor
	}

	now := time.Now()
	if !expiresAt.IsZero() && !expiresAt.After(now) {
		return nil, fmt.Errorf("失效时间 %v 不晚于生效时间 %v：%w", expiresAt, now, ErrInvalidEffectiveRange)
	}

	normalizedReason, err := normalizeReason(reason, false)
	if err != nil {
		return nil, err
	}

	return &MembershipRole{
		ID:           uuid.NewV7().String(),
		TenantID:     tenantID.String(),
		MembershipID: membershipID.String(),
		RoleID:       roleID.String(),
		AssignedBy:   assignedBy,
		AssignedAt:   now,
		EffectiveAt:  now,
		ExpiresAt:    expiresAt,
		Status:       status,
		Reason:       normalizedReason,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// normalizeReason 归一化变更原因：去两侧空白并校验长度上限。
//
// required 为 true 时拒绝空原因：撤销是敏感操作，
// 没有理由的撤销会让事后审计无从下手，因此 Revoke 走这一分支，
// 而分配理由允许留空（分配本身由 assigned_by / assigned_at 追溯）。
//
// 去空白而不只是判空串：纯空白的原因落到库里会撞上
// chk_membership_roles_reason（btrim(reason) <> ”），
// 与其等着撞约束，不如在入口就当成"没填"。
//
// 上限按字符数（rune）计而不是字节数：数据库的 length(reason) 数的是字符，
// 中文原因按字节算会让应用层与数据库差出三倍，
// 出现"应用层放行了、数据库却拒绝"的裂缝。
func normalizeReason(reason string, required bool) (string, error) {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		if required {
			return "", fmt.Errorf("%w: 不能为空", ErrInvalidReason)
		}
		return "", nil
	}
	if n := utf8.RuneCountInString(trimmed); n > maxReasonLen {
		return "", fmt.Errorf("%w: 长度 %d 超过上限 %d", ErrInvalidReason, n, maxReasonLen)
	}
	return trimmed, nil
}

// canTransition 校验一次状态转换是否被允许，不允许时返回包装了
// ErrInvalidStatusTransition 的错误。
//
// 三个转换方法统一走这里，收益有三：
//   - 每个方法允许的来源状态集中在一行里，不会出现某个方法漏掉终态检查；
//   - 空实体与非法状态在同一处判定：空实体返回 ErrNilMembershipRole，
//     不会被误归类到 ErrInvalidState（见 ErrNilMembershipRole 的注释）；
//   - 未知状态（含从数据库读到的空值）不在 allowed 内，一律拒绝（fail closed）。
func canTransition(r *MembershipRole, action string, allowed ...Status) error {
	if r == nil {
		return ErrNilMembershipRole
	}
	for _, s := range allowed {
		if r.Status == s {
			return nil
		}
	}
	return fmt.Errorf("授权当前状态 %q 不能%s：%w", r.Status, action, ErrInvalidStatusTransition)
}

// Approve 审批通过一条待生效的授权，状态转为 active。
//
// 允许的来源状态：仅 pending。active 不需要再次审批，
// revoked / expired 是终态，需要重新授予时请新建一行（见 Status 的注释）。
//
// now 由调用方给出，保证同一批业务操作使用同一时间基准，也便于测试。
// 它会写入 EffectiveAt 而不是只改状态：pending 期间 effective_at 只是
// 构造时刻的占位（该列 NOT NULL），审批通过的时刻才是这条授权真正开始生效的时刻。
//
// 审批时刻晚于到期时间时直接拒绝：此时该授权已经过期，
// 正确处置是走 Expire 而不是先批准再立刻失效。少了这一校验，
// 写入的值会撞上 chk_membership_roles_expires_after_effective。
//
// UpdatedAt 不在此处修改：updated_at 由数据库触发器统一维护
// （见 migrations/000001），避免应用层与数据库各写一份导致时间不一致。
// 代价是转换后内存实例的 UpdatedAt 是旧值，不可用于乐观锁或回写。
func (r *MembershipRole) Approve(now time.Time) error {
	if err := canTransition(r, "审批通过", StatusPending); err != nil {
		return err
	}
	if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		return fmt.Errorf("审批时刻 %v 不早于到期时间 %v：%w", now, r.ExpiresAt, ErrNotExpirable)
	}
	r.Status = StatusActive
	r.EffectiveAt = now
	return nil
}

// Revoke 人工撤销一条授权，状态转为 revoked。
//
// 允许的来源状态：pending、active。前者是审批被拒，后者是生效后被收回。
// reason 必须非空：撤销是敏感操作，理由用于事后审计，
// 空原因返回 ErrInvalidReason（见 normalizeReason 的注释）。
//
// reason 覆写原值而不是追加：本列承载"最近一次变更的原因"，
// 一次授权的分配理由在撤销之后已不再重要。
func (r *MembershipRole) Revoke(reason string) error {
	if err := canTransition(r, "撤销", StatusPending, StatusActive); err != nil {
		return err
	}
	normalized, err := normalizeReason(reason, true)
	if err != nil {
		return err
	}
	r.Status = StatusRevoked
	r.Reason = normalized
	return nil
}

// Expire 把一条到期的授权置为 expired，由定时任务推进。
//
// 允许的来源状态：pending、active。
// 前者是"过期前仍未审批"，后者是"用到了期"。
//
// 两条前置校验缺一不可，否则一次越权的定时任务就能改坏数据：
//   - ExpiresAt 为零值（长期有效）时拒绝：这样的授权永远不会到期；
//   - now 早于 ExpiresAt 时拒绝：还没到期就判死等于提前收回权限。
//
// 两种情形都返回以 ErrNotExpirable 为基类的错误，只在文案上区分。
// now 由调用方给出，与其余转换保持一致；通常直接取 ListDueForExpiry 的同一个 now，
// 保证"扫出来的一批"与"逐个判死"用的是同一时刻。
func (r *MembershipRole) Expire(now time.Time) error {
	if err := canTransition(r, "判为到期", StatusPending, StatusActive); err != nil {
		return err
	}
	if r.ExpiresAt.IsZero() {
		return fmt.Errorf("该授权未设置到期时间：%w", ErrNotExpirable)
	}
	if now.Before(r.ExpiresAt) {
		return fmt.Errorf("到期时间 %v 尚未到达（当前 %v）：%w", r.ExpiresAt, now, ErrNotExpirable)
	}
	r.Status = StatusExpired
	return nil
}
