# ADR-0002: SQLite (WAL) for the single-candidate deployment

**Status:** accepted (2026-11-20)

**Context.** The brief recommends PostgreSQL "subject to audit". The system serves one candidate (tens of jobs a day). The host has no Postgres. Ruxt itself runs on SQLite/D1. gorouter in this repository already uses `modernc.org/sqlite`.

**Decision.** Use SQLite in WAL mode via the pure-Go `modernc.org/sqlite`, with `_txlock=immediate`, `busy_timeout` and foreign keys. The task queue lives in the same database. Migrations are embedded SQL files.

**Consequences.** + No extra infrastructure; backup is copying one file (see the runbook). + Queue and state commit atomically. − Single writer and single core instance. **Upgrade path:** the SQL is ANSI apart from `ON CONFLICT` (also valid in Postgres) and `?` placeholders. A Postgres store would swap the driver, rewrite placeholders to `$n`, and replace the lease `SELECT … LIMIT 1` with `FOR UPDATE SKIP LOCKED`.
