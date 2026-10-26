// Package domain 保存跨聚合共享的领域错误基类。
//
// 各聚合（user、role、permission 等）自行定义专属错误，
// 需要统一归类时以本包的 ErrInvalidKey 为基类包装。
package domain

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidKey 是所有"关键字段不合法"错误的基类错误。
	ErrInvalidKey = errors.New("关键字段不合法")

	// ErrInvalidSlug 表示租户唯一标识不合法。
	ErrInvalidSlug = fmt.Errorf("%w: 租户唯一标识", ErrInvalidKey)

	// ErrInvalidName 表示名称为空或超长。
	ErrInvalidName = fmt.Errorf("%w: 名称不能为空，或长度不能超过 255 个字符", ErrInvalidKey)
)
