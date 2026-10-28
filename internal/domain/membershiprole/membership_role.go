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
// 鉴权侧不必再比对当前时间与 effective_at。
//
// 但"只看状态"只对 effective_at 成立，对 expires_at 不成立：
// expired 只能由定时任务经 ListDueForExpiry + Expire 推进，
// 任务没跑到之前，一条已经过了 expires_at 的 active 行在库里仍然是 active。
// 因此"尚未失效"必须同时看两个维度——状态属于 pending / active，
// 且尚未到期（expires_at 为空或晚于判定时刻）。
// 这一判定由 Repository 的查询方法承担，见 repository.go 的"失效判定"小节。
type Status string

// 一次角色授权的全部状态取值。
const (
	StatusPending Status = "pending" // 待审批，尚未生效
	StatusActive  Status = "active"  // 已生效
	StatusRevoked Status = "revoked" // 已被人工撤销
	StatusExpired Status = "expired" // 已到期失效
)

// Valid 报告状态取值是否在受控词表内。
//
// 从数据库读出的行也要走这条判定：未知状态一律判为非法（fail closed）。
// 最现实的来源是滚动发布——新版本写入了一个旧版本还不认识的状态，
// 旧版本读到它时宁可报错，也不能把它当成"未失效"继续参与鉴权。
//
// 与 user.UserStatus.Valid 同形：受控词表的封闭性判定不放在调用方，
// 而是跟着类型走，任何一处漏判都不会悄悄放行未知取值。
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusActive, StatusRevoked, StatusExpired:
		return true
	default:
		return false
	}
}

// Terminal 报告状态是否为终态：终态行不能再转出，也不该被改写。
//
// 适配层的 Update 用它把终态排除在更新前提之外：
// 少了这一道，读入一条 revoked 的行、把导出字段 Status 改成 active
// 就能直接落库——一次已撤销的授权被静默复活，
// 而状态机（canTransition）只守护 Approve / Revoke / Expire 三个方法，
// 完全管不到"裸改字段再 Update"这条路径。
//
// 未知状态一并判为终态（fail closed）：宁可拒绝一次更新，
// 也不能放过一个不认识的状态。
func (s Status) Terminal() bool {
	if !s.Valid() {
		return true
	}
	return s == StatusRevoked || s == StatusExpired
}

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
// 三个业务时间戳与两个行级时间戳各司其职，不得互相代替：
//   - AssignedAt：授权动作发生的时间，恒非空；
//   - EffectiveAt：开始生效的时间，恒非空；
//   - ExpiresAt：失效时间，零值表示长期有效；
//   - CreatedAt：行的创建时间，恒非空；
//   - UpdatedAt：行的最后修改时间，由数据库触发器维护（见下）。
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
	Version      int       // 乐观锁版本号
}

// 本包只保留"只属于成员角色授权"的错误：状态取值、状态转换、原因、
// 有效期与写操作结局。
//
// 引用别的聚合的标识一律不定义在本包，统一引用 domain 里的跨聚合定义：
// 租户标识用 domain.ErrInvalidTenantID、角色标识用 domain.ErrInvalidRoleID、
// 操作者用 domain.ErrInvalidActor、成员关系标识用 domain.ErrInvalidMembershipID。
// 理由与 domain.ErrInvalidPermissionID 被 role 聚合引用时一样：
// 被别的聚合引用的词汇属于跨聚合概念，留在持有者的包里会逼着引用方
// 反向依赖持有者（见 domain/error.go 的"归属口径"一段）。

// ErrInvalidStatusTransition 表示当前状态不允许执行该状态转换。
//
// 归类到 domain.ErrInvalidState 而不是 ErrInvalidKey：
// 状态机拒绝的是"当下不能做这个操作"，入参本身并没有问题。
var ErrInvalidStatusTransition = fmt.Errorf("%w: 成员角色状态转换", domain.ErrInvalidState)

