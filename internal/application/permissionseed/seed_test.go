package permissionseed

import (
	"context"
	"errors"
	"testing"

	"github.com/memories-cn/user-management-api/internal/domain/permission"
)

// stubWriter 记录 Seeder 交下来的目录，并返回预设的结果或错误。
//
// 不引入 mock 框架：本仓只用标准库（见 go.mod），
// 而这里要观察的不过就是"传了什么进来、结果是否被原样返回"。
type stubWriter struct {
	entries []permission.Entry
	result  permission.SeedResult
	err     error
}

var _ permission.Writer = (*stubWriter)(nil)

func (s *stubWriter) EnsureCatalog(ctx context.Context, entries []permission.Entry) (permission.SeedResult, error) {
	s.entries = entries
	if s.err != nil {
		return permission.SeedResult{}, s.err
	}
	return s.result, nil
}

// TestNewSeeder写入端口为空时返回错误
func TestNewSeeder写入端口为空时返回错误(t *testing.T) {
	if _, err := NewSeeder(nil); err == nil {
		t.Fatal("写入端口为空时应返回错误")
	}
}

// TestEnsureCatalog把全量目录交给写入端口
//
// Seeder 不带自己那份权限列表：它只负责把 domain/permission 的系统目录交出去，
// 目录一旦有两份就必然会漂移。
func TestEnsureCatalog把全量目录交给写入端口(t *testing.T) {
	w := &stubWriter{result: permission.SeedResult{Inserted: 3, Updated: 1, Unchanged: 11}}
	s, err := NewSeeder(w)
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	got, err := s.EnsureCatalog(context.Background())
	if err != nil {
		t.Fatalf("同步失败：%v", err)
	}

	if got.Inserted != 3 || got.Updated != 1 || got.Unchanged != 11 {
		t.Fatalf("结果未被原样返回：%+v", got)
	}
	if len(w.entries) != len(permission.All()) {
		t.Fatalf("交给写入端口 %d 项，期望 %d 项", len(w.entries), len(permission.All()))
	}
	for i, e := range permission.All() {
		if w.entries[i] != e {
			t.Fatalf("第 %d 项为 %+v，期望 %+v", i, w.entries[i], e)
		}
	}
}

// TestEnsureCatalog透传写入端口的错误
//
// 写库失败不能在本层被吞掉：上层要知道这次同步没成功。
func TestEnsureCatalog透传写入端口的错误(t *testing.T) {
	want := errors.New("数据库连接失败")
	s, err := NewSeeder(&stubWriter{err: want})
	if err != nil {
		t.Fatalf("构造失败：%v", err)
	}

	if _, err := s.EnsureCatalog(context.Background()); !errors.Is(err, want) {
		t.Fatalf("错误 = %v，期望 %v", err, want)
	}
}
