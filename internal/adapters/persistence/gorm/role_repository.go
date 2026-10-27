package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
	domainrole "github.com/memories-cn/user-management-api/internal/domain/role"
	"gorm.io/gorm"
)

// rolePermissionBatchSize 是批量插入角色权限关联时每批的行数。
// 一个角色的权限通常在十余项量级，分批只是避免某天出现超大角色时一次性压进单条语句。
const rolePermissionBatchSize = 100

// RoleRepository 用 GORM 实现 domainrole.Repository。
type RoleRepository struct {
	db *gorm.DB
}

var _ domainrole.Repository = (*RoleRepository)(nil)

// NewRoleRepository 创建角色仓储。
func NewRoleRepository(db *gorm.DB) (*RoleRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}
	return &RoleRepository{db: db}, nil
}

// roleRecord 是 roles 表的持久化模型。
//
// updated_at 标为只读（<-:false）的理由与 userRecord 一致：
// 该列由数据库触发器统一维护（见 migrations/000003 的 trg_roles_set_updated_at，
// 触发器函数 set_updated_at 定义在 migrations/000001），
// 否则 GORM 的自动时间戳会与触发器抢写同一列。
type roleRecord struct {
	ID          string    `gorm:"column:id;primaryKey"`
	TenantID    string    `gorm:"column:tenant_id"`
	Key         string    `gorm:"column:key"`
	Name        string    `gorm:"column:name"`
	IsSystem    bool      `gorm:"column:is_system"`
	Description string    `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at;<-:false"`
}

// TableName 固定表名为 roles，不依赖 GORM 默认的复数转换。
func (roleRecord) TableName() string { return "roles" }

