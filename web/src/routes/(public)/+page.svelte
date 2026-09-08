<script lang="ts">
	import { resolve } from '$app/paths';

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
	<div class="hero-copy">
		<p class="eyebrow">Media processing</p>
		<h1>Transcode video.<br />Prove it worked.</h1>
		<p class="lede">
			An adaptive ladder, packaged for streaming, with every output checked against what you asked
			for before anything is delivered.
		</p>
		<div class="cta">
			<a class="primary" href={resolve('/docs')}>Read the docs</a>
			<a class="secondary" href={resolve('/docs/api')}>API reference</a>
		</div>
	</div>

	<div class="hero-code">
		<div class="code-head"><span class="dot"></span>One request</div>
		<pre>{request}</pre>
		<div class="code-head"><span class="dot"></span>What comes back</div>
		<pre>{response}</pre>
	</div>
</section>

<section class="numbers">
	<div><strong>4</strong><span>renditions from one source</span></div>
	<div><strong>2</strong><span>protocols, one set of segments</span></div>
	<div><strong>41</strong><span>checks before delivery</span></div>
	<div><strong>0</strong><span>media bytes through the API</span></div>
</section>

<section>
	<h2>How it works</h2>
	<ol class="steps">
		{#each steps as step, i (step.n)}
			<li>
				<span class="step-n">{i + 1}</span>
				<strong>{step.n}</strong>
				<span class="muted">{step.d}</span>
			</li>
		{/each}
	</ol>
</section>

<section class="split">
	<div>
		<h2>One source in, a ladder out</h2>
		<p class="muted">
			Renditions encode in parallel across machines. Each is capped at the source, so nothing is
			blown up to fill a rung it cannot.
		</p>
		<div class="ladder">
			{#each ladder as rung (rung.label)}
				<div class="rung">
					<span class="rung-label mono">{rung.label}</span>
					<span class="bar" style="width: {rung.width}%"></span>
				</div>
			{/each}
		</div>
	</div>

	<div class="hero-code">
		<div class="code-head"><span class="dot"></span>GET /v1/jobs/&#123;id&#125;/artifacts</div>
		<pre>{artifacts}</pre>
	</div>
</section>

<section>
	<h2>What you get</h2>
	<div class="cards">
		{#each features as f (f.t)}
			<article>
				<h3>{f.t}</h3>
				<p class="muted">{f.d}</p>
			</article>
		{/each}
	</div>
</section>

<section class="claim">
	<h2>Exit code zero is not success</h2>
	<p>
		An encoder that finishes cleanly can still hand you a truncated file. Every output is fetched
		back and checked: it is the right size, its checksum matches, it plays, and its codec,
		resolution, duration, audio and HDR are what the plan asked for. If any of that fails, the job
		fails and nothing is delivered.
	</p>
</section>

<section class="split">
	<div>
		<h2>Built to lose a machine</h2>
		<p class="muted">
			Work is leased. A machine that stops reporting has its work reclaimed and redone elsewhere,
			and its late results are refused, so one task never produces two answers. A crash, a kill and
			a network partition all look the same and all recover the same way.
		</p>
	</div>
	<div>
		<h2>Not a CDN, not a player</h2>
		<p class="muted">
			Transflux produces artifacts a CDN can serve and a player can play. It does not try to be
			either, and it does not do viewer analytics. Media never passes through the API: uploads go
			straight to your storage and playback is fetched from it.
		</p>
	</div>
</section>

<section class="closing">
	<h2>Run it yourself</h2>
	<p class="muted">
		Two Go binaries, PostgreSQL, and any S3-compatible storage. Add a machine and it registers
		itself, declares what it can do, and starts taking work.
	</p>
	<div class="cta">
		<a class="primary" href={resolve('/docs')}>Read the docs</a>
	</div>
</section>

<style>
	section {
		margin: 0 0 96px;
	}

	.hero {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 56px;
		align-items: center;
		padding: 64px 0 40px;
	}
	.eyebrow {
		font-size: 12px;
		text-transform: uppercase;
		letter-spacing: 0.1em;
		color: var(--accent);
		font-variation-settings: 'wght' 600;
		margin: 0 0 16px;
	}
	h1 {
		font-size: clamp(38px, 5vw, 58px);
		line-height: 1.04;
		letter-spacing: -0.035em;
		font-variation-settings: 'wght' 700;
		margin: 0 0 22px;
	}
	.lede {
		font-size: 18px;
		line-height: 1.55;
		color: var(--muted);
		margin: 0 0 32px;
		max-width: 30em;
	}
	.cta {
		display: flex;
		gap: 12px;
		flex-wrap: wrap;
	}
	.cta a {
		border-radius: var(--radius-sm);
		padding: 12px 22px;
		font-variation-settings: 'wght' 600;
	}
	.cta a:hover {
		text-decoration: none;
	}
	.cta .primary {
		background: var(--accent);
		color: var(--accent-contrast);
	}
	.cta .primary:hover {
		filter: brightness(1.06);
	}
	.cta .secondary {
		border: 1px solid var(--line-strong);
		color: var(--text);
	}
	.cta .secondary:hover {
		border-color: var(--accent);
		color: var(--accent);
	}

	.hero-code {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		overflow: hidden;
	}
	.code-head {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 11px 16px;
		border-bottom: 1px solid var(--line);
		font-size: 12px;
		color: var(--muted);
		background: var(--panel-2);
	}
	.dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--accent);
		flex: none;
	}
	pre {
		margin: 0;
		padding: 16px;
		font-family: var(--font-mono);
		font-size: 12.5px;
		line-height: 1.65;
		color: var(--text);
		overflow-x: auto;
	}

	.numbers {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
		gap: 1px;
		background: var(--line);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		overflow: hidden;
	}
	.numbers div {
		background: var(--panel);
		padding: 26px 22px;
	}
	.numbers strong {
		display: block;
		font-size: 34px;
		letter-spacing: -0.03em;
		color: var(--accent);
		font-variation-settings: 'wght' 700;
		line-height: 1;
	}
	.numbers span {
		display: block;
		margin-top: 8px;
		color: var(--muted);
		font-size: 13px;
	}

	h2 {
		font-size: 26px;
		display: block;
		letter-spacing: -0.025em;
		font-variation-settings: 'wght' 700;
		margin: 0 0 14px;
		color: var(--text);
		text-transform: none;
	}
	h3 {
		font-size: 15px;
		margin: 0 0 6px;
		font-variation-settings: 'wght' 600;
	}

	.steps {
		list-style: none;
		margin: 24px 0 0;
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
		padding: 22px 20px;
		display: flex;
		flex-direction: column;
		gap: 6px;
	}
	.step-n {
		width: 24px;
		height: 24px;
		border-radius: 50%;
		background: var(--accent-soft);
		color: var(--accent);
		display: grid;
		place-items: center;
		font-size: 12px;
		font-variation-settings: 'wght' 600;
		margin-bottom: 4px;
	}
	.steps .muted {
		font-size: 13px;
	}

	.split {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 48px;
		align-items: start;
	}

	.ladder {
		margin-top: 24px;
		display: flex;
		flex-direction: column;
		gap: 10px;
	}
	.rung {
		display: flex;
		align-items: center;
		gap: 12px;
	}
	.rung-label {
		width: 52px;
		color: var(--muted);
		font-size: 12px;
	}
	.bar {
		height: 26px;
		border-radius: var(--radius-sm);
		background: var(--accent-soft);
		border: 1px solid var(--accent);
	}

	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
		gap: 14px;
		margin-top: 24px;
	}
	.cards article {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 20px;
	}
	.cards p {
		margin: 0;
		font-size: 14px;
	}

	.claim {
		background: var(--panel);
		border: 1px solid var(--line);
		border-radius: var(--radius);
		padding: 40px;
	}
	.claim p {
		max-width: 60ch;
		margin: 0;
		color: var(--muted);
		font-size: 16px;
		line-height: 1.6;
	}

	.closing {
		text-align: center;
		padding: 24px 0 8px;
	}
	.closing h2 {
		text-align: center;
	}
	.closing p {
		max-width: 46ch;
		margin: 0 auto 26px;
	}
	.closing .cta {
		justify-content: center;
	}

	@media (max-width: 860px) {
		.hero,
		.split {
			grid-template-columns: 1fr;
			gap: 36px;
		}
		.hero {
			padding: 40px 0 56px;
		}
		section {
			margin-bottom: 64px;
		}
		.claim {
			padding: 28px;
		}
	}
</style>
