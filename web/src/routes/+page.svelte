<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type Job, type Worker } from '$lib/api';
	import { ago, short } from '$lib/format';
	import State from '$lib/components/State.svelte';
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
			error = e instanceof Error ? e.message : String(e);
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
		<div class="label">jobs in flight</div>
	</div>
	<div class="stat">
		<div class="value">{online}</div>
		<div class="label">workers online</div>
	</div>
	<div class="stat">
		<div class="value">{failed}</div>
		<div class="label">failed jobs</div>
	</div>
</div>

<h2>Recent jobs</h2>
<div class="panel">
	{#if jobs.length === 0}
		<p class="muted">No jobs yet. Create an asset, upload a source, then run a pipeline.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Job</th><th>State</th><th>Created</th><th>Finished</th></tr>
			</thead>
			<tbody>
				{#each jobs as job (job.id)}
					<tr>
						<td><a class="mono" href={resolve('/jobs/[id]', { id: job.id })}>{short(job.id)}</a></td
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

<h2>Fleet</h2>
<div class="panel">
	{#if workers.length === 0}
		<p class="muted">No workers have registered.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Worker</th><th>State</th><th>Arch</th><th>Operations</th><th>Heartbeat</th></tr>
			</thead>
			<tbody>
				{#each workers as w (w.id)}
					<tr>
						<td><a href={resolve('/workers')}>{w.name}</a></td>
						<td><State value={w.state} /></td>
						<td class="muted">{w.arch}{w.gpu_model ? ` · ${w.gpu_model}` : ''}</td>
						<td class="muted mono">{(w.capabilities.operations ?? []).join(', ') || '—'}</td>
						<td class="muted">{ago(w.last_heartbeat_at)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>
