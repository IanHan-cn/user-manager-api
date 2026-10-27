package platformsetup

import (
	"errors"
	"testing"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/permission"
)

// 本包此前没有任何测试文件，而它承载着"租户创建时预置系统角色"这条链路。
// 下面的用例全部落在纯函数上（ensureInCatalog、normalizeCodes），
// 不需要连数据库——与本仓"不连库也能判定"的既定手法一致。

// Test预置角色的权限都在系统目录中
//
// 这是防止"预置矩阵与权限目录漂移"最有价值的一条守卫：
// 两者都是代码里写死的受控词表，一旦各改各的，
// 漂移只会在某个租户创建的那一刻才暴露，
// 而那时的错误看起来像"数据库没准备好"，很容易被误判成环境问题。
func Test预置角色的权限都在系统目录中(t *testing.T) {
	for _, preset := range systemRolePresets {
		t.Run(preset.key, func(t *testing.T) {
			if err := ensureInCatalog(normalizeCodes(preset.permissions)); err != nil {
				t.Fatalf("预置角色 %s 的权限不在系统目录中：%v", preset.key, err)
			}
		})
	}
}

// Test预置矩阵覆盖了目录里的全部权限
//
// 目录里存在一项却没有任何角色能持有它，意味着这项权限永远不会被授予——
// 要么是目录多写了，要么是预置矩阵漏写了。两种都是需要人来判断的事，
// 用例只负责把它暴露出来。
func Test预置矩阵覆盖了目录里的全部权限(t *testing.T) {
	used := make(map[string]struct{})
	for _, preset := range systemRolePresets {
		for _, code := range preset.permissions {
			used[code] = struct{}{}
		}
	}

	for _, e := range permission.All() {
		if _, ok := used[e.Code]; !ok {
			t.Errorf("权限 %q 在目录中，但没有任何预置角色持有它", e.Code)
		}
	}
}

// TestEnsureInCatalog拒绝目录外的代码
//
// 目录外的代码是"预置矩阵写错了"，与"库里缺行"是两回事：
// 前者要查源码，后者要查部署步骤。
func TestEnsureInCatalog拒绝目录外的代码(t *testing.T) {
	err := ensureInCatalog([]string{"tenant:frobnicate"})
	if !errors.Is(err, permission.ErrUnknownCode) {
		t.Fatalf("错误 = %v，期望归入 permission.ErrUnknownCode", err)
	}
	// 仍然挂在字段级基类上：它确实是一个"关键字段不合法"的取值。
	if !errors.Is(err, domain.ErrInvalidKey) {
		t.Fatalf("错误 %v 未归类到 domain.ErrInvalidKey", err)
	}
}

// TestEnsureInCatalog与ErrInvalidCode是两类错误
//
// 二者的排障方向不同：格式错要去查调用的构造过程，
// 不在目录里则要去看是不是目录该扩充、还是调用方写了自己想象出来的权限。
// 合成一类会让排障方向被带偏。
func TestEnsureInCatalog与ErrInvalidCode是两类错误(t *testing.T) {
	if errors.Is(permission.ErrUnknownCode, permission.ErrInvalidCode) {
		t.Fatal("ErrUnknownCode 与 ErrInvalidCode 不应是同一类错误")
	}
}

// TestEnsureInCatalog接受目录内的代码
func TestEnsureInCatalog接受目录内的代码(t *testing.T) {
	for _, e := range permission.All() {
		t.Run(e.Code, func(t *testing.T) {
			if err := ensureInCatalog([]string{e.Code}); err != nil {
				t.Fatalf("目录内的代码被拒绝：%v", err)
			}
		})
	}
}

// TestEnsureInCatalog空列表通过
//
// 空集合不是错误：调用方可能只是想先校验一批。
func TestEnsureInCatalog空列表通过(t *testing.T) {
	if err := ensureInCatalog(nil); err != nil {
		t.Fatalf("空列表不应报错：%v", err)
	}
}

// TestNormalizeCodes归一化并去重
//
// 归一化把大小写与空白差异折叠掉，去重让"语义上相同"的入参
// 不升级成一次写失败。两者都直接影响权限集合的正确性。
func TestNormalizeCodes归一化并去重(t *testing.T) {
	got := normalizeCodes([]string{"  TENANT:READ  ", "tenant:read", "tenant:read"})

	if len(got) != 1 || got[0] != "tenant:read" {
		t.Fatalf("结果 = %v，期望 [tenant:read]", got)
	}
}

// TestNormalizeCodes丢弃非法代码
//
// 非法代码在进到这里之前已被 ValidateCodes 拦下，
// 因此这里的丢弃是兜底而不是主路径。
func TestNormalizeCodes丢弃非法代码(t *testing.T) {
	got := normalizeCodes([]string{"tenant:read", "not-a-code", "缺少冒号"})

	if len(got) != 1 || got[0] != "tenant:read" {
		t.Fatalf("结果 = %v，期望 [tenant:read]", got)
	}
}
