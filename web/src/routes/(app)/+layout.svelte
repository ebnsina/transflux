<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { apiKey, setApiKey, clearApiKey, api, type Identity } from '$lib/api';
	import { describe } from '$lib/problem';
	import { short } from '$lib/format';
	import { groups, section } from '$lib/nav';
	import { currentTrail, setTrail } from '$lib/breadcrumb.svelte';
	import { apply, label as themeLabel, stored, type Theme } from '$lib/theme.svelte';
	import Logo from '$lib/components/Logo.svelte';
	import Icon from '$lib/components/Icon.svelte';

	let { children } = $props();

	let key = $state('');
	let identity = $state<Identity | null>(null);
	let error = $state('');
	let checking = $state(true);
	let theme = $state<Theme>('system');
	let menuOpen = $state(false);
	let accountOpen = $state(false);

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

	function chooseTheme(choice: Theme) {
		theme = choice;
		apply(choice);
	}

	const themes: Theme[] = ['system', 'light', 'dark'];

	$effect(() => {
		if (!accountOpen) return;
		const dismiss = (event: Event) => {
			if (!(event.target as HTMLElement)?.closest('.sidebar-foot')) accountOpen = false;
		};
		const onKey = (event: KeyboardEvent) => {
			if (event.key === 'Escape') accountOpen = false;
		};
		window.addEventListener('click', dismiss, true);
		window.addEventListener('keydown', onKey);
		return () => {
			window.removeEventListener('click', dismiss, true);
			window.removeEventListener('keydown', onKey);
		};
	});

	const active = $derived(section(page.url.pathname));
	const trail = $derived(currentTrail());
</script>

{#if checking}
	<p class="centred muted">Signing you in…</p>
{:else if !identity}
	<section class="signin">
		<div class="signin-head">
			<Logo size={26} />
			<button class="icon" onclick={() => chooseTheme(themes[(themes.indexOf(theme) + 1) % 3])}>
				{themeLabel(theme)}
			</button>
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
			<a class="brand" href={resolve('/(app)/dashboard')} onclick={() => (menuOpen = false)}>
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
								<Icon name={item.icon} />
								<span>{item.label}</span>
							</a>
						{/each}
					</div>
				{/each}
			</nav>

			<div class="sidebar-foot">
				<!-- Settings that belong to the person rather than to a page live
				     behind the account, which is where someone looks for them. -->
				<button
					class="account"
					class:open={accountOpen}
					onclick={() => (accountOpen = !accountOpen)}
					aria-expanded={accountOpen}
					aria-haspopup="menu"
				>
					<Icon name="account" />
					<span class="account-name">Account {short(identity.tenant_id)}</span>
					<Icon name="chevron" />
				</button>

				{#if accountOpen}
					<div class="menu" role="menu">
						<p class="menu-title">Appearance</p>
						{#each themes as choice (choice)}
							<button
								role="menuitemradio"
								aria-checked={theme === choice}
								class:selected={theme === choice}
								onclick={() => chooseTheme(choice)}
							>
								{themeLabel(choice)}
							</button>
						{/each}
						<div class="menu-rule"></div>
						<button role="menuitem" onclick={signOut}>Sign out</button>
					</div>
				{/if}
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
					<a href={resolve('/(app)/dashboard')}>Dashboard</a>
					{#if active && active.href !== '/(app)/dashboard'}
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
			</header>

			<main>
				{@render children()}
			</main>
		</div>
	</div>
{/if}
