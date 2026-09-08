<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type Job, type Worker } from '$lib/api';
	import { ago, short } from '$lib/format';
	import { describe } from '$lib/problem';
	import State from '$lib/components/State.svelte';
	import { operation } from '$lib/words';
	import Poll from '$lib/components/Poll.svelte';

	let jobs = $state<Job[]>([]);
	let workers = $state<Worker[]>([]);
	let error = $state('');

	async function load() {
		try {
			const [j, w] = await Promise.all([
				api.get<{ jobs: Job[] }>('/v1/jobs?limit=10'),
				api.get<{ workers: Worker[] }>('/v1/workers')
			]);
			jobs = j.jobs;
			workers = w.workers;
			error = '';
		} catch (e) {
			error = describe(e);
		}
	}

	$effect(() => {
		void load();
	});

	const running = $derived(
		jobs.filter((j) => j.state === 'running' || j.state === 'pending').length
	);
	const online = $derived(workers.filter((w) => w.state === 'online').length);
	const failed = $derived(jobs.filter((j) => j.state === 'failed').length);
</script>

<Poll active={running > 0} {load} />

<h1>Overview</h1>

{#if error}<p class="error">{error}</p>{/if}

<div class="grid">
	<div class="stat">
		<div class="value">{running}</div>
		<div class="label">being processed</div>
	</div>
	<div class="stat">
		<div class="value">{online}</div>
		<div class="label">machines available</div>
	</div>
	<div class="stat">
		<div class="value">{failed}</div>
		<div class="label">need attention</div>
	</div>
</div>

<h2>Recent activity</h2>
<div class="panel">
	{#if jobs.length === 0}
		<p class="muted">Nothing has been processed yet. Add some media to get started.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Reference</th><th>Status</th><th>Started</th><th>Finished</th></tr>
			</thead>
			<tbody>
				{#each jobs as job (job.id)}
					<tr>
						<td
							><a class="mono" href={resolve('/(app)/jobs/[id]', { id: job.id })}>{short(job.id)}</a
							></td
						>
						<td><State value={job.state} /></td>
						<td class="muted">{ago(job.created_at)}</td>
						<td class="muted">{job.finished_at ? ago(job.finished_at) : '—'}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>

<h2>Machines</h2>
<div class="panel">
	{#if workers.length === 0}
		<p class="muted">No machines are connected, so nothing can be processed yet.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Machine</th><th>Status</th><th>Type</th><th>Can do</th><th>Last seen</th></tr>
			</thead>
			<tbody>
				{#each workers as w (w.id)}
					<tr>
						<td><a href={resolve('/(app)/workers')}>{w.name}</a></td>
						<td><State value={w.state} /></td>
						<td class="muted">{w.arch}{w.gpu_model ? ` · ${w.gpu_model}` : ''}</td>
						<td class="muted">
							{(w.capabilities.operations ?? []).map(operation).join(' · ') || '—'}
						</td>
						<td class="muted">{ago(w.last_heartbeat_at)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>
