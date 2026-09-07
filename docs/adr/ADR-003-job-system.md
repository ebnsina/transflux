# ADR-003 — Job system: own task table, no River, no Temporal

Status: accepted

## Context
Tasks must be durable, retryable, cancellable and leased. Candidates: River
(Postgres-backed Go queue), Temporal, or our own tables.

## Decision
Own `tasks` / `task_attempts` tables and a lease loop.

## Why
Generic queues assign work to *any* free consumer. Our central problem is the
opposite: only some workers can run a given task (encoder availability, GPU,
architecture, memory, slot class), and the choice must be explainable and
persisted. That matching logic cannot live inside a generic queue — it would
have to be reimplemented on top of it anyway, leaving the queue as an
unnecessary layer holding a second copy of task state.

We also need first-class task attempts, per-class retry policies, chunk
dependencies, and per-attempt resource accounting. All of that is domain data,
not queue metadata.

## Consequences
- We own the lease/expiry sweeper. It is roughly 200 lines and it is the piece
  we most need to control.
- No hidden queue semantics to debug at 3am.

## Rejected
- **River** — good library, wrong shape: no capability-based consumer matching.
- **Temporal** — a whole distributed runtime, an extra cluster to operate, and a
  workflow model that suits neither our capability matching nor (later) live
  pipelines. VOD workflows here are a short DAG in Postgres. Revisit only if we
  grow genuinely long-lived, human-in-the-loop, multi-day workflows.
