package gorm

import (
	"context"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/membership"
	"gorm.io/gorm"
)

// MembershipRepository 用 GORM 实现 membership.Repository。
type MembershipRepository struct {
	db *gorm.DB
}

var _ membership.Repository = (*MembershipRepository)(nil)

// NewMembershipRepository 创建成员关系仓储。
func NewMembershipRepository(db *gorm.DB) (*MembershipRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}
	return &MembershipRepository{db: db}, nil
}

// membershipRecord 是 memberships 表的持久化模型。
//
// 三个业务时间戳用指针类型：库里以 NULL 表示"尚未发生"
// （见 migrations/000007 的列注释），而领域层用 time.Time 零值表达同一语义。
// 不用指针就会把零值时间（0001-01-01）写进列里——
// 它既不是"空"也不是一个有意义的时间点，还会撞上
// chk_memberships_status_timestamps（invited 要求三个时间戳全为 NULL）。
//
// updated_at 标为只读（<-:false）的理由与 userRecord 一致：
// 该列由数据库触发器维护（见 migrations/000007 的 trg_memberships_set_updated_at），
// 否则 GORM 的自动时间戳会与触发器抢写同一列。
type membershipRecord struct {
	ID          string     `gorm:"column:id;primaryKey"`
	TenantID    string     `gorm:"column:tenant_id"`
	UserID      string     `gorm:"column:user_id"`
	Status      string     `gorm:"column:status"`
	InvitedBy   string     `gorm:"column:invited_by"`
	InvitedAt   time.Time  `gorm:"column:invited_at"`
	JoinedAt    *time.Time `gorm:"column:joined_at"`
	SuspendedAt *time.Time `gorm:"column:suspended_at"`
	LeftAt      *time.Time `gorm:"column:left_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at;<-:false"`
}

// TableName 固定表名为 memberships，不依赖 GORM 默认的复数转换。
func (membershipRecord) TableName() string { return "memberships" }

// membershipUpdatableColumns 是 Update 允许写入的列，与 toUpdateMap 的键一一对应。
//
// 刻意不含 id、tenant_id、user_id：前三者是成员关系的业务键，
// 一条记录只刻画"某用户在某租户中的当前成员资格"（见端口注释的"重新邀请"小节），
// 换了其中任何一个都已经是另一条关系，而不是"改了一下"。
// created_at 与 updated_at 同理排除：一个不可变，一个归触发器维护。
var membershipUpdatableColumns = []string{
	"status",
	"invited_by",
	"invited_at",
	"joined_at",
	"suspended_at",
	"left_at",
}

