import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

// SvelteKit options live here rather than in svelte.config.js: when the Vite
// plugin is given options, svelte.config.js is ignored entirely, and having
// both is a silent trap.
export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				runes: ({ filename }: { filename: string }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// Single-page app: every route is served by the fallback and
			// rendered in the browser (src/routes/+layout.ts sets ssr = false).
			// Without a fallback, adapter-static tries to prerender dynamic
			// routes like /jobs/[id], which it cannot know the ids for.
			adapter: adapter({ fallback: '200.html' })
		})
	],
	server: {
		port: 5173,
		// In development the API is reached through the same origin, so the
		// browser never makes a cross-origin request and there is no CORS
		// configuration to keep in step between dev and production.
		proxy: {
			'/v1': 'http://localhost:7080',
			'/healthz': 'http://localhost:7080'
		}
	}
});