// ErrInvalidStatusValue 表示授权状态不在受控词表内。
//
// 与 ErrInvalidStatusTransition 是两回事：
// 后者是"取值合法，但这一步转换不被允许"（状态机问题，归 ErrInvalidState）；
// 前者是"取值本身就不认识"（字段问题，归 ErrInvalidKey）。
// 分开的理由是处置方式不同：前者要修数据来源，后者只是操作时机不对。
var ErrInvalidStatusValue = fmt.Errorf("%w: 授权状态取值", domain.ErrInvalidKey)

// Expire 的两条前置校验各有各的含义，因此分成两个错误而不是共用同一个：
// 共用时上层只能看到"不能判为到期"，分不清是该改查询条件还是该再等等。
var (
	// ErrNoExpiry 表示对一条长期有效的授权调用了 Expire。
	//
	// 没有到期时间的授权永远不会到期，扫出它的查询条件写错了——
	// 这是调用方的前提错误，越权的定时任务就是这么改坏数据的。
	ErrNoExpiry = fmt.Errorf("%w: 该授权没有到期时间", domain.ErrInvalidState)

	// ErrNotYetExpired 表示到期时间尚未到达。
	//
	// 正常的"时机未到"：再等等就能判了，不是错误的前提。
	ErrNotYetExpired = fmt.Errorf("%w: 到期时间尚未到达", domain.ErrInvalidState)

	// ErrAlreadyExpired 表示审批时刻已晚于到期时间，该授权早已过期。
	//
	// 方向与上面两个相反：不是"还不能判"，而是"早该判了"。
	// 正确处置是走 Expire，而不是先批准再让它立刻失效。
	ErrAlreadyExpired = fmt.Errorf("%w: 该授权已过期", domain.ErrInvalidState)
)

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

// ErrInvalidMembershipRoleRecord 表示一批取值组合在一起不成立。
//
// 它是兜底而不是主防线：migrations/000009 的五条 CHECK 在领域层都有对应校验——
// status 取值由 Status.Valid 与 Validate 守护，assigned_by 非空白、
// reason 非空白且不超过 maxReasonLen 由 normalizeReason 与 Validate 守护，
// expires_at 晚于 effective_at 由 Validate 守护，
// expired 必须带到期时间由 Validate 守护。
// 走到这里说明两侧口径已经漂移，或调用方绕过了构造函数直接改了字段。
//
// 归入 domain.ErrInvalidState：被拒绝的是"这批取值组合在一起不成立"，
// 而不是某一个字段的格式不合法——后者归 ErrInvalidKey 那一类。
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

	// ErrInvalidMembershipRoleID 表示授权自身的主键标识为空或非法。
	//
	// 归 domain.ErrInvalidKey 而不是记录级错误：
	// 主键格式非法是"某个字段的取值不合法"，
	// 而记录级错误（ErrInvalidMembershipRoleRecord）说的是
	// "这批取值组合在一起不成立"（状态与时间戳不自洽等）。
	// 混为一谈会让客户端把一次入参错误当成状态冲突去重试，
	// 而不是当成 400 直接拒绝。
	ErrInvalidMembershipRoleID = fmt.Errorf("%w: 授权标识", domain.ErrInvalidKey)
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

	// ErrMembershipRoleConflict 表示该授权在并发修改时已被其他人更新（乐观锁冲突）。
	// 适配层应当将其翻译为 HTTP 409 Conflict，防止静默覆盖。
	ErrMembershipRoleConflict = fmt.Errorf("%w: 成员角色授权已被其他人修改", domain.ErrConflict)
)

// pgTimePrecision 是 PostgreSQL TIMESTAMPTZ 的时间精度（微秒）。
const pgTimePrecision = time.Microsecond

// alignToPG 把时间对齐到数据库的精度；零值原样返回。
//
// 为什么必须对齐：Go 的 time.Time 是纳秒精度，写进 TIMESTAMPTZ 会被截断到微秒。
// 若领域层在纳秒粒度上判定"失效时间晚于生效时间"并放行，
// 落库后两者可能被截断成同一微秒，撞上
// chk_membership_roles_expires_after_effective——
// 一次本该在入口拦下的入参错误会变成一次数据库拒绝。
// 对齐之后，领域层的判定与数据库的判定用的是同一批值，裂缝不存在。
//
// Truncate 顺带剥离单调时钟分量，只留壁钟时间：
// 落库再读回的值因此与内存里的值可用 Before / After 直接比较。
func alignToPG(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.Truncate(pgTimePrecision)
}

