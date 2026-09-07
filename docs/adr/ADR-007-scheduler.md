# ADR-007 — Scheduler: hard constraints then weighted score, explanations persisted

Status: accepted

## Context
Workers are heterogeneous (x86/ARM, GPU/CPU, differing encoders and versions).
Tasks have real requirements. Bad placement wastes expensive capacity; opaque
placement is undebuggable.

## Decision
Two phases. Hard constraints filter to feasible tasks for the asking worker.
A weighted product then ranks them:
`priority × capability_fit × capacity_headroom × fairness × locality × age`.
The winning decision's explanation is stored on the task attempt and exposed
via the API.

## Why
- Constraints and preferences are different things; conflating them produces
  either infeasible placements or unexplainable ones.
- `capability_fit` deliberately prefers the *least* capable feasible worker, so
  GPU nodes are not consumed by work any CPU node could do.
- `age` prevents starvation of low-priority tenants.
- Persisted explanations turn "why did this take an hour" from an investigation
  into a database query.

## Consequences
- Weights are configuration and will be tuned against measured throughput.
- Scheduling is per-lease-request, so there is no global optimum — an acceptable
  trade for a stateless, restartable, contention-free scheduler.
- Slot capacity per workload class is declared by the worker, not derived from
  core count (a 32-core host is not 32 concurrent AV1 encodes).

## Rejected
- **ML / learned scheduling** — unexplainable, and we have no data yet.
- **Bin-packing global optimiser** — requires central state and stalls on
  contention; revisit only if measured utilisation is poor.
- **Round-robin / random** — cannot express hard constraints at all.
