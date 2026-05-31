# Memo: `Backend.Close(ctx)` is an aspirational contract (deferred)

Status: **known gap** — the context is threaded through the public API but no
backend honors it yet. Tracking note so nobody assumes deadline-bounded shutdown
exists.

## Current state

PR #115 changed `store/runtime.Persistence.Close()` to
`Persistence.Close(ctx context.Context)` and forwards `ctx` to
`persistencepkg.Backend.Close(ctx)`. That fixed the public-API inconsistency
(it previously hardcoded `context.Background()`, so a library consumer had no way
to pass a context at all).

**But the context has no runtime effect today.** The chain bottoms out at
backends that ignore it:

```go
func (s *SQLiteBackend) Close(context.Context) error   { ... return s.dbStore.Close() } // param unnamed
func (p *PostgresBackend) Close(context.Context) error { ... return p.dbStore.Close() } // param unnamed
func (m *MarkdownBackend) Close(context.Context) error { return nil }
```

and the underlying stores take no context at all:
`sqlite.Store.Close() error`, `postgres.Store.Close() error`.

So passing a deadline/cancellable ctx to `Persistence.Close(ctx)` currently does
nothing — Close runs synchronously regardless.

## What it would take to make it real (future work)

1. Add `ctx` to `store/internal/sqlite.Store.Close(ctx)` and
   `store/internal/postgres.Store.Close(ctx)` and actually honor it (e.g. bound a
   flush/drain, abort on cancellation).
2. Name and forward the param in the three `Backend` implementations
   (`SQLiteBackend`, `PostgresBackend`; `MarkdownBackend` is a no-op).

This is a feature (graceful, deadline-bounded shutdown), not a refactor. Value is
low for closing a local SQLite handle (fast/synchronous); it matters more for
Postgres connection-pool draining or in-flight transactions. Defer until there is
a concrete shutdown-deadline requirement.

The threading done in PR #115 is the prerequisite: once a backend honors `ctx`,
it already receives the caller's real context end-to-end.
