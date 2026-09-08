<script lang="ts">
	import Hls from 'hls.js';

	let { src, poster }: { src: string; poster?: string } = $props();

	type Level = { index: number; height: number; bitrate: number };

	let video: HTMLVideoElement;
	let hls: Hls | null = null;

	let levels = $state<Level[]>([]);
	let chosen = $state(-1); // -1 is automatic
	let playing = $state(-1); // what is actually on screen
	let error = $state('');
	let native = $state(false);

	$effect(() => {
		if (!video || !src) return;

		// Safari plays HLS itself, and its own switching is better than
		// anything we would do through Media Source Extensions.
		if (!Hls.isSupported()) {
			if (video.canPlayType('application/vnd.apple.mpegurl')) {
				native = true;
				video.src = src;
				return;
			}
			error = 'This browser cannot play HLS.';
			return;
		}

		const instance = new Hls({ enableWorker: true });
		hls = instance;

		instance.on(Hls.Events.MANIFEST_PARSED, () => {
			levels = instance.levels
				.map((l, index) => ({ index, height: l.height, bitrate: l.bitrate }))
				.sort((a, b) => b.height - a.height);
		});

		// What the player chose is not the same question as what we asked for:
		// on automatic it changes with the connection, and seeing it is the
		// point of a test player.
		instance.on(Hls.Events.LEVEL_SWITCHED, (_e, data) => {
			playing = data.level;
		});

		instance.on(Hls.Events.ERROR, (_e, data) => {
			if (!data.fatal) return;
			// A fatal error is recoverable often enough to be worth trying
			// before telling someone it is broken.
			switch (data.type) {
				case Hls.ErrorTypes.NETWORK_ERROR:
					instance.startLoad();
					break;
				case Hls.ErrorTypes.MEDIA_ERROR:
					instance.recoverMediaError();
					break;
				default:
					error = 'Playback stopped and could not be recovered.';
					instance.destroy();
			}
		});

		instance.loadSource(src);
		instance.attachMedia(video);

		return () => {
			instance.destroy();
			hls = null;
		};
	});

	function choose(index: number) {
		chosen = index;
		if (hls) {
			// currentLevel switches immediately, discarding what is buffered;
			// that is what you want when checking a rendition by eye.
			hls.currentLevel = index;
		}
	}

	function describe(l: Level): string {
		return `${l.height}p · ${Math.round(l.bitrate / 1000)} kbps`;
	}
</script>

<div class="player">
	<video bind:this={video} {poster} controls playsinline></video>

	{#if error}
		<p class="error">{error}</p>
	{:else if native}
		<p class="muted note">
			This browser plays HLS itself, so it chooses the quality. Open it in a browser that uses Media
			Source Extensions to pick one by hand.
		</p>
	{:else if levels.length}
		<div class="quality">
			<span class="muted label">Quality</span>
			<button class:selected={chosen === -1} onclick={() => choose(-1)}>
				Automatic
				{#if chosen === -1 && playing >= 0}
					<span class="now">{levels.find((l) => l.index === playing)?.height ?? '?'}p</span>
				{/if}
			</button>
			{#each levels as level (level.index)}
				<button class:selected={chosen === level.index} onclick={() => choose(level.index)}>
					{describe(level)}
				</button>
			{/each}
		</div>
		<p class="muted note">
			Switching quality by hand drops what is already buffered, so the change is visible immediately
			rather than at the next segment.
		</p>
	{:else}
		<p class="muted note">Loading…</p>
	{/if}
</div>

<style>
	.player {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}
	video {
		width: 100%;
		max-height: 60vh;
		background: #000;
		border: 1px solid var(--line);
		border-radius: var(--radius);
	}
	.quality {
		display: flex;
		flex-wrap: wrap;
		gap: 8px;
		align-items: center;
	}
	.label {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.07em;
		margin-right: 4px;
	}
	.quality button.selected {
		border-color: var(--accent);
		background: var(--accent-soft);
		color: var(--accent);
	}
	.now {
		opacity: 0.7;
		font-size: 12px;
		margin-left: 4px;
	}
	.note {
		font-size: 13px;
		margin: 0;
	}
</style>
