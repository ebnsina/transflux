<script lang="ts">
	import { state } from '$lib/words';

	let { value }: { value: string } = $props();

	// A state's colour should mean the same thing everywhere: green is done,
	// red needs attention, blue is in flight.
	const tone: Record<string, string> = {
		succeeded: 'ok',
		complete: 'ok',
		ready: 'ok',
		online: 'ok',
		pass: 'ok',
		active: 'ok',
		running: 'run',
		leased: 'run',
		queued: 'run',
		building: 'run',
		validating: 'run',
		probing: 'run',
		pending: '',
		draining: 'warn',
		unhealthy: 'warn',
		warn: 'warn',
		source_ready: 'warn',
		failed: 'bad',
		cancelled: 'bad',
		expired: 'bad',
		offline: 'bad',
		fail: 'bad'
	};
</script>

<span class="badge {tone[value] ?? ''}">{state(value)}</span>