// New 构造一条立即生效的角色授权，status 为 active。
//
// tenantID、membershipID、roleID、assignedBy 必须是有效的 UUID，不接受 uuid.Nil()：
// 四者都引用本系统已存在的实体，非法 ID 应当在调用方解析入参时就失败，
// 而不是一路带到写入数据库才发现。实体字段仍存 UUIDv7 字符串，
// 与 membership.New 的既有写法保持一致。
//
// now 是授权动作发生的时刻，由调用方给出：
// 与 Approve(now) / Expire(now) 同一口径——同一批业务操作应当使用同一时间基准，
// 也便于测试构造"刚好到期""生效瞬间"这类边界。
// 它不提供预约未来生效的入口（理由见 Status 类型注释），
// 因此 now 同时就是生效时间。
//
// expiresAt 为零值表示长期有效；非空时必须晚于 now，
// 否则返回 ErrInvalidEffectiveRange。
// reason 是可选的分配理由：去空白后为空即留空，超过 maxReasonLen 返回 ErrInvalidReason。
func New(
	tenantID, membershipID, roleID, assignedBy uuid.UUID,
	now, expiresAt time.Time, reason string,
) (*MembershipRole, error) {
	if assignedBy == uuid.Nil() {
		return nil, domain.ErrInvalidActor
	}
	return newMembershipRole(tenantID, membershipID, roleID, assignedBy.String(), StatusActive, now, expiresAt, reason)
}

// NewPending 构造一条待审批的角色授权，status 为 pending。
//
// 与 New 的唯一差别是初始状态：pending 需经 Approve 才生效，
// 在此之前不参与鉴权（鉴权侧只认 active）。其余入参校验见 New。
func NewPending(
	tenantID, membershipID, roleID, assignedBy uuid.UUID,
	now, expiresAt time.Time, reason string,
) (*MembershipRole, error) {
	if assignedBy == uuid.Nil() {
		return nil, domain.ErrInvalidActor
	}
	return newMembershipRole(tenantID, membershipID, roleID, assignedBy.String(), StatusPending, now, expiresAt, reason)
}

// NewSystem 以系统身份构造一条立即生效的角色授权，
// 分配人固定记为 domain.SystemCreator（"system"）——它不是 UUID，
// 因此走本构造函数而不是 New。
//
// 用于系统初始化（例如把 owner 角色授予第一个成员）等没有自然人分配人的场景。
func NewSystem(
	tenantID, membershipID, roleID uuid.UUID,
	now, expiresAt time.Time, reason string,
) (*MembershipRole, error) {
	return newMembershipRole(tenantID, membershipID, roleID, domain.SystemCreator, StatusActive, now, expiresAt, reason)
}

// NewSystemPending 以系统身份构造一条待审批的角色授权，
// 分配人固定记为 domain.SystemCreator。其余语义见 NewPending 与 NewSystem。
func NewSystemPending(
	tenantID, membershipID, roleID uuid.UUID,
	now, expiresAt time.Time, reason string,
) (*MembershipRole, error) {
	return newMembershipRole(tenantID, membershipID, roleID, domain.SystemCreator, StatusPending, now, expiresAt, reason)
}

// newMembershipRole 校验必填字段并生成初始实体，供上面四个构造函数共用。
//
// assignedBy 保持字符串：它既可能是自然人的 UUID 字符串，
// 也可能是 domain.SystemCreator 这类系统伪标识，无法收窄为 uuid.UUID。
func newMembershipRole(
	tenantID, membershipID, roleID uuid.UUID,
	assignedBy string,
	status Status,
	now, expiresAt time.Time, reason string,
) (*MembershipRole, error) {
	if tenantID == uuid.Nil() {
		return nil, domain.ErrInvalidTenantID
	}
	if membershipID == uuid.Nil() {
		return nil, domain.ErrInvalidMembershipID
	}
	if roleID == uuid.Nil() {
		return nil, domain.ErrInvalidRoleID
	}
	// 去空白而不只是判空串：纯空白的分配人落到库里会撞上
	// chk_membership_roles_assigned_by_not_blank（要求去空白后不为空）。
	if strings.TrimSpace(assignedBy) == "" {
		return nil, domain.ErrInvalidActor
	}

	// 两个时刻都对齐到数据库精度后再比较，否则纳秒级的余量
	// 会在落库时被截断掉，判定与 CHECK 得出不同结论（见 alignToPG）。
	now = alignToPG(now)
	expiresAt = alignToPG(expiresAt)
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
		Version:      1, // 初始版本号为 1
	}, nil
}

