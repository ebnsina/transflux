<script lang="ts">
	// Re-runs a loader while a job is in flight, so a page showing work in
	// progress does not need a manual refresh to be useful.
	let {
		active,
		every = 2000,
		load
	}: { active: boolean; every?: number; load: () => Promise<void> } = $props();

	$effect(() => {
		if (!active) return;
		const timer = setInterval(() => void load(), every);
		return () => clearInterval(timer);
	});
</script>
