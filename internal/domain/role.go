package domain

import (
	"regexp"
	"time"
)

// 系统预置角色标识。
var (
	KeyOwner  = "owner"
	KeyAdmin  = "admin"
	KeyMember = "member"
	KeyViewer = "viewer"
)

type Role struct {
	ID          string
	TenantID    string
	Key         string
	Name        string
	IsSystem    bool
	Description string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// 角色标识与名称的长度约束。
const (
	maxKeyLen  = 64
	maxNameLen = 255
)

// keyPattern 限定角色标识只含小写字母、数字与下划线，且以小写字母开头。
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
