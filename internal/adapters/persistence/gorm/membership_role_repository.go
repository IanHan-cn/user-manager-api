package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membershiprole"
	"gorm.io/gorm"
)

// MembershipRoleRepository 用 GORM 实现 membershiprole.Repository。
type MembershipRoleRepository struct {
	db *gorm.DB
}

var _ membershiprole.Repository = (*MembershipRoleRepository)(nil)

// NewMembershipRoleRepository 创建成员角色授权仓储。
func NewMembershipRoleRepository(db *gorm.DB) (*MembershipRoleRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}
	return &MembershipRoleRepository{db: db}, nil
}

// membershipRoleRecord 是 membership_roles 表的持久化模型。
//
// expires_at 与 reason 用指针类型：
//   - expires_at 以 NULL 表示"长期有效"（见 migrations/000009 的列注释），
//     而领域层用 time.Time 零值表达同一语义，不用指针就会把零值时间
//     （0001-01-01）写进列里，撞上 chk_membership_roles_expires_after_effective；
//   - reason 以 NULL 表示"没填"，空串写进去会撞上 chk_membership_roles_reason。
//
// updated_at 标为只读（<-:false）的理由与其余各表一致：
// 该列由数据库触发器维护（见 migrations/000009 的 trg_membership_roles_set_updated_at），
// 否则 GORM 的自动时间戳会与触发器抢写同一列。
type membershipRoleRecord struct {
	ID           string     `gorm:"column:id;primaryKey"`
	TenantID     string     `gorm:"column:tenant_id"`
	MembershipID string     `gorm:"column:membership_id"`
	RoleID       string     `gorm:"column:role_id"`
	AssignedBy   string     `gorm:"column:assigned_by"`
	AssignedAt   time.Time  `gorm:"column:assigned_at"`
	EffectiveAt  time.Time  `gorm:"column:effective_at"`
	ExpiresAt    *time.Time `gorm:"column:expires_at"`
	Status       string     `gorm:"column:status"`
	Reason       *string    `gorm:"column:reason"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;<-:false"`
	Version      int        `gorm:"column:version"` // 映射 version 列
}

// TableName 固定表名为 membership_roles，不依赖 GORM 默认的复数转换。
func (membershipRoleRecord) TableName() string { return "membership_roles" }

// membershipRoleUpdatableColumns 是 Update 允许写入的列，与 toUpdateMap 的键一一对应。
//
// 刻意不含 id、tenant_id、membership_id、role_id：前四者是一次授权的业务键与归属，
// 换了其中任何一个都已是另一次授权，而不是"改了一下"。
// assigned_by / assigned_at 同样排除：它们记录授权动作本身，
// 允许覆写就等于允许事后改写"谁授的权、什么时候授的"。
//
// expires_at 也刻意排除：到期时间由构造函数一次性决定，
// 三条状态转换（Approve / Revoke / Expire）都不改它。
// 它在白名单里时，一次"没有带 ExpiresAt 的实体"的 Update
// 会把库里的到期时间静默写成 NULL——
// 一条有期限的临时授权就此变成永久有效，且没有任何错误。
// 将来要支持续期，请另开专门的端口方法（例如 ExtendExpiry），
// 由领域层校验新的到期时间，而不是放开整列。
//
// created_at 不可变，updated_at 归触发器维护。
var membershipRoleUpdatableColumns = []string{
	"status",
	"reason",
	"effective_at",
	"version", // 乐观锁令牌，成功时自增
}

// membershipRoleNotExpiredStatuses 是"尚未失效"的状态集合。
//
// 取值必须与 idx_membership_roles_membership_role_active 的索引条件
// （status IN ('pending', 'active')）逐字一致：
// 唯一性只对这批状态成立，查询若用另一套口径，
// 就会出现"库里挡住了、查询却说没有"或反之的自相矛盾。
var membershipRoleNotExpiredStatuses = []string{
	string(membershiprole.StatusPending),
	string(membershiprole.StatusActive),
}

