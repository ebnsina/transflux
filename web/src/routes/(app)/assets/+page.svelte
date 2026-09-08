<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type Asset } from '$lib/api';
	import { ago, short } from '$lib/format';
	import { describe } from '$lib/problem';
	import State from '$lib/components/State.svelte';

	let assets = $state<Asset[]>([]);
	let error = $state('');
	let name = $state('');
	let creating = $state(false);

	async function load() {
		try {
			assets = (await api.get<{ assets: Asset[] }>('/v1/assets?limit=100')).assets;
			error = '';
		} catch (e) {
			error = describe(e);
		}
	}

	$effect(() => {
		void load();
	});

	async function create(event: SubmitEvent) {
		event.preventDefault();
		creating = true;
		try {
			await api.post('/v1/assets', { name: name.trim() || null });
			name = '';
			await load();
		} catch (e) {
			error = describe(e);
		}
		creating = false;
	}
</script>

<div class="spread">
	<h1>Media</h1>
	<form class="row" onsubmit={create}>
		<input type="text" bind:value={name} placeholder="Name your media" />
		<button class="primary" type="submit" disabled={creating}>Create</button>
	</form>
</div>

{#if error}<p class="error">{error}</p>{/if}

<div class="panel">
	{#if assets.length === 0}
		<p class="muted">Nothing here yet. Add some media above.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Name</th><th>Reference</th><th>Status</th><th>Added</th></tr>
			</thead>
			<tbody>
				{#each assets as asset (asset.id)}
					<tr>
						<td
							><a href={resolve('/(app)/assets/[id]', { id: asset.id })}
								>{asset.name ?? 'Untitled'}</a
							></td
						>
						<td class="muted mono">{short(asset.id)}</td>
						<td><State value={asset.status} /></td>
						<td class="muted">{ago(asset.created_at)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>