// toRoleRecord 把领域实体转成持久化模型。
func toRoleRecord(r *domainrole.Role) roleRecord {
	return roleRecord{
		ID:          r.ID,
		TenantID:    r.TenantID,
		Key:         r.Key,
		Name:        r.Name,
		IsSystem:    r.IsSystem,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// toEntity 把持久化模型还原成领域实体。
func (r roleRecord) toEntity() *domainrole.Role {
	return &domainrole.Role{
		ID:          r.ID,
		TenantID:    r.TenantID,
		Key:         r.Key,
		Name:        r.Name,
		IsSystem:    r.IsSystem,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// rolePermissionRecord 是 role_permissions 表的持久化模型。
//
// 三个 ID 列都没有外键（见 migrations/000008 的"刻意不加外键"小节），
// 引用完整性由本文件的写路径保证：只有 ReplacePermissions 会写这张表，
// 而它先校验角色属于本租户、且待写入的权限都真实存在。
type rolePermissionRecord struct {
	ID           string    `gorm:"column:id;primaryKey"`
	TenantID     string    `gorm:"column:tenant_id"`
	RoleID       string    `gorm:"column:role_id"`
	PermissionID string    `gorm:"column:permission_id"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at;<-:false"`
}

// TableName 固定表名为 role_permissions。
func (rolePermissionRecord) TableName() string { return "role_permissions" }

// requireUUID 校验一个引用别处实体的标识串是合法且非零的 UUID，返回规范形态。
//
// 只在写路径上使用：写操作拿着非法标识去查库，
// 最好的结果是得到一个不知所云的类型转换错误，
// 最坏的结果是悄悄写成一行脏数据。
//
// 归一化口径（去空白、解析、拒绝 uuid.Nil、输出小写规范形态）
// 与领域层的 NormalizePermissionIDs 一致，差别只在空串：
// 写路径把空串当非法标识（写操作必须有明确目标），
// 读路径把空串当"没有依据可查"（见 normalizeLookupID）。
func requireUUID(value string, invalid error) (string, error) {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil || parsed == uuid.Nil() {
		return "", fmt.Errorf("%w: %q", invalid, value)
	}
	return parsed.String(), nil
}

// normalizeLookupID 归一化读路径上的单个标识。
//
// 返回值 empty 为 true 表示该标识为空——"没有依据可查"，
// 调用方据此返回空结果而不是错误。
//
// 非空但非法的标识是另一种情况：那是调用方 bug
//（把租户 ID 当成角色 ID 传进来，或者 UUID 拼错了），
// 静默当成"查不到"会把一次程序缺陷伪装成正常的业务结果，
// 因此这里返回错误，与写路径 requireUUID 的口径一致。
//
// 归一化之后返回的是规范形态：带空白、大写、花括号写法的标识
// 会被折叠成库里真正存着的那一种，避免"判空用去空白后的值、
// 查询却用原始值"这种自相矛盾。
func normalizeLookupID(value string, invalid error) (normalized string, empty bool, err error) {
	if strings.TrimSpace(value) == "" {
		return "", true, nil
	}

	normalized, err = requireUUID(value, invalid)
	if err != nil {
		return "", false, err
	}
	return normalized, false, nil
}

// translateRoleWriteError 把数据库错误翻译成领域错误，不把驱动原生错误抛给上层。
//
// 只认 TranslateError 已经翻成 GORM 语义错误的那两类（见 bootstrap.OpenDB）：
//   - ErrDuplicatedKey（23505）：撞上唯一约束，归入 ErrRoleExists；
//   - ErrCheckConstraintViolated（23514）：值被 CHECK 约束拒绝，归入领域参数类错误。
//
// 单独成函数是为了让这两条判定能脱离数据库被测试：
// 它们只看错误类型，不需要真的连一次库。
func translateRoleWriteError(err error, role *domainrole.Role) error {
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: tenant_id=%s key=%s: %w",
			domainrole.ErrRoleExists, role.TenantID, role.Key, err)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: key=%s: %w", domainrole.ErrInvalidRoleKey, role.Key, err)
	default:
		return err
	}
}

// Create 写入角色，撞上 (tenant_id, key) 唯一约束时返回 ErrRoleExists。
//
// 错误怎么翻译见 translateRoleWriteError：它依赖 bootstrap 打开的 TranslateError，
// 先由 postgres 驱动把 23505 翻成 gorm.ErrDuplicatedKey，再转成领域错误，
// 不把驱动原生错误抛给上层。
func (r *RoleRepository) Create(ctx context.Context, role *domainrole.Role) error {
	if role == nil {
		return domainrole.ErrNilRole
	}

	rec := toRoleRecord(role)
	db := dBFromContext(ctx, r.db)
	if err := db.WithContext(ctx).Create(&rec).Error; err != nil {
		return translateRoleWriteError(err, role)
	}
	return nil
}

// FindByID 查询指定租户下的角色，未命中返回 (nil, nil)。
//
// 标识为空表示"没有依据可查"，返回 (nil, nil)；
// 标识非空但非法（不是合法 UUID）返回字段级错误——
// 那是调用方 bug，静默当成"查不到"会把程序缺陷伪装成正常的业务结果。
// 口径与两个 List 方法一致。
func (r *RoleRepository) FindByID(ctx context.Context, tenantID, roleID string) (*domainrole.Role, error) {
	tenant, empty, err := normalizeLookupID(tenantID, domain.ErrInvalidTenantID)
	if err != nil {
		return nil, err
	}
	if empty {
		return nil, nil
	}

	role, empty, err := normalizeLookupID(roleID, domain.ErrInvalidRoleID)
	if err != nil {
		return nil, err
	}
	if empty {
		return nil, nil
	}

	return r.findOne(ctx, "tenant_id = ? AND id = ?", tenant, role)
}

// FindByKey 查询指定租户下指定标识的角色，未命中返回 (nil, nil)。
//
// 租户标识与 FindByID 同口径：空则不查，非法则报错。
// 角色标识是业务 key 而不是 UUID，取值非法不构成错误：
// 一个不存在的 key 本来就是"未命中"，返回 (nil, nil) 即可。
func (r *RoleRepository) FindByKey(ctx context.Context, tenantID, key string) (*domainrole.Role, error) {
	tenant, empty, err := normalizeLookupID(tenantID, domain.ErrInvalidTenantID)
	if err != nil {
		return nil, err
	}
	if empty {
		return nil, nil
	}

	normalizedKey := domainrole.NormalizeKey(key)
	if normalizedKey == "" {
		return nil, nil
	}

	return r.findOne(ctx, "tenant_id = ? AND key = ?", tenant, normalizedKey)
}

// findOne 按条件取一条角色，任一条件值为空即短路返回 (nil, nil)。
//
// 空值在这里统一短路，而不是让每个 Find 各判一次：
// 租户 ID 或角色 ID 缺失意味着"这次查询没有依据"，不是"有一条待查的数据"，
// 查库只会白白消耗一次往返，更糟的是会把空字符串打进 UUID 列。
//
// 调用方（FindByID / FindByKey）已经先判过空并做过归一化，
// 这里的判空是兜底：保证"条件为空绝不进 SQL"这条约定只有一处实现。
func (r *RoleRepository) findOne(ctx context.Context, cond string, args ...string) (*domainrole.Role, error) {
	for _, arg := range args {
		if arg == "" {
			return nil, nil
		}
	}

	var rec roleRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).Where(cond, toAnys(args)...).Take(&rec).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return rec.toEntity(), nil
}

// toAnys 把字符串参数转成 GORM 占位符所需的 []any。
func toAnys(args []string) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

// ListPermissionIDs 取回角色当前的权限标识集合。
//
// 只查 role_permissions 本身：表上冗余了 tenant_id，
// 租户条件可以直接命中 idx_role_permissions_tenant_role，不必 join roles。
//
// 两个标识先归一化再查询：去空白、大写、花括号写法都会被折叠成
// 库里真正存着的那一种，避免"判空用去空白后的值、查询却用原始值"
// 这种自相矛盾——后者会让一次合法查询静默地返回空集。
//
// 标识为空时返回空切片（没有依据可查，不是错误）；
// 标识非空但非法时返回字段级错误（那是调用方 bug，
// 静默当成"查不到"会把程序缺陷伪装成正常的业务结果）。
func (r *RoleRepository) ListPermissionIDs(ctx context.Context, tenantID, roleID string) ([]string, error) {
	tenant, empty, err := normalizeLookupID(tenantID, domain.ErrInvalidTenantID)
	if err != nil {
		return nil, err
	}
	if empty {
		return []string{}, nil
	}

	role, empty, err := normalizeLookupID(roleID, domain.ErrInvalidRoleID)
	if err != nil {
		return nil, err
	}
	if empty {
		return []string{}, nil
	}

	var permissionIDs []string
	db := dBFromContext(ctx, r.db)
	err = db.WithContext(ctx).
		Model(&rolePermissionRecord{}).
		Where("tenant_id = ? AND role_id = ?", tenant, role).
		Pluck("permission_id", &permissionIDs).Error
	if err != nil {
		return nil, fmt.Errorf("查询角色权限关联失败：tenant_id=%s role_id=%s：%w", tenant, role, err)
	}

	if permissionIDs == nil {
		return []string{}, nil
	}
	return permissionIDs, nil
}

// ListRoleIDsByPermissionID 取回本租户内持有某项权限的全部角色。
//
// 单表查询即可完成：role_permissions 冗余了 tenant_id，
// 不必 join roles 也能把结果限制在本租户内。
// idx_role_permissions_permission_role 以 permission_id 打头，
// 恰好是该过滤条件的前缀列。
//
// 标识的归一化与空值语义同 ListPermissionIDs，不再重复。
func (r *RoleRepository) ListRoleIDsByPermissionID(ctx context.Context, tenantID, permissionID string) ([]string, error) {
	tenant, empty, err := normalizeLookupID(tenantID, domain.ErrInvalidTenantID)
	if err != nil {
		return nil, err
	}
	if empty {
		return []string{}, nil
	}

	perm, empty, err := normalizeLookupID(permissionID, domain.ErrInvalidPermissionID)
	if err != nil {
		return nil, err
	}
	if empty {
		return []string{}, nil
	}

	var roleIDs []string
	db := dBFromContext(ctx, r.db)
	err = db.WithContext(ctx).
		Model(&rolePermissionRecord{}).
		Where("tenant_id = ? AND permission_id = ?", tenant, perm).
		Order("role_id").
		Pluck("role_id", &roleIDs).Error
	if err != nil {
		return nil, fmt.Errorf("反查持有权限的角色失败：tenant_id=%s permission_id=%s：%w", tenant, perm, err)
	}

	if roleIDs == nil {
		return []string{}, nil
	}
	return roleIDs, nil
}

// ReplacePermissions 用给定权限集合整体替换角色的权限关联。
//
// 写入前依次做三件事：归一化权限标识（domainrole.NormalizePermissionIDs）、
// 校验租户与角色标识、确认角色确实属于本租户且待写入的权限都真实存在。
// 三者缺一都可能在没有外键的表上留下脏行：tenant_id 与 permission_id 两列
// 都没有任何数据库约束兜底，这里是唯一的防线。
//
// 整个替换是原子的：删除旧关联与插入新关联要么都生效，要么都不生效。
// 事务边界由本方法自己保证——调用方无需预先开启事务；
// 若调用方已经开启了事务（句柄经 context 传入），
// GORM 会以 SAVEPOINT 把本方法嵌套进外层事务，内外两层的事务语义都成立。
//
// 写法故意取差分而不是"全删再全插"：保留下来的那部分权限的 created_at 不动，
// 也让没有实际变化的一次替换退化成零写入。
func (r *RoleRepository) ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error {
	// 先收敛入参：非法标识在这里就停下，绝不能带着半成品进入数据库。
	desired, err := domainrole.NormalizePermissionIDs(permissionIDs)
	if err != nil {
		return err
	}

	tenantID, err = requireUUID(tenantID, domain.ErrInvalidTenantID)
	if err != nil {
		return err
	}
	roleID, err = requireUUID(roleID, domain.ErrInvalidRoleID)
	if err != nil {
		return err
	}

	db := dBFromContext(ctx, r.db)

	// 事务由本方法开启：删除与插入必须同生共死。
	// 若不包事务，"删除已提交、插入失败"会让角色半途失去部分权限，
	// 违反端口契约"一次失败的替换不能让角色半途失去部分权限"。
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return r.replacePermissions(ctx, tx, tenantID, roleID, desired)
	})
}