// toMembershipRoleRecord 把领域实体转成持久化模型。
func toMembershipRoleRecord(r *membershiprole.MembershipRole) membershipRoleRecord {
	return membershipRoleRecord{
		ID:           r.ID,
		TenantID:     r.TenantID,
		MembershipID: r.MembershipID,
		RoleID:       r.RoleID,
		AssignedBy:   r.AssignedBy,
		AssignedAt:   r.AssignedAt,
		EffectiveAt:  r.EffectiveAt,
		ExpiresAt:    toNullableTime(r.ExpiresAt),
		Status:       string(r.Status),
		Reason:       toNullableString(r.Reason),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		Version:      r.Version,
	}
}

// toEntity 把持久化模型还原成领域实体。
//
// 状态取值必须在受控词表内，否则返回 ErrInvalidStatusValue：
// 未知状态最现实的来源是滚动发布——新版本写入了一个旧版本还不认识的状态。
// 在这里就拦下而不是等 Status.Valid() 在别处判定，
// 是为了让"读到一个不认识的状态"成为一个可归类的错误，
// 而不是一路以"未失效"的身份混进鉴权结果（fail closed，见 Status.Valid）。
func (r membershipRoleRecord) toEntity() (*membershiprole.MembershipRole, error) {
	status := membershiprole.Status(r.Status)
	if !status.Valid() {
		return nil, fmt.Errorf("%w: id=%s status=%q", membershiprole.ErrInvalidStatusValue, r.ID, r.Status)
	}
	return &membershiprole.MembershipRole{
		ID:           r.ID,
		TenantID:     r.TenantID,
		MembershipID: r.MembershipID,
		RoleID:       r.RoleID,
		AssignedBy:   r.AssignedBy,
		AssignedAt:   r.AssignedAt,
		EffectiveAt:  r.EffectiveAt,
		ExpiresAt:    fromNullableTime(r.ExpiresAt),
		Status:       status,
		Reason:       fromNullableString(r.Reason),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		Version:      r.Version,
	}, nil
}

// toUpdateMap 返回用于更新的列值映射，键集合必须与 membershipRoleUpdatableColumns 一致。
//
// 用 map 而不是结构体：结构体更新时零值字段会被 GORM 跳过，
// 而"清空 reason"正是要把一个可空列写成 NULL——
// 只有显式的 map 才能表达"这一列要写 NULL"而不是"这一列没填"。
func (r membershipRoleRecord) toUpdateMap() map[string]any {
	return map[string]any{
		"status":       r.Status,
		"reason":       r.Reason,
		"effective_at": r.EffectiveAt,
		"version":      gorm.Expr("version + 1"), // 乐观锁：成功时自增
	}
}

// membershipRoleEntities 把一批持久化模型还原成领域实体，空输入返回空切片而非 nil。
//
// 任一行的状态不在受控词表内时整批失败：宁可让一次列表查询报错，
// 也不能把未知状态的行当成正常数据交给上层（见 toEntity）。
func membershipRoleEntities(recs []membershipRoleRecord) ([]*membershiprole.MembershipRole, error) {
	out := make([]*membershiprole.MembershipRole, 0, len(recs))
	for _, rec := range recs {
		entity, err := rec.toEntity()
		if err != nil {
			return nil, err
		}
		out = append(out, entity)
	}
	return out, nil
}

