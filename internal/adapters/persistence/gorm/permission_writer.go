package gorm

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain/permission"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// permissionUpsertBatchSize 是单次 UPSERT 的批大小。
// 权限目录只有十余项，批量只是为了让实现不依赖"目录一定很小"这个假设。
const permissionUpsertBatchSize = 100

// permissionRecord 是 permissions 表的持久化模型。
//
// 刻意没有 UpdatedAt：permissions 表本身没有 updated_at 列
// （见 migrations/000004）。权限是不可变的受控词表，除文案外没有可更新的业务字段，
// 连"最后更新于"也没有落地的必要。多写一列就会让 GORM 去更新一个不存在的列。
type permissionRecord struct {
	ID          string    `gorm:"column:id;primaryKey"`
	Code        string    `gorm:"column:code"`
	Resource    string    `gorm:"column:resource"`
	Action      string    `gorm:"column:action"`
	Description string    `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

// TableName 固定表名为 permissions，理由同 userRecord：
// 不依赖 GORM 默认的复数转换，避免重命名结构体时悄悄换了一张表。
func (permissionRecord) TableName() string { return "permissions" }

// newPermissionRecord 由目录项构造待写入的行。
//
// ID 与 CreatedAt 在这里生成：目录项只描述"要有什么权限"，
// 主键与落库时间是存储层的属性，不该出现在 domain/permission.Entry 里。
func newPermissionRecord(e permission.Entry) permissionRecord {
	return permissionRecord{
		ID:          uuid.NewV7().String(),
		Code:        e.Code,
		Resource:    e.Resource,
		Action:      e.Action,
		Description: e.Description,
		CreatedAt:   time.Now(),
	}
}

// PermissionWriter 用 GORM 实现 permission.Writer，是 Seeder 唯一的落地部件。
type PermissionWriter struct {
	db *gorm.DB
}

var _ permission.Writer = (*PermissionWriter)(nil)

// NewPermissionWriter 创建权限目录写入器。
func NewPermissionWriter(db *gorm.DB) (*PermissionWriter, error) {
	if db == nil {
		return nil, ErrMissingDB
	}
	return &PermissionWriter{db: db}, nil
}

// EnsureCatalog 幂等写入权限目录，实现见 permission.Writer 的端口注释。
//
// 三步：先按 code 查一次库里已有哪些、再把目录项分成"要写"与"不用动"，
// 最后只对可能需要写的那部分发 UPSERT。
// 先查询不是为了判断要不要插入——真正保证幂等的是 UPSERT 本身——
// 而是为了让调用方拿到准确的 Inserted/Updated/Unchanged 三项统计：
// 没有这次查询就只能笼统地报告"同步完成"，看不出到底有没有改动。
//
// 分类与计数的判定在 diffCatalog 里完成（纯函数，可单独测试）；
// 本方法只负责数据库往返。
func (w *PermissionWriter) EnsureCatalog(ctx context.Context, entries []permission.Entry) (permission.SeedResult, error) {
	if err := permission.ValidateCatalog(entries); err != nil {
		return permission.SeedResult{}, err
	}
	if len(entries) == 0 {
		return permission.SeedResult{}, nil
	}

	db := dBFromContext(ctx, w.db)

	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}

	var existing []permissionRecord
	err := db.WithContext(ctx).
		Select("code", "description").
		Where("code IN ?", codes).
		Find(&existing).Error
	if err != nil {
		return permission.SeedResult{}, fmt.Errorf("查询已有权限目录失败：%w", err)
	}

	currentDesc := make(map[string]string, len(existing))
	for _, rec := range existing {
		currentDesc[rec.Code] = rec.Description
	}

	result, toWrite := diffCatalog(entries, currentDesc)

	// 一份都没变，连 UPSERT 都不必发。
	if len(toWrite) == 0 {
		return result, nil
	}

	records := make([]permissionRecord, 0, len(toWrite))
	for _, e := range toWrite {
		records = append(records, newPermissionRecord(e))
	}

	// ON CONFLICT (code) 是幂等的真正保障：
	// 先查再插的做法在两次执行之间没有锁，并发时仍会撞上唯一约束。
	// 冲突时只更新文案——resource 与 action 由 CHECK 约束与 code 强制一致，
	// 一条已存在的权限不可能需要改这两个值。
	err = db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "code"}},
			DoUpdates: clause.AssignmentColumns([]string{"description"}),
		}).
		CreateInBatches(&records, permissionUpsertBatchSize).Error
	if err != nil {
		return permission.SeedResult{}, fmt.Errorf("写入权限目录失败：%w", err)
	}

	return result, nil
}

// diffCatalog 计算目录项与库中现状的差分。
//
// 返回值有两层含义：
//   - result 是本次同步的三项统计（新增/更新/未变），
//     三者互斥且覆盖全部目录项；
//   - toWrite 是需要落库的目录项，未变的项不在其中。
//
// 抽成纯函数是为了让它能被单独测试：
// 三项计数必须互斥且求和等于目录项总数——漏计一项，
// "这次到底改了什么"的汇报就会失真，而它藏在一次数据库往返之间。
func diffCatalog(entries []permission.Entry, current map[string]string) (permission.SeedResult, []permission.Entry) {
	var result permission.SeedResult
	toWrite := make([]permission.Entry, 0, len(entries))

	for _, e := range entries {
		desc, found := current[e.Code]
		switch {
		case !found:
			result.Inserted++
			toWrite = append(toWrite, e)
		case desc != e.Description:
			// 文案变了：把它一并写进去，让 description 始终跟着代码里的目录走。
			result.Updated++
			toWrite = append(toWrite, e)
		default:
			result.Unchanged++
		}
	}

	return result, toWrite
}
