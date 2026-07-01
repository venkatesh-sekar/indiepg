//go:build e2e

// Package harness is the FROZEN shared rig for the indiepg backend e2e suite.
//
// Scenario authors consume this package; they do not modify its core types or
// signatures. The surface is:
//
//   - Up(t, Options) *Env / (*Env).Close — start a uniquely-named compose project
//     (panel + minio + minio-init), wait for readiness, return handles; teardown
//     and log-dump-on-failure are registered on t.Cleanup.
//   - (*Env).Panel — typed HTTP client against the real panel API (Bearer + CSRF).
//   - (*Env).PG    — Postgres ground truth via `psql` inside the panel container.
//   - (*Env).S3    — MinIO assertions via minio-go (object existence/listing/counts).
//   - (*Env).SystemctlIsActive / Exec / ExecAsUser — service state + raw exec.
//   - Await — bounded, deterministic polling on an explicit predicate (no sleeps).
//
// Adding a scenario: drop a new `scenario_<name>_test.go` (//go:build e2e,
// package e2e) that calls harness.Up and asserts on real ground truth. Add a new
// typed Panel method by calling the exported (*Panel).Do / GET / POST helpers —
// no need to touch this package's internals.
//
// Every docker/go command runs with the command sandbox disabled and
// DOCKER_CONTEXT=default in this environment; the Makefile's `e2e` targets set
// that up. See test/e2e/README is intentionally omitted — the foundation report
// is the authoritative author brief.
package harness
