<script lang="ts">
	// The public shell. Deliberately separate from the dashboard layout: these
	// pages have no sidebar, no API key, and nothing to wait for.
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { apply, stored } from '$lib/theme.svelte';
	import Logo from '$lib/components/Logo.svelte';

	let { children } = $props();

	// The landing page lays out its own full-width sections; the docs want the
	// same measure as the header and footer.
	const wide = $derived(page.url.pathname === '/');

	// Over the hero the header has no ground of its own, so the glow behind it
	// carries straight through. It takes one on as soon as the page moves,
	// because from then on there is content passing underneath to separate.
	let scrolled = $state(false);
	$effect(() => {
		const onScroll = () => (scrolled = window.scrollY > 8);
		onScroll();
		window.addEventListener('scroll', onScroll, { passive: true });
		return () => window.removeEventListener('scroll', onScroll);
	});

	// A reader who chose a theme in the dashboard keeps it here.
	$effect(() => {
		apply(stored());
	});
</script>

<div class="shell">
	<header class:solid={scrolled || !wide}>
		<div class="bar">
			<a class="brand" href={resolve('/')}><Logo /></a>
			<nav>
				<a href={resolve('/')}>Overview</a>
				<a href={resolve('/docs')}>Docs</a>
				<a href={resolve('/docs/api')}>API</a>
			</nav>
			<a class="cta" href={resolve('/docs')}>Get started</a>
		</div>
	</header>

	<main class:wide>
		{@render children()}
	</main>

	<footer>
		<div class="bar foot">
			<div class="col brandcol">
				<Logo />
				<p class="muted">A transcoding and delivery pipeline you can run yourself.</p>
			</div>
			<div class="col">
				<h2>Product</h2>
				<a href={resolve('/')}>Overview</a>
				<a href={resolve('/docs')}>Documentation</a>
				<a href={resolve('/docs/api')}>API reference</a>
			</div>
			<div class="col">
				<h2>Pipeline</h2>
				<span class="muted">Adaptive renditions</span>
				<span class="muted">HLS and DASH</span>
				<span class="muted">Output validation</span>
			</div>
			<div class="col">
				<h2>Run it</h2>
				<span class="muted">Self-hosted</span>
				<span class="muted">PostgreSQL</span>
				<span class="muted">S3-compatible storage</span>
			</div>
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
		max-width: 1080px;
		margin: 0 auto;
		padding: 0 24px;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 18px;
	}

	/* Translucent rather than solid: the page keeps moving underneath it, which
	   is what tells the reader the header is fixed and the page is not. */
	header {
		border-bottom: 1px solid transparent;
		position: sticky;
		top: 0;
		z-index: 5;
		transition:
			background-color 0.2s ease,
			border-color 0.2s ease;
	}
	header.solid {
		border-bottom-color: var(--line);
		background: color-mix(in srgb, var(--bg) 72%, transparent);
		backdrop-filter: saturate(160%) blur(12px);
	}
	header .bar {
		min-height: 64px;
	}
	.brand:hover {
		text-decoration: none;
	}

	nav {
		display: flex;
		align-items: center;
		gap: 26px;
		margin-right: auto;
		margin-left: 34px;
	}
	nav a {
		color: var(--muted);
		font-size: 13.5px;
		font-variation-settings: 'wght' 500;
	}
	nav a:hover {
		color: var(--text);
		text-decoration: none;
	}

	.cta {
		background: var(--brand);
		color: var(--brand-ink);
		border-radius: var(--radius-sm);
		padding: 10px 20px;
		font-size: 14px;
		font-variation-settings: 'wght' 600;
		white-space: nowrap;
	}
	.cta:hover {
		text-decoration: none;
		filter: brightness(1.08);
	}

	main {
		flex: 1;
		width: 100%;
		max-width: 1080px;
		margin: 0 auto;
		padding: 44px 24px 96px;
		min-width: 0;
	}
	main.wide {
		max-width: none;
		padding: 0;
	}

	footer {
		border-top: 1px solid var(--line);
		font-size: 13px;
		padding: 56px 0 48px;
	}
	.foot {
		align-items: flex-start;
		gap: 40px;
	}
	.col {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.brandcol {
		max-width: 260px;
		margin-right: auto;
	}
	.brandcol p {
		margin: 0;
		line-height: 1.5;
	}
	.col h2 {
		font-size: 13px;
		margin: 0 0 2px;
		color: var(--text);
		text-transform: none;
		letter-spacing: 0;
		display: block;
		font-variation-settings: 'wght' 600;
	}
	.col a {
		color: var(--muted);
	}
	.col a:hover {
		color: var(--text);
		text-decoration: none;
	}

	@media (max-width: 860px) {
		.bar {
			padding: 0 18px;
		}
		nav {
			margin-left: 20px;
			gap: 16px;
		}
		main {
			padding: 32px 18px 72px;
		}
		main.wide {
			padding: 0;
		}
		.foot {
			flex-wrap: wrap;
			gap: 32px;
		}
		.brandcol {
			flex: 1 1 100%;
		}
	}
</style>
