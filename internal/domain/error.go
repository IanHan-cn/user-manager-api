package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidKey = errors.New("关键字段不合法")

	// ErrInvalidZitadelUserID 表示外部身份标识非法。
	ErrInvalidZitadelUserID = errors.New("zitadel_user_id 不能为空")

	// ErrReservedSystemKey 表示自定义角色使用了系统预置角色的标识。
	ErrReservedSystemKey = errors.New("该标识属于系统预置角色，不可用于自定义角色")

	ErrInvalidTenantID = errors.New("租户标识不能为空")
)

var (
	//Tenant Slug 不合法
	ErrInvalidSlug = fmt.Errorf("%w: 租户唯一标识", ErrInvalidKey)

	// ErrInvalidCode 表示权限代码格式不合法。
	ErrInvalidCode = fmt.Errorf("%w: 权限对外标识", ErrInvalidKey)

	// ErrInvalidKey 表示角色标识格式不合法。
	ErrInvalidRoleKey = fmt.Errorf("%w: 角色标识格式", ErrInvalidKey)

	// Name 不合法
	ErrInvalidName = fmt.Errorf("%w: 名称不能为空，或长度不能超过 255 个字符", ErrInvalidKey)
)
