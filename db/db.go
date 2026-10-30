// Package db holds the schema and seed data. Postgres applies them on first
// start of the compose volume; tests apply the same files to a throwaway
// database, so both always run against one definition.
package db

import _ "embed"

//go:embed schema.sql
var Schema string

//go:embed seed.sql
var Seed string
