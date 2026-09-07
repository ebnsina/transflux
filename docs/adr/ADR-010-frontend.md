# ADR-010 — Admin UI: deferred; Svelte + Vite SPA when built

Status: accepted (build at P1)

## Context
The frontend is an internal administration and observability surface: assets,
jobs, tasks, attempts, worker fleet, scheduling explanations. Authenticated,
internal, no SEO, no public traffic. It is not a customer-facing product.

## Decision
No frontend in P0 — the API is the operational surface. When built at P1, it is
a plain **Svelte + Vite** SPA served as static files behind the same reverse
proxy.

## Why
- Building a dashboard against a domain model that will move for months means
  building it twice. Everything the UI needs must exist in the API regardless,
  so API-first keeps the UI a client and never a special case.
- Svelte + Vite is less machinery than the alternatives for this job: no VDOM
  runtime, stores and transitions in the framework rather than in dependencies,
  smaller bundle, less boilerplate per component. The output is a directory of
  static assets — no server runtime to deploy or operate.

## Consequences
- P0 operability comes from the API, structured logs and metrics.
- TanStack Query and Table have Svelte adapters; TanStack **Router** does not
  (React/Solid only), so routing is `svelte-routing` or hand-rolled. Acceptable
  for an admin SPA with a shallow route tree.
- Component library, if one is wanted, is shadcn-svelte rather than shadcn.

## Rejected
- **SvelteKit** — ships a server we have no use for on an authenticated
  internal tool with no SSR or SEO requirement.
- **Next.js** — same server objection, more of it.
- **React + Vite** — perfectly workable and the larger ecosystem, but more
  runtime and more boilerplate for no benefit this UI can spend.
