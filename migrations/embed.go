// Package migrations 通过 go:embed 把 SQL 迁移文件编入二进制，
// 使迁移命令无需依赖外部文件系统即可运行。
package migrations

import "embed"

// FS 包含全部 up/down 迁移文件，供 cmd/migrate 使用。
//
//go:embed *.sql
var FS embed.FS
