// Package role 定义租户角色实体、角色标识约束与构造规则。
//
// 分层约束：本包属于 Domain，不得依赖 Gin、GORM、Redis 或 Casbin。
// 角色标识是租户内的受控词表：系统预置角色不可被自定义角色占用。
package role

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// 系统预置角色标识。
var (
	KeyOwner  = "owner"
	KeyAdmin  = "admin"
	KeyMember = "member"
	KeyViewer = "viewer"
)

// SystemKeys 是系统预置角色标识集合。
var SystemKeys = map[string]struct{}{
	KeyOwner: {}, KeyAdmin: {}, KeyMember: {}, KeyViewer: {},
}

var (
	// ErrReservedSystemKey 表示自定义角色使用了系统预置角色的标识。
	ErrReservedSystemKey = errors.New("该标识属于系统预置角色，不可用于自定义角色")

	// ErrInvalidRoleKey 表示角色标识格式不合法。
	ErrInvalidRoleKey = fmt.Errorf("%w: 角色标识格式", domain.ErrInvalidKey)

	// ErrInvalidTenantID 表示租户标识不能为空。
	ErrInvalidTenantID = errors.New("租户标识不能为空")
)

type Role struct {
	ID          string // 主键
	TenantID    string // 租户 ID
	Key         string // 角色标识
	Name        string // 显示名字
	IsSystem    bool   // 是否系统预设
	Description string // 描述

	CreatedAt time.Time // 创建时间
	UpdatedAt time.Time //修改时间
}

// 角色标识与名称的长度约束。
const (
	maxKeyLen  = 64
	maxNameLen = 255
)

// keyPattern 限定角色标识只含小写字母、数字与下划线，且以小写字母开头。
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// NormalizeKey 归一化角色标识：去空白并转小写。
func NormalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// IsSystemKey 报告给定标识是否为系统预置角色。
func IsSystemKey(key string) bool {
	_, ok := SystemKeys[NormalizeKey(key)]
	return ok
}

// NewCustomRole 构造租户自定义角色，key 不得命中系统预置角色集合。
func NewCustomRole(tenantID uuid.UUID, key, name, description string) (*Role, error) {

	if IsSystemKey(key) {
		return nil, ErrReservedSystemKey
	}
	return newRole(tenantID, key, name, description, false)
}

// NewSystemRole 构造系统预置角色，key 必须命中系统预置角色集合，is_system 恒为 true。
func NewSystemRole(tenantID uuid.UUID, key, name, description string) (*Role, error) {
	if !IsSystemKey(key) {
		return nil, ErrInvalidRoleKey
	}
	return newRole(tenantID, key, name, description, true)
}

// newRole 构造角色，key 不得为空，长度不得超过 maxKeyLen，且必须满足 keyPattern。
func newRole(tenantID uuid.UUID, key, name, description string, isSystem bool) (*Role, error) {
	if tenantID == uuid.Nil() {
		return nil, ErrInvalidTenantID
	}

	key = NormalizeKey(key)
	if key == "" || len(key) > maxKeyLen || !keyPattern.MatchString(key) {
		return nil, ErrInvalidRoleKey
	}

	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLen {
		return nil, domain.ErrInvalidName
	}

	now := time.Now()
	return &Role{
		ID:          uuid.NewV7().String(),
		TenantID:    tenantID.String(),
		Key:         key,
		Name:        name,
		Description: strings.TrimSpace(description),
		IsSystem:    isSystem,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}
