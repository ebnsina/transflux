<script lang="ts">
	import { resolve } from '$app/paths';
	import { page } from '$app/state';
	import { apiKey, setApiKey, clearApiKey, api, type Identity } from '$lib/api';
	import { short } from '$lib/format';
	import { apply, label, next, stored, type Theme } from '$lib/theme.svelte';
	import '../app.css';

	let { children } = $props();

	let key = $state('');
	let identity = $state<Identity | null>(null);
	let error = $state('');
	let checking = $state(true);
	let theme = $state<Theme>('system');

	// Restore the stored preference on load, before anything is measured
	// against it.
	$effect(() => {
		theme = stored();
		apply(theme);
	});

	function cycleTheme() {
		theme = next(theme);
		apply(theme);
	}

	// Verify the stored key on load rather than assuming it still works: keys
	// are revocable, and a dashboard that silently shows nothing is worse than
	// one that says why.
	$effect(() => {
		void verify();
	});

	async function verify() {
		checking = true;
		error = '';
		if (!apiKey()) {
			identity = null;
			checking = false;
			return;
		}
		try {
			identity = await api.get<Identity>('/v1/me');
		} catch (e) {
			identity = null;
			error = e instanceof Error ? e.message : String(e);
		}
		checking = false;
	}

	async function connect(event: SubmitEvent) {
		event.preventDefault();
		setApiKey(key);
		key = '';
		await verify();
	}

	function disconnect() {
		clearApiKey();
		identity = null;
	}

	const nav = [
		{ href: '/' as const, label: 'Overview' },
		{ href: '/assets' as const, label: 'Assets' },
		{ href: '/jobs' as const, label: 'Jobs' },
		{ href: '/workers' as const, label: 'Workers' }
	];
</script>

<div class="shell">
	<header>
		<a class="brand" href={resolve('/')}>transflux</a>
		{#if identity}
			<nav>
				{#each nav as item (item.href)}
					<a
						href={resolve(item.href)}
						class:active={item.href === '/'
							? page.url.pathname === '/'
							: page.url.pathname.startsWith(item.href)}>{item.label}</a
					>
				{/each}
			</nav>
			<div class="identity">
				<span class="muted">tenant {short(identity.tenant_id)}</span>
				<button class="icon" onclick={cycleTheme} title="Theme: {label(theme)}">
					{label(theme)}
				</button>
				<button class="link" onclick={disconnect}>sign out</button>
			</div>
		{/if}
	</header>

	<main>
		{#if checking}
			<p class="muted">Checking credentials…</p>
		{:else if identity}
			{@render children()}
		{:else}
			<section class="signin">
				<div style="display:flex;justify-content:flex-end;margin-bottom:8px">
					<button class="icon" onclick={cycleTheme}>{label(theme)}</button>
				</div>
				<h1>Connect</h1>
				<p class="muted">
					Paste an API key. Create one with
					<code>transflux bootstrap -name "Your tenant"</code>.
				</p>
				<form onsubmit={connect}>
					<input
						type="password"
						bind:value={key}
						placeholder="tf_live_…"
						autocomplete="off"
						spellcheck="false"
					/>
					<button type="submit" disabled={!key.trim()}>Connect</button>
				</form>
				{#if error}<p class="error">{error}</p>{/if}
			</section>
		{/if}
	</main>
</div>
