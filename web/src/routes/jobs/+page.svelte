<script lang="ts">
	import { resolve } from '$app/paths';
	import { api, type Job } from '$lib/api';
	import { ago, short } from '$lib/format';
	import State from '$lib/components/State.svelte';
	import Poll from '$lib/components/Poll.svelte';

	let jobs = $state<Job[]>([]);
	let error = $state('');

	async function load() {
		try {
			jobs = (await api.get<{ jobs: Job[] }>('/v1/jobs?limit=100')).jobs;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		void load();
	});

	const active = $derived(jobs.some((j) => j.state === 'running' || j.state === 'pending'));
</script>

<Poll {active} {load} />

<h1>Jobs</h1>
{#if error}<p class="error">{error}</p>{/if}

<div class="panel">
	{#if jobs.length === 0}
		<p class="muted">No jobs yet.</p>
	{:else}
		<table>
			<thead>
				<tr><th>Job</th><th>State</th><th>Created</th><th>Finished</th><th>Reason</th></tr>
			</thead>
			<tbody>
				{#each jobs as job (job.id)}
					<tr>
						<td><a class="mono" href={resolve('/jobs/[id]', { id: job.id })}>{short(job.id)}</a></td
						>
						<td><State value={job.state} /></td>
						<td class="muted">{ago(job.created_at)}</td>
						<td class="muted">{job.finished_at ? ago(job.finished_at) : '—'}</td>
						<td class="muted">{job.failure_reason ?? ''}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	{/if}
</div>
