# ADR-001 — Control plane language: Go

Status: accepted

## Context
The control plane is an HTTP API, a Postgres-backed state machine, a scheduler,
and a lease manager. It is I/O bound. It runs no media code (ADR-004). Candidates:
Go and Rust.

## Decision
Go.

## Why
- The work is coordination, not computation. Rust's advantages (no GC, memory
  safety in hot paths) buy nothing where the bottleneck is Postgres and the
  network. The media work happens in FFmpeg, in a separate process, in a
  separate binary.
- pgx is the best Postgres driver in either ecosystem; sqlc gives typed SQL
  without an ORM.
- Single static binary, trivial cross-compilation for ARM/GPU worker hosts,
  `net/http` in stdlib, first-class `context` cancellation — which is exactly
  the shape of lease/cancel propagation.
- Faster iteration on a system whose domain model will move for months.

## Consequences
- GC pauses are irrelevant at this scale and workload.
- If a worker-side hot path ever proves CPU-bound in Go (it should not — it is
  process supervision and I/O), that single component can be rewritten without
  touching the control plane. The worker protocol is the seam.

## Rejected
**Rust** — real but unneeded gains, slower iteration on a moving domain model,
smaller pool of people who can operate it. Revisit only with a measured
control-plane CPU bottleneck, which would be surprising.
