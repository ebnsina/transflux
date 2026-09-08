<script lang="ts">
	import { resolve } from '$app/paths';
	import Code from '../Code.svelte';

	type Endpoint = {
		method: string;
		path: string;
		summary: string;
		body?: string;
		returns?: string;
	};

	const health: Endpoint[] = [
		{
			method: 'GET',
			path: '/healthz',
			summary:
				'Liveness. Answers if the process is running at all. No authentication. Use it to decide whether to restart an instance.'
		},
		{
			method: 'GET',
			path: '/readyz',
			summary:
				'Readiness. Answers if the process can serve traffic, including its dependencies. No authentication. Use it to decide whether to send traffic to an instance.'
		}
	];

	const identity: Endpoint[] = [
		{
			method: 'GET',
			path: '/v1/me',
			summary: 'The tenant and key behind the credential you presented.',
			returns: '{"tenant_id":"...","api_key_id":"...","scopes":["..."]}'
		}
	];

	const assets: Endpoint[] = [
		{
			method: 'POST',
			path: '/v1/assets',
			summary:
				'Create an asset and its first version. Both fields are optional. Repeating an external_id you have used before returns 409 asset_exists rather than a second asset, which gives you idempotency under your own identifier: retry a request you are unsure about and you either create the asset or learn it already exists.',
			body: '{"name":"film.mp4","external_id":"your-own-id"}',
			returns: '201 with the asset and its first version'
		},
		{
			method: 'GET',
			path: '/v1/assets?limit=50',
			summary: 'List your assets, newest first. limit is between 1 and 200.'
		},
		{
			method: 'GET',
			path: '/v1/assets/{id}',
			summary:
				'One asset with its versions. Each version carries a media object once it has been probed; before that the field is absent, because nothing has looked inside the file yet.'
		}
	];

	const uploads: Endpoint[] = [
		{
			method: 'POST',
			path: '/v1/assets/{id}/uploads',
			summary:
				'Open an upload. size_bytes is the exact length of the file you are about to send, and is checked at completion. Pass asset_version_id to attach the upload to a version other than the newest.',
			body: '{"size_bytes":123,"content_type":"video/mp4","asset_version_id":"optional"}',
			returns:
				'201 {"upload":{"id","part_size","part_count","expires_at",...},"parts":[{"number","url"}]}'
		},
		{
			method: 'PUT',
			path: '<presigned part url>',
			summary:
				'Sent by you directly to storage, not to the API. Part N carries the byte range [(N-1) * part_size, N * part_size) of your file. Parts may go in any order and in parallel.'
		},
		{
			method: 'GET',
			path: '/v1/uploads/{id}',
			summary:
				'Resume. Asks storage which parts actually arrived rather than trusting a record the client kept, so a client that crashed mid-upload, or lost track of what it had sent, still resumes correctly. Fresh URLs come back for the missing parts, because the originals may have expired.',
			returns: '{"uploaded_parts":[...],"missing_parts":[...],"parts":[{"number","url"}]}'
		},
		{
			method: 'POST',
			path: '/v1/uploads/{id}/complete',
			summary:
				'Verify that every part is present and correctly sized, assemble the object, and check the stored size against the size you declared. Idempotent, so a retry after a timeout is safe. The source is unverified until this succeeds, and nothing will process an unverified source.'
		},
		{
			method: 'DELETE',
			path: '/v1/uploads/{id}',
			summary: 'Abort the upload and discard the parts already sent.'
		}
	];

	const pipelinesEndpoints: Endpoint[] = [
		{
			method: 'GET',
			path: '/v1/pipelines',
			summary: 'The pipelines you are allowed to run, with the name you pass when creating a job.'
		}
	];

	const jobs: Endpoint[] = [
		{
			method: 'POST',
			path: '/v1/jobs',
			summary:
				'Run one pipeline against one asset version. idempotency_key lets you retry safely under your own identifier. priority defaults to 100; lower runs sooner.',
			body:
				'{"asset_version_id":"...","pipeline":"stream-h264",' +
				'"idempotency_key":"optional","priority":100}',
			returns: '201 {"job":{...}}'
		},
		{
			method: 'GET',
			path: '/v1/jobs?limit=50',
			summary: 'List your jobs, newest first.'
		},
		{
			method: 'GET',
			path: '/v1/jobs/{id}',
			summary:
				'One job with its tasks, its attempts and its validation results. Poll this while state is pending or running.',
			returns: '{"job":{...},"tasks":[...],"attempts":[...],"validation":{...}}'
		},
		{
			method: 'POST',
			path: '/v1/jobs/{id}/cancel',
			summary: 'Ask for the job to stop. Returns 204.'
		}
	];

	const artifacts: Endpoint[] = [
		{
			method: 'GET',
			path: '/v1/jobs/{id}/artifacts',
			summary:
				'What the job produced, grouped into artifact sets. Each set has a version and a state of building, validating, complete or failed.',
			returns:
				'{"artifact_sets":[{"version":1,"state":"complete","artifacts":[{"id","kind","label","size_bytes","checksum_algo","media"}]}]}'
		},
		{
			method: 'GET',
			path: '/v1/artifacts/{id}/download',
			summary:
				'A signed URL for the file itself, valid for fifteen minutes. Short, because the link needs no key and cannot be revoked once handed out.',
			returns: '{"url":"...","expires_in_seconds":900,"size_bytes":123}'
		},
		{
			method: 'POST',
			path: '/v1/artifacts/{id}/playback',
			summary:
				'A playback link for an HLS master playlist or a sprite index, valid for four hours. A player follows it with no API key.',
			returns:
				'{"url":"/playback/<token>/master.m3u8","expires_at":"...","expires_in_seconds":14400}'
		}
	];

	const errorBody = `HTTP/1.1 401 Unauthorized
Content-Type: application/json

{
  "error": {
    "code": "unauthorized",
    "message": "A valid API key is required."
  }
}`;

	const errorCodes: [string, string, string][] = [
		['unauthorized', '401', 'No usable API key was presented.'],
		['forbidden', '403', 'The key is valid but lacks the scope this endpoint needs.'],
		['not_found', '404', 'No such resource, or it belongs to another tenant.'],
		['invalid_request', '400', 'The request body or query is malformed or out of range.'],
		['asset_exists', '409', 'An asset already exists with that external_id.'],
		['source_missing', '409', 'The asset version has no source file to work from.'],
		[
			'source_unverified',
			'409',
			'The upload has not been completed, so the source is not trusted.'
		],
		[
			'probe_required',
			'409',
			'The pipeline plans against the source tracks, and no probe has run.'
		],
		['unknown_pipeline', '400', 'No pipeline by that name is available to you.'],
		['upload_incomplete', '409', 'One or more parts are missing.'],
		['size_mismatch', '409', 'The assembled object does not match the size you declared.'],
		['upload_not_open', '409', 'The upload has already been completed or aborted.'],
		['too_large', '400', 'The declared size or part count is beyond the upload limits.'],
		['not_playable', '409', 'The artifact is not something a playback link can be issued for.'],
		['link_expired', '410', 'The download or playback link has passed its expiry.'],
		['link_invalid', '403', 'The link token does not verify.'],
		['internal_error', '500', 'Something failed on our side. Safe to retry.'],
		['database_unavailable', '503', 'The store is unreachable. Retry with backoff.']
	];

	const trackFields = `container, duration_ms, bitrate_bps, size_bytes
tracks[]:
  kind             video | audio | subtitle
  codec
  width, height
  fps_num, fps_den     frame rate as a rational, so 30000/1001 stays exact
  pixel_format
  bit_depth
  language
  channels
  sample_rate_hz
  color_primaries, color_transfer, color_matrix, color_range
  hdr_format       sdr | hdr10 | hlg | hdr10plus | dolby_vision`;
