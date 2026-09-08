<script lang="ts">
	import { page } from '$app/state';
	import { api, type ArtifactSet } from '$lib/api';
	import { describe } from '$lib/problem';
	import { setTrail } from '$lib/breadcrumb.svelte';
	import Player from '$lib/components/Player.svelte';

	const jobId = $derived(page.params.id!);

	let src = $state('');
	let poster = $state('');
	let expires = $state(0);
	let error = $state('');

	async function load() {
		try {
			setTrail('Playing');
			const { artifact_sets } = await api.get<{ artifact_sets: ArtifactSet[] }>(
				`/v1/jobs/${jobId}/artifacts`
			);

			const artifacts = artifact_sets.flatMap((s) => s.artifacts);
			const playlist = artifacts.find((a) => a.label === 'hls_master');
			if (!playlist) {
				error = 'This job did not produce anything that can be streamed.';
				return;
			}

			// A playback link, not a download link: the player follows it with no
			// key of its own.
			const link = await api.post<{ url: string; expires_in_seconds: number }>(
				`/v1/artifacts/${playlist.id}/playback`
			);
			src = link.url;
			expires = Math.round(link.expires_in_seconds / 3600);

			const still = artifacts.find((a) => a.kind === 'poster');
			if (still) {
				const download = await api.get<{ url: string }>(`/v1/artifacts/${still.id}/download`);
				poster = download.url;
			}
			error = '';
		} catch (e) {
			error = describe(e);
		}
	}

	$effect(() => {
		void load();
	});
</script>

<h1>Playing</h1>
<p class="muted">
	The player follows a link that carries its own permission, so it needs no account. Pick a size to
	see that rendition on screen.
</p>

{#if error}
	<p class="error">{error}</p>
{:else if src}
	<Player {src} {poster} />
	<p class="muted" style="margin-top:14px">This link stops working after {expires} hours.</p>
{:else}
	<p class="muted">Preparing…</p>
{/if}
