package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 本文件守护 CloseDB 的契约，且刻意不连真实数据库：
// postgres 驱动在 DisableAutomaticPing 下只解析 DSN、不发起连接，
// 因此用例可以在没有数据库的环境里稳定运行。

// TestCloseDB传入空句柄返回nil
//
// 空句柄意味着"没有连接需要释放"，
// 关闭函数必须能安全地接受它，而不是让调用方各自判空。
func TestCloseDB传入空句柄返回nil(t *testing.T) {
	if err := CloseDB(nil); err != nil {
		t.Fatalf("关闭空句柄时应返回 nil，实际 %v", err)
	}
}

// TestCloseDB关闭后连接池不可用
//
// 关闭必须真正落到底层连接池上：
// 若只做了表面处理，sql.DB 仍能 Ping 通过，连接就会泄漏。
func TestCloseDB关闭后连接池不可用(t *testing.T) {
	db := newTestDB(t)

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 sql.DB 失败：%v", err)
	}

	if err := CloseDB(db); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}

	if err := sqlDB.Ping(); err == nil {
		t.Fatal("关闭后 Ping 应当报错")
	}
}

// TestCloseDB可重复调用
//
// 释放动作常出现在 defer 与显式调用的组合里，
// 重复关闭必须是无害的，不能因此让整条退出路径失败。
func TestCloseDB可重复调用(t *testing.T) {
	db := newTestDB(t)

	if err := CloseDB(db); err != nil {
		t.Fatalf("首次关闭失败：%v", err)
	}
	if err := CloseDB(db); err != nil {
		t.Fatalf("重复关闭应当无害，实际 %v", err)
	}
}

// newTestDB 造一个不连接真实数据库的 GORM 句柄。
//
// DSN 只需格式合法：postgres 驱动会解析它并交给 sql.Open，
// 而 sql.Open 是惰性的，真正的连接要等到第一次 Ping 才发生。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "postgres://test:test@127.0.0.1:5432/test?sslmode=disable",
	}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("构造数据库句柄失败：%v", err)
	}
	return db
}

// TestClose数据库关闭失败时仍释放Redis且不丢失错误
//
// 关闭资源的顺序不应由前一项的成败决定：
// 数据库关闭失败时 Redis 连接池仍要被释放，
// 且数据库的错误必须出现在返回值里，不能被后续分支的返回值顶掉。
func TestClose数据库关闭失败时仍释放Redis且不丢失错误(t *testing.T) {
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})

	// 取不到底层 sql.DB 的 gorm 句柄，关闭它必然失败，用来模拟数据库侧的关闭故障。
	// 注意必须带上非 nil 的 Config：ConnPool 挂在 Config 上，Config 为 nil 会直接空指针。
	app := &Bootstrap{
		db:          &gorm.DB{Config: &gorm.Config{}},
		redisClient: redisClient,
	}

	err := app.Close()
	if err == nil {
		t.Fatal("数据库关闭失败时应返回错误")
	}
	if !strings.Contains(err.Error(), "invalid db") {
		t.Fatalf("err = %v，应包含数据库侧的失败原因", err)
	}

	// Redis 必须已经释放：若被数据库的失败跳过，这里就不会是"client is closed"。
	if perr := redisClient.Ping(context.Background()).Err(); perr == nil {
		t.Fatal("Redis 应已被关闭，Ping 不应成功")
	} else if !strings.Contains(perr.Error(), "closed") {
		t.Fatalf("err = %v，期望 Redis 已关闭", perr)
	}
}
