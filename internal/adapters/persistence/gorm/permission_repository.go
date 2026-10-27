package gorm

import (
	"context"
	"errors"
	"fmt"

	"github.com/memories-cn/user-management-api/internal/domain/permission"
	"gorm.io/gorm"
)

// PermissionRepository 用 GORM 实现 permission.Repository，只提供只读访问。
//
// 写能力刻意不在这里：它由 PermissionWriter 单独承载（见 permission_writer.go），
// 与本类型共用同一份 permissionRecord，避免同表出现两份会漂移的持久化模型。
type PermissionRepository struct {
	db *gorm.DB
}

var _ permission.Repository = (*PermissionRepository)(nil)

// NewPermissionRepository 创建系统权限目录仓储。
func NewPermissionRepository(db *gorm.DB) (*PermissionRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}
	return &PermissionRepository{db: db}, nil
}

// toEntity 把持久化模型还原成领域实体。
//
// 定义在读侧而不是写侧：Writer 只需要把 Entry 落库，不需要读回实体，
// 而读侧的三个方法都要做这一步。
func (r permissionRecord) toEntity() *permission.Permission {
	return &permission.Permission{
		ID:          r.ID,
		Code:        r.Code,
		Resource:    r.Resource,
		Action:      r.Action,
		Description: r.Description,
		CreatedAt:   r.CreatedAt,
	}
}

// permissionEntities 把一批持久化模型还原成领域实体，空输入返回空切片而非 nil。
func permissionEntities(recs []permissionRecord) []*permission.Permission {
	out := make([]*permission.Permission, 0, len(recs))
	for _, rec := range recs {
		out = append(out, rec.toEntity())
	}
	return out
}

// normalizePermissionCode 归一化单个权限代码。
//
// 返回值 empty 为 true 表示代码为空或只有空白——"没有依据可查"，
// 调用方据此返回 (nil, nil) 而不是报错。
//
// 非空但非法的代码不报错，而是同样按"未命中"处理：权限代码与角色标识一样
// 是业务 key 而不是 UUID，一个不存在的 code 本来就是"查不到"
// （口径见 RoleRepository.FindByKey）。若把格式错误当成调用方 bug 抛回去，
// 上层每查一个用户输入的 code 就得多接一类错误。
func normalizePermissionCode(code string) (normalized string, empty bool) {
	resource, action, err := permission.ParseCode(code)
	if err != nil {
		return "", true
	}
	return permission.BuildCode(resource, action), false
}

// normalizePermissionCodes 归一化并去重一组权限代码，保持首次出现的顺序。
//
// 与 normalizePermissionCode 的口径不同：批量查询是"我就要这几项"，
// 任一项写错都必须报出来（见 FindByCodes），因此非法代码在这里返回错误
// 而不是被当成未命中——静默丢弃会让调用方以为权限齐了，实际少了几项。
func normalizePermissionCodes(codes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		resource, action, err := permission.ParseCode(code)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", permission.ErrInvalidCode, code)
		}
		normalized := permission.BuildCode(resource, action)
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

// indexPermissionRecords 按权限代码建索引，供顺序还原使用。
func indexPermissionRecords(recs []permissionRecord) map[string]permissionRecord {
	out := make(map[string]permissionRecord, len(recs))
	for _, rec := range recs {
		out[rec.Code] = rec
	}
	return out
}

// orderPermissionsByCodes 按入参顺序还原查询结果，任一代码缺失即返回错误。
//
// 抽成纯函数是为了让它能被单独测试：顺序还原与缺失判定是 FindByCodes
// 两条最容易写错的契约，而它们藏在一次数据库往返之间。
//
// 缺失报 ErrUnknownCode（"这个词不在受控词表里"）而不是 ErrInvalidCode
// （"格式错了"）：能走到这里的代码格式都已由 normalizePermissionCodes 校验过，
// 缺的那一项是目录与数据库不同步，排障方向完全不同。
func orderPermissionsByCodes(codes []string, indexed map[string]permissionRecord) ([]*permission.Permission, error) {
	out := make([]*permission.Permission, 0, len(codes))
	for _, code := range codes {
		rec, found := indexed[code]
		if !found {
			return nil, fmt.Errorf("%w: %s", permission.ErrUnknownCode, code)
		}
		out = append(out, rec.toEntity())
	}
	return out, nil
}

// ListAll 返回系统权限目录，按 code 升序。
//
// 排序不是端口要求，但目录要拿去展示与比对，
// 没有稳定顺序时两次调用可能给出两份顺序不同的结果。
// code 上有唯一索引 idx_permissions_code，该排序直接走索引顺序。
func (r *PermissionRepository) ListAll(ctx context.Context) ([]*permission.Permission, error) {
	var recs []permissionRecord
	db := dBFromContext(ctx, r.db)
	if err := db.WithContext(ctx).Order("code").Find(&recs).Error; err != nil {
		return nil, fmt.Errorf("查询系统权限目录失败：%w", err)
	}
	return permissionEntities(recs), nil
}

// FindByCode 依据权限代码查询，未命中返回 (nil, nil)。
//
// 代码为空或格式非法同样返回 (nil, nil)：它是业务 key，
// 一个不存在的 key 本来就是"未命中"，格式错误不构成调用方 bug。
func (r *PermissionRepository) FindByCode(ctx context.Context, code string) (*permission.Permission, error) {
	normalized, empty := normalizePermissionCode(code)
	if empty {
		return nil, nil
	}

	var rec permissionRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).Where("code = ?", normalized).Take(&rec).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询权限失败：code=%s：%w", normalized, err)
	}
	return rec.toEntity(), nil
}

// FindByCodes 批量查询权限，返回顺序与入参一致；任一代码不存在即返回错误。
//
// 顺序一致是端口明写的契约：调用方拿结果去按位置对应自己传入的语义
// （先查后写角色权限），顺序一乱就会把权限挂到别的语义上。
// 因此这里不在 SQL 里靠 IN 的返回顺序，而是查回后按入参顺序重新排列。
//
// "任一不存在即返回错误"是为了避免把不存在的权限静默写进角色：
// 少返回一项时调用方通常只按长度判断是否取齐，静默丢弃会让它以为取齐了。
func (r *PermissionRepository) FindByCodes(ctx context.Context, codes []string) ([]*permission.Permission, error) {
	normalized, err := normalizePermissionCodes(codes)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return []*permission.Permission{}, nil
	}

	var recs []permissionRecord
	db := dBFromContext(ctx, r.db)
	err = db.WithContext(ctx).Where("code IN ?", normalized).Find(&recs).Error
	if err != nil {
		return nil, fmt.Errorf("批量查询权限失败：%w", err)
	}

	return orderPermissionsByCodes(normalized, indexPermissionRecords(recs))
}

// isRecordNotFound 报告错误是否为"未命中"。
//
// 读方法都要判它，单独成函数是为了让 gorm.ErrRecordNotFound 的引用只有一处：
// 未命中不是错误，漏判一次就会把一次正常的空结果变成 500。
func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