// membershipRoleWriteTargets 组装成员角色授权写操作的结局配置。
func membershipRoleWriteTargets(r *membershiprole.MembershipRole) writeTargets {
	return writeTargets{
		conflict:         membershiprole.ErrMembershipRoleExists,
		conflictIdentity: fmt.Sprintf("membership_id=%s role_id=%s", r.MembershipID, r.RoleID),
		invalid:          membershiprole.ErrInvalidMembershipRoleRecord,
		invalidIdentity:  fmt.Sprintf("id=%s status=%s", r.ID, r.Status),
		byConstraint: map[string]constraintOutcome{
			// 主键重复是写入本身的缺陷，与"已持有该角色"无关：
			// 混为一谈会让排障去找业务键，而真正该查的是 ID。
			"membership_roles_pkey": {
				err:      domain.ErrDuplicateID,
				identity: fmt.Sprintf("id=%s", r.ID),
			},
			// 业务唯一索引：同一成员关系对同一角色只能有一行未失效的授权。
			"idx_membership_roles_membership_role_active": {
				err:      membershiprole.ErrMembershipRoleExists,
				identity: fmt.Sprintf("membership_id=%s role_id=%s", r.MembershipID, r.RoleID),
			},
			// 字段级 CHECK：与领域层的同名字段错误同一口径。
			// 不分流的话，同一个非法取值从领域层进来是 400，
			// 从数据库层进来却是 409——同一语义两个状态码。
			"chk_membership_roles_reason": {
				err:      membershiprole.ErrInvalidReason,
				identity: "reason",
			},
			"chk_membership_roles_expires_after_effective": {
				err:      membershiprole.ErrInvalidEffectiveRange,
				identity: fmt.Sprintf("expires_at=%v effective_at=%v", r.ExpiresAt, r.EffectiveAt),
			},
			"chk_membership_roles_assigned_by_not_blank": {
				err:      domain.ErrInvalidActor,
				identity: fmt.Sprintf("assigned_by=%s", r.AssignedBy),
			},
			// 下面两条领域层没有同口径的字段级错误：
			// status 取值域与"expired 必须带到期时间"都只在绕过领域层时才会触发，
			// 它们是两侧口径漂移的信号，仍归记录级错误。
			"chk_membership_roles_status": {
				err:      membershiprole.ErrInvalidMembershipRoleRecord,
				identity: fmt.Sprintf("id=%s status=%s", r.ID, r.Status),
			},
			"chk_membership_roles_expired_has_expires_at": {
				err:      membershiprole.ErrInvalidMembershipRoleRecord,
				identity: fmt.Sprintf("id=%s status=%s", r.ID, r.Status),
			},
		},
	}
}

// translateMembershipRoleWriteError 把数据库错误翻译成领域错误，两条判定见
// classifyWriteError。留这个薄壳是为了让调用处的意图自带聚合语义，
// 不必知道 writeTargets 要怎么拼。
func translateMembershipRoleWriteError(err error, r *membershiprole.MembershipRole) error {
	return classifyWriteError(err, membershipRoleWriteTargets(r))
}

// Create 写入一条角色授权，撞上 idx_membership_roles_membership_role_active
// （该成员关系已有一行 pending / active 的同角色授权）时返回 ErrMembershipRoleExists。
//
// 插入前先推进：同一 (membership_id, role_id) 上"已经到期但状态尚未推进"的
// 授权行会被推进为 expired（见 advanceExpiredDueGrants）。
// 少了这一步，那批行会一直占着部分唯一索引的槽位，让这次分配撞 23505，
// 而事实是那个角色早已到期——调用方看到的却是"该成员已持有该角色"。
//
// 错误怎么翻译见 translateMembershipRoleWriteError。
//
// 写路径的三道入口校验，顺序刻意由便宜到昂贵：
//   - m.Validate()：领域层校验取值自洽。实体的字段是导出的，
//     调用方可以不走构造函数直接改写它们，这一道是唯一能拦住
//     "值本身不合法"的地方——数据库 CHECK 只能给出记录级错误（409），
//     而那本该是入参错误（400）。标识的 UUID 合法性也在这里校验，
//     与 role_repository.go 的 requireUUID 同一口径，只是放在领域层，
//     连一次数据库访问都省了；
//   - ensureMembershipAndRoleInTenant：引用完整性。本表无外键，
//     租户归属只能由写路径保证；
//   - advanceExpiredDueGrants：让到期未推进的行腾出唯一索引槽位。
//
// 错误怎么翻译见 translateMembershipRoleWriteError。
func (r *MembershipRoleRepository) Create(ctx context.Context, m *membershiprole.MembershipRole) error {
	if m == nil {
		return membershiprole.ErrNilMembershipRole
	}
	if err := m.Validate(); err != nil {
		return err
	}

	db := dBFromContext(ctx, r.db)

	// 推进与插入必须同处一个事务：事务由调用方通过 ports.Transactor 开启，
	// 这里复用同一个句柄，不自行开事务也不自行提交。
	if err := ensureMembershipAndRoleInTenant(ctx, db, m.TenantID, m.MembershipID, m.RoleID); err != nil {
		return err
	}
	if err := r.advanceExpiredDueGrants(ctx, db, m); err != nil {
		return err
	}

	rec := toMembershipRoleRecord(m)
	if err := db.WithContext(ctx).Create(&rec).Error; err != nil {
		return translateMembershipRoleWriteError(err, m)
	}
	return nil
}

