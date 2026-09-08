<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';
	import { api, type ArtifactSet, type JobDetail } from '$lib/api';
	import { ago, bytes, seconds, short } from '$lib/format';
	import { describe } from '$lib/problem';
	import { check as checkName, operation } from '$lib/words';
	import { setTrail } from '$lib/breadcrumb.svelte';
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
			setTrail(short(detail.job.id));
		} catch (e) {
			error = describe(e);
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
			error = describe(e);
		}
	}

	async function download(artifactId: string) {
		const { url } = await api.get<{ url: string }>(`/v1/artifacts/${artifactId}/download`);
		window.open(url, '_blank');
	}

	const failedChecks = $derived(
		(detail?.validation?.checks ?? []).filter((c) => c.status === 'fail')
	);

	// Only a finished package can be watched: a set still being built has
	// segments the playlist refers to but storage does not have yet.
	const streamable = $derived(
		sets.some((s) => s.state === 'complete' && s.artifacts.some((a) => a.label === 'hls_master'))
	);
</script>

<Poll {active} {load} />

{#if error}<p class="error">{error}</p>{/if}

{#if detail}
	<div class="spread">
		<div>
			<h1>Processing <span class="mono">{short(detail.job.id)}</span></h1>
			<p class="muted">
				started {ago(detail.job.created_at)}
				{#if detail.job.finished_at}· finished {ago(detail.job.finished_at)}{/if}
			</p>
		</div>
		<div class="row">
			<State value={detail.job.state} />
			{#if streamable}
				<a class="play" href={resolve('/(app)/play/[id]', { id })}>Watch</a>
			{/if}
			{#if active}<button onclick={cancel}>Cancel</button>{/if}
		</div>
	</div>

	<h2>Steps</h2>
	<div class="panel">
		<table>
			<thead>
				<tr><th>Step</th><th>Status</th><th>Tries</th><th>What happened</th></tr>
			</thead>
			<tbody>
				{#each detail.tasks as task (task.id)}
					<tr>
						<td>{operation(task.operation)}</td>
						<td><State value={task.state} /></td>
						<td class="muted">{task.attempt_count} of {task.max_attempts}</td>
						<td class="muted">
							<!-- The stored reason is written for operators and can contain
							     internal detail, so the reader gets the shape of the problem
							     instead. -->
							{#if task.failure_class === 'permanent_input'}
								The media could not be used
							{:else if task.failure_class === 'permanent_config'}
								This request could not be carried out
							{:else if task.failure_reason}
								Interrupted, and we stopped retrying
							{:else}—{/if}
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>

	{#if detail.attempts?.length}
		<h2>Runs</h2>
		<div class="panel">
			<table>
				<thead>
					<tr>
						<th>Step</th><th>Try</th><th>Machine</th><th>Status</th>
						<th>Processing time</th><th>Elapsed</th><th>Memory used</th><th>Why here</th>
					</tr>
				</thead>
				<tbody>
					{#each detail.attempts as attempt (attempt.id)}
						<tr>
							<td>{operation(attempt.operation)}</td>
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
										{openReason === attempt.id ? 'Hide' : 'Show'}
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
			Quality checks {#if failedChecks.length}<span class="badge bad"
					>{failedChecks.length} did not pass</span
				>{/if}
		</h2>
		<div class="panel">
			<table>
				<thead>
					<tr><th>Output</th><th>What we checked</th><th>Result</th></tr>
				</thead>
				<tbody>
					{#each detail.validation.checks as check (check.label + check.name)}
						<tr>
							<td class="muted">{check.label || 'Overall'}</td>
							<td>{checkName(check.name)}</td>
							<td><State value={check.status} /></td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}

	{#each sets as set (set.id)}
		<h2>Results <State value={set.state} /></h2>
		<div class="panel">
			{#if set.artifacts.length === 0}
				<p class="muted">Nothing has been produced yet.</p>
			{:else}
				<table>
					<thead>
						<tr><th>Name</th><th>Size</th><th>Details</th><th></th></tr>
					</thead>
					<tbody>
						{#each set.artifacts as artifact (artifact.id)}
							<tr>
								<td class="mono">{artifact.label}</td>
								<td class="muted">{bytes(artifact.size_bytes)}</td>
								<td class="muted mono">
									{#if artifact.media}
										{artifact.media.width}×{artifact.media.height}
										{artifact.media.codec}
										{#if artifact.media.color}
											<span class="badge warn" style="margin-left:6px">HDR colour</span>
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

<style>
	.play {
		background: var(--accent);
		color: var(--accent-contrast);
		border: 1px solid var(--accent);
		border-radius: var(--radius-sm);
		padding: 8px 16px;
		font-variation-settings: 'wght' 600;
	}
	.play:hover {
		text-decoration: none;
		filter: brightness(1.06);
	}
</style>
