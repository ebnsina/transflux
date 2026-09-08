<script lang="ts">
	// The public shell. Deliberately separate from the dashboard layout: these
	// pages have no sidebar, no API key, and nothing to wait for.
	import { resolve } from '$app/paths';
	import { apply, stored } from '$lib/theme.svelte';
	import Logo from '$lib/components/Logo.svelte';

	let { children } = $props();

	// A reader who chose a theme in the dashboard keeps it here.
	$effect(() => {
		apply(stored());
	});
</script>

<div class="shell">
	<header>
		<div class="bar">
			<a class="brand" href={resolve('/')}><Logo /></a>
			<nav>
				<a href={resolve('/')}>Overview</a>
				<a href={resolve('/docs')}>Docs</a>
			</nav>
		</div>
	</header>

	<main>
		{@render children()}
	</main>

	<footer>
		<div class="bar">
			<span class="muted">Transflux, a transcoding and delivery pipeline you can run yourself.</span
			>
			<a href={resolve('/docs')}>Docs</a>
		</div>
	</footer>
</div>

<style>
	.shell {
		display: flex;
		flex-direction: column;
		min-height: 100vh;
	}

	/* One measure for the header, the page and the footer, so the edges line
	   up down the whole document. */
	.bar {
		width: 100%;
		max-width: 1000px;
		margin: 0 auto;
		padding: 0 24px;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 18px;
	}

	header {
		border-bottom: 1px solid var(--line);
		background: var(--panel);
		position: sticky;
		top: 0;
		z-index: 5;
	}
	header .bar {
		min-height: 57px;
	}
	.brand:hover {
		text-decoration: none;
	}

	nav {
		display: flex;
		align-items: center;
		gap: 20px;
	}
	nav a {
		color: var(--muted);
		font-variation-settings: 'wght' 500;
	}
	nav a:hover {
		color: var(--accent);
		text-decoration: none;
	}

	main {
		flex: 1;
		width: 100%;
		max-width: 1000px;
		margin: 0 auto;
		padding: 0 24px 96px;
		min-width: 0;
	}

	footer {
		border-top: 1px solid var(--line);
		background: var(--panel);
		font-size: 13px;
	}
	footer .bar {
		min-height: 64px;
	}

	@media (max-width: 860px) {
		.bar {
			padding: 0 18px;
		}
		main {
			padding: 0 18px 72px;
		}
		nav {
			gap: 14px;
		}
		footer .bar {
			padding-top: 14px;
			padding-bottom: 14px;
			align-items: flex-start;
			flex-direction: column;
			gap: 8px;
		}
	}
</style>