// advanceExpiredDueGrants 把同一 (tenant_id, membership_id, role_id) 上
// "已经到期但状态尚未推进"的授权行推进为 expired。
//
// 为什么必须做这一步：部分唯一索引 idx_membership_roles_membership_role_active
// 的条件是 status IN ('pending', 'active')，它看不到时间。
// 定时任务把到期行推进为 expired 之前，一条过了 expires_at 的 active 行
// 会一直占着 (membership_id, role_id) 的唯一槽位，于是"同一个角色重新分配"
// 会撞 23505，被翻译成 ErrMembershipRoleExists——而事实是那个角色早已到期。
// 部分索引的条件无法引用 now()（PostgreSQL 只接受不可变表达式），
// 这一步因此只能在应用层做。
//
// 判定时刻取当前时间，不接受调用方传入的时间基准：
// 推进的是"客观已经过期"的行，调用方若传一个将来的时刻，
// 就会把别人仍在使用的授权提前作废。
//
// 只动 status 与 version 两列：
//   - effective_at 不动：到期不改写生效时间（与领域层的 Expire 一致）；
//   - reason 不动：到期不覆盖分配理由（与领域层的 Expire 一致）；
//   - version 自增：这行确实被写过，版本号必须反映出来，
//     否则持有旧副本的调用方会拿着同一个版本号覆盖掉这次推进；
//   - updated_at 交给触发器（见 migrations/000009 的 trg_membership_roles_set_updated_at）。
func (r *MembershipRoleRepository) advanceExpiredDueGrants(
	ctx context.Context, db *gorm.DB, m *membershiprole.MembershipRole,
) error {
	result := db.WithContext(ctx).
		Model(&membershipRoleRecord{}).
		Where("tenant_id = ? AND membership_id = ? AND role_id = ?"+
			" AND status IN ? AND expires_at IS NOT NULL AND expires_at <= ?",
			m.TenantID, m.MembershipID, m.RoleID,
			membershipRoleNotExpiredStatuses, time.Now()).
		Select([]string{"status", "version"}).
		Updates(map[string]any{
			"status":  string(membershiprole.StatusExpired),
			"version": gorm.Expr("version + 1"),
		})
	if result.Error != nil {
		return translateMembershipRoleWriteError(result.Error, m)
	}
	return nil
}

// ensureMembershipAndRoleInTenant 校验成员关系与角色确实属于同一个租户。
//
// 为什么必须做：本表全仓一律不加外键（见 migrations/000009 的表注释），
// 引用完整性由写路径兜底。少了这一道，调用方把租户 A 的 id
// 与租户 B 的成员关系拼在一起调用 Create，
// 就能静默写入一行跨租户的授权——tenant_id 是冗余列，
// 此后按 (tenant_id=A, membership_id=B成员) 查询照样查得到它。
//
// 只查"该主键在本租户下是否存在"，不带任何业务过滤，
// 与 role_repository.go 的 roleExistsInTenant、ensurePermissionsExist 同一手法。
//
// 刻意不校验成员关系与角色的业务有效性（例如成员关系是否已离开租户）：
// 这一层只负责租户隔离——"跨不跨租户"是数据归属问题，必须由适配层兜死；
// 而"能不能给一个已离开的成员关系再授权"是业务规则，归应用层判断。
// 本仓的应用层尚未落地这条规则，把它写进适配层会让这里先于产品决策定死语义。
// 代价是这一层不阻止"给已离开的成员关系继续授权"——
// 真正的拦截点是应用层，而不是这里。
//
// 还有一条前提要写明：memberships 与 roles 的 tenant_id 都不可变，
// 且本仓当前没有删除它们的路径，因此"校验通过之后归属被改"这个竞态
// 今天不会发生。一旦将来引入删除路径，必须先清理本表，
// 否则这里会出现孤儿行（membership_roles 里的行指向已不存在的成员关系）。
func ensureMembershipAndRoleInTenant(
	ctx context.Context, db *gorm.DB, tenantID, membershipID, roleID string,
) error {
	exists, err := membershipExistsInTenant(ctx, db, tenantID, membershipID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: 成员关系 %s 不属于租户 %s",
			domain.ErrInvalidMembershipID, membershipID, tenantID)
	}

	exists, err = roleExistsInTenant(ctx, db, tenantID, roleID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: 角色 %s 不属于租户 %s",
			domain.ErrInvalidRoleID, roleID, tenantID)
	}
	return nil
}

