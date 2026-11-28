package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

const Version int64 = 30 // 00030_container_s3_bindings.sql
