package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainuser "github.com/memories-cn/user-management-api/internal/domain/user"
	"gorm.io/gorm"
)

var ErrMissingDB = errors.New("gorm database is required")

type UserRepository struct {
	db *gorm.DB
}

func NewUserRepository(
	db *gorm.DB,
) (*UserRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}

	return &UserRepository{db: db}, nil
}

var _ domainuser.Repository = (*UserRepository)(nil)

// userRecord 是 users 表的持久化模型。
//
// 领域实体不得出现 GORM 标签，因此列名映射与写入权限都收敛在本类型上：
//   - updated_at 由数据库触发器维护（见 migrations/000001），故标为只读（<-:false），
//     否则 GORM 的自动时间戳会与触发器抢写同一列，
//     破坏"updated_at 只有数据库一个写入方"的约定；
//   - 其余列与 domainuser.User 一一对应，转换只发生在 toUserRecord 与 toEntity，
//     领域层对外始终只看到实体，看不到表结构。
type userRecord struct {
	ID            string    `gorm:"column:id;primaryKey"`
	Username      string    `gorm:"column:username"`
	DisplayName   string    `gorm:"column:display_name"`
	Email         string    `gorm:"column:email"`
	Phone         string    `gorm:"column:phone"`
	AvatarURL     string    `gorm:"column:avatar_url"`
	ZitadelUserID string    `gorm:"column:zitadel_user_id"`
	Status        string    `gorm:"column:status"`
	EmailVerified bool      `gorm:"column:email_verified"`
	CreatedAt     time.Time `gorm:"column:created_at"`
	UpdatedAt     time.Time `gorm:"column:updated_at;<-:false"`
}

// TableName 固定表名为 users，不依赖 GORM 默认的复数转换，
// 避免重命名实体时悄悄换了一张表。
func (userRecord) TableName() string { return "users" }

// userUpdatableColumns 是 Update 允许写入的列，与 toUpdateMap 的键一一对应。
//
// 刻意不含 id、created_at、updated_at：前两者创建后不可变，后者归触发器维护。
// 这里用列名白名单而不是交给 GORM 自行推断，
// 是为了让"哪些列会被写"在实现里显式可见，新增列时就必须同步这两处。
var userUpdatableColumns = []string{
	"username",
	"display_name",
	"email",
	"phone",
	"avatar_url",
	"zitadel_user_id",
	"status",
	"email_verified",
}

// toUserRecord 把领域实体转成持久化模型。
func toUserRecord(u *domainuser.User) userRecord {
	return userRecord{
		ID:            u.ID,
		Username:      u.Username,
		DisplayName:   u.DisplayName,
		Email:         u.Email,
		Phone:         u.Phone,
		AvatarURL:     u.AvatarURL,
		ZitadelUserID: u.ZitadelUserID,
		Status:        string(u.Status),
		EmailVerified: u.EmailVerified,
		CreatedAt:     u.CreatedAt,
		UpdatedAt:     u.UpdatedAt,
	}
}