// membershipExistsInTenant 报告指定成员关系是否存在于指定租户下。
func membershipExistsInTenant(
	ctx context.Context, db *gorm.DB, tenantID, membershipID string,
) (bool, error) {
	var id string
	err := db.WithContext(ctx).
		Model(&membershipRecord{}).
		Select("id").
		Where("id = ? AND tenant_id = ?", membershipID, tenantID).
		Take(&id).Error
	if err != nil {
		if isRecordNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("校验成员关系归属失败：%w", err)
	}
	return true, nil
}

// Update 更新一条角色授权的可变字段，记录不存在时返回 ErrMembershipRoleNotFound，
// 版本号对不上时返回 ErrMembershipRoleConflict。
//
// 条件里刻意带上 tenant_id：授权是租户边界内的对象，
// 只按主键更新时，一条来自别处（或被串改）的 id 就能改到别的租户的行。
// 这也是端口"任何写入都必须以 tenant_id 为条件"的落地。
//
// version 是乐观锁令牌：WHERE 带上调用方读到的版本号，
// 命中 0 行时区分"记录不存在"与"已被他人改过"，
// 后者必须报错而不是静默覆盖。
//
// 成功后会把传入实体的 Version 自增：调用方拿着同一份实体继续更新即可，
// 不必先重新查询（重新查询能额外拿到最新的 updated_at）。
//
// 写入前先走 m.Validate()：实体的字段是导出的，状态转换方法只在内存副本上守护，
// Update 这条路径完全信任传入实体的取值。少了这一道，
// 把 status 改成任意串、把原因塞成纯空白、把到期时间调到生效时间之前
// 都能一路写到数据库，靠 CHECK 兜底时上层拿到的是记录级错误而不是入参错误。
//
// 只写 membershipRoleUpdatableColumns 列出的列，updated_at 由触发器维护，本方法不写。
func (r *MembershipRoleRepository) Update(ctx context.Context, m *membershiprole.MembershipRole) error {
	if m == nil {
		return membershiprole.ErrNilMembershipRole
	}
	// 空标识先短路，理由与查询侧一致：id、tenant_id 都是 UUID 列，
	// 空串进去只会换来一次类型错误，而按端口口径这就是"要改的行不存在"。
	// 少了这一段，一次本该精确可判的 ErrMembershipRoleNotFound 会变成
	// 语义不明的驱动原生错误，最终落成 500。
	//
	// 这一段必须排在 Validate 之前：空的 id 按端口口径是"没有要改的行"，
	// 而不是"标识不合法"，两者的基类不同（NotFound 对 InvalidKey）。
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.TenantID) == "" {
		return fmt.Errorf("%w: id=%s tenant_id=%s",
			membershiprole.ErrMembershipRoleNotFound, m.ID, m.TenantID)
	}
	if err := m.Validate(); err != nil {
		return err
	}

	rec := toMembershipRoleRecord(m)
	db := dBFromContext(ctx, r.db)

	// WHERE 有三重前提，缺一不可：
	//   - tenant_id：租户边界（见端口的"租户隔离"小节）；
	//   - version：乐观锁，命中 0 行说明这行已被他人改过；
	//   - status 属于非终态：终态行是历史，不该被改写。
	//
	// 第三条尤其关键：状态机（canTransition）只守护
	// Approve / Revoke / Expire 三个领域方法，管不到"裸改导出字段再 Update"。
	// 少了它，读入一条 revoked 的行、把 Status 改成 active 就能直接落库——
	// 一条已撤销的授权被静默复活，并立刻出现在鉴权查询结果里。
	result := db.WithContext(ctx).
		Model(&membershipRoleRecord{}).
		Where("id = ? AND tenant_id = ? AND version = ? AND status IN ?",
			m.ID, m.TenantID, m.Version, membershipRoleNotExpiredStatuses).
		Select(membershipRoleUpdatableColumns).
		Updates(rec.toUpdateMap())

	if result.Error != nil {
		return translateMembershipRoleWriteError(result.Error, m)
	}

	// 命中 0 行时取回这一行，据此判定是三种结局中的哪一种。
	if result.RowsAffected == 0 {
		var existing membershipRoleRecord
		err := db.WithContext(ctx).
			Select("id", "status").
			Where("id = ? AND tenant_id = ?", m.ID, m.TenantID).
			Take(&existing).Error

		if err != nil {
			if !isRecordNotFound(err) {
				return fmt.Errorf("检查成员角色授权是否存在失败：%w", err)
			}
			return classifyMembershipRoleUpdate(false, "", m.ID)
		}

		return classifyMembershipRoleUpdate(true, existing.Status, m.ID)
	}

	// 更新成功后，同步自增内存实体的版本号，方便后续连续调用
	m.Version++
	return nil
}

