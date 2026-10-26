package gorm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/memories-cn/user-management-api/internal/domain"
	domainuser "github.com/memories-cn/user-management-api/internal/domain/user"
	"gorm.io/gorm"
)

// 本文件守护 user.Repository 端口在适配层的契约，且刻意不连数据库：
// 选的都是"不触碰 *gorm.DB 就能判定"的分支——空实体、空入参、错误翻译、RowsAffected 判定，
// 因此可以直接用零值 &UserRepository{}。
// 一旦哪天这些分支改成先查库，用例会以 panic 的形式立刻失败，
// 而不是悄悄退化成依赖环境的集成测试。

// newTestUser 造一个领域层认可的用户，供错误翻译用例取用标识。
func newTestUser(t *testing.T) *domainuser.User {
	t.Helper()
	u, err := domainuser.New("zitadel-uid-1", domainuser.WithEmail("alice@example.com"))
	if err != nil {
		t.Fatalf("构造用户失败：%v", err)
	}
	return u
}

// TestNewUserRepository缺少数据库句柄返回错误
func TestNewUserRepository缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewUserRepository(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// Test写操作遇到空实体返回ErrNilUser
//
// 空实体是调用方 bug：必须在触碰数据库之前就被拦下，
// 且不得混入业务分类，否则上层会把它当成一次正常的 4xx。
func Test写操作遇到空实体返回ErrNilUser(t *testing.T) {
	r := &UserRepository{}
	ctx := context.Background()

	cases := map[string]func() error{
		"Create": func() error { return r.Create(ctx, nil) },
		"Update": func() error { return r.Update(ctx, nil) },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, domainuser.ErrNilUser) {
				t.Fatalf("err = %v，期望 ErrNilUser", err)
			}
			if !errors.Is(err, domain.ErrNilEntity) {
				t.Fatalf("err = %v，应归类到 domain.ErrNilEntity", err)
			}
			for _, base := range []error{domain.ErrInvalidKey, domain.ErrConflict, domain.ErrNotFound} {
				if errors.Is(err, base) {
					t.Fatalf("err = %v，不应归类到 %v", err, base)
				}
			}
		})
	}
}

// Test查询方法遇到空入参不查库
//
// 句柄是 nil：只要真的发起查询就会 panic，
// 因此本用例同时锁住"三个 finder 的空入参一律短路"这条契约——
// 漏掉 FindByID 就会把空串打进 UUID 列，拿到驱动错误而不是约定的 (nil, nil)。
func Test查询方法遇到空入参不查库(t *testing.T) {
	r := &UserRepository{}
	ctx := context.Background()

	cases := map[string]func() (*domainuser.User, error){
		"FindByID 空串": func() (*domainuser.User, error) {
			return r.FindByID(ctx, "")
		},
		"FindByID 纯空白": func() (*domainuser.User, error) {
			return r.FindByID(ctx, "   ")
		},
		"FindByZitadelUserID 空串": func() (*domainuser.User, error) {
			return r.FindByZitadelUserID(ctx, "")
		},
		"FindByZitadelUserID 纯空白": func() (*domainuser.User, error) {
			return r.FindByZitadelUserID(ctx, " \t ")
		},
		"FindByEmail 空串": func() (*domainuser.User, error) {
			return r.FindByEmail(ctx, "")
		},
		"FindByEmail 纯空白": func() (*domainuser.User, error) {
			return r.FindByEmail(ctx, "  ")
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			if got != nil || err != nil {
				t.Fatalf("空入参应返回 (nil, nil)，实际 (%v, %v)", got, err)
			}
		})
	}
}

// Test唯一约束冲突归入ErrUserExists
//
// 端口要求"撞上唯一约束必须返回 ErrUserExists，不得静默吞掉后返回 nil"：
// 同步流程正是靠它区分"该用户已同步"与"尚未同步"。
func Test唯一约束冲突归入ErrUserExists(t *testing.T) {
	u := newTestUser(t)
	// 模拟驱动报错，且约束名取自 email 而不是 zitadel_user_id：
	// 以此锁住"错误信息不得写死冲突列"这一点。
	err := fmt.Errorf("duplicate key value violates unique constraint %q: %w",
		"idx_users_email", gorm.ErrDuplicatedKey)

	got := translateWriteError(err, u)
	if !errors.Is(got, domainuser.ErrUserExists) {
		t.Fatalf("err = %v，期望 ErrUserExists", got)
	}
	if !errors.Is(got, domain.ErrConflict) {
		t.Fatalf("err = %v，应归类到 domain.ErrConflict", got)
	}
	if errors.Is(got, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，不应归类到 domain.ErrInvalidKey", got)
	}
	// 底层错误必须保留：真正被违反的约束名只有驱动知道。
	if !errors.Is(got, gorm.ErrDuplicatedKey) {
		t.Fatalf("err = %v，应保留底层错误以便定位约束名", got)
	}
}

// TestCHECK约束违例归入领域参数类错误
//
// 领域层与数据库 CHECK 一旦漂移（或调用方绕过 New 直接改字段），
// 写入就会撞上 23514。它必须变成可归类的领域错误，
// 否则只能以驱动原生错误的形式冒到上层，落成一个无从分类的 500。
func TestCHECK约束违例归入领域参数类错误(t *testing.T) {
	u := newTestUser(t)
	err := fmt.Errorf("new row violates check constraint %q: %w",
		"chk_users_email_verified_requires_email", gorm.ErrCheckConstraintViolated)

	got := translateWriteError(err, u)
	if !errors.Is(got, domainuser.ErrInvalidUserRecord) {
		t.Fatalf("err = %v，期望 ErrInvalidUserRecord", got)
	}
	if !errors.Is(got, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", got)
	}
	if errors.Is(got, domain.ErrConflict) || errors.Is(got, domain.ErrNotFound) {
		t.Fatalf("err = %v，不应归类到写操作结局类", got)
	}
}

// Test未识别的数据库错误原样上抛
//
// 翻译只覆盖已知的两类：连接失败、超时等必须原样冒泡，
// 不得被误判成"用户已存在"或"记录不合法"，否则故障会被上层当成业务结果处理。
func Test未识别的数据库错误原样上抛(t *testing.T) {
	boom := errors.New("connection reset")
	got := translateWriteError(boom, newTestUser(t))
	if !errors.Is(got, boom) {
		t.Fatalf("err = %v，期望原样返回底层错误", got)
	}
}

// TestUpdate结果判定的三条分支
func TestUpdate结果判定的三条分支(t *testing.T) {
	u := newTestUser(t)

	if err := translateUpdateResult(&gorm.DB{RowsAffected: 1}, u); err != nil {
		t.Fatalf("改到一行时应返回 nil，实际 %v", err)
	}

	// 没改到任何行即目标不存在：静默成功会让上层误以为同步生效。
	err := translateUpdateResult(&gorm.DB{RowsAffected: 0}, u)
	if !errors.Is(err, domainuser.ErrUserNotFound) {
		t.Fatalf("err = %v，期望 ErrUserNotFound", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
	}

	// 出错时走同一套翻译，不得绕过。
	err = translateUpdateResult(&gorm.DB{Error: gorm.ErrDuplicatedKey}, u)
	if !errors.Is(err, domainuser.ErrUserExists) {
		t.Fatalf("err = %v，期望 ErrUserExists", err)
	}
}