// toEntity 把持久化模型还原成领域实体。
func (r userRecord) toEntity() *domainuser.User {
	return &domainuser.User{
		ID:            r.ID,
		Username:      r.Username,
		DisplayName:   r.DisplayName,
		Email:         r.Email,
		Phone:         r.Phone,
		AvatarURL:     r.AvatarURL,
		ZitadelUserID: r.ZitadelUserID,
		Status:        domainuser.UserStatus(r.Status),
		EmailVerified: r.EmailVerified,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

// toUpdateMap 给出 Update 的列值对，键集合必须与 userUpdatableColumns 一致。
//
// 用 map 而不是结构体：按结构体更新时零值字段会被 GORM 跳过，
// 而 username 为空串、email_verified 为 false 都是需要落库的合法取值。
func (r userRecord) toUpdateMap() map[string]any {
	return map[string]any{
		"username":        r.Username,
		"display_name":    r.DisplayName,
		"email":           r.Email,
		"phone":           r.Phone,
		"avatar_url":      r.AvatarURL,
		"zitadel_user_id": r.ZitadelUserID,
		"status":          r.Status,
		"email_verified":  r.EmailVerified,
	}
}

func (r *UserRepository) FindByID(ctx context.Context, id string) (*domainuser.User, error) {
	return r.findOne(ctx, "id = ?", strings.TrimSpace(id))
}

// FindByZitadelUserID 按外部身份标识查询用户，同步流程的主入口。
func (r *UserRepository) FindByZitadelUserID(ctx context.Context, zitadelUserID string) (*domainuser.User, error) {
	return r.findOne(ctx, "zitadel_user_id = ?", strings.TrimSpace(zitadelUserID))
}

// FindByEmail 按邮箱查询用户。
//
// 入参在这里再归一化一次：库里存的是 NormalizeEmail 的输出，
// 索引也建在该列上，大小写或空白不一致会直接打不中索引。
func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domainuser.User, error) {
	return r.findOne(ctx, "email = ?", domainuser.NormalizeEmail(email))
}

// findOne 按条件取一条记录，未命中返回 (nil, nil)。
//
// 空条件值在这里统一短路，而不是让每个 Find 各判一次：
// 空主键、空外部标识、空邮箱都是"这一项缺失"，不是一条待查的数据，
// 查库只会白白消耗一次往返；更重要的是口径只有一处，
// 不会出现某个 finder 漏判、把空值打进 UUID 列后抛回驱动错误的裂缝。
func (r *UserRepository) findOne(ctx context.Context, cond, arg string) (*domainuser.User, error) {
	if arg == "" {
		return nil, nil
	}

	var rec userRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).Where(cond, arg).Take(&rec).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return rec.toEntity(), nil
}

// Create 写入用户，撞上唯一约束时返回 domainuser.ErrUserExists。
//
// 错误怎么翻译见 translateWriteError：它依赖 bootstrap 打开的 TranslateError，
// 先由 postgres 驱动把 23505 翻成 gorm.ErrDuplicatedKey，再转成领域错误，
// 不把驱动原生错误抛给上层。
func (r *UserRepository) Create(ctx context.Context, u *domainuser.User) error {
	if u == nil {
		return domainuser.ErrNilUser
	}

	rec := toUserRecord(u)
	db := dBFromContext(ctx, r.db)
	if err := db.WithContext(ctx).Create(&rec).Error; err != nil {
		return translateWriteError(err, u)
	}
	return nil
}

// Update 全量更新用户的业务字段，记录不存在时返回 domainuser.ErrUserNotFound。
//
// 只写 userUpdatableColumns 列出的列，updated_at 由触发器维护，本方法不写。
func (r *UserRepository) Update(ctx context.Context, u *domainuser.User) error {
	if u == nil {
		return domainuser.ErrNilUser
	}

	rec := toUserRecord(u)
	db := dBFromContext(ctx, r.db)
	result := db.WithContext(ctx).
		Model(&userRecord{}).
		Where("id = ?", u.ID).
		Select(userUpdatableColumns).
		Updates(rec.toUpdateMap())
	return translateUpdateResult(result, u)
}

// userWriteTargets 组装用户写操作的结局配置。
func userWriteTargets(u *domainuser.User) writeTargets {
	return writeTargets{
		conflict:         domainuser.ErrUserExists,
		conflictIdentity: fmt.Sprintf("id=%s zitadel_user_id=%s", u.ID, u.ZitadelUserID),
		invalid:          domainuser.ErrInvalidUserRecord,
		invalidIdentity:  fmt.Sprintf("id=%s", u.ID),
	}
}

// translateWriteError 把数据库错误翻译成领域错误，两条判定见 classifyWriteError。
//
// 文案刻意不指名冲突的是哪一列：zitadel_user_id、username、email 各有唯一索引，
// 写死任何一个都会在撞上另一个时把排障带偏；
// 真正被违反的约束名只有驱动知道，因此底层错误被挂在链上原样保留。
func translateWriteError(err error, u *domainuser.User) error {
	return classifyWriteError(err, userWriteTargets(u))
}

// translateUpdateResult 判定 Update 的执行结果：出错翻译成领域错误，
// 没改到任何行返回 ErrUserNotFound。两条判定见 classifyUpdateResult。
func translateUpdateResult(result *gorm.DB, u *domainuser.User) error {
	return classifyUpdateResult(result, updateTargets{
		write:    userWriteTargets(u),
		notFound: domainuser.ErrUserNotFound,
		identity: fmt.Sprintf("id=%s", u.ID),
	})
}
