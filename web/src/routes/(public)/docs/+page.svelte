<script lang="ts">
	import { resolve } from '$app/paths';
	import Code from './Code.svelte';

	const identity = `curl -s https://api.example.com/v1/me \\
  -H "Authorization: Bearer $TF_KEY"

{
  "tenant_id": "ten_8f3a",
  "api_key_id": "key_2c91",
  "scopes": ["assets:write", "jobs:write", "assets:read", "jobs:read"]
}`;

	const walkthrough = `# The control plane address and your key, once, for the whole session.
export TF=https://api.example.com
export TF_KEY=tf_live_9f2c4b1ad0e34c6f

# 1. Create an asset. external_id is your own identifier; reusing it later
#    returns 409 asset_exists instead of creating a duplicate.
curl -s -X POST $TF/v1/assets \\
  -H "Authorization: Bearer $TF_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"name":"interview.mp4","external_id":"cms-4417"}'

# -> {"asset":{"id":"ast_7Kd2","...":"..."},
#     "version":{"id":"ver_3Qm8","...":"..."}}

# 2. Open an upload against that asset. The size is the exact byte length of
#    the file you are about to send; completion is checked against it.
curl -s -X POST $TF/v1/assets/ast_7Kd2/uploads \\
  -H "Authorization: Bearer $TF_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"size_bytes":41238912,"content_type":"video/mp4"}'

# -> {"upload":{"id":"upl_5Wz1","part_size":8388608,"part_count":5,
#               "expires_at":"2026-01-04T11:20:00Z"},
#     "parts":[{"number":1,"url":"https://storage.example.com/..."}, ...]}

# 3. Send the bytes straight to storage. Part N is the byte range
#    [(N-1) * part_size, N * part_size) of your file.
split -b 8388608 interview.mp4 part-
curl -s -X PUT --upload-file part-aa "<url for part 1>"
curl -s -X PUT --upload-file part-ab "<url for part 2>"
# ... and so on for every part

# 4. Complete. This checks each part is present and the right size, assembles
#    the object, then compares the stored size with the size you declared.
curl -s -X POST $TF/v1/uploads/upl_5Wz1/complete \\
  -H "Authorization: Bearer $TF_KEY"

# 5. Probe the source, so later pipelines know what they are planning against.
curl -s -X POST $TF/v1/jobs \\
  -H "Authorization: Bearer $TF_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"asset_version_id":"ver_3Qm8","pipeline":"probe"}'

# -> {"job":{"id":"job_A1b2","state":"pending","...":"..."}}

# 6. Poll until the job leaves pending or running.
curl -s $TF/v1/jobs/job_A1b2 -H "Authorization: Bearer $TF_KEY"

# 7. Now run the streaming pipeline against the same version. The probe is
#    not consumed by this; you can run as many jobs as you like.
curl -s -X POST $TF/v1/jobs \\
  -H "Authorization: Bearer $TF_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"asset_version_id":"ver_3Qm8","pipeline":"stream-h264","idempotency_key":"cms-4417-stream"}'

# -> {"job":{"id":"job_C3d4","state":"pending","...":"..."}}

# 8. Poll again until job.state is succeeded.
curl -s $TF/v1/jobs/job_C3d4 -H "Authorization: Bearer $TF_KEY"

# 9. List what the job produced.
curl -s $TF/v1/jobs/job_C3d4/artifacts -H "Authorization: Bearer $TF_KEY"

# -> {"artifact_sets":[{"version":1,"state":"complete",
#       "artifacts":[{"id":"art_M9n0","kind":"manifest","label":"HLS master",
#                     "...":"..."}, ...]}]}

# 10. Mint a playback link for the master playlist. A player follows this
#     with no API key of its own.
curl -s -X POST $TF/v1/artifacts/art_M9n0/playback \\
  -H "Authorization: Bearer $TF_KEY"

# -> {"url":"https://api.example.com/playback/eyJhbGc.../master.m3u8",
#     "expires_at":"2026-01-04T15:20:00Z","expires_in_seconds":14400}`;
</script>

<svelte:head>
	<title>Transflux API documentation</title>
	<meta
		name="description"
		content="How to upload media to Transflux, run transcoding pipelines against it, and hand the results to a player."
	/>
</svelte:head>

<h1>Documentation</h1>
<p class="muted">
	Transflux takes a media file you own, records what is inside it, produces the renditions and
	packaging you ask for, and hands out short-lived links to the results. This page explains the
	shape of that work. The <a href={resolve('/docs/api')}>API reference</a> lists every endpoint you can
	call.
</p>

