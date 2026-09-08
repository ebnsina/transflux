<script lang="ts">
	import { api, type Worker } from '$lib/api';
	import { ago, bytes } from '$lib/format';
	import State from '$lib/components/State.svelte';
	import Poll from '$lib/components/Poll.svelte';

	let workers = $state<Worker[]>([]);
	let error = $state('');

	async function load() {
		try {
			workers = (await api.get<{ workers: Worker[] }>('/v1/workers')).workers;
			error = '';
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}

	$effect(() => {
		void load();
	});

	async function setState(id: string, state: string) {
		try {
			await api.post(`/v1/workers/${id}/state`, { state });
			await load();
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
		}
	}
</script>

<Poll active={true} every={5000} {load} />

<h1>Workers</h1>
<p class="muted">
	Workers are shared infrastructure: one worker serves every tenant, and the scheduler matches on
	declared capability rather than on which machine it is.
</p>

{#if error}<p class="error">{error}</p>{/if}

{#if workers.length === 0}
	<div class="panel"><p class="muted">No workers have registered.</p></div>
{/if}

{#each workers as w (w.id)}
	<div class="panel" style="margin-bottom:12px">
		<div class="spread">
			<div>
				<strong>{w.name}</strong>
				<span class="muted"> · {w.hostname}</span>
			</div>
			<div class="row">
				<State value={w.state} />
				{#if w.state === 'online'}
					<button onclick={() => setState(w.id, 'draining')}>Drain</button>
				{:else if w.state === 'draining'}
					<button onclick={() => setState(w.id, 'online')}>Return to service</button>
				{/if}
			</div>
		</div>

		<table style="margin-top:10px">
			<tbody>
				<tr>
					<td class="muted" style="width:160px">Hardware</td>
					<td>
						{w.os}/{w.arch} · {w.cpu_cores} cores · {bytes(w.memory_bytes)}
						{#if w.gpu_model}· {w.gpu_model}{/if}
					</td>
				</tr>
				<tr>
					<td class="muted">FFmpeg</td>
					<td class="mono">{w.ffmpeg_version}</td>
				</tr>
				<tr>
					<td class="muted">Operations</td>
					<td class="mono">{(w.capabilities.operations ?? []).join(', ') || '—'}</td>
				</tr>
				<tr>
					<td class="muted">Slot capacity</td>
					<td class="mono">
						<!-- Declared by the worker, never derived from core count: a
						     32-core box is not 32 concurrent encodes. -->
						{Object.entries(w.slot_capacity)
							.map(([k, v]) => `${k}:${v}`)
							.join('  ') || '—'}
					</td>
				</tr>
				<tr>
					<td class="muted">Encoders</td>
					<td class="muted">{(w.capabilities.encoders ?? []).length} detected</td>
				</tr>
				<tr>
					<td class="muted">Heartbeat</td>
					<td class="muted">{ago(w.last_heartbeat_at)}</td>
				</tr>
			</tbody>
		</table>
	</div>
{/each}