// toMembershipRecord 把领域实体转成持久化模型。
func toMembershipRecord(m *membership.Membership) membershipRecord {
	return membershipRecord{
		ID:          m.ID,
		TenantID:    m.TenantID,
		UserID:      m.UserID,
		Status:      string(m.Status),
		InvitedBy:   m.InvitedBy,
		InvitedAt:   m.InvitedAt,
		JoinedAt:    toNullableTime(m.JoinedAt),
		SuspendedAt: toNullableTime(m.SuspendedAt),
		LeftAt:      toNullableTime(m.LeftAt),
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
}

// toEntity 把持久化模型还原成领域实体。
func (r membershipRecord) toEntity() *membership.Membership {
	return &membership.Membership{
		ID:          r.ID,
		TenantID:    r.TenantID,
		UserID:      r.UserID,
		Status:      membership.Status(r.Status),
		InvitedBy:   r.InvitedBy,
		InvitedAt:   r.InvitedAt,
		JoinedAt:    fromNullableTime(r.JoinedAt),
		SuspendedAt: fromNullableTime(r.SuspendedAt),
		LeftAt:      fromNullableTime(r.LeftAt),
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// toUpdateMap 给出 Update 的列值对，键集合必须与 membershipUpdatableColumns 一致。
//
// 用 map 而不是结构体：按结构体更新时零值字段会被 GORM 跳过，
// 而"清空 SuspendedAt"（恢复成员身份）正是要把一个列写成 NULL。
func (r membershipRecord) toUpdateMap() map[string]any {
	return map[string]any{
		"status":       r.Status,
		"invited_by":   r.InvitedBy,
		"invited_at":   r.InvitedAt,
		"joined_at":    r.JoinedAt,
		"suspended_at": r.SuspendedAt,
		"left_at":      r.LeftAt,
	}
}

// membershipEntities 把一批持久化模型还原成领域实体，空输入返回空切片而非 nil。
func membershipEntities(recs []membershipRecord) []*membership.Membership {
	out := make([]*membership.Membership, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.toEntity())
	}
	return out
}

// membershipWriteTargets 组装成员关系写操作的结局配置。
func membershipWriteTargets(m *membership.Membership) writeTargets {
	return writeTargets{
		conflict:         membership.ErrMembershipExists,
		conflictIdentity: fmt.Sprintf("tenant_id=%s user_id=%s", m.TenantID, m.UserID),
		invalid:          membership.ErrInvalidMembershipRecord,
		invalidIdentity:  fmt.Sprintf("id=%s status=%s", m.ID, m.Status),
		byConstraint: map[string]constraintOutcome{
			// 主键重复是写入本身的缺陷，与"成员关系已存在"无关。
			"memberships_pkey": {
				err:      domain.ErrDuplicateID,
				identity: fmt.Sprintf("id=%s", m.ID),
			},
			// 业务唯一索引：同一租户下同一用户只能有一条成员关系。
			"idx_memberships_tenant_user": {
				err:      membership.ErrMembershipExists,
				identity: fmt.Sprintf("tenant_id=%s user_id=%s", m.TenantID, m.UserID),
			},
			// 字段级 CHECK：与领域层的同名字段错误同一口径。
			"chk_memberships_invited_by_not_blank": {
				err:      domain.ErrInvalidActor,
				identity: fmt.Sprintf("invited_by=%s", m.InvitedBy),
			},
			// 下面两条领域层没有同口径的字段级错误：
			// 状态取值域与状态时间戳自洽都只在绕过领域层时才会触发，
			// 它们是两侧口径漂移的信号，仍归记录级错误。
			"chk_memberships_status": {
				err:      membership.ErrInvalidMembershipRecord,
				identity: fmt.Sprintf("id=%s status=%s", m.ID, m.Status),
			},
			"chk_memberships_status_timestamps": {
				err:      membership.ErrInvalidMembershipRecord,
				identity: fmt.Sprintf("id=%s status=%s", m.ID, m.Status),
			},
		},
	}
}

// translateMembershipWriteError 把数据库错误翻译成领域错误，两条判定见
// classifyWriteError。留这个薄壳是为了让调用处的意图自带聚合语义，
// 不必知道 writeTargets 要怎么拼。
func translateMembershipWriteError(err error, m *membership.Membership) error {
	return classifyWriteError(err, membershipWriteTargets(m))
}

// translateMembershipUpdateResult 判定 Update 的执行结果：出错翻译成领域错误，
// 没改到任何行返回 ErrMembershipNotFound。两条判定见 classifyUpdateResult。
func translateMembershipUpdateResult(result *gorm.DB, m *membership.Membership) error {
	return classifyUpdateResult(result, updateTargets{
		write:    membershipWriteTargets(m),
		notFound: membership.ErrMembershipNotFound,
		identity: fmt.Sprintf("id=%s", m.ID),
	})
}

// Create 写入成员关系，撞上 (tenant_id, user_id) 唯一约束时返回 ErrMembershipExists。
//
// 错误怎么翻译见 translateMembershipWriteError。
func (r *MembershipRepository) Create(ctx context.Context, m *membership.Membership) error {
	if m == nil {
		return membership.ErrNilMembership
	}

	rec := toMembershipRecord(m)
	db := dBFromContext(ctx, r.db)
	if err := db.WithContext(ctx).Create(&rec).Error; err != nil {
		return translateMembershipWriteError(err, m)
	}
	return nil
}

// Update 全量更新成员关系的业务字段，记录不存在时返回 ErrMembershipNotFound。
//
// 条件里刻意带上 tenant_id：成员关系是租户边界内的对象，
// 只按主键更新时，一条来自别处（或被串改）的 id 就能改到别的租户的行。
// 这也是端口"任何写入都必须以 tenant_id 为条件"的落地。
//
// 只写 membershipUpdatableColumns 列出的列，updated_at 由触发器维护，本方法不写。
func (r *MembershipRepository) Update(ctx context.Context, m *membership.Membership) error {
	if m == nil {
		return membership.ErrNilMembership
	}
	// 空标识先短路，理由与查询侧一致：id、tenant_id 都是 UUID 列，
	// 空串进去只会换来一次类型错误，而按端口口径这就是"要改的行不存在"。
	// 少了这一段，一次本该精确可判的 ErrMembershipNotFound 会变成
	// 语义不明的驱动原生错误，最终落成 500。
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.TenantID) == "" {
		return fmt.Errorf("%w: id=%s tenant_id=%s",
			membership.ErrMembershipNotFound, m.ID, m.TenantID)
	}

	rec := toMembershipRecord(m)
	db := dBFromContext(ctx, r.db)
	result := db.WithContext(ctx).
		Model(&membershipRecord{}).
		Where("id = ? AND tenant_id = ?", m.ID, m.TenantID).
		Select(membershipUpdatableColumns).
		Updates(rec.toUpdateMap())
	return translateMembershipUpdateResult(result, m)
}

// GetByID 按主键查询成员关系，未命中返回 (nil, nil)。
//
// uuid.Nil 表示"没有依据可查"，直接短路：把它打进 UUID 列只会换来一次
// 不知所云的类型转换错误，而约定的答案是 (nil, nil)。
func (r *MembershipRepository) GetByID(ctx context.Context, id uuid.UUID) (*membership.Membership, error) {
	if id == uuid.Nil() {
		return nil, nil
	}
	return r.findOne(ctx, "id = ?", id.String())
}

// GetByTenantAndUser 查询指定租户下指定用户的成员关系，未命中返回 (nil, nil)。
//
// 租户内的日常鉴权一律走本方法而不是 GetByID：它是唯一自带租户条件的查询，
// 取回的结果不必再校验 TenantID。
func (r *MembershipRepository) GetByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) (*membership.Membership, error) {
	if tenantID == uuid.Nil() || userID == uuid.Nil() {
		return nil, nil
	}
	return r.findOne(ctx, "tenant_id = ? AND user_id = ?", tenantID.String(), userID.String())
}

