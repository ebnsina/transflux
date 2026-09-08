<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type Job } from '$lib/api';
	import { ago, short } from '$lib/format';
	import { describe } from '$lib/problem';
	import State from '$lib/components/State.svelte';
	import Poll from '$lib/components/Poll.svelte';

	let jobs = $state<Job[]>([]);
	let error = $state('');

	async function load() {
		try {
			jobs = (await api.get<{ jobs: Job[] }>('/v1/jobs?limit=100')).jobs;
			error = '';
		} catch (e) {
			error = describe(e);
		}
	}

	$effect(() => {
		void load();
	});

	const active = $derived(jobs.some((j) => j.state === 'running' || j.state === 'pending'));
</script>

<Poll {active} {load} />

<h1>Activity</h1>
{#if error}<p class="error">{error}</p>{/if}

<div class="panel">
	{#if jobs.length === 0}
		<p class="muted">Nothing has been processed yet.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Reference</th><th>Status</th><th>Started</th><th>Finished</th></tr>
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
