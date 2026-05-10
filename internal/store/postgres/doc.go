// Package postgres implements the Postgres-backed durable stores used by dango.
//
// The package currently provides the runner persistence stores used by runtime
// persistence wiring: request stream event logs, runner records, and snapshot
// cursors. Migrations are embedded and applied on Open.
package postgres
