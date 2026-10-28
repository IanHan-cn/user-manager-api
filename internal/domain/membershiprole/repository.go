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
// # 失效判定
//
// "尚未失效"是一个二维判定，缺一不可：
//
//	状态属于 pending / active，且尚未到期（expires_at 为 NULL 或晚于判定时刻）。
//
// 只看状态是不够的：expired 只能由定时任务经 ListDueForExpiry + Expire 推进，
// 定时任务没跑到之前，一条已经过了 expires_at 的 active 行在库里仍然是 active。
// 少了时间维度，两个后果同时成立：
//   - 鉴权侧把一条早已过期的授权当成仍然生效，这是一个真实的越权窗口；
//   - 重新分配被挡住：部分唯一索引 idx_membership_roles_membership_role_active
//     的条件同样只看状态，一条过期未推进的 active 行会一直占着
//     (membership_id, role_id) 的唯一槽位，新插一行撞 23505，
//     运维侧看到的提示却是"该成员已持有该角色"，与事实正好相反。
//
// 因此约定：
//   - 凡是回答"现在还算不算生效"的方法，实现方都必须带上时间条件；
//   - 判定时刻 now 由调用方给出（与 ListDueForExpiry 同一口径）：
//     同一次业务判断里的多个查询必须用同一个 now，
//     否则两次判定会得出不同结论。
//
// 到期边界与领域层对齐：expires_at > now 才算尚未到期，
// expires_at == now 即已到期（Expire 在 now == ExpiresAt 时是允许的）。
//
// # 租户隔离
//
// MembershipRole 是租户边界内的对象，因此：
//   - 任何写入都必须以 tenant_id 为条件；
//   - 所有查询都必须带 tenant_id 条件，包括 GetByID，不得跨租户返回数据；
//     实现方应当把该条件写进查询 helper 内部，而不是交给每个调用点自己拼——
//     交给调用点就等于"迟早漏一处"，而漏掉的后果是跨租户读到一行；
//   - 租户内的日常鉴权用 GetByMembershipAndRole：它按 (tenant_id, membership_id, role_id)
//     三者定位，取回结果不必再校验归属；
//   - tenant_id 是冗余列、没有外键兜底（见 migrations/000009 的表注释），
//     上面这些条件是把租户边界落到 SQL 上的唯一防线。
//
// # 成员关系状态
//
// 本端口只管授权自己（membership_roles）这一张表，不 join memberships：
// 因此"这条成员关系是否仍然有效"（是否已离开租户、是否被暂停）
// 不在查询结果里体现。调用方若要把授权用于鉴权，
// 必须另行确认成员关系本身仍处于有效状态——
// 这是应用层的职责：适配层负责数据归属（跨不跨租户），
// 业务有效性归业务规则。
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
// "到期"这一侧还要多一步：一条已经过了 expires_at 但状态仍是 active 的行，
// 在定时任务推进之前与"尚未失效"无法区分，它会一直占着唯一索引的槽位。
// 因此实现方在 Create 插入新行之前，必须先把这批行推进为 expired
// （见 Create 的注释），调用方不必自己先扫一遍。
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
	//
	// 实现方必须先推进再插入：同一 (membership_id, role_id) 上"已经到期但状态
	// 尚未推进"的授权行要先被推进为 expired，否则它会一直占着部分唯一索引的
	// 槽位，让这次分配撞 23505（见类型注释的"失效判定"与"重新分配"小节）。
	// 推进与插入必须在同一事务内完成：是否处于事务由调用方通过
	// ports.Transactor 决定，实现方既不自行开事务，也不自行提交。
	Create(ctx context.Context, r *MembershipRole) error

	// Update 更新一条角色授权的可变字段。
	//
	// 不可写的列为 id、tenant_id、membership_id、role_id、assigned_by、
	// assigned_at、created_at、updated_at、expires_at 九项：
	// 前四者是一次授权的业务键与归属，换了其中任何一个就已是另一次授权，
	// 而不是"改了一下"；assigned_by / assigned_at 记录授权动作本身，不可覆写，
	// 否则"谁授的权"会被事后改写；created_at 不可变，updated_at 归触发器维护。
	//
	// expires_at 也在不可写之列：到期时间由构造函数一次性决定，
	// 三条状态转换（Approve / Revoke / Expire）都不改它。
	// 允许覆写它等于允许把一条有期限的授权改成永久有效——
	// 一次没带 ExpiresAt 的 Update 就会静默造成这个后果。
	// 将来要支持续期，请另开专门的端口方法（例如 ExtendExpiry），
	// 由领域层校验新的到期时间，而不是放开整列。
	//
	// 真正会被写入的只有 status、reason、effective_at、version 四项：
	// version 是乐观锁令牌，实现方在更新成功时自增它。
	//
	// 实现方必须以调用方读到的 version 作为更新前提，并且必须排除终态行：
	//   - 前提对不上说明这行已被他人改过 → ErrMembershipRoleConflict；
	//   - 落在终态行上说明要改的是一条历史 → ErrInvalidStatusTransition，
	//     终态是历史，改它等于"复活"一次已撤销的授权。
	// 命中 0 行时实现方要取回该行加以区分，不得一律报成冲突：
	// 都报冲突的话调用方会"重新读取再重试"，而终态行重试多少次都不会成功。
	//
	// 实现方负责在更新成功后把调用方实体的 version 自增，
	// 调用方据此可以对同一行发起下一次更新，不必先重新查询。
	//
	// 实现方会校验整个实体的自洽性（取值合法、标识规范、状态在词表内、
	// 有效期自洽等），因此调用方必须传入读回后的**完整实体**，
	// 不支持只带少数字段的部分更新。
	// 这一点必须写明：否则调用方会按"只改 status 就只传 status"的直觉写，
	// 结果被实现方以"成员关系标识为空或非法"拒绝，
	// 错误信息还会让人以为是数据坏了而不是用法不对。
	//
	// 实现方**不重放状态机的规则**：状态转换的合法性由调用方经领域方法
	// （Approve / Revoke / Expire）保证，Update 只负责把结果持久化。
	// 之所以这样分工：转换规则只有领域层知道，适配层复制一份必然随领域层漂移。
	// 调用方若绕过领域方法直接改导出字段再 Update，
	// 实现方只拦得住终态行（见上一段）与取值不合法的字段；
	// 好在鉴权读路径独立地按 status 与 expires_at 过滤，
	// 被绕过的转换不会因此带来额外的授权。
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
	//
	// 必须带 tenant_id 条件：本表 tenant_id 是没有外键兜底的冗余列
	// （见类型注释的"租户隔离"小节），少了它一次查询就能读到别的租户的行。
	// 租户内的日常鉴权请改用 GetByMembershipAndRole。
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*MembershipRole, error)

	// GetByMembershipAndRole 查询指定成员关系在指定角色上"尚未失效"的那条授权，
	// 未命中返回 (nil, nil)。
	//
	// "尚未失效"按类型注释的"失效判定"小节执行：状态属于 pending / active，
	// 且尚未到期（expires_at 为 NULL 或晚于 now）。
	//
	// 必须自带 status IN ('pending', 'active') 条件：
	// 唯一性只对未失效的授权成立（见类型注释的"重新分配"小节），
	// 同一对取值可能有若干历史行，少了该条件就会命中已失效的历史，
	// 让调用方误以为这个角色还不能被重新分配。
	//
	// 租户内的日常鉴权一律使用本方法而不是 GetByID：
	// 它是唯一自带租户条件的单条查询，取回的结果不必再校验 TenantID。
	//
	// now 是本次判定的时刻：同一次业务判断里的多个查询必须传同一个值。
	GetByMembershipAndRole(
		ctx context.Context, tenantID, membershipID, roleID uuid.UUID, now time.Time,
	) (*MembershipRole, error)

	// ListByMembershipID 查询指定成员关系的全部授权，含已撤销与已到期的历史行，
	// 返回顺序见类型注释的"排序与分页"小节。
	ListByMembershipID(ctx context.Context, tenantID, membershipID uuid.UUID) ([]*MembershipRole, error)

	// ListActiveByMembershipID 查询指定成员关系当前生效的全部角色授权，
	// 返回顺序见类型注释的"排序与分页"小节。
	//
	// "当前生效"按类型注释的"失效判定"小节执行：
	// status = active，且尚未到期（expires_at 为 NULL 或晚于 now）。
	// 少了时间维度，定时任务推进之前，已经过期的授权会继续出现在鉴权结果里。
	//
	// 与 ListByMembershipID 分开而不是让调用方自行过滤：
	// "这个成员现在能用哪些角色"是鉴权热路径，
	// 不必每次都把历史行取回来再丢掉。
	//
	// now 是本次判定的时刻：同一次业务判断里的多个查询必须传同一个值。
	ListActiveByMembershipID(
		ctx context.Context, tenantID, membershipID uuid.UUID, now time.Time,
	) ([]*MembershipRole, error)

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
	// limit 是本次最多返回多少条，由调用方给出且必须为正整数：
	// 这条查询没有上限时，服务中断几小时后恢复的积压量
	// 会在一次调用里全部读进内存。分批即反复调用本方法，
	// 已推进的行不再满足 status 条件，下一批自然是剩下的。
	//
	// limit 不是正整数时实现方必须返回 domain.ErrInvalidLimit，
	// 而不是返回空切片：上限为 0 与"确实没有到期授权"对调用方是完全不同的两件事，
	// 用空结果表达无效入参会让到期推进任务静默空转，
	// 而"直到返回条数小于上限才停"的分批循环还会因此永不结束。
	// 这与"标识为零值返回空切片"是两种语义，不要混为一谈。
	//
	// 调用方应当把本次的 now 一路传给 Expire：
	// "扫出来的一批"与"逐个判死"必须用同一时刻，
	// 否则中途跨过某个 expires_at 会让两次判定得出不同结论。
	ListDueForExpiry(ctx context.Context, tenantID uuid.UUID, now time.Time, limit int) ([]*MembershipRole, error)
}
