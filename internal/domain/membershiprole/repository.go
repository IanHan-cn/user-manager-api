package membershiprole

import (
	"context"
	"time"
	"uuid"
)

// Repository 是成员角色授权聚合的持久化端口，由适配层实现。
//
// # 通用约定
//
// 所有方法都必须满足：
//   - 未命中不是错误：单条查询返回 (nil, nil)，列表查询返回空切片而非 nil；
//   - 只有基础设施故障（连接失败、超时、SQL 执行失败）才返回 error，
//     "查不到数据"必须返回空结果而不是错误；
//   - 一次授权的 Business Key 是 (membership_id, role_id)，
//     但唯一性只对未失效的授权成立（见下方"重新分配"小节）；
//     重复授权由数据库部分唯一索引兜底，实现方必须把约束冲突翻译成
//     ErrMembershipRoleExists 返回，不得静默吞掉后返回 nil（见 Create）；
//   - "找不到记录"只有写操作才报错，且统一返回 ErrMembershipRoleNotFound；
//     查询类方法不适用这一条，它们未命中返回空结果；
//   - created_at 由实现方写入、updated_at 由数据库触发器维护
//     （见 migrations/000001），实现方不得在 SQL 里另行写 updated_at；
//   - 所有写操作必须在事务内执行（ports.Transactor）：
//     授权变更与 Casbin 策略投影要同批提交，避免回滚后留下半截状态。
//
// # 租户隔离
//
// MembershipRole 是租户边界内的对象，因此：
//   - 任何写入都必须以 tenant_id 为条件；
//   - 除 GetByID 外，所有查询都必须带 tenant_id 条件，不得跨租户返回数据；
//   - 用 GetByID 取回授权后，必须校验返回值的 TenantID 与当前租户一致，
//     或者直接使用 GetByMembershipAndRole；
//   - tenant_id 是冗余列、没有外键兜底（见 migrations/000009 的表注释），
//     上面这些条件是把租户边界落到 SQL 上的唯一防线。
//
// # 重新分配
//
// 授权被撤销（revoked）或到期（expired）之后，可以再次把同一个角色授予同一条
// 成员关系：此时调用 Create 新插一行，旧行原样保留作为历史。
// 二者并不冲突——idx_membership_roles_membership_role_active 只约束
// status IN ('pending', 'active') 的行。
//
// 与之相对，终态行不得被"复活"：
// 不要试图把 revoked / expired 改回 active 来重新授权，
// 那会抹掉上一次授权的存在痕迹（谁授的、何时失效、因何变更），
// 也让"该成员曾经持有该角色"这件事变得无法追溯。
//
// # 排序与分页
//
// 列表方法按 created_at 升序、created_at 相同时按 id 升序返回
// （ListDueForExpiry 除外，它按 expires_at、id 升序，理由见该方法）。
// 顺序已固化，后续加入 limit / offset 分页不会改变各页之间的稳定性。
// 当前接口不提供分页参数：授权的列表 API 尚未落地，
// 待落地时统一以 ListParams（含 limit、offset、总数）的形态扩展。
type Repository interface {
	// Create 创建一条角色授权。
	// 主键、初始状态与时间戳由领域层生成，实现方原样落库，不得改写；
	// 撞上 idx_membership_roles_membership_role_active（即该成员关系
	// 已有一行 pending / active 的同角色授权）时返回 ErrMembershipRoleExists，
	// 不得静默吞掉后返回 nil——那会让上层误以为授权已经生效。
	// 传入空实体返回 ErrNilMembershipRole。
	Create(ctx context.Context, r *MembershipRole) error

	// Update 更新一条角色授权的可变字段。
	//
	// 不可写的列为 id、tenant_id、membership_id、role_id、assigned_by、
	// assigned_at、created_at、updated_at 八项：
	// 前四者是一次授权的业务键与归属，换了其中任何一个就已是另一次授权，
	// 而不是"改了一下"；assigned_by / assigned_at 记录授权动作本身，不可覆写，
	// 否则"谁授的权"会被事后改写；created_at 不可变，updated_at 归触发器维护。
	// 真正会被写入的只有 status、reason、effective_at、expires_at 四项。
	//
	// 更新必须以 tenant_id 为条件（见类型注释的"租户隔离"小节）：
	// 传入的授权归属与库中不一致时按未命中处理，
	// 返回 ErrMembershipRoleNotFound 而不是去改别的租户的行。
	//
	// 传入空实体返回 ErrNilMembershipRole；
	// id 或 tenant_id 为空同样按"要改的行不存在"处理。
	// 记录不存在时返回 ErrMembershipRoleNotFound：更新写入不存在的行属于
	// 调用方 bug，静默成功会让上层误以为更新生效。
	Update(ctx context.Context, r *MembershipRole) error

	// GetByID 按主键查询授权，未命中返回 (nil, nil)。
	// 本方法不带 tenant_id 条件，取回后必须校验 TenantID，
	// 或直接改用 GetByMembershipAndRole。
	GetByID(ctx context.Context, id uuid.UUID) (*MembershipRole, error)

	// GetByMembershipAndRole 查询指定成员关系在指定角色上"尚未失效"的那条授权，
	// 未命中返回 (nil, nil)。
	//
	// 必须自带 status IN ('pending', 'active') 条件：
	// 唯一性只对未失效的授权成立（见类型注释的"重新分配"小节），
	// 同一对取值可能有若干历史行，少了该条件就会命中已失效的历史，
	// 让调用方误以为这个角色还不能被重新分配。
	//
	// 租户内的日常鉴权一律使用本方法而不是 GetByID：
	// 它是唯一自带租户条件的单条查询，取回的结果不必再校验 TenantID。
	GetByMembershipAndRole(ctx context.Context, tenantID, membershipID, roleID uuid.UUID) (*MembershipRole, error)

	// ListByMembershipID 查询指定成员关系的全部授权，含已撤销与已到期的历史行，
	// 返回顺序见类型注释的"排序与分页"小节。
	ListByMembershipID(ctx context.Context, tenantID, membershipID uuid.UUID) ([]*MembershipRole, error)

	// ListActiveByMembershipID 查询指定成员关系当前生效的全部角色授权
	// （status = active），返回顺序同上。
	//
	// 与 ListByMembershipID 分开而不是让调用方自行过滤：
	// "这个成员现在能用哪些角色"是鉴权热路径，
	// 条件与索引 idx_membership_roles_tenant_membership 对齐、只回必要行，
	// 不必每次都把历史行取回来再丢掉。
	ListActiveByMembershipID(ctx context.Context, tenantID, membershipID uuid.UUID) ([]*MembershipRole, error)

	// ListByRoleID 查询指定角色被授予给哪些成员关系（含历史行），
	// 返回顺序同上。
	//
	// 与 ListByMembershipID 是同一个多对多关联的两个方向：
	// 缺少后者时，"撤销或调整这个角色会波及哪些成员"就只能全表扫描，
	// 而这类判断往往发生在角色变更之前的确认环节。
	ListByRoleID(ctx context.Context, tenantID, roleID uuid.UUID) ([]*MembershipRole, error)

	// ListDueForExpiry 查询指定租户下已经到期、但状态尚未推进的授权，
	// 供定时任务逐个调用 Expire 后 Update 落库。
	//
	// 条件固定为 status IN ('pending', 'active') AND expires_at <= now：
	// expires_at 为 NULL（长期有效）的行天然不满足后者，无需额外过滤。
	// 排序刻意按 expires_at、id 升序而不是 created_at：
	// 到期越久的越该先处理，且同一批内顺序稳定，便于分批与断点续跑。
	//
	// 调用方应当把本次的 now 一路传给 Expire：
	// "扫出来的一批"与"逐个判死"必须用同一时刻，
	// 否则中途跨过某个 expires_at 会让两次判定得出不同结论。
	ListDueForExpiry(ctx context.Context, tenantID uuid.UUID, now time.Time) ([]*MembershipRole, error)
}
