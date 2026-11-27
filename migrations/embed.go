package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

const Version int64 = 29 // 00029_container_postgres_bindings.sql
