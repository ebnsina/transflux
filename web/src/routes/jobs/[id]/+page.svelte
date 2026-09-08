<script lang="ts">
	import { page } from '$app/state';
	import { api, type ArtifactSet, type JobDetail } from '$lib/api';
	import { ago, bytes, seconds, short } from '$lib/format';
	import State from '$lib/components/State.svelte';
	import Poll from '$lib/components/Poll.svelte';

	const id = $derived(page.params.id!);

	let detail = $state<JobDetail | null>(null);
	let sets = $state<ArtifactSet[]>([]);
	let error = $state('');
	let openReason = $state<string | null>(null);

	async function load() {
		try {
			detail = await api.get<JobDetail>(`/v1/jobs/${id}`);
			sets = (await api.get<{ artifact_sets: ArtifactSet[] }>(`/v1/jobs/${id}/artifacts`))
				.artifact_sets;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		void load();
	});

	const active = $derived(detail?.job.state === 'running' || detail?.job.state === 'pending');

	async function cancel() {
		try {
			await api.post(`/v1/jobs/${id}/cancel`);
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	async function download(artifactId: string) {
		const { url } = await api.get<{ url: string }>(`/v1/artifacts/${artifactId}/download`);
		window.open(url, '_blank');
	}

	const failedChecks = $derived(
		(detail?.validation?.checks ?? []).filter((c) => c.status === 'fail')
	);
</script>

<Poll {active} {load} />

{#if error}<p class="error">{error}</p>{/if}

{#if detail}
	<div class="spread">
		<div>
			<h1>Job <span class="mono">{short(detail.job.id)}</span></h1>
			<p class="muted">
				created {ago(detail.job.created_at)}
				{#if detail.job.finished_at}· finished {ago(detail.job.finished_at)}{/if}
			</p>
		</div>
		<div class="row">
			<State value={detail.job.state} />
			{#if active}<button onclick={cancel}>Cancel</button>{/if}
		</div>
	</div>

	<h2>Tasks</h2>
	<div class="panel">
		<table>
			<thead>
				<tr><th>Operation</th><th>State</th><th>Attempts</th><th>Failure</th></tr>
			</thead>
			<tbody>
				{#each detail.tasks as task (task.id)}
					<tr>
						<td>{task.operation}</td>
						<td><State value={task.state} /></td>
						<td class="muted">{task.attempt_count} of {task.max_attempts}</td>
						<td class="muted">
							{#if task.failure_reason}
								<span class="badge bad">{task.failure_class}</span>
								{task.failure_reason}
							{:else}—{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>

	{#if detail.attempts?.length}
		<h2>Attempts</h2>
		<div class="panel">
			<table>
				<thead>
					<tr>
						<th>Operation</th><th>#</th><th>Worker</th><th>State</th>
						<th>CPU</th><th>Wall</th><th>Peak memory</th><th>Why</th>
					</tr>
				</thead>
				<tbody>
					{#each detail.attempts as attempt (attempt.id)}
						<tr>
							<td>{attempt.operation}</td>
							<td class="muted">{attempt.attempt_number}</td>
							<td class="muted">{attempt.worker_name}</td>
							<td><State value={attempt.state} /></td>
							<td class="muted">{seconds(attempt.cpu_seconds)}</td>
							<td class="muted">{seconds(attempt.wall_seconds)}</td>
							<td class="muted">{bytes(attempt.peak_memory_bytes)}</td>
							<td>
								{#if attempt.schedule_reason}
									<button
										class="link"
										onclick={() => (openReason = openReason === attempt.id ? null : attempt.id)}
									>
										{openReason === attempt.id ? 'hide' : 'show'}
									</button>
								{:else}—{/if}
							</td>
						</tr>
						{#if openReason === attempt.id}
							<tr>
								<td colspan="8">
									<!-- The scheduler stores why it chose this worker, so a slow
									     job is a question the system can answer itself. -->
									<pre class="reason">{attempt.schedule_reason}</pre>
								</td>
							</tr>
						{/if}
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	{#if detail.validation?.checks?.length}
		<h2>
			Validation {#if failedChecks.length}<span class="badge bad">{failedChecks.length} failed</span
				>{/if}
		</h2>
		<div class="panel">
			<table>
				<thead>
					<tr><th>Artifact</th><th>Check</th><th>Status</th><th>Detail</th></tr>
				</thead>
				<tbody>
					{#each detail.validation.checks as check (check.label + check.name)}
						<tr>
							<td class="muted">{check.label || '(set)'}</td>
							<td>{check.name.replace(/_/g, ' ')}</td>
							<td><State value={check.status} /></td>
							<td class="muted mono">
								{check.detail && Object.keys(check.detail).length
									? Object.entries(check.detail)
											.map(([k, v]) => `${k}=${v}`)
											.join('  ')
									: '—'}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	{#each sets as set (set.id)}
		<h2>Artifacts · version {set.version} <State value={set.state} /></h2>
		<div class="panel">
			{#if set.artifacts.length === 0}
				<p class="muted">Nothing registered yet.</p>
			{:else}
				<table>
					<thead>
						<tr><th>Label</th><th>Kind</th><th>Size</th><th>Media</th><th></th></tr>
					</thead>
					<tbody>
						{#each set.artifacts as artifact (artifact.id)}
							<tr>
								<td class="mono">{artifact.label}</td>
								<td class="muted">{artifact.kind}</td>
								<td class="muted">{bytes(artifact.size_bytes)}</td>
								<td class="muted mono">
									{#if artifact.media}
										{artifact.media.width}×{artifact.media.height}
										{artifact.media.codec}
										{#if artifact.media.color}
											<span class="badge warn" style="margin-left:6px">hdr</span>
										{/if}
									{:else}—{/if}
								</td>
								<td>
									<!-- A short-lived signed URL, so a copied link stops working. -->
									<button onclick={() => download(artifact.id)}>Download</button>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	{/each}
{:else if !error}
	<p class="muted">Loading…</p>
{/if}
