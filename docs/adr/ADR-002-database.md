# ADR-002 — Database: PostgreSQL, pgx, sqlc

Status: accepted

## Context
We need transactional job/task state, leases with expiry, capability matching
queries, and strong constraints. All state must survive a control-plane restart.

## Decision
PostgreSQL as the single source of truth. `pgx/v5` driver. `sqlc` for typed
query generation. Hand-written SQL for everything in the scheduling path.

## Why
- Leasing is `SELECT ... FOR UPDATE SKIP LOCKED` — a Postgres primitive.
- State transitions need row-level locking and real transactions.
- Capability matching is a filtered, ordered query; an ORM obscures exactly the
  query we most need to read, explain and index.
- One dependency covers durable state, queueing and reporting.

## Consequences
- Scheduling throughput is bounded by one Postgres. That is thousands of leases
  per second — orders of magnitude past where encode capacity binds first.
- No second datastore. No Redis. Adding one requires a new ADR and a measurement.

## Rejected
- **ORM (GORM/ent)** — the scheduler's queries are the system's core logic and
  must be explicit.
- **Redis for queueing** — a second consistency domain to buy nothing; leases
  must be transactional with task state.
