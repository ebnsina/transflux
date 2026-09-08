# Transflux dashboard

An operator interface for the control plane: assets and their tracks, jobs with
their tasks, attempts and validation, the worker fleet, and artifact downloads.

SvelteKit with `adapter-static`, so the build is a directory of files and there
is no server runtime to deploy or operate. The API key is held in the browser;
there is no session server, and a cookie would imply one.

## Development

```sh
npm install
npm run dev      # http://localhost:5173, proxying /v1 to localhost:7080
```

The dev server proxies the API so the browser stays on one origin — the same
arrangement nginx provides in production, which means there is no CORS
configuration that could differ between the two.

## Checks

```sh
npm run lint     # prettier + eslint
npm run check    # svelte-check
npm run build
```

## Notes

- SvelteKit options live in `vite.config.ts`. When the Vite plugin is given
  options, `svelte.config.js` is ignored entirely; having both is a silent trap.
- Storage is not proxied through this origin. SigV4 signs the request path, so
  stripping a prefix on the way through would invalidate every presigned URL —
  the browser uploads to storage directly, which is the path a customer's own
  client takes too.
