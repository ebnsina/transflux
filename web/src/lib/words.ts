/**
 * Plain words for internal names.
 *
 * The API speaks the domain's language — attempts, leases, operations,
 * artifacts — because that is what the system is built out of. A person
 * reading a screen should not have to learn any of it, so the translation
 * happens here rather than leaking into every page.
 */

const states: Record<string, string> = {
	pending: 'Waiting',
	queued: 'Waiting',
	leased: 'Starting',
	running: 'In progress',
	succeeded: 'Done',
	complete: 'Done',
	failed: "Didn't work",
	cancelled: 'Cancelled',
	expired: 'Interrupted',
	// asset and version states
	draft: 'Awaiting media',
	pending_source: 'Awaiting media',
	source_ready: 'Ready to process',
	probing: 'Reading media',
	ready: 'Ready',
	building: 'Preparing',
	validating: 'Checking',
	// worker states
	online: 'Available',
	draining: 'Finishing up',
	offline: 'Unavailable',
	unhealthy: 'Having trouble',
	// check outcomes
	pass: 'Passed',
	warn: 'Worth a look',
	fail: 'Failed'
};

export function state(value: string): string {
	return states[value] ?? value.replace(/_/g, ' ');
}

const operations: Record<string, string> = {
	probe: 'Read the media',
	plan: 'Work out what to make',
	encode: 'Convert',
	package: 'Package for streaming',
	protect: 'Protect',
	thumbnail: 'Make thumbnails',
	subtitle: 'Prepare subtitles',
	audio: 'Prepare audio',
	transcribe: 'Transcribe speech',
	clip: 'Make a clip',
	watermark: 'Add a watermark',
	validate: 'Check the result'
};

export function operation(value: string): string {
	return operations[value] ?? value;
}

const pipelines: Record<string, string> = {
	probe: 'Read this media',
	'transcode-h264': 'Make a 720p version'
};

export function pipeline(name: string): string {
	return pipelines[name] ?? name;
}

const checks: Record<string, string> = {
	exists: 'The file is there',
	size: 'The file is the expected size',
	checksum: 'The file arrived intact',
	playable: 'It plays',
	video_present: 'It has video',
	audio_present: 'It has audio',
	audio_codec: 'The audio is the right format',
	codec: 'The video is the right format',
	resolution: 'The size is right',
	duration: 'The length is right',
	hdr_format: 'The colour was kept',
	artifact_count: 'Everything expected was made'
};

export function check(value: string): string {
	return checks[value] ?? value.replace(/_/g, ' ');
}

/**
 * A message a person can act on.
 *
 * Raw errors carry storage keys, identifiers and internal state, and none of
 * that helps the reader — so the API's error code chooses the wording and the
 * original text is never shown.
 */
const problems: Record<string, string> = {
	unauthorized: "That access key wasn't accepted. Check it and try again.",
	forbidden: "This key doesn't have permission to do that.",
	not_found: "We couldn't find that.",
	no_key: 'Enter your access key to continue.',
	asset_exists: 'Something with that reference already exists.',
	source_missing: 'Add the media file first.',
	source_unverified: 'The upload has not finished yet.',
	probe_required: 'Read the media first, then this option becomes available.',
	unknown_pipeline: "That option isn't available.",
	upload_incomplete: 'Some of the file is still missing. Try uploading again.',
	size_mismatch: "The file that arrived doesn't match what was expected. Try again.",
	upload_not_open: 'That upload has already finished or was cancelled.',
	too_large: 'That file is too large.',
	invalid_request: "Something in that request wasn't right.",
	database_unavailable: 'The service is temporarily unavailable. Try again shortly.',
	internal_error: 'Something went wrong on our side. Please try again.'
};

export function problem(code: string | undefined): string {
	return problems[code ?? ''] ?? 'Something went wrong. Please try again.';
}