// normalizeReason 归一化变更原因：去两侧空白并校验长度上限。
//
// required 为 true 时拒绝空原因：撤销是敏感操作，
// 没有理由的撤销会让事后审计无从下手，因此 Revoke 走这一分支，
// 而分配理由允许留空（分配本身由 assigned_by / assigned_at 追溯）。
//
// 去空白而不只是判空串：纯空白的原因落到库里会撞上
// chk_membership_roles_reason（要求去空白后不为空），
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
// 审批时刻不早于到期时间时直接拒绝：此时该授权已经过期，
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
	now = alignToPG(now)
	if !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt) {
		return fmt.Errorf("审批时刻 %v 不早于到期时间 %v：%w", now, r.ExpiresAt, ErrAlreadyExpired)
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
//   - ExpiresAt 为零值（长期有效）时返回 ErrNoExpiry：
//     这样的授权永远不会到期，扫出它说明查询条件写错了；
//   - now 早于 ExpiresAt 时返回 ErrNotYetExpired：
//     还没到期就判死等于提前收回权限。
//
// 两者分开而不是共用同一个错误：上层据此能分清
// "该改查询条件"与"该再等一会儿"，排障方向不同。
//
// now 由调用方给出，与其余转换保持一致；通常直接取 ListDueForExpiry 的同一个 now，
// 保证"扫出来的一批"与"逐个判死"用的是同一时刻。
func (r *MembershipRole) Expire(now time.Time) error {
	if err := canTransition(r, "判为到期", StatusPending, StatusActive); err != nil {
		return err
	}
	now = alignToPG(now)
	if r.ExpiresAt.IsZero() {
		return ErrNoExpiry
	}
	if now.Before(r.ExpiresAt) {
		return fmt.Errorf("到期时间 %v 尚未到达（当前 %v）：%w", r.ExpiresAt, now, ErrNotYetExpired)
	}
	r.Status = StatusExpired
	return nil
}

// Validate 校验实体的取值是否自洽，供 Repository 实现方在写入前调用。
//
// 为什么需要它：实体的字段是导出的，调用方可以绕过构造函数直接改写它们，
// 因此"由构造函数构出来的"不等于"写出去的一定合法"。
// 少了这一道，非法取值只能等数据库 CHECK 兜底，
// 而 CHECK 拒绝时上层拿到的是记录级错误，本该在入口拦下的入参问题
// 被当成了一次服务端冲突。
//
// 校验项与 migrations/000009 的五条 CHECK 一一对应
// （状态取值、分配人非空白、原因非空白且不超长、失效晚于生效、expired 必带到期时间），
// 另加标识的 UUID 合法性与行级必填项——那几项是 CHECK 拦不住的：
// 标识非法时列类型会直接报错，上层拿到的是驱动原生错误而不是可归类的 4xx。
//
// 它不校验跨聚合的引用完整性（例如"成员关系是否真属于本租户"）：
// 那需要读别的聚合的表，归适配层在写路径上校验。
func (r *MembershipRole) Validate() error {
	if r == nil {
		return ErrNilMembershipRole
	}

	// 四个标识：必须是非零的合法 UUID。
	if err := requireValidID("id", r.ID, ErrInvalidMembershipRoleID); err != nil {
		return err
	}
	if err := requireValidID("tenant_id", r.TenantID, domain.ErrInvalidTenantID); err != nil {
		return err
	}
	if err := requireValidID("membership_id", r.MembershipID, domain.ErrInvalidMembershipID); err != nil {
		return err
	}
	if err := requireValidID("role_id", r.RoleID, domain.ErrInvalidRoleID); err != nil {
		return err
	}

	// 分配人既可能是 UUID 也可能是 domain.SystemCreator，
	// 因此只校验"去空白后非空"——与 chk_membership_roles_assigned_by_not_blank 同口径。
	if strings.TrimSpace(r.AssignedBy) == "" {
		return fmt.Errorf("%w: 分配人为空白", domain.ErrInvalidActor)
	}

	// 状态必须在受控词表内，未知取值一律判非法（fail closed，见 Status.Valid）。
	if !r.Status.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidStatusValue, r.Status)
	}

	// 原因允许为空（落库为 NULL），但不能是纯空白，也不能超过长度上限。
	if r.Reason != "" {
		trimmed := strings.TrimSpace(r.Reason)
		if trimmed == "" {
			return fmt.Errorf("%w: 原因为纯空白", ErrInvalidReason)
		}
		if n := utf8.RuneCountInString(trimmed); n > maxReasonLen {
			return fmt.Errorf("%w: 长度 %d 超过上限 %d", ErrInvalidReason, n, maxReasonLen)
		}
	}

	// 生效时间必须非零：该列 NOT NULL，零值会写成一个无意义的年份。
	if r.EffectiveAt.IsZero() {
		return fmt.Errorf("%w: 生效时间为零值", ErrInvalidMembershipRoleRecord)
	}
	// 两个时刻都先对齐到数据库精度再比较（见 alignToPG）：
	// 纳秒级的余量会在落库时被截断，不先对齐就会出现
	// "这里放行了、数据库的 CHECK 却拒绝"的裂缝。
	// 只在这里对齐、不回写实体，保持本函数无副作用。
	effectiveAt := alignToPG(r.EffectiveAt)
	expiresAt := alignToPG(r.ExpiresAt)
	// 失效时间为零值表示长期有效，否则必须严格晚于生效时间。
	if !expiresAt.IsZero() && !expiresAt.After(effectiveAt) {
		return fmt.Errorf("失效时间 %v 不晚于生效时间 %v：%w",
			expiresAt, effectiveAt, ErrInvalidEffectiveRange)
	}
	// expired 只能由"到了 expires_at"推进而来。
	if r.Status == StatusExpired && r.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: expired 必须带到期时间", ErrInvalidMembershipRoleRecord)
	}

	// 行级必填项：分配动作的时间、行的创建时间与乐观锁版本号。
	if r.AssignedAt.IsZero() {
		return fmt.Errorf("%w: 分配时间为零值", ErrInvalidMembershipRoleRecord)
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("%w: 创建时间为零值", ErrInvalidMembershipRoleRecord)
	}
	if r.Version <= 0 {
		return fmt.Errorf("%w: 版本号 %d 非法", ErrInvalidMembershipRoleRecord, r.Version)
	}

	return nil
}

// requireValidID 校验一个标识字段：非空、合法的 UUID、不为 uuid.Nil，
// 且必须是规范形态。
//
// 前三条与适配层的 requireUUID 同一口径，差别只在它面向领域实体的字段，
// 因此按字段给出定位文案，而不是按值。
//
// 第四条是这里额外的：uuid.Parse 接受的形态比 PostgreSQL 的 uuid 输入函数更宽
// ——urn:uuid: 前缀、花括号、32 位无连字符十六进制它都认，
// 而后两者 PostgreSQL 并不接受。只校验不归一化的话，
// 就会出现"领域层判合法、落库时数据库报 22P02 类型错误"的裂缝：
// 恰好是本类校验想堵住的"非法标识变成驱动原生错误（500）"。
// 要求规范形态也顺带杜绝了"用去空白后的值校验、却把原始值写进去"的不对称。
func requireValidID(name, value string, invalid error) error {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil || parsed == uuid.Nil() {
		return fmt.Errorf("%w: %s=%q", invalid, name, value)
	}
	if parsed.String() != value {
		return fmt.Errorf("%w: %s=%q 不是规范形态，期望 %q", invalid, name, value, parsed.String())
	}
	return nil
}
