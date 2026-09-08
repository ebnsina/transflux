<script lang="ts">
	import { resolve } from '$app/paths';
	import { tokens } from '$lib/highlight';

	// Real requests and real output. A developer decides from what the thing
	// actually returns, not from an adjective.
	const request = `curl -X POST https://api.example.com/v1/jobs \\
  -H "Authorization: Bearer tf_live_..." \\
  -d '{"asset_version_id": "01a08180-...",
       "pipeline": "stream-h264"}'`;

	const response = `{
  "job": {
    "id": "01a08180-48ed-7010-b777-850500c804cb",
    "state": "pending"
  }
}`;

	const artifacts = `{
  "artifact_sets": [{
    "state": "complete",
    "artifacts": [
      { "label": "1080p_h264",   "kind": "rendition" },
      { "label": "720p_h264",    "kind": "rendition" },
      { "label": "480p_h264",    "kind": "rendition" },
      { "label": "360p_h264",    "kind": "rendition" },
      { "label": "hls_master",   "kind": "manifest"  },
      { "label": "dash_manifest","kind": "manifest"  },
      { "label": "poster",       "kind": "poster"    },
      { "label": "sprite",       "kind": "sprite"    }
    ]
  }]
}`;

	const ladder = [
		{ label: '1080p', width: 100 },
		{ label: '720p', width: 74 },
		{ label: '480p', width: 50 },
		{ label: '360p', width: 34 }
	];

	const steps = [
		{ n: 'Upload', d: 'Resumable, straight to your storage' },
		{ n: 'Inspect', d: 'Every track, codec and colour' },
		{ n: 'Convert', d: 'A ladder, in parallel' },
		{ n: 'Check', d: 'Against what you asked for' },
		{ n: 'Deliver', d: 'Signed links a player follows' }
	];

	const features = [
		{
			t: 'Adaptive out of the box',
			d: 'One set of CMAF segments serves HLS and DASH. Nothing is stored twice.'
		},
		{
			t: 'Colour survives',
			d: 'HDR10, HLG, HDR10+ and Dolby Vision are detected and carried through, not flattened.'
		},
		{
			t: 'Never upscales',
			d: 'A 720p source produces 720p and below. Rungs it cannot fill are not made.'
		},
		{
			t: 'Posters and scrubbing',
			d: 'A poster and a sprite sheet with a WebVTT index, taken from the source.'
		},
		{
			t: 'Immutable outputs',
			d: 'A re-run writes a new version alongside. A link that worked yesterday still does.'
		},
		{
			t: 'Yours to host',
			d: 'Two Go binaries, PostgreSQL, and any S3-compatible storage.'
		}
	];
</script>

<svelte:head>
	<title>Transflux, media transcoding you can check the work of</title>
	<meta
		name="description"
		content="Transcode video into adaptive renditions, validate every output against what was asked for, and deliver over short-lived signed links. Self-hostable."
	/>
</svelte:head>

<section class="hero">
	<div class="glow" aria-hidden="true"></div>
	<div class="measure">
		<h1>Transcode video.<br />Prove it worked.</h1>
		<p class="lede">
			An adaptive ladder, packaged for streaming, with every output checked against what you asked
			for before anything is delivered.
		</p>
		<div class="cta">
			<a class="primary" href={resolve('/docs')}>Get started</a>
			<a class="secondary" href={resolve('/docs/api')}>API reference</a>
		</div>
	</div>

	<div class="measure shot">
		<div class="window">
			<div class="chrome">
				<span class="tab is-on">Request</span>
				<span class="tab">stream-h264</span>
				<span class="path mono">POST /v1/jobs</span>
			</div>
			<div class="split">
				<pre><code>{@html tokens(request, 'shell')}</code></pre>
				<pre class="ret"><code>{@html tokens(response, 'json')}</code></pre>
			</div>
		</div>
	</div>
</section>

<section class="strip">
	<div class="measure numbers">
		<div><strong>4</strong><span>renditions from one source</span></div>
		<div><strong>2</strong><span>protocols, one set of segments</span></div>
		<div><strong>41</strong><span>checks before delivery</span></div>
		<div><strong>0</strong><span>media bytes through the API</span></div>
	</div>
</section>

