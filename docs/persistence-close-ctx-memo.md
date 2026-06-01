# Memo: `Backend.Close(ctx)` — RESOLVED

Status: **resolved** (2026-06-02). The context threaded into `Persistence.Close(ctx)`
is now honored end-to-end for the database-backed backends.

## The gap (historical)

`Persistence.Close(ctx)` forwarded `ctx` to `persistencepkg.Backend.Close(ctx)`,
but the SQLite/Postgres backends ignored it and called the underlying
`sql.DB.Close()` (which takes no context), so a shutdown deadline had no effect.

## Resolution

The bound is applied at the **backend layer** (where `ctx` arrives) rather than
by changing `store/internal/{sqlite,postgres}.Store.Close()` — that method has
~30 test callers and is a thin `db.Close()` wrapper.

`store/internal/backend/close.go` adds `closeWithContext(ctx, closeFn)`: it runs
the close in a goroutine and selects on `ctx.Done()`, returning `ctx.Err()` if the
deadline hits first. Since `sql.DB.Close()` cannot be interrupted, the abandoned
close finishes in the background (a best-effort drain). `SQLiteBackend.Close` and
`PostgresBackend.Close` use it; `MarkdownBackend` holds no DB resources and stays a
no-op. The contract is documented on `Backend.Close` in `runner/persistence/backend.go`.

`Persistence.Close(ctx)` → (composite `closeBoth` forwards `ctx`) → `Backend.Close(ctx)`
→ `closeWithContext` now caps the actual database close by the caller's deadline.
