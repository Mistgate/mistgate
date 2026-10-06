# 0002. The storage seam is the `database/sql` driver

Status: accepted, 2026-10-06.

## Context

All panel state goes through `internal/panel/store` (`Store{W, R *sql.DB}`, about 190 methods, 38+ migrations). On the
edge the database is D1 (SQLite underneath), which speaks SQL but has no interactive transactions: a batch of statements
is atomic, a `BEGIN … read … decide … COMMIT` round trip is not possible. The spike ran the real `store` package over a
small D1 `database/sql` driver.

## Decision

- Keep `database/sql` as the interface. The VPS edition keeps modernc SQLite; the edge edition gets `edge/d1driver`.
  No repository-pattern rewrite, no new abstraction layer.
- The same migration files serve both editions. New migrations avoid explicit transactions, `-- +goose NO TRANSACTION`
  and `PRAGMA foreign_keys`; CI applies the whole directory to SQLite and to a local D1.
- Every transaction in `store` takes one of three shapes, checked by a test on both drivers:
  1. a fixed list of writes (becomes one atomic `db.batch` on D1);
  2. one write uses a guarded write (`… WHERE <cond>`) and its outcome comes from `RowsAffected`; a diagnostic `SELECT`
     before it in the same batch chooses the failure reason. Several writes that must all happen or none start with
     `guard(cond)`, then plain writes without per-statement conditions or markers. On `errGuard`, a plain read after the
     batch chooses the error to return; the guard provides atomicity;
  3. declared serialised: it runs inside the Durable Object that owns that area (the agent hot path lives in the node's
     Durable Object).
  Read-then-write inside a transaction is allowed only in shape 3.
- Hot reads are cached in the isolate with the TTLs the code already uses; each admin call and subscription fetch has a
  small sequential query budget (D1 costs ~38 ms per query from a Worker).

## Consequences

- About 22 read-then-write transactions are rewritten to shapes 2 or 3. This also removes race windows on the VPS edition.
- The schema stays one source of truth, and a backup moves between editions without conversion of the schema version.
