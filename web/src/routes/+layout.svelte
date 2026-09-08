<script lang="ts">
	// The root layout carries only what every page needs. The signed-in
	// dashboard and the public pages have their own, because a marketing page
	// should not wait behind a sign-in check.
	import '../app.css';

	let { children } = $props();
</script>

{@render children()}
