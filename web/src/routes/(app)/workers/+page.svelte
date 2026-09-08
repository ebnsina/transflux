<script lang="ts">
	import { api, type Worker } from '$lib/api';
	import { ago, bytes } from '$lib/format';
	import { describe } from '$lib/problem';
	import { operation } from '$lib/words';
	import State from '$lib/components/State.svelte';
	import Poll from '$lib/components/Poll.svelte';

	let workers = $state<Worker[]>([]);
	let error = $state('');

	async function load() {
		try {
			workers = (await api.get<{ workers: Worker[] }>('/v1/workers')).workers;
			error = '';
		} catch (e) {
			error = describe(e);
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
			error = describe(e);
		}
	}
</script>

<Poll active={true} every={5000} {load} />

<h1>Machines</h1>
<p class="muted">
	These do the actual work. Each one reports what it is able to do, and work is only sent to a
	machine that can handle it.
</p>

{#if error}<p class="error">{error}</p>{/if}

{#if workers.length === 0}
	<div class="panel"><p class="muted">No machines are connected yet.</p></div>
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
					<button onclick={() => setState(w.id, 'draining')}>Stop sending work</button>
				{:else if w.state === 'draining'}
					<button onclick={() => setState(w.id, 'online')}>Start sending work again</button>
				{/if}
			</div>
		</div>

		<table style="margin-top:10px">
			<tbody>
				<tr>
					<td class="muted" style="width:170px">Hardware</td>
					<td>
						{w.os}/{w.arch} · {w.cpu_cores} cores · {bytes(w.memory_bytes)}
						{#if w.gpu_model}· {w.gpu_model}{/if}
					</td>
				</tr>
				<tr>
					<td class="muted">Media software</td>
					<td class="mono">FFmpeg {w.ffmpeg_version}</td>
				</tr>
				<tr>
					<td class="muted">Can do</td>
					<td>{(w.capabilities.operations ?? []).map(operation).join(' · ') || '—'}</td>
				</tr>
				<tr>
					<td class="muted">At the same time</td>
					<td class="mono">
						<!-- Declared by the worker, never derived from core count: a
						     32-core box is not 32 concurrent encodes. -->
						{Object.entries(w.slot_capacity)
							.map(([k, v]) => `${v} × ${operation(k).toLowerCase()}`)
							.join(' · ') || '—'}
					</td>
				</tr>
				<tr>
					<td class="muted">Formats supported</td>
					<td class="muted">{(w.capabilities.encoders ?? []).length}</td>
				</tr>
				<tr>
					<td class="muted">Last seen</td>
					<td class="muted">{ago(w.last_heartbeat_at)}</td>
				</tr>
			</tbody>
		</table>
	</div>
{/each}
