// Package migrations embeds candidate, unexecuted production-schema migrations.
// It does not connect, apply migrations or create credentials automatically.
package migrations

import "embed"

//go:embed 000*.sql
var Files embed.FS
var Order = [...]string{"0001_base19.sql", "0002_control7.sql", "0003_admission2.sql", "0004_admission_constraints.sql", "0005_indexes_roles.sql", "0006_commands_payload_json.sql", "0007_gateway_lease_connection.sql"}
