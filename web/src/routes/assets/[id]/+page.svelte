<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
	import { api, type Asset, type Pipeline, type Track } from '$lib/api';
	import { bytes, duration, fps } from '$lib/format';
	import State from '$lib/components/State.svelte';

	const id = $derived(page.params.id!);

	let asset = $state<Asset | null>(null);
	let pipelines = $state<Pipeline[]>([]);
	let error = $state('');
	let notice = $state('');
	let uploading = $state(false);
	let progress = $state(0);
	let files = $state<FileList | null>(null);

	async function load() {
		try {
			const [a, p] = await Promise.all([
				api.get<Asset>(`/v1/assets/${id}`),
				api.get<{ pipelines: Pipeline[] }>('/v1/pipelines')
			]);
			asset = a;
			pipelines = p.pipelines;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		void load();
	});

	const version = $derived(asset?.versions?.[0]);
	const media = $derived(version?.media);

	/**
	 * Uploads directly to storage over presigned URLs. The bytes never pass
	 * through the control plane, which is the same path a customer's own
	 * client would take.
	 */
	async function upload() {
		const file = files?.[0];
		if (!file || !version) return;

		uploading = true;
		progress = 0;
		error = '';
		try {
			const created = await api.post<{
				upload: { id: string; part_size: number; part_count: number };
				parts: { number: number; url: string }[];
			}>(`/v1/assets/${id}/uploads`, {
				asset_version_id: version.id,
				size_bytes: file.size,
				content_type: file.type || 'application/octet-stream'
			});

			const { part_size } = created.upload;
			for (const part of created.parts) {
				const start = (part.number - 1) * part_size;
				const chunk = file.slice(start, Math.min(start + part_size, file.size));
				const res = await fetch(part.url, { method: 'PUT', body: chunk });
				if (!res.ok) throw new Error(`Uploading part ${part.number} failed: ${res.status}`);
				progress = Math.round((part.number / created.upload.part_count) * 100);
			}

			await api.post(`/v1/uploads/${created.upload.id}/complete`);
			notice = 'Upload complete and verified.';
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
		uploading = false;
	}

	async function run(pipeline: string) {
		if (!version) return;
		try {
			const created = await api.post<{ job: { id: string } }>('/v1/jobs', {
				asset_version_id: version.id,
				pipeline
			});
			await goto(resolve('/jobs/[id]', { id: created.job.id }));
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	function describe(t: Track): string {
		if (t.kind === 'video') {
			return `${t.width}×${t.height} ${t.codec} ${t.pixel_format ?? ''} ${t.bit_depth ?? 8}-bit · ${fps(t.fps_num, t.fps_den)} fps`;
		}
		if (t.kind === 'audio') {
			return `${t.codec} · ${t.channels ?? '?'}ch · ${t.sample_rate_hz ?? '?'} Hz${t.language ? ` · ${t.language}` : ''}`;
		}
		return t.codec ?? t.kind;
	}
</script>

{#if error}<p class="error">{error}</p>{/if}
{#if notice}<p class="muted">{notice}</p>{/if}

{#if asset}
	<div class="spread">
		<div>
			<h1>{asset.name ?? '(unnamed asset)'}</h1>
			<p class="muted mono">{asset.id}</p>
		</div>
		<State value={asset.status} />
	</div>

	{#if version}
		<h2>Source</h2>
		<div class="panel">
			<div class="spread">
				<div class="row">
					<State value={version.status} />
					{#if media}
						<span class="muted">
							{media.container} · {duration(media.duration_ms)} · {bytes(media.size_bytes)}
						</span>
					{/if}
				</div>
				{#if version.status === 'pending_source'}
					<div class="row">
						<input type="file" onchange={(e) => (files = e.currentTarget.files)} />
						<button class="primary" onclick={upload} disabled={uploading || !files?.length}>
							{uploading ? `Uploading ${progress}%` : 'Upload'}
						</button>
					</div>
				{/if}
			</div>

			{#if media?.tracks?.length}
				<table style="margin-top:12px">
					<thead>
						<tr><th>Track</th><th>Detail</th><th>Colour</th></tr>
					</thead>
					<tbody>
						{#each media.tracks as track (track.stream_index)}
							<tr>
								<td>{track.kind}</td>
								<td class="muted">{describe(track)}</td>
								<td class="muted mono">
									{#if track.kind === 'video'}
										{track.color_primaries ?? '—'} / {track.color_transfer ?? '—'}
										{#if track.hdr_format && track.hdr_format !== 'sdr'}
											<span class="badge warn" style="margin-left:6px">{track.hdr_format}</span>
										{/if}
									{:else}
										—
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{:else if version.status !== 'pending_source'}
				<p class="muted" style="margin-top:12px">
					Not probed yet. Run the probe pipeline to see what this contains.
				</p>
			{/if}
		</div>

		{#if version.status !== 'pending_source'}
			<h2>Run a pipeline</h2>
			<div class="panel">
				{#each pipelines as pipeline (pipeline.name)}
					<div class="spread" style="padding:6px 0">
						<div>
							<strong>{pipeline.name}</strong>
							<div class="muted">{pipeline.description}</div>
						</div>
						<button onclick={() => run(pipeline.name)}>Run</button>
					</div>
				{/each}
			</div>
		{/if}
	{/if}
{:else if !error}
	<p class="muted">Loading…</p>
{/if}