</script>

<svelte:head>
	<title>Transflux API reference</title>
	<meta
		name="description"
		content="Every Transflux endpoint: assets, uploads, pipelines, jobs and artifacts, with error codes and limits."
	/>
</svelte:head>

<h1>API reference</h1>
<p class="muted">
	Every path below is relative to your control plane address. Everything under <span class="mono"
		>/v1</span
	>
	needs an <a href={resolve('/docs')}>API key</a>. If you have not read the overview, start there:
	the endpoints make more sense in the order you actually call them.
</p>

<h2>Health</h2>
{@render list(health)}

<h2>Identity</h2>
{@render list(identity)}

<h2>Assets</h2>
{@render list(assets)}

<h2>What a probe records</h2>
<p class="muted">
	Once the <span class="mono">probe</span> pipeline has run, each asset version carries a
	<span class="mono">media</span> object describing the source as it actually is, not as it was declared.
	Ladder pipelines read these fields to decide what to produce, which is why they insist on a probe first.
</p>
<Code code={trackFields} />

<h2>Uploads</h2>
<p class="muted">
	Media bytes go straight to storage and never pass through the API. That keeps a large file off our
	request path entirely, so the upload is bounded by your connection to storage rather than by ours.
</p>
{@render list(uploads)}

<h2>Upload limits</h2>
<div class="panel">
	<table>
		<tbody>
			<tr><td>Minimum part size</td><td class="muted">8 MiB, except for the final part.</td></tr>
			<tr><td>Maximum parts</td><td class="muted">10000 per upload.</td></tr>
			<tr><td>Maximum object size</td><td class="muted">5 TiB.</td></tr>
		</tbody>
	</table>
