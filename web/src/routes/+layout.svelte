<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { apiKey, setApiKey, clearApiKey, api, type Identity } from '$lib/api';
	import { describe } from '$lib/problem';
	import { short } from '$lib/format';
	import { groups, section } from '$lib/nav';
	import { currentTrail, setTrail } from '$lib/breadcrumb.svelte';
	import { apply, label as themeLabel, next, stored, type Theme } from '$lib/theme.svelte';
	import Logo from '$lib/components/Logo.svelte';
	import '../app.css';

	let { children } = $props();

	let key = $state('');
	let identity = $state<Identity | null>(null);
	let error = $state('');
	let checking = $state(true);
	let theme = $state<Theme>('system');
	let menuOpen = $state(false);

	$effect(() => {
		theme = stored();
		apply(theme);
	});

	// Verify the stored key rather than assuming it still works: keys are
	// revocable, and a dashboard that silently shows nothing is worse than one
	// that says why.
	$effect(() => {
		void verify();
	});

	// A detail page owns its own label, so leaving one has to clear it or the
	// crumb would follow you to the next page.
	$effect(() => {
		const path = page.url.pathname;
		return () => {
			if (path !== page.url.pathname) setTrail(null);
		};
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
			error = describe(e);
		}
		checking = false;
	}

	async function signIn(event: SubmitEvent) {
		event.preventDefault();
		setApiKey(key);
		key = '';
		await verify();
	}

	function signOut() {
		clearApiKey();
		identity = null;
	}

	function cycleTheme() {
		theme = next(theme);
		apply(theme);
	}

	const active = $derived(section(page.url.pathname));
	const trail = $derived(currentTrail());
</script>

{#if checking}
	<p class="centred muted">Signing you in…</p>
{:else if !identity}
	<section class="signin">
		<div class="signin-head">
			<Logo size={26} />
			<button class="icon" onclick={cycleTheme}>{themeLabel(theme)}</button>
		</div>
		<h1>Sign in</h1>
		<p class="muted">Enter the access key for your account to continue.</p>
		<form onsubmit={signIn}>
			<input
				type="password"
				bind:value={key}
				placeholder="Access key"
				autocomplete="off"
				spellcheck="false"
			/>
			<button class="primary" type="submit" disabled={!key.trim()}>Sign in</button>
		</form>
		{#if error}<p class="error" style="margin-top:14px">{error}</p>{/if}
	</section>
{:else}
	<div class="app" class:menu-open={menuOpen}>
		<aside class="sidebar">
			<a class="brand" href={resolve('/')} onclick={() => (menuOpen = false)}>
				<Logo />
			</a>

			<nav>
				{#each groups as group (group.title)}
					<div class="group">
						<p class="group-title">{group.title}</p>
						{#each group.items as item (item.href)}
							<a
								href={resolve(item.href)}
								class:active={active?.href === item.href}
								onclick={() => (menuOpen = false)}
							>
								<span class="item-label">{item.label}</span>
								<span class="item-hint">{item.hint}</span>
							</a>
						{/each}
					</div>
				{/each}
			</nav>

			<div class="sidebar-foot">
				<span class="muted">Account {short(identity.tenant_id)}</span>
				<button class="link" onclick={signOut}>Sign out</button>
			</div>
		</aside>

		<div class="content">
			<header class="topbar">
				<button
					class="icon menu-toggle"
					onclick={() => (menuOpen = !menuOpen)}
					aria-label="Toggle navigation">Menu</button
				>

				<nav class="crumbs" aria-label="Breadcrumb">
					<a href={resolve('/')}>Dashboard</a>
					{#if active && active.href !== '/'}
						<span class="sep">/</span>
						{#if trail}
							<a href={resolve(active.href)}>{active.label}</a>
							<span class="sep">/</span>
							<span aria-current="page">{trail}</span>
						{:else}
							<span aria-current="page">{active.label}</span>
						{/if}
					{/if}
				</nav>

				<button class="icon" onclick={cycleTheme} title="Theme: {themeLabel(theme)}">
					{themeLabel(theme)}
				</button>
			</header>

			<main>
				{@render children()}
			</main>
		</div>
	</div>
{/if}
