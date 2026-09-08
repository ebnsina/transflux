<script lang="ts">
	// An encoding ladder: one source resolved into progressively smaller
	// renditions. It says what the product does, and it stays legible at the
	// 20px it is actually drawn at.
	let { size = 22 }: { size?: number } = $props();
</script>

<span class="logo">
	<svg
		width={size}
		height={size}
		viewBox="0 0 24 24"
		fill="none"
		aria-hidden="true"
		focusable="false"
	>
		<rect x="0.75" y="0.75" width="22.5" height="22.5" rx="6.5" class="frame" />
		<rect x="5" y="6.5" width="14" height="3" rx="1.5" class="bar source" />
		<rect x="5" y="11.5" width="9.5" height="3" rx="1.5" class="bar" />
		<rect x="5" y="16.5" width="5.5" height="3" rx="1.5" class="bar" />
	</svg>
	<span class="wordmark">Transflux</span>
</span>

<style>
	.logo {
		display: inline-flex;
		align-items: center;
		gap: 9px;
	}
	.frame {
		stroke: var(--line-strong);
		stroke-width: 1.5;
	}
	.bar {
		fill: var(--muted);
	}
	.bar.source {
		fill: var(--accent);
	}
	.wordmark {
		font-weight: 700;
		font-variation-settings: 'wght' 700;
		letter-spacing: -0.025em;
		font-size: 16px;
		color: var(--text);
	}
</style>