// classifyMembershipRoleUpdate 判定 Update 命中 0 行时的结局。
//
// 原因只有三种，处置各不相同：
//   - 目标行压根不存在：调用方要改的东西不在库里（404）；
//   - 库里是一个不认识的状态：取值问题（400），多半是滚动发布留下的脏数据；
//   - 目标行是终态：终态是历史，改它等于"复活"一次已撤销的授权（409，状态类）；
//   - 目标行存在、非终态，但版本号对不上：这行已被他人改过（409，冲突类）。
//
// 后两者必须分开：都报成冲突的话，调用方会按"重新读取再重试"处理，
// 而终态行重试多少次都不会成功——它根本不是重试能解决的问题。
//
// 单独成函数是为了让它能脱离数据库验证：命中 0 行之后的分支
// 在集成测试里要靠 Docker 才能跑到。
func classifyMembershipRoleUpdate(exists bool, status, id string) error {
	if !exists {
		return fmt.Errorf("%w: id=%s", membershiprole.ErrMembershipRoleNotFound, id)
	}
	current := membershiprole.Status(status)
	if !current.Valid() {
		// 库里是一个不认识的状态：这不是"终态不可改"，而是取值问题。
		// 两者的处置方向完全不同——前者要改数据来源，后者只是操作时机不对。
		return fmt.Errorf("%w: id=%s status=%q",
			membershiprole.ErrInvalidStatusValue, id, status)
	}
	if current.Terminal() {
		return fmt.Errorf("%w: id=%s 已是终态，不可再改",
			membershiprole.ErrInvalidStatusTransition, id)
	}
	return membershiprole.ErrMembershipRoleConflict
}

// GetByID 按主键查询授权，未命中返回 (nil, nil)。
//
// 条件里带 tenant_id：授权是租户边界内的对象，而 tenant_id 是没有外键兜底的冗余列
// （见 migrations/000009 的表注释）。少了它，一个来自别处（或被串改）的主键
// 就能读到别的租户的授权行——取回的 TenantID 与本租户不一致时，
// 调用方还必须再校验一次，等于把租户隔离的责任推给了每一处调用。
// 租户内的日常查询请改用 GetByMembershipAndRole。
//
// 任一标识的零值表示"没有依据可查"，直接短路：把它打进 UUID 列只会换来一次
// 不知所云的类型转换错误，而约定的答案是 (nil, nil)。
func (r *MembershipRoleRepository) GetByID(
	ctx context.Context, tenantID, id uuid.UUID,
) (*membershiprole.MembershipRole, error) {
	if tenantID == uuid.Nil() || id == uuid.Nil() {
		return nil, nil
	}
	return r.findOne(ctx, tenantID, "id = ?", id.String())
}

