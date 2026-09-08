/**
 * Client for the Transflux control plane.
 *
 * The API key lives in localStorage rather than a cookie: this is an operator
 * tool served as static files, there is no session server, and a cookie would
 * imply one.
 */

const KEY_STORAGE = 'transflux.api_key';

export function apiKey(): string {
	if (typeof localStorage === 'undefined') return '';
	return localStorage.getItem(KEY_STORAGE) ?? '';
}

export function setApiKey(key: string) {
	localStorage.setItem(KEY_STORAGE, key.trim());
}

export function clearApiKey() {
	localStorage.removeItem(KEY_STORAGE);
}

export class ApiError extends Error {
	constructor(
		readonly status: number,
		readonly code: string,
		message: string
	) {
		super(message);
	}
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
	const key = apiKey();
	if (!key) throw new ApiError(401, 'no_key', 'No API key has been set.');

	const res = await fetch(path, {
		method,
		headers: {
			Authorization: `Bearer ${key}`,
			...(body === undefined ? {} : { 'Content-Type': 'application/json' })
		},
		body: body === undefined ? undefined : JSON.stringify(body)
	});

	if (res.status === 204) return undefined as T;

	const text = await res.text();
	const parsed = text ? JSON.parse(text) : {};
	if (!res.ok) {
		// The API returns a consistent envelope, so surface its own words
		// rather than inventing a message.
		const err = parsed?.error ?? {};
		throw new ApiError(res.status, err.code ?? 'error', err.message ?? res.statusText);
	}
	return parsed as T;
}

export const api = {
	get: <T>(path: string) => request<T>('GET', path),
	post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
	del: <T>(path: string) => request<T>('DELETE', path)
};

// ── shapes returned by the API ────────────────────────────────────────────

export type Identity = { tenant_id: string; api_key_id: string; scopes: string[] };

export type Track = {
	kind: string;
	stream_index: number;
	codec?: string;
	language?: string;
	width?: number;
	height?: number;
	fps_num?: number;
	fps_den?: number;
	pixel_format?: string;
	bit_depth?: number;
	color_primaries?: string;
	color_transfer?: string;
	color_matrix?: string;
	hdr_format?: string;
	channels?: number;
	sample_rate_hz?: number;
};

export type Media = {
	container?: string;
	duration_ms?: number;
	bitrate_bps?: number;
	size_bytes?: number;
	tracks: Track[];
};

export type Version = {
	id: string;
	asset_id: string;
	version: number;
	status: string;
	created_at: string;
	media?: Media;
};

export type Asset = {
	id: string;
	external_id?: string;
	name?: string;
	status: string;
	lifecycle: string;
	created_at: string;
	versions?: Version[];
};

export type Job = {
	id: string;
	asset_version_id: string;
	state: string;
	priority: number;
	failure_reason?: string;
	created_at: string;
	started_at?: string;
	finished_at?: string;
};

export type Task = {
	id: string;
	operation: string;
	state: string;
	attempt_count: number;
	max_attempts: number;
	failure_class?: string;
	failure_reason?: string;
};

export type Attempt = {
	id: string;
	operation: string;
	attempt_number: number;
	worker_name: string;
	state: string;
	schedule_reason?: string;
	cpu_seconds?: number;
	wall_seconds?: number;
	peak_memory_bytes?: number;
	bytes_out?: number;
	failure_class?: string;
	failure_reason?: string;
	leased_at: string;
	finished_at?: string;
};

export type Check = {
	label: string;
	category: string;
	name: string;
	status: string;
	detail?: Record<string, unknown>;
};

export type JobDetail = {
	job: Job;
	tasks: Task[];
	attempts?: Attempt[];
	validation?: { checks: Check[] };
};

export type Artifact = {
	id: string;
	kind: string;
	label: string;
	size_bytes: number;
	checksum_algo?: string;
	media?: Record<string, unknown>;
	created_at: string;
};

export type ArtifactSet = {
	id: string;
	version: number;
	state: string;
	created_at: string;
	completed_at?: string;
	artifacts: Artifact[];
};

export type Worker = {
	id: string;
	name: string;
	hostname: string;
	os: string;
	arch: string;
	cpu_cores: number;
	memory_bytes: number;
	gpu_model?: string;
	ffmpeg_version: string;
	capabilities: { operations?: string[]; encoders?: string[] };
	slot_capacity: Record<string, number>;
	state: string;
	last_heartbeat_at: string;
};

export type Pipeline = { name: string; description: string };