</div>
<p class="muted">
	The part size the API chooses already satisfies these limits for the size you declare. They matter
	when you are deciding whether a file can be handled at all: anything beyond them is refused with
	<span class="mono">too_large</span> at the point you open the upload, rather than after you have sent
	the bytes.
</p>

<h2>Pipelines</h2>
{@render list(pipelinesEndpoints)}
<div class="panel">
	<table>
		<thead>
			<tr><th>Name</th><th>What it does</th></tr>
		</thead>
		<tbody>
			<tr>
				<td class="mono">probe</td>
				<td class="muted">
					Looks inside the source and records what it contains. Cheap, and a prerequisite for the
					ladder pipelines.
				</td>
			</tr>
			<tr>
				<td class="mono">transcode-h264</td>
				<td class="muted">A single 720p version. Suitable when one file is all you need.</td>
			</tr>
			<tr>
				<td class="mono">ladder-h264</td>
				<td class="muted">
					1080p, 720p, 480p and 360p, so a player can adapt to the connection it finds.
				</td>
			</tr>
			<tr>
				<td class="mono">stream-h264</td>
				<td class="muted">
					The same ladder, packaged for streaming, with posters and thumbnail sprites.
				</td>
			</tr>
		</tbody>
	</table>
</div>

<h2>Jobs</h2>
<p class="muted">
	A job never modifies the source. Many jobs can run against the same asset version, so you can
	probe once and then produce a single rendition today and a full streaming package later without
	re-uploading anything.
</p>
{@render list(jobs)}
<div class="panel">
	<p>
		<span class="mono">job.state</span> is one of
		<span class="mono">pending</span>, <span class="mono">running</span>,
		<span class="mono">succeeded</span>, <span class="mono">failed</span> or
		<span class="mono">cancelled</span>. Each task carries an
		<span class="mono">operation</span>
		(probe, encode, package, thumbnail or validate), its own state, and
		<span class="mono">attempt_count</span>
		against <span class="mono">max_attempts</span>, so you can tell a job that is retrying from one
		that is stuck. Each validation check has a
		<span class="mono">label</span>, a <span class="mono">name</span> and a
		<span class="mono">status</span> of pass, warn or fail.
	</p>
	<p>
		Submitting returns <span class="mono">409 source_unverified</span> if the upload has not been
		completed, and <span class="mono">409 probe_required</span> for any pipeline that plans against the
		source tracks, which is anything producing a ladder, when no probe has run.
	</p>
</div>

<h2>Artifacts</h2>
<p class="muted">
	Artifacts are immutable. Re-running a pipeline produces a new artifact set version rather than
	overwriting the old one, so a link you handed out yesterday keeps pointing at the file it pointed
	at yesterday.
