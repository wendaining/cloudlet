package migrations

import "embed"

//go:embed *.sql
var Files embed.FS

const Version int64 = 24 // 00024_container_postgres_bindings.sql