// GetByMembershipAndRole 查询指定成员关系在指定角色上"尚未失效"的那条授权，
// 未命中返回 (nil, nil)。
//
// 状态条件与部分唯一索引 idx_membership_roles_membership_role_active 逐字一致
// （取值见 membershipRoleNotExpiredStatuses）：
// 少了它就会命中已撤销或已到期的历史行，让调用方误以为这个角色还不能被重新分配。
//
// 到期条件与状态条件缺一不可：expired 只能由定时任务推进，
// 定时任务没跑到之前，一条已经过了 expires_at 的 active 行在库里仍是 active。
// 只按状态过滤会同时造成两个后果——鉴权侧把它当成仍然生效（越权窗口），
// 以及重新分配被它挡住（撞唯一索引后被误报成"该成员已持有该角色"）。
//
// 边界与领域层的 Expire 对齐：expires_at > now 才算尚未到期，
// expires_at == now 即已到期（Expire 在 now == ExpiresAt 时是允许的）。
func (r *MembershipRoleRepository) GetByMembershipAndRole(
	ctx context.Context, tenantID, membershipID, roleID uuid.UUID, now time.Time,
) (*membershiprole.MembershipRole, error) {
	if tenantID == uuid.Nil() || membershipID == uuid.Nil() || roleID == uuid.Nil() {
		return nil, nil
	}
	return r.findOne(ctx, tenantID,
		"membership_id = ? AND role_id = ? AND status IN ?"+
			" AND (expires_at IS NULL OR expires_at > ?)",
		membershipID.String(), roleID.String(),
		membershipRoleNotExpiredStatuses, now)
}

// findOne 按条件取一条授权，未命中返回 (nil, nil)。
//
// tenantID 由本方法自己拼进条件，不交给调用方写：
// 租户条件是每一处读都必须带的东西，让它出现在"条件字符串"里
// 就等于把它交给每个调用点自己记得写——迟早会漏一处，
// 而漏掉的后果是跨租户读到一行。
// 调用方只负责给出"除租户之外"的业务条件。
func (r *MembershipRoleRepository) findOne(
	ctx context.Context, tenantID uuid.UUID, cond string, args ...any,
) (*membershiprole.MembershipRole, error) {
	var rec membershipRoleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND ("+cond+")", append([]any{tenantID.String()}, args...)...).
		Take(&rec).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询成员角色授权失败：%w", err)
	}
	return rec.toEntity()
}

// ListByMembershipID 查询指定成员关系的全部授权（含历史行），
// 按 created_at、id 升序。
//
// (tenant_id, membership_id, created_at, id) 上有 idx_membership_roles_tenant_membership，
// 列顺序与排序键一致，扫描直接走索引顺序。
func (r *MembershipRoleRepository) ListByMembershipID(
	ctx context.Context, tenantID, membershipID uuid.UUID,
) ([]*membershiprole.MembershipRole, error) {
	if tenantID == uuid.Nil() || membershipID == uuid.Nil() {
		return []*membershiprole.MembershipRole{}, nil
	}
	return r.listBy(ctx, tenantID, "membership_id = ?", membershipID.String())
}

// ListActiveByMembershipID 查询指定成员关系当前生效的角色授权，
// 按 created_at、id 升序。
//
// "当前生效"要同时满足 status = active 与尚未到期：
// 只按状态过滤的话，定时任务推进之前，已经过期的授权会继续出现在鉴权结果里。
//
// 条件与 ListByMembershipID 只差 status 与到期判定：这两列都不在索引前缀里，
// 因此这次扫描会在索引命中后回表过滤，代价可接受——
// 一个成员持有的角色数是十以内的量级。
func (r *MembershipRoleRepository) ListActiveByMembershipID(
	ctx context.Context, tenantID, membershipID uuid.UUID, now time.Time,
) ([]*membershiprole.MembershipRole, error) {
	if tenantID == uuid.Nil() || membershipID == uuid.Nil() {
		return []*membershiprole.MembershipRole{}, nil
	}
	return r.listBy(ctx, tenantID,
		"membership_id = ? AND status = ?"+
			" AND (expires_at IS NULL OR expires_at > ?)",
		membershipID.String(), membershiprole.StatusActive, now)
}