</p>
{@render list(artifacts)}
<div class="panel">
	<table>
		<thead>
			<tr><th>Kind</th><th>What it is</th></tr>
		</thead>
		<tbody>
			<tr
				><td class="mono">rendition</td><td class="muted">One encoded version of the media.</td></tr
			>
			<tr
				><td class="mono">manifest</td><td class="muted"
					>A playlist describing a set of renditions.</td
				></tr
			>
			<tr
				><td class="mono">poster</td><td class="muted">A still frame for use before playback.</td
				></tr
			>
			<tr
				><td class="mono">sprite</td><td class="muted"
					>A grid of thumbnails for a scrub bar preview.</td
				></tr
			>
			<tr
				><td class="mono">sprite_index</td><td class="muted"
					>The mapping from a time offset to a tile in the sprite.</td
				></tr
			>
			<tr
				><td class="mono">segment_set</td><td class="muted"
					>The media segments a manifest refers to.</td
				></tr
			>
			<tr><td class="mono">subtitle</td><td class="muted">A subtitle or caption track.</td></tr>
		</tbody>
	</table>
</div>

<h2>How a playback link works</h2>
<div class="panel">
	<p>
		A playback link authorises the small text files, the manifests, which are served through the
		control plane. The media segments themselves are fetched straight from storage over URLs signed
		for that occasion. Viewer bandwidth therefore never crosses the API, while the entry point stays
		something you can issue, time-limit and stop issuing.
	</p>
	<p>
		DASH manifests are produced and stored, and you can download them, but they are not served this
		way. A DASH segment template describes segment URLs by pattern rather than listing them, so
		there is no set of individual URLs to sign. Serving DASH to viewers needs a CDN that does token
		authentication at the edge.
	</p>
</div>

<h2>Errors</h2>
<div class="panel">
	<p>Every failure returns an HTTP status and a JSON body of the same shape:</p>
</div>
<Code code={errorBody} />
<div class="panel">
	<p>
		Every authentication failure returns an identical 401, whether the key is unknown, malformed,
		revoked, expired, or belongs to a suspended account. The response deliberately tells you nothing
		that distinguishes those cases, so the endpoint cannot be used to probe which keys exist. If
		your key stopped working, check it at its source rather than trying to read the difference off
		the response.
	</p>
	<p>
		A database outage returns 500, not 401. An authentication failure means the credential was
		rejected; it never means we could not reach the store.
	</p>
</div>
<div class="panel">
	<table>
		<thead>
			<tr><th>Code</th><th>Status</th><th>Meaning</th></tr>
		</thead>
		<tbody>
			{#each errorCodes as [code, status, meaning] (code)}
				<tr>
					<td class="mono">{code}</td>
					<td class="muted">{status}</td>
					<td class="muted">{meaning}</td>
				</tr>
			{/each}
		</tbody>
	</table>
</div>

{#snippet list(items: Endpoint[])}
	<div class="panel">
		{#each items as e (e.method + e.path)}
			<div class="endpoint">
				<div class="head">
					<span class="badge {e.method === 'GET' ? 'run' : e.method === 'DELETE' ? 'bad' : 'ok'}"
						>{e.method}</span
					>
					<span class="mono path">{e.path}</span>
				</div>
				<p class="muted">{e.summary}</p>
				{#if e.body}
					<div class="detail">
						<span class="muted label">Body</span><span class="mono">{e.body}</span>
					</div>
				{/if}
				{#if e.returns}
					<div class="detail">
						<span class="muted label">Returns</span><span class="mono">{e.returns}</span>
					</div>
				{/if}
			</div>
		{/each}
	</div>
{/snippet}

<style>
	.endpoint {
		padding: 16px 0;
		border-bottom: 1px solid var(--line);
	}
	.endpoint:last-child {
		border-bottom: none;
	}
	.head {
		display: flex;
		align-items: center;
		gap: 10px;
		flex-wrap: wrap;
	}
	.path {
		font-variation-settings: 'wght' 500;
		word-break: break-all;
	}
	.endpoint p {
		margin: 6px 0 0;
	}
	.detail {
		display: flex;
		gap: 10px;
		margin-top: 8px;
		font-size: 12.5px;
		align-items: baseline;
	}
	.detail .mono {
		min-width: 0;
		word-break: break-word;
	}
	.label {
		font-size: 11px;
		text-transform: uppercase;
		letter-spacing: 0.07em;
		flex: none;
		width: 62px;
	}
</style>