// replacePermissions 在给定事务句柄上执行替换的实际步骤。
//
// 单独成函数是为了让事务边界清晰：外层负责开启事务，
// 本函数只管在事务里干活，不再自己开事务。
//
// 已知覆盖缺口：本函数及其调用的两处查询（roleExistsInTenant、
// ensurePermissionsExist 的 Count）都需要真实数据库，
// 因此按本仓"不连库"的既定手法未被用例覆盖。
// 被它们包住的判定——差分计算、存在性计数、归属失败分类——
// 已分别抽成纯函数并有对应用例；SQL 本身的正确性需要集成测试补上。
func (r *RoleRepository) replacePermissions(ctx context.Context, db *gorm.DB, tenantID, roleID string, desired []string) error {
	// 两道防线都必须在删除旧关联之前完成：
	// 否则一次失败会先丢掉角色原有的权限，再发现问题。
	//
	// 顺序也有讲究：先确认目标（角色）属于本租户，再确认载荷（权限）真实存在。
	// 跨租户访问与"权限不存在"同时成立时，先报前者——
	// 它是契约明写的 ErrRoleNotFound，也更接近调用方真正想知道的事：
	// "这个角色我根本不该碰"。
	ok, err := roleExistsInTenant(ctx, db, tenantID, roleID)
	if err != nil {
		return err
	}
	if !ok {
		return roleNotFoundError(tenantID, roleID)
	}

	if err := ensurePermissionsExist(ctx, db, desired); err != nil {
		return err
	}

	var existing []string
	err = db.WithContext(ctx).
		Model(&rolePermissionRecord{}).
		Where("tenant_id = ? AND role_id = ?", tenantID, roleID).
		Pluck("permission_id", &existing).Error
	if err != nil {
		return fmt.Errorf("查询角色权限关联失败：tenant_id=%s role_id=%s：%w", tenantID, roleID, err)
	}

	toRemove, toInsert := diffPermissions(existing, desired)

	if len(toRemove) > 0 {
		err := db.WithContext(ctx).
			Where("tenant_id = ? AND role_id = ? AND permission_id IN ?", tenantID, roleID, toRemove).
			Delete(&rolePermissionRecord{}).Error
		if err != nil {
			return fmt.Errorf("删除角色权限关联失败：tenant_id=%s role_id=%s：%w", tenantID, roleID, err)
		}
	}

	if len(toInsert) == 0 {
		return nil
	}

	// 同一批插入共用同一个时间基准，让一次替换产生的行有整齐的 created_at。
	now := time.Now()
	records := make([]rolePermissionRecord, 0, len(toInsert))
	for _, permissionID := range toInsert {
		records = append(records, rolePermissionRecord{
			ID:           uuid.NewV7().String(),
			TenantID:     tenantID,
			RoleID:       roleID,
			PermissionID: permissionID,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	if err := db.WithContext(ctx).CreateInBatches(&records, rolePermissionBatchSize).Error; err != nil {
		return fmt.Errorf("写入角色权限关联失败：tenant_id=%s role_id=%s：%w", tenantID, roleID, err)
	}
	return nil
}

// diffPermissions 计算从现状到目标集合的差分。
//
// 抽成纯函数是为了让它能被单独测试：
// 差分算反了（删掉该保留的、留下该删的）是这类实现最容易犯的错，
// 而它藏在数据库往返之间，真出问题时只能靠线上数据才发现。
//
// 语义：
//   - toRemove：现状里有、目标里没有的，需要删除；
//   - toInsert：目标里有、现状里没有的，需要插入；
//   - 两者都有的关联不动，因此保留下来的那部分 created_at 保持不变。
//
// 两份入参都已归一化（都是 permissions.id 的 UUID 字符串），
// 因此可以直接按字符串比较，无需再考虑大小写或空白。
func diffPermissions(existing, desired []string) (toRemove, toInsert []string) {
	current := make(map[string]struct{}, len(existing))
	for _, id := range existing {
		current[id] = struct{}{}
	}

	target := make(map[string]struct{}, len(desired))
	for _, id := range desired {
		target[id] = struct{}{}
	}

	for _, id := range existing {
		if _, keep := target[id]; !keep {
			toRemove = append(toRemove, id)
		}
	}

	for _, id := range desired {
		if _, exists := current[id]; !exists {
			toInsert = append(toInsert, id)
		}
	}

	return toRemove, toInsert
}

// ensurePermissionsExist 校验给定的权限标识在系统权限目录中都真实存在。
//
// role_permissions 表刻意不加外键（见 migrations/000008 的"刻意不加外键"小节），
// 引用完整性由应用层兜底：角色归属由 roleExistsInTenant 校验，
// 权限存在性由本函数校验。缺了任何一半，写路径都可能留下孤儿行——
// 角色"持有"了一项并不存在的权限，授权界面显示已授权，实际却无法鉴权，
// 而 ListPermissionIDs 会把它原样读回来，调用方无从分辨真伪。
//
// 校验必须发生在删除旧关联之前：否则一次失败会先丢掉角色原有的权限，
// 再发现新权限里有不合法的那一项。
//
// 这里复用 permission_writer.go 里定义的 permissionRecord：
// 同包内同一张表只保留一份持久化模型，两处定义反而可能漂移。
func ensurePermissionsExist(ctx context.Context, db *gorm.DB, permissionIDs []string) error {
	if len(permissionIDs) == 0 {
		return nil
	}

	var count int64
	err := db.WithContext(ctx).
		Model(&permissionRecord{}).
		Where("id IN ?", permissionIDs).
		Count(&count).Error
	if err != nil {
		return fmt.Errorf("校验权限存在性失败：%w", err)
	}
	return verifyPermissionCount(int(count), len(permissionIDs))
}

// verifyPermissionCount 判定存在性校验的结果：查到的条数少于应有的条数，
// 说明有权限标识在系统权限目录里根本不存在。
//
// 单独成函数是为了让这条判定能脱离数据库被测试——
// 它是"不留下孤儿行"这道防线的唯一落点，
// 而判定本身只需要两个数字，不需要真的连一次库。
func verifyPermissionCount(found, want int) error {
	if found == want {
		return nil
	}
	return fmt.Errorf("%w: %d 项权限不存在于系统权限目录",
		domain.ErrInvalidPermissionID, want-found)
}

// roleNotFoundError 构造"角色不属于本租户"的错误。
//
// 单独成函数是为了让这条防线能脱离数据库被测试：
// role_permissions 无外键兜底，跨租户写入的唯一防线就在这里。
func roleNotFoundError(tenantID, roleID string) error {
	return fmt.Errorf("%w: tenant_id=%s role_id=%s", domainrole.ErrRoleNotFound, tenantID, roleID)
}

// roleExistsInTenant 确认角色属于给定租户。
//
// 只取 id 一列并加 LIMIT 1：这里要的只是"有没有"，
// 完整实体加载会白白多读一半字段。
func roleExistsInTenant(ctx context.Context, db *gorm.DB, tenantID, roleID string) (bool, error) {
	var id string
	err := db.WithContext(ctx).
		Model(&roleRecord{}).
		Select("id").
		Where("id = ? AND tenant_id = ?", roleID, tenantID).
		Take(&id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("校验角色归属失败：%w", err)
	}
	return true, nil
}
