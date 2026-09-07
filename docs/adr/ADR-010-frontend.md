# ADR-010 — Admin UI: deferred, API-first

Status: accepted (revisit at P1)

## Context
The frontend is an internal administration and observability surface: assets,
jobs, tasks, attempts, worker fleet, scheduling explanations. It is not a
customer-facing product.

## Decision
No frontend in P0. The API is the product surface; operations run on it
directly. Choose the stack at P1, when the real screens are known.

## Why
- Building a dashboard against a domain model that will move for months means
  rebuilding it.
- Everything the UI would show must exist in the API anyway. API-first makes
  the UI a client, never a special case.

## Consequences
- P0 operability comes from the API, structured logs and metrics.
- Leaning choice for P1: **React + Vite + TanStack Query/Router**, as a plain
  SPA served as static files. No SSR requirement (authenticated internal tool),
  no SEO, no server runtime to deploy — it stays a bucket of static assets
  behind the same reverse proxy. SvelteKit and Next.js are both reasonable and
  both add a server we do not need.
