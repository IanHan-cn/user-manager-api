package membership

import (
	"context"
	"testing"
	"uuid"
)

// stubRepository 是 Repository 端口的最小实现，只为锁定端口签名：
// 签名一旦变动，本文件会立即编译失败，而不是等到适配层实现时才发现。
type stubRepository struct{}

var _ Repository = (*stubRepository)(nil)

func (stubRepository) Create(ctx context.Context, m *Membership) error { return nil }
func (stubRepository) Update(ctx context.Context, m *Membership) error { return nil }

func (stubRepository) GetByID(ctx context.Context, id uuid.UUID) (*Membership, error) {
	return nil, nil
}

func (stubRepository) GetByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) (*Membership, error) {
	return nil, nil
}

// 列表查询无匹配时返回空切片而不是 nil，这是端口契约的一部分。
func (stubRepository) GetByTenantID(ctx context.Context, tenantID uuid.UUID) ([]*Membership, error) {
	return []*Membership{}, nil
}

func (stubRepository) GetByUserID(ctx context.Context, userID uuid.UUID) ([]*Membership, error) {
	return []*Membership{}, nil
}

// TestRepository_空结果形态符合契约
//
// 单条查询未命中返回 (nil, nil)，列表查询无匹配返回空切片而不是 nil，
// 让调用方可以统一用 len() 判断，不必区分 nil 与空。
func TestRepository_空结果形态符合契约(t *testing.T) {
	var r Repository = stubRepository{}
	ctx := context.Background()

	if got, err := r.GetByID(ctx, uuid.New()); got != nil || err != nil {
		t.Fatalf("单条查询未命中应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
	if got, err := r.GetByTenantAndUser(ctx, uuid.New(), uuid.New()); got != nil || err != nil {
		t.Fatalf("单条查询未命中应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}

	byTenant, err := r.GetByTenantID(ctx, uuid.New())
	if err != nil {
		t.Fatalf("GetByTenantID 不应报错：%v", err)
	}
	if byTenant == nil {
		t.Fatal("GetByTenantID 无匹配时应返回空切片，而不是 nil")
	}
	if len(byTenant) != 0 {
		t.Fatalf("GetByTenantID 返回 %d 条，期望 0 条", len(byTenant))
	}

	byUser, err := r.GetByUserID(ctx, uuid.New())
	if err != nil {
		t.Fatalf("GetByUserID 不应报错：%v", err)
	}
	if byUser == nil {
		t.Fatal("GetByUserID 无匹配时应返回空切片，而不是 nil")
	}
	if len(byUser) != 0 {
		t.Fatalf("GetByUserID 返回 %d 条，期望 0 条", len(byUser))
	}
}

// TestRepository_写入方法接受领域实体
func TestRepository_写入方法接受领域实体(t *testing.T) {
	var r Repository = stubRepository{}
	m, err := New(uuid.New(), uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if err := r.Create(context.Background(), m); err != nil {
		t.Fatalf("Create 失败：%v", err)
	}
	if err := r.Update(context.Background(), m); err != nil {
		t.Fatalf("Update 失败：%v", err)
	}
}