// findOne 按条件取一条成员关系，未命中返回 (nil, nil)。
func (r *MembershipRepository) findOne(ctx context.Context, cond string, args ...string) (*membership.Membership, error) {
	var rec membershipRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).Where(cond, toAnys(args)...).Take(&rec).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询成员关系失败：%w", err)
	}
	return rec.toEntity(), nil
}

// GetByTenantID 查询指定租户下的全部成员关系，按 created_at、id 升序。
//
// (tenant_id, created_at, id) 三列上有 idx_memberships_tenant_created_at，
// 列顺序与排序键一致，扫描直接走索引顺序。
func (r *MembershipRepository) GetByTenantID(ctx context.Context, tenantID uuid.UUID) ([]*membership.Membership, error) {
	if tenantID == uuid.Nil() {
		return []*membership.Membership{}, nil
	}
	return r.listBy(ctx, "tenant_id = ?", tenantID.String())
}

// GetByUserID 查询指定用户在全部租户下的成员关系（跨租户查询），
// 按 created_at、id 升序。
//
// 本方法是端口刻意保留的唯一跨租户查询，调用方必须在使用前完成按租户的鉴权
// （见端口注释的"租户隔离"小节），实现方不再重复兜底。
func (r *MembershipRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]*membership.Membership, error) {
	if userID == uuid.Nil() {
		return []*membership.Membership{}, nil
	}
	return r.listBy(ctx, "user_id = ?", userID.String())
}

// listBy 按条件取一批成员关系，排序口径见端口注释的"排序与分页"小节。
//
// 无匹配时返回空切片而不是 nil：调用方可以统一用 len() 判断，不必区分 nil 与空。
func (r *MembershipRepository) listBy(ctx context.Context, cond string, args ...string) ([]*membership.Membership, error) {
	var recs []membershipRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).
		Where(cond, toAnys(args)...).
		Order("created_at, id").
		Find(&recs).Error
	if err != nil {
		return nil, fmt.Errorf("查询成员关系列表失败：%w", err)
	}
	return membershipEntities(recs), nil
}
