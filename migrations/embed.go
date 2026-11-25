package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

const Version int64 = 11 // 00011_s3_bindings.sql
