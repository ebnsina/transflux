<script lang="ts">
	import { resolve } from '$app/paths';

	// The pipeline, in the order a file actually moves through it.
	const stages = [
		{
			name: 'Upload',
			body: 'Resumable uploads go straight to object storage. The upload is verified before any work is scheduled.'
		},
		{
			name: 'Inspect',
			body: 'The source is probed and recorded: container, duration, and every video, audio and subtitle track with its codec, resolution, frame rate, pixel format, bit depth and language.'
		},
		{
			name: 'Convert',
			body: 'Renditions are encoded in parallel across machines, then packaged to CMAF so one set of segments serves both HLS and DASH.'
		},
		{
			name: 'Check',
			body: 'Every output is validated before it is delivered: size, checksum, that it plays, and that codec, resolution, duration, audio and HDR match what was asked for.'
		},
		{
			name: 'Deliver',
			body: 'Playback uses short-lived signed links. A player follows them with no API key, and media is fetched straight from storage rather than proxied.'
		}
	];

	const capabilities = [
		{
			title: 'Colour and HDR are preserved',
			body: 'HDR10, HLG, HDR10+ and Dolby Vision are detected and carried through encoding. Nothing is quietly flattened to SDR.'
		},
		{
			title: 'Ladders that fit the source',
			body: '1080p, 720p, 480p and 360p H.264 with AAC audio. Nothing is ever upscaled, and a rung the source cannot fill is simply not made.'
		},
		{
			title: 'Truncated output is caught',
			body: 'An encoder can exit successfully and still leave a short file. Validation compares the result against what was requested, so that file never reaches a viewer.'
		},
		{
			title: 'Work survives a lost machine',
			body: 'Each task holds a lease. A lease that stops being renewed is reclaimed and the task is redone elsewhere, without producing duplicate results.'
		},
		{
			title: 'Posters and scrubbing thumbnails',
			body: 'Poster images, plus a sprite sheet and a WebVTT index for the thumbnail strip a player shows while seeking.'
		},
		{
			title: 'Multi-tenant by default',
			body: 'Tenants are separated, and access is granted with API keys carrying explicit scopes.'
		}
	];
</script>

<svelte:head>
	<title>Transflux, media transcoding and delivery</title>
	<meta
		name="description"
		content="Transflux takes a source file, encodes an adaptive ladder, checks the result and serves it over signed links. Self-hostable."
	/>
</svelte:head>

<section class="hero">
	<h1>Media transcoding you can check the work of.</h1>
	<p class="lede">
		Transflux takes a source file, records what it contains, encodes an adaptive ladder, validates
		every output against what was asked for, and serves the result over short-lived signed links.
	</p>
	<p class="row">
		<a class="cta" href={resolve('/docs')}>Read the docs</a>
		<span class="muted">Two Go binaries, PostgreSQL, and any S3-compatible storage.</span>
	</p>
</section>

<h2>The pipeline</h2>
<ol class="stages">
	{#each stages as stage, i (stage.name)}
		<li class="panel stage">
			<div class="step mono">{i + 1}</div>
			<div>
				<h3>{stage.name}</h3>
				<p class="muted">{stage.body}</p>
			</div>
		</li>
	{/each}
</ol>

<h2>What it does</h2>
<div class="grid">
	{#each capabilities as item (item.title)}
		<div class="stat card">
			<h3>{item.title}</h3>
			<p class="muted">{item.body}</p>
		</div>
	{/each}
</div>

<h2>What it is not</h2>
<div class="panel">
	<p>
		This list matters as much as the one above it. Transflux is not a CDN, not a video player and
		not an analytics product. There is no DRM and no live streaming yet. It hands finished media to
		whatever you already use for those.
	</p>
</div>

<h2>Running it yourself</h2>
<div class="panel">
	<p>
		Transflux is self-hostable. It needs two Go binaries, a PostgreSQL database and any
		S3-compatible object storage. Media is read and written by the workers and served to players
		directly from storage, so the bytes never pass through a service you do not run.
	</p>
	<p class="last"><a href={resolve('/docs')}>Read the docs</a></p>
</div>

<style>
	.hero {
		padding: 88px 0 24px;
		max-width: 680px;
	}
	.hero h1 {
		font-size: 40px;
		line-height: 1.14;
		letter-spacing: -0.03em;
		margin: 0 0 18px;
	}
	.lede {
		font-size: 17px;
		line-height: 1.6;
		color: var(--muted);
		margin: 0 0 26px;
	}

	.cta {
		display: inline-block;
		background: var(--accent);
		color: var(--accent-contrast);
		border: 1px solid var(--accent);
		border-radius: var(--radius-sm);
		padding: 9px 18px;
		font-variation-settings: 'wght' 600;
	}
	.cta:hover {
		text-decoration: none;
		filter: brightness(1.06);
	}

	.stages {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 10px;
	}
	.stage {
		display: flex;
		align-items: flex-start;
		gap: 16px;
		padding: 18px 20px;
	}
	.step {
		flex: none;
		width: 26px;
		height: 26px;
		border: 1px solid var(--line-strong);
		border-radius: var(--radius-pill);
		display: grid;
		place-items: center;
		font-size: 12px;
		color: var(--accent);
		background: var(--accent-soft);
	}
	.stage p {
		margin: 4px 0 0;
	}

	.card h3,
	.stage h3 {
		font-size: 15px;
		font-variation-settings: 'wght' 600;
		font-weight: 600;
		margin: 0;
		letter-spacing: -0.01em;
	}
	.card p {
		margin: 6px 0 0;
	}

	.panel p.last {
		padding-top: 0;
	}

	@media (max-width: 860px) {
		.hero {
			padding-top: 56px;
		}
		.hero h1 {
			font-size: 30px;
		}
		.lede {
			font-size: 16px;
		}
	}
</style>
