# ADR-010 — Admin UI: SvelteKit as a static single-page app

Status: accepted (supersedes the deferral and the plain-Svelte leaning)

## Context
The frontend is an internal administration and observability surface: assets,
jobs, tasks, attempts, worker fleet, artifacts and scheduling explanations.
Authenticated, internal, no SEO, no public traffic.

It was deferred through P0 because the domain model moved in nearly every
slice — task specs became typed JSON, artifacts appeared, then validation — and
a dashboard built early would have been rebuilt three times. That reason has
now expired: the model is stable and everything a UI needs is in the API.

## Decision
**SvelteKit** with **`adapter-static`** in single-page mode, served as static
files by the same nginx that proxies the API.

## Why
- SvelteKit is what the team asked for, and it brings routing, layouts, typed
  route resolution and a scaffold (`sv create`, `sv add`) that is maintained
  rather than assembled by hand.
- `adapter-static` keeps the property the earlier decision valued: the build is
  a directory of files. There is no server runtime to deploy, no SSR to reason
  about, and nothing new to operate.
- One origin serves the app and proxies the API, so the browser never makes a
  cross-origin request. There is no CORS configuration that could differ
  between development and production.

## Consequences
- SvelteKit options live in `vite.config.ts`. When the Vite plugin is given
  options, `svelte.config.js` is ignored entirely — having both is a trap that
  fails silently, which is why there is only one.
- The API key is held in `localStorage`. This is an operator tool served as
  static files with no session server; a cookie would imply one that does not
  exist.
- Storage is not proxied through the dashboard origin. SigV4 signs the request
  path, so stripping a prefix would invalidate every presigned URL. The browser
  uploads to storage directly, which is the same path a customer's own client
  takes.
- Adding SSR later means changing an adapter, not rewriting the app.

## Rejected
- **Plain Svelte + Vite** — the earlier leaning. It avoids a server, but
  `adapter-static` avoids one too, and doing without SvelteKit means
  hand-rolling routing and losing the official scaffolding.
- **Next.js** — a server we have no use for, and a second language ecosystem to
  keep current.
