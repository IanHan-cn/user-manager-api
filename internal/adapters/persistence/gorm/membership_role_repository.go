package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

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
// created_at 不可变，updated_at 归触发器维护。
var membershipRoleUpdatableColumns = []string{
	"status",
	"reason",
	"effective_at",
	"expires_at",
	"version", // 允许更新 version
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
func (r membershipRoleRecord) toEntity() *membershiprole.MembershipRole {
	return &membershiprole.MembershipRole{
		ID:           r.ID,
		TenantID:     r.TenantID,
		MembershipID: r.MembershipID,
		RoleID:       r.RoleID,
		AssignedBy:   r.AssignedBy,
		AssignedAt:   r.AssignedAt,
		EffectiveAt:  r.EffectiveAt,
		ExpiresAt:    fromNullableTime(r.ExpiresAt),
		Status:       membershiprole.Status(r.Status),
		Reason:       fromNullableString(r.Reason),
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		Version:      r.Version,
	}
}

// toUpdateMap 给出 Update 的列值对，键集合必须与 membershipRoleUpdatableColumns 一致。
//
// 用 map 而不是结构体：按结构体更新时零值字段会被 GORM 跳过，
// 而"清空 reason"（撤销时没写理由之外的情形）正是要把一个列写成 NULL。
func (r membershipRoleRecord) toUpdateMap() map[string]any {
	return map[string]any{
		"status":       r.Status,
		"reason":       r.Reason,
		"effective_at": r.EffectiveAt,
		"expires_at":   r.ExpiresAt,
		"version":      gorm.Expr("version + 1"), // 更新时自动加 1
	}
}

// membershipRoleEntities 把一批持久化模型还原成领域实体，空输入返回空切片而非 nil。
func membershipRoleEntities(recs []membershipRoleRecord) []*membershiprole.MembershipRole {
	out := make([]*membershiprole.MembershipRole, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.toEntity())
	}
	return out
}

