package user

import (
	"context"
	"testing"
)

// stubRepository 是 Repository 端口的最小实现，只为锁定端口签名：
// 签名一旦变动，本文件会立即编译失败，而不是等到适配层实现时才发现。
type stubRepository struct{}

var _ Repository = (*stubRepository)(nil)

func (stubRepository) FindByID(ctx context.Context, id string) (*User, error) {
	return nil, nil
}

func (stubRepository) FindByZitadelUserID(ctx context.Context, zitadelUserID string) (*User, error) {
	return nil, nil
}

func (stubRepository) FindByEmail(ctx context.Context, email string) (*User, error) {
	return nil, nil
}

func (stubRepository) Create(ctx context.Context, u *User) error { return nil }

func (stubRepository) Update(ctx context.Context, u *User) error { return nil }

// TestRepository_未命中返回空结果而不是错误
//
// "查不到数据"不是基础设施故障：同步流程正是靠 (nil, nil) 来区分
// "该用户尚未同步"（走 Create）与"已同步"（走 Update）。
func TestRepository_未命中返回空结果而不是错误(t *testing.T) {
	var r Repository = stubRepository{}
	ctx := context.Background()

	if got, err := r.FindByID(ctx, "u-1"); got != nil || err != nil {
		t.Fatalf("FindByID 未命中应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
	if got, err := r.FindByZitadelUserID(ctx, "zitadel-uid-1"); got != nil || err != nil {
		t.Fatalf("FindByZitadelUserID 未命中应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
	if got, err := r.FindByEmail(ctx, "alice@example.com"); got != nil || err != nil {
		t.Fatalf("FindByEmail 未命中应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
}

// TestRepository_写入方法接受领域实体
func TestRepository_写入方法接受领域实体(t *testing.T) {
	var r Repository = stubRepository{}
	u, err := New("zitadel-uid-1", WithEmail("alice@example.com"))
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if err := r.Create(context.Background(), u); err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if err := r.Update(context.Background(), u); err != nil {
		t.Fatalf("Update 失败：%v", err)
	}
}
