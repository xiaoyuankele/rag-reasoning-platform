// Package migrations 提供编译进 Go 后端的 SQL 迁移文件。
package migrations

import "embed"

// Files 包含所有正向迁移。部署时不需要另外复制 SQL 目录。
//
//go:embed *.up.sql
var Files embed.FS