<h2>The flow, end to end</h2>
<div class="panel">
	<table>
		<tbody>
			<tr>
				<td class="step mono">1</td>
				<td>
					<strong>Create an asset.</strong>
					<div class="muted">
						An asset is the thing you are working with, such as one film or one episode. It holds
						versions; a version is a particular source file. Creating an asset gives you its first
						version.
					</div>
				</td>
			</tr>
			<tr>
				<td class="step mono">2</td>
				<td>
					<strong>Upload the source.</strong>
					<div class="muted">
						You ask for an upload, then send the bytes directly to storage using the presigned URLs
						you get back. Media does not pass through the API, so a large file is limited by your
						link to storage rather than by ours.
					</div>
				</td>
			</tr>
			<tr>
				<td class="step mono">3</td>
				<td>
					<strong>Complete the upload.</strong>
					<div class="muted">
						Completion verifies the parts, assembles the object and checks its size against what you
						declared. Until it succeeds the source is unverified and nothing will process it.
					</div>
				</td>
			</tr>
			<tr>
				<td class="step mono">4</td>
				<td>
					<strong>Probe.</strong>
					<div class="muted">
						The <span class="mono">probe</span> pipeline looks inside the file and records the container,
						duration, bitrate and every track it contains. Pipelines that build a ladder plan against
						those tracks, so they need a probe first.
					</div>
				</td>
			</tr>
			<tr>
				<td class="step mono">5</td>
				<td>
					<strong>Run a pipeline.</strong>
					<div class="muted">
						A job runs one pipeline against one asset version. Jobs never modify the source, and
						many jobs can run against the same version, so trying a different pipeline costs you
						nothing already done.
					</div>
				</td>
			</tr>
			<tr>
				<td class="step mono">6</td>
				<td>
					<strong>Collect the artifacts.</strong>
					<div class="muted">
						A finished job exposes an artifact set: renditions, manifests, posters, thumbnail
						sprites, subtitles. Artifacts are immutable, and a re-run adds a new set version rather
						than overwriting the old one.
					</div>
				</td>
			</tr>
			<tr>
				<td class="step mono">7</td>
				<td>
					<strong>Hand out a link.</strong>
					<div class="muted">
						Download links are signed for fifteen minutes. Playback links last four hours and are
						followed by a player with no API key of its own.
					</div>
				</td>
			</tr>
		</tbody>
	</table>
</div>

<h2>Authentication</h2>
<div class="panel">
	<p>
		Every endpoint under <span class="mono">/v1</span> requires an API key, sent as a bearer token:
	</p>
	<p class="mono">Authorization: Bearer tf_live_...</p>
	<p>
		Live keys are prefixed <span class="mono">tf_live_</span> and test keys
		<span class="mono">tf_test_</span>. The prefix is there so a key is recognisable in a log or a
		configuration file and can be told apart at a glance from one belonging to the other
		environment. Treat a key as a credential: it is not scoped to an origin and carries no user
		identity, so it belongs on a server, never in a browser or a mobile app.
	</p>
	<p>
		Call <span class="mono">GET /v1/me</span> to confirm which tenant and which key you are using:
	</p>
</div>
<Code code={identity} />

<h2>Scopes</h2>
<div class="panel">
	<table>
		<thead>
			<tr><th>Scope</th><th>Grants</th></tr>
		</thead>
		<tbody>
			<tr>
				<td class="mono">assets:read</td>
				<td class="muted">Listing and reading assets, versions and artifacts.</td>
			</tr>
			<tr>
				<td class="mono">assets:write</td>
				<td class="muted">Creating assets, and opening, resuming and completing uploads.</td>
			</tr>
			<tr>
				<td class="mono">jobs:read</td>
				<td class="muted">Listing and reading jobs, their tasks and their validation results.</td>
			</tr>
			<tr>
				<td class="mono">jobs:write</td>
				<td class="muted">Submitting and cancelling jobs.</td>
			</tr>
		</tbody>
	</table>
</div>
<p class="muted">
	Issue keys with the narrowest scopes that do the job. A key that only submits jobs and reads their
	results cannot create assets or open uploads, which limits what a leak costs you.
</p>

<h2>A complete worked example</h2>
<p class="muted">
	From an empty account to a playable link, using curl. Substitute your own control plane address
	and key.
</p>
<Code code={walkthrough} />

<h2>Where to go next</h2>
<div class="panel">
	<p>
		The <a href={resolve('/docs/api')}>API reference</a> documents every endpoint, the media fields a
		probe records, the artifact kinds a pipeline can produce, the error codes, and the upload limits.
	</p>
</div>

<style>
	.step {
		color: var(--muted);
		width: 28px;
		vertical-align: top;
		padding-top: 15px;
	}
	td strong {
		font-variation-settings: 'wght' 600;
	}
	td .muted {
		margin-top: 2px;
	}
	.panel > p.mono {
		font-size: 13px;
	}
</style>