<section class="measure">
	<p class="eyebrow">The pipeline</p>
	<h2>Five stages, one request</h2>
	<p class="sub">
		You submit a job. Everything below happens across as many machines as you have, and you are told
		which stage a job is in the whole way through.
	</p>
	<ol class="steps">
		{#each steps as step, i (step.n)}
			<li>
				<span class="step-n mono">{String(i + 1).padStart(2, '0')}</span>
				<strong>{step.n}</strong>
				<span class="muted">{step.d}</span>
			</li>
		{/each}
	</ol>
</section>

<section class="measure split-2">
	<div>
		<p class="eyebrow">Renditions</p>
		<h2>One source in, a ladder out</h2>
		<p class="sub">
			Renditions encode in parallel across machines. Each is capped at the source, so nothing is
			blown up to fill a rung it cannot.
		</p>
		<div class="ladder">
			{#each ladder as rung, i (rung.label)}
				<div
					class="bar"
					style="width: {rung.width}%; animation-delay: {i * 0.35}s"
					title="{rung.label} rendition"
				>
					<span class="mono">{rung.label}</span>
				</div>
			{/each}
		</div>
	</div>

	<div class="window">
		<div class="chrome">
			<span class="tab is-on">Artifacts</span>
			<span class="path mono">GET /v1/jobs/&#123;id&#125;/artifacts</span>
		</div>
		<pre><code>{@html tokens(artifacts, 'json')}</code></pre>
	</div>
</section>

<section class="measure">
	<p class="eyebrow">Built in</p>
	<h2>Everything a stream needs</h2>
	<div class="cards">
		{#each features as f (f.t)}
			<article>
				<h3>{f.t}</h3>
				<p class="muted">{f.d}</p>
			</article>
		{/each}
	</div>
</section>

<section class="measure quote">
	<h2>Exit code zero is not success.</h2>
	<p>
		An encoder that finishes cleanly can still hand you a truncated file. Every output is fetched
		back and checked: it is the right size, its checksum matches, it plays, and its codec,
		resolution, duration, audio and HDR are what the plan asked for. If any of that fails, the job
		fails and nothing is delivered.
	</p>
</section>

<section class="measure split-2 plain">
	<div>
		<h2 class="small">Built to lose a machine</h2>
		<p class="muted">
			Work is leased. A machine that stops reporting has its work reclaimed and redone elsewhere,
			and its late results are refused, so one task never produces two answers. A crash, a kill and
			a network partition all look the same and all recover the same way.
		</p>
	</div>
	<div>
		<h2 class="small">Not a CDN, not a player</h2>
		<p class="muted">
			Transflux produces artifacts a CDN can serve and a player can play. It does not try to be
			either, and it does not do viewer analytics. Media never passes through the API: uploads go
			straight to your storage and playback is fetched from it.
		</p>
	</div>
</section>

<section class="closing">
	<div class="glow low" aria-hidden="true"></div>
	<div class="measure">
		<h2>Run it yourself</h2>
		<p class="sub">
			Two Go binaries, PostgreSQL, and any S3-compatible storage. Add a machine and it registers
			itself, declares what it can do, and starts taking work.
		</p>
		<div class="cta">
			<a class="primary" href={resolve('/docs')}>Get started</a>
			<a class="secondary" href={resolve('/docs/api')}>API reference</a>
		</div>
	</div>
</section>

<style>
	/* Longhands, not the shorthand: a section is both the measure and the band,
	   and a shorthand here would reset the vertical padding it sets below. */
	.measure {
		width: 100%;
		max-width: 1080px;
		margin: 0 auto;
		padding-left: 24px;
		padding-right: 24px;
	}

	section {
		padding: 96px 0;
	}

	/* Hero
	   ------------------------------------------------------------------ */
	.hero {
		position: relative;
		text-align: center;
		padding: 140px 0 96px;
		overflow: hidden;
	}
	.glow {
		position: absolute;
		top: -280px;
		left: 50%;
		width: 900px;
		height: 520px;
		transform: translateX(-50%);
		background: radial-gradient(
			ellipse at center,
			color-mix(in srgb, var(--brand) 30%, transparent),
			transparent 68%
		);
		filter: blur(60px);
		opacity: 0.5;
		pointer-events: none;
	}
	.glow.low {
		top: auto;
		bottom: -280px;
		opacity: 0.35;
	}

	h1 {
		font-size: clamp(42px, 6vw, 68px);
		line-height: 1.05;
		letter-spacing: -0.033em;
		font-variation-settings: 'wght' 620;
		margin: 0 0 22px;
	}
	.lede {
		font-size: 19px;
		line-height: 1.55;
		color: var(--muted);
		margin: 0 auto 34px;
		max-width: 34em;
	}

	.cta {
		display: flex;
		gap: 10px;
		justify-content: center;
		flex-wrap: wrap;
	}
	.cta a {
		border-radius: var(--radius-sm);
		padding: 15px 30px;
		font-size: 15.5px;
		font-variation-settings: 'wght' 600;
	}
	.cta a:hover {
		text-decoration: none;
	}
	.cta .primary {
		background: var(--brand);
		color: var(--brand-ink);
	}
	.cta .primary:hover {
		filter: brightness(1.08);
	}
	.cta .secondary {
		border: 1px solid var(--line-strong);
		color: var(--text);
		background: var(--panel);
	}
	.cta .secondary:hover {
		border-color: var(--accent);
		color: var(--accent);
	}

	.shot {
		margin-top: 64px;
		text-align: left;
	}

	/* A code window, standing in for the product shot a hosted service would
	   put here. The API is the product for the reader this page is aimed at. */
	.window {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		overflow: hidden;
	}
	.chrome {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 8px 12px;
		border-bottom: 1px solid var(--line);
		background: var(--panel-2);
		font-size: 12px;
		color: var(--muted);
	}
	.tab {
		padding: 4px 10px;
		border-radius: var(--radius-sm);
	}
	.tab.is-on {
		background: var(--panel);
		color: var(--text);
		border: 1px solid var(--line);
	}
	.path {
		margin-left: auto;
		font-size: 11.5px;
	}
	.split {
		display: grid;
		grid-template-columns: 1fr 1fr;
	}
	.split .ret {
		border-left: 1px solid var(--line);
	}
	pre {
		margin: 0;
		padding: 20px;
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 1.7;
		color: var(--text);
		overflow-x: auto;
	}

	/* Sections
	   ------------------------------------------------------------------ */
	.eyebrow {
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--accent);
		font-variation-settings: 'wght' 600;
		margin: 0 0 14px;
	}
	h2 {
		display: block;
		font-size: clamp(28px, 3.4vw, 40px);
		line-height: 1.12;
		letter-spacing: -0.028em;
		font-variation-settings: 'wght' 620;
		color: var(--text);
		text-transform: none;
		margin: 0 0 16px;
	}
	h2.small {
		font-size: 21px;
		letter-spacing: -0.02em;
	}
	.sub {
		font-size: 16.5px;
		line-height: 1.6;
		color: var(--muted);
		margin: 0;
		max-width: 46em;
	}
	h3 {
		font-size: 15px;
		margin: 0 0 6px;
		font-variation-settings: 'wght' 600;
	}

	.strip {
		border-top: 1px solid var(--line);
		border-bottom: 1px solid var(--line);
		padding: 0;
	}
	.numbers {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
	}
	.numbers div {
		padding: 34px 0;
	}
	.numbers strong {
		display: block;
		font-size: 36px;
		letter-spacing: -0.03em;
		color: var(--text);
		font-variation-settings: 'wght' 620;
		line-height: 1;
	}
	.numbers span {
		display: block;
		margin-top: 9px;
		color: var(--muted);
		font-size: 13px;
	}

	.steps {
		list-style: none;
		margin: 44px 0 0;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
		gap: 1px;
		background: var(--line);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		overflow: hidden;
	}
	.steps li {
		background: var(--panel);
		padding: 24px 20px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.step-n {
		color: var(--accent);
		font-size: 12px;
		margin-bottom: 6px;
	}
	.steps .muted {
		font-size: 13px;
	}

	.split-2 {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 56px;
		align-items: center;
	}
	/* A grid track is min-content wide by default, and a <pre> has no natural
	   maximum, so without this the code column pushes the whole grid past the
	   page. */
	.split-2 > * {
		min-width: 0;
	}
	.plain {
		align-items: start;
		gap: 56px;
	}
	.plain p {
		margin: 0;
		font-size: 15px;
		line-height: 1.6;
	}

	.ladder {
		margin-top: 34px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	/* The label rides inside its own bar, so every rung starts on the same left
	   edge as the heading above it rather than behind a label column. */
	.bar {
		position: relative;
		display: flex;
		align-items: center;
		height: 38px;
		padding: 0 14px;
		border-radius: var(--radius-sm);
		background: color-mix(in srgb, var(--brand) 16%, transparent);
		border: 1px solid color-mix(in srgb, var(--brand) 55%, transparent);
		overflow: hidden;
	}
	.bar span {
		font-size: 12px;
		color: var(--text);
		position: relative;
		z-index: 1;
	}
	/* A pass of light travelling along each rung, staggered down the ladder:
	   the rungs encode at the same time, and a still picture cannot say that. */
	.bar::after {
		content: '';
		position: absolute;
		inset: 0;
		background: linear-gradient(
			100deg,
			transparent 20%,
			color-mix(in srgb, var(--brand) 34%, transparent) 50%,
			transparent 80%
		);
		transform: translateX(-100%);
		animation: sweep 2.8s ease-in-out infinite;
		animation-delay: inherit;
	}
	@keyframes sweep {
		0% {
			transform: translateX(-100%);
		}
		60%,
		100% {
			transform: translateX(100%);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.bar::after {
			animation: none;
		}
	}

	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
		gap: 14px;
		margin-top: 44px;
	}
	.cards article {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 22px;
	}
	.cards p {
		margin: 0;
		font-size: 14px;
	}

	.quote {
		text-align: center;
	}
	.quote h2 {
		max-width: 20ch;
		margin: 0 auto 20px;
	}
	.quote p {
		max-width: 62ch;
		margin: 0 auto;
		color: var(--muted);
		font-size: 16.5px;
		line-height: 1.65;
	}

	.closing {
		position: relative;
		overflow: hidden;
		text-align: center;
		border-top: 1px solid var(--line);
		padding: 104px 0 120px;
	}
	.closing .sub {
		margin: 0 auto 32px;
		max-width: 44ch;
	}

	@media (max-width: 860px) {
		section {
			padding: 64px 0;
		}
		.hero {
			padding: 64px 0 56px;
		}
		.measure {
			padding-left: 18px;
			padding-right: 18px;
		}
		.split,
		.split-2 {
			grid-template-columns: 1fr;
			gap: 36px;
		}
		.split .ret {
			border-left: 0;
			border-top: 1px solid var(--line);
		}
		.shot {
			margin-top: 44px;
		}
		.closing {
			padding: 72px 0 88px;
		}
	}
</style>
