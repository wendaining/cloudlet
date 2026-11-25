package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

const Version int64 = 13 // 00013_s3_bindings.sql
