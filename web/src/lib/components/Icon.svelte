<script lang="ts">
	// A small stroke set drawn on one 16px grid, so weights match across the
	// sidebar rather than each icon carrying its own optical size.
	export type Name = 'dashboard' | 'media' | 'activity' | 'machines' | 'chevron' | 'account';

	let { name, size = 16 }: { name: Name; size?: number } = $props();
</script>

<svg
	width={size}
	height={size}
	viewBox="0 0 16 16"
	fill="none"
	stroke="currentColor"
	stroke-width="1.4"
	stroke-linecap="round"
	stroke-linejoin="round"
	aria-hidden="true"
	focusable="false"
>
	{#if name === 'dashboard'}
		<rect x="1.75" y="1.75" width="5" height="5" rx="1.4" />
		<rect x="9.25" y="1.75" width="5" height="5" rx="1.4" />
		<rect x="1.75" y="9.25" width="5" height="5" rx="1.4" />
		<rect x="9.25" y="9.25" width="5" height="5" rx="1.4" />
	{:else if name === 'media'}
		<rect x="1.5" y="2.75" width="13" height="10.5" rx="2" />
		<path d="M6.5 6.4v3.2L9.6 8z" />
	{:else if name === 'activity'}
		<path d="M1.75 8h2.6l1.6-4 2.6 8 1.7-4h3.6" />
	{:else if name === 'machines'}
		<rect x="1.75" y="2.25" width="12.5" height="4.5" rx="1.4" />
		<rect x="1.75" y="9.25" width="12.5" height="4.5" rx="1.4" />
		<path d="M4.4 4.5h.01M4.4 11.5h.01" />
	{:else if name === 'account'}
		<circle cx="8" cy="5.6" r="2.6" />
		<path d="M2.9 13.6a5.4 5.4 0 0 1 10.2 0" />
	{:else if name === 'chevron'}
		<path d="M4.5 6.25 8 9.75l3.5-3.5" />
	{/if}
</svg>