// ListByRoleID 查询指定角色被授予给哪些成员关系（含历史行），
// 按 created_at、id 升序。
//
// (tenant_id, role_id) 上有 idx_membership_roles_tenant_role，
// 该索引只覆盖等值过滤，排序键 created_at 不在其中，
// 因此这里会有一次显式排序；角色下的成员数量有限，代价可接受。
func (r *MembershipRoleRepository) ListByRoleID(
	ctx context.Context, tenantID, roleID uuid.UUID,
) ([]*membershiprole.MembershipRole, error) {
	if tenantID == uuid.Nil() || roleID == uuid.Nil() {
		return []*membershiprole.MembershipRole{}, nil
	}
	return r.listBy(ctx, tenantID, "role_id = ?", roleID.String())
}

// listBy 按条件取一批授权，排序口径见端口注释的"排序与分页"小节。
//
// 租户条件由本方法自己拼进去，理由同 findOne。
//
// 无匹配时返回空切片而不是 nil：调用方可以统一用 len() 判断，不必区分 nil 与空。
func (r *MembershipRoleRepository) listBy(
	ctx context.Context, tenantID uuid.UUID, cond string, args ...any,
) ([]*membershiprole.MembershipRole, error) {
	var recs []membershipRoleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND ("+cond+")", append([]any{tenantID.String()}, args...)...).
		Order("created_at, id").
		Find(&recs).Error
	if err != nil {
		return nil, fmt.Errorf("查询成员角色授权列表失败：%w", err)
	}
	return membershipRoleEntities(recs)
}

// ListDueForExpiry 查询指定租户下已到期、但状态尚未推进的授权，
// 按 expires_at、id 升序，最多返回 limit 条。
//
// expires_at 为 NULL（长期有效）的行天然不满足 expires_at <= now，无需额外过滤。
//
// limit 必须由调用方给出：这是一条定时任务的热路径，没有上限时
// 一次调用会把整个租户的到期行全部读进内存——
// 服务中断几小时后恢复，积压量足以把这一批撑爆。
// 分批由调用方用同一个 now 反复调用即可：
// 已推进的行不再满足 status 条件，下一批自然是剩下的。
//
// now 由调用方给出，且应当与随后 Expire 用的是同一个值：
// "扫出来的一批"与"逐个判死"若跨过了某个 expires_at，两次判定会得出不同结论。
//
// limit 不是正整数时返回 domain.ErrInvalidLimit 而不是空切片：
// 上限为 0 与"确实没有到期授权"对调用方是完全不同的两件事，
// 用空结果表达无效入参会让到期推进任务静默空转（扫不到 ⇒ 不推进 ⇒ 过期数据一直留着），
// 而"直到返回条数小于上限才停"的分批循环还会因此永不结束。
//
// 这与"标识为零值返回空切片"是两种语义，不要混为一谈：
// 后者是"没有依据可查"，前者是"入参本身不合法"。
func (r *MembershipRoleRepository) ListDueForExpiry(
	ctx context.Context, tenantID uuid.UUID, now time.Time, limit int,
) ([]*membershiprole.MembershipRole, error) {
	if limit <= 0 {
		return nil, domain.ErrInvalidLimit
	}
	if tenantID == uuid.Nil() {
		return []*membershiprole.MembershipRole{}, nil
	}

	var recs []membershipRoleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND status IN ? AND expires_at <= ?",
			tenantID.String(), membershipRoleNotExpiredStatuses, now).
		// 排序口径见端口注释：到期越久的越该先处理，且与 ListBy* 的 created_at 不同。
		// 索引 idx_membership_roles_due 的列序与这里的过滤+排序一致，
		// 扫描直接走索引顺序，不需要额外排序。
		Order("expires_at, id").
		Limit(limit).
		Find(&recs).Error
	if err != nil {
		return nil, fmt.Errorf("查询到期授权失败：%w", err)
	}
	return membershipRoleEntities(recs)
}