// membershipRoleWriteTargets 组装成员角色授权写操作的结局配置。
func membershipRoleWriteTargets(r *membershiprole.MembershipRole) writeTargets {
	return writeTargets{
		conflict:         membershiprole.ErrMembershipRoleExists,
		conflictIdentity: fmt.Sprintf("membership_id=%s role_id=%s", r.MembershipID, r.RoleID),
		invalid:          membershiprole.ErrInvalidMembershipRoleRecord,
		invalidIdentity:  fmt.Sprintf("id=%s status=%s", r.ID, r.Status),
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
func (r *MembershipRoleRepository) Create(ctx context.Context, m *membershiprole.MembershipRole) error {
	if m == nil {
		return membershiprole.ErrNilMembershipRole
	}

	db := dBFromContext(ctx, r.db)
	// 推进与插入必须同处一个事务：事务由调用方通过 ports.Transactor 开启，
	// 这里复用同一个句柄，不自行开事务也不自行提交。
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
// 只写 membershipRoleUpdatableColumns 列出的列，updated_at 由触发器维护，本方法不写。
func (r *MembershipRoleRepository) Update(ctx context.Context, m *membershiprole.MembershipRole) error {
	if m == nil {
		return membershiprole.ErrNilMembershipRole
	}
	// 空标识先短路，理由与查询侧一致：id、tenant_id 都是 UUID 列，
	// 空串进去只会换来一次类型错误，而按端口口径这就是"要改的行不存在"。
	// 少了这一段，一次本该精确可判的 ErrMembershipRoleNotFound 会变成
	// 语义不明的驱动原生错误，最终落成 500。
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.TenantID) == "" {
		return fmt.Errorf("%w: id=%s tenant_id=%s",
			membershiprole.ErrMembershipRoleNotFound, m.ID, m.TenantID)
	}

	rec := toMembershipRoleRecord(m)
	db := dBFromContext(ctx, r.db)

	// 核心：WHERE 条件中加入 version = m.Version 乐观锁校验
	result := db.WithContext(ctx).
		Model(&membershipRoleRecord{}).
		Where("id = ? AND tenant_id = ? AND version = ?", m.ID, m.TenantID, m.Version).
		Select(membershipRoleUpdatableColumns).
		Updates(rec.toUpdateMap())

	if result.Error != nil {
		return translateMembershipRoleWriteError(result.Error, m)
	}

	// 影响行数为 0 时，判定是"数据不存在（404）"还是"并发冲突被修改（409）"
	if result.RowsAffected == 0 {
		var existing membershipRoleRecord
		err := db.WithContext(ctx).
			Select("id").
			Where("id = ? AND tenant_id = ?", m.ID, m.TenantID).
			Take(&existing).Error

		if err != nil {
			if !isRecordNotFound(err) {
				return fmt.Errorf("检查成员角色授权是否存在失败：%w", err)
			}
			return classifyMembershipRoleUpdate(false, m.ID)
		}

		// 记录存在，但 version 对不上 -> 说明被其他人并发修改过
		return classifyMembershipRoleUpdate(true, m.ID)
	}

	// 更新成功后，同步自增内存实体的版本号，方便后续连续调用
	m.Version++
	return nil
}

// classifyMembershipRoleUpdate 判定 Update 命中 0 行时的结局。
//
// 命中 0 行说明更新没有落到任何行，原因只有两种：
//   - 目标行压根不存在（exists 为 false）：调用方要改的东西不在库里；
//   - 目标行存在但版本号对不上（exists 为 true）：这行已被他人改过。
//
// 两种情形的处置完全不同：前者是调用方 bug（404），
// 后者要让调用方重新读取后再决定（409），绝不能静默覆盖。
//
// 单独成函数是为了让它能脱离数据库验证：命中 0 行之后的分支
// 在集成测试里要靠 Docker 才能跑到。
func classifyMembershipRoleUpdate(exists bool, id string) error {
	if exists {
		return membershiprole.ErrMembershipRoleConflict
	}
	return fmt.Errorf("%w: id=%s", membershiprole.ErrMembershipRoleNotFound, id)
}

// GetByID 按主键查询授权，未命中返回 (nil, nil)。
//
// uuid.Nil 表示"没有依据可查"，直接短路：把它打进 UUID 列只会换来一次
// 不知所云的类型转换错误，而约定的答案是 (nil, nil)。
//
// 本方法不带租户条件，取回后必须校验 TenantID，或直接改用 GetByMembershipAndRole。
func (r *MembershipRoleRepository) GetByID(ctx context.Context, id uuid.UUID) (*membershiprole.MembershipRole, error) {
	if id == uuid.Nil() {
		return nil, nil
	}
	return r.findOne(ctx, "id = ?", id.String())
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
	return r.findOne(ctx,
		"tenant_id = ? AND membership_id = ? AND role_id = ? AND status IN ?"+
			" AND (expires_at IS NULL OR expires_at > ?)",
		tenantID.String(), membershipID.String(), roleID.String(),
		membershipRoleNotExpiredStatuses, now)
}

// findOne 按条件取一条授权，未命中返回 (nil, nil)。
func (r *MembershipRoleRepository) findOne(
	ctx context.Context, cond string, args ...any,
) (*membershiprole.MembershipRole, error) {
	var rec membershipRoleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).Where(cond, args...).Take(&rec).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询成员角色授权失败：%w", err)
	}
	return rec.toEntity(), nil
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
	return r.listBy(ctx, "tenant_id = ? AND membership_id = ?", tenantID.String(), membershipID.String())
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
	return r.listBy(ctx,
		"tenant_id = ? AND membership_id = ? AND status = ?"+
			" AND (expires_at IS NULL OR expires_at > ?)",
		tenantID.String(), membershipID.String(), membershiprole.StatusActive, now)
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
	return r.listBy(ctx, "tenant_id = ? AND role_id = ?", tenantID.String(), roleID.String())
}

// listBy 按条件取一批授权，排序口径见端口注释的"排序与分页"小节。
//
// 无匹配时返回空切片而不是 nil：调用方可以统一用 len() 判断，不必区分 nil 与空。
func (r *MembershipRoleRepository) listBy(
	ctx context.Context, cond string, args ...any,
) ([]*membershiprole.MembershipRole, error) {
	var recs []membershipRoleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).
		Where(cond, args...).
		Order("created_at, id").
		Find(&recs).Error
	if err != nil {
		return nil, fmt.Errorf("查询成员角色授权列表失败：%w", err)
	}
	return membershipRoleEntities(recs), nil
}

// ListDueForExpiry 查询指定租户下已到期、但状态尚未推进的授权，
// 按 expires_at、id 升序。
//
// expires_at 为 NULL（长期有效）的行天然不满足 expires_at <= now，无需额外过滤。
//
// now 由调用方给出，且应当与随后 Expire 用的是同一个值：
// "扫出来的一批"与"逐个判死"若跨过了某个 expires_at，两次判定会得出不同结论。
func (r *MembershipRoleRepository) ListDueForExpiry(
	ctx context.Context, tenantID uuid.UUID, now time.Time,
) ([]*membershiprole.MembershipRole, error) {
	if tenantID == uuid.Nil() {
		return []*membershiprole.MembershipRole{}, nil
	}

	var recs []membershipRoleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).
		Where("tenant_id = ? AND status IN ? AND expires_at <= ?",
			tenantID.String(), membershipRoleNotExpiredStatuses, now).
		// 排序口径见端口注释：到期越久的越该先处理，且与 ListBy* 的 created_at 不同。
		Order("expires_at, id").
		Find(&recs).Error
	if err != nil {
		return nil, fmt.Errorf("查询到期授权失败：%w", err)
	}
	return membershipRoleEntities(recs), nil
}
