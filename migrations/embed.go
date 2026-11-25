package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

const Version int64 = 13 // 00013_postgres_bindings.sql
