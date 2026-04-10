// Package datadir resolves the canonical filesystem layout used by dango.
//
// The package is the single source of truth for where the control plane stores
// durable state on disk. [Locator] maps a data root to the SQLite database,
// registry files, task directories, edge directories, handoff files, output
// directories, and other persisted artifacts produced by the orchestrator and
// runner. Keeping those path rules in one package is important because the same
// layout is consumed by registry persistence, task persistence, runner
// scheduling, runtime execution, and tests.
//
// The normal workflow is: create a [Locator] with [New], call [Locator.Ensure]
// once to create the top-level directories, and then use the various path
// accessors and Ensure* helpers as higher-level packages persist data. The
// orchestrator uses it to locate registry and task files, the runner uses it
// to materialize edge working directories and handoffs, and tests use it to
// assert on the same stable directory structure.
//
// Most methods are pure path constructors and intentionally do not perform I/O.
// That separation lets callers compute paths freely while reserving directory
// creation for [Locator.Ensure], [Locator.EnsureToolDir], [Locator.EnsureTaskDir],
// and [Locator.EnsureEdgeDir]. The package therefore acts as the filesystem
// companion to SQLite: the store keeps relational state, while datadir defines
// where the larger artifacts live.
package datadir
