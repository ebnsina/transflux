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

## Where River *is* the right answer
River requires every working client to hold a Postgres pool
(`river.NewClient(riverpgxv5.New(dbPool), ...)`). That is disqualifying for the
media fleet (see Rejected) but entirely appropriate for control-plane-internal
background work: the planner, storage reconciliation, GC sweeps, webhook
delivery, periodic maintenance. Those run in-process, legitimately have database
access, and want exactly what River provides.

P0 has three such loops (lease expiry, reconciler, GC), which a `time.Ticker`
covers without a dependency. **Adopt River for internal jobs once that list
passes roughly five**, rather than hand-rolling a second queue. This is a
separate concern from media task distribution and does not change the decision
above.

## Rejected
- **River for the media fleet** — good library, wrong shape here, for four
  reasons in descending order of cost:
  1. *Security.* A working client needs direct Postgres credentials. Our workers
     run FFmpeg over untrusted media on machines we may not control; giving each
     one a database connection puts Postgres inside the blast radius that
     ADR-004 exists to contain. Today a compromised worker can act only on the
     single attempt whose lease it holds.
  2. *Capability is a vector, not a label.* River matches on queue name. Queue
     names can approximate `encode.hevc.gpu`, but `memory >= 14GB`,
     `free_disk >= 40GB`, `72% headroom` and `ffmpeg >= 7.1` cannot be encoded
     in a string — leaving either queue-name explosion or a second matching
     layer on top, at which point River holds a duplicate copy of task state.
  3. *Slot accounting.* River Pro offers per-queue concurrency limits; we need a
     shared weighted pool (4×h264 **or** 2×hevc **or** 1×av1 from one budget).
  4. *Attempts are domain data.* We join attempts to artifacts, resource
     accounting and scheduling explanations; River tracks attempts as queue
     metadata.
- **Temporal** — a whole distributed runtime, an extra cluster to operate, and a
  workflow model that suits neither our capability matching nor (later) live
  pipelines. VOD workflows here are a short DAG in Postgres. Revisit only if we
  grow genuinely long-lived, human-in-the-loop, multi-day workflows.
