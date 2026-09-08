<script lang="ts">
	// Every sample on these pages is passed in as a string from a script block
	// rather than written as markup, so braces in JSON never have to be escaped.
	let { code, caption = '' }: { code: string; caption?: string } = $props();
</script>

<figure>
	{#if caption}<figcaption class="muted">{caption}</figcaption>{/if}
	<pre>{code}</pre>
</figure>

<style>
	figure {
		margin: 0 0 14px;
	}
	figcaption {
		font-size: 12px;
		margin-bottom: 6px;
	}
	pre {
		background: var(--panel-2);
		border: 1px solid var(--line);
		border-radius: var(--radius-sm);
		padding: 14px 16px;
		margin: 0;
		overflow-x: auto;
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 1.65;
		tab-size: 2;
	}
</style>
