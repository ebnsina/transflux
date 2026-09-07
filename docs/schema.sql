-- Transflux schema proposal (design artifact, not migrations yet).
-- Conventions:
--   * ids are uuidv7 generated app-side (time-ordered, index-friendly)
--   * every tenant-scoped table carries tenant_id and indexes it first
--   * money/bytes/durations are explicit units in the column name
--   * enums are text + CHECK, not pg enums (cheaper to evolve)

-- ── tenancy ─────────────────────────────────────────────────────────────
create table tenants (
  id            uuid primary key,
  name          text not null,
  status        text not null check (status in ('active','suspended')),
  quotas        jsonb not null default '{}',  -- storage_bytes, concurrent_tasks, rps
  created_at    timestamptz not null default now()
);

create table users (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  email         citext not null,
  role          text not null check (role in ('owner','admin','operator','viewer')),
  created_at    timestamptz not null default now(),
  unique (tenant_id, email)
);

create table api_keys (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  name          text not null,
  key_hash      bytea not null,               -- argon2id; plaintext shown once
  key_prefix    text not null,                -- for identification in the UI
  scopes        text[] not null default '{}',
  last_used_at  timestamptz,
  expires_at    timestamptz,
  revoked_at    timestamptz,
  created_at    timestamptz not null default now()
);
create index on api_keys (key_prefix) where revoked_at is null;

-- ── assets ──────────────────────────────────────────────────────────────
create table assets (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  external_id   text,                         -- customer's own id, optional
  name          text,
  status        text not null check (status in ('draft','ready','deleted')),
  metadata      jsonb not null default '{}',
  created_at    timestamptz not null default now(),
  deleted_at    timestamptz,
  unique (tenant_id, external_id)
);
create index on assets (tenant_id, created_at desc);

-- an asset version pins one immutable source. Replacing the source makes a
-- new version; jobs always reference a version, never a bare asset.
create table asset_versions (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  asset_id      uuid not null references assets,
  version       int  not null,
  status        text not null check (status in ('pending_source','probing','ready','failed')),
  created_at    timestamptz not null default now(),
  unique (asset_id, version)
);

create table source_files (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  asset_version_id  uuid not null references asset_versions,
  storage_bucket    text not null,
  storage_key       text not null,
  size_bytes        bigint not null,
  checksum_algo     text not null,            -- 'sha256' | 'crc32c'
  checksum          bytea not null,
  content_type      text,
  verified_at       timestamptz,              -- NULL ⇒ no job may run on it
  created_at        timestamptz not null default now(),
  unique (storage_bucket, storage_key)
);

-- ── uploads ─────────────────────────────────────────────────────────────
create table uploads (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  asset_version_id  uuid not null references asset_versions,
  storage_bucket    text not null,
  storage_key       text not null,
  storage_upload_id text,                     -- S3 multipart id
  declared_size_bytes bigint,
  declared_checksum bytea,
  status            text not null check (status in
                      ('created','in_progress','completing','completed','aborted','expired')),
  expires_at        timestamptz not null,
  created_at        timestamptz not null default now(),
  completed_at      timestamptz
);
create index on uploads (status, expires_at);

create table upload_parts (
  upload_id     uuid not null references uploads on delete cascade,
  part_number   int  not null check (part_number between 1 and 10000),
  size_bytes    bigint not null,
  etag          text not null,
  checksum      bytea,
  uploaded_at   timestamptz not null default now(),
  primary key (upload_id, part_number)        -- duplicate part = idempotent overwrite
);

-- ── probe ───────────────────────────────────────────────────────────────
create table probes (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  asset_version_id  uuid not null references asset_versions unique,
  container         text,
  duration_ms       bigint,
  bitrate_bps       bigint,
  size_bytes        bigint,
  raw               jsonb not null,           -- full ffprobe output, kept verbatim
  probed_at         timestamptz not null default now()
);

create table tracks (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  asset_version_id  uuid not null references asset_versions,
  probe_id          uuid not null references probes,
  kind              text not null check (kind in ('video','audio','subtitle','data')),
  stream_index      int  not null,
  codec             text,
  duration_ms       bigint,
  bitrate_bps       bigint,
  language          text,                     -- BCP-47
  title             text,
  is_default        boolean not null default false,
  is_forced         boolean not null default false,
  role              text,                     -- 'main','commentary','description','captions'
  -- video
  width             int,
  height            int,
  fps_num           int,
  fps_den           int,
  is_vfr            boolean,
  pixel_format      text,
  bit_depth         int,
  rotation_degrees  int,
  -- colour / HDR: modelled from day one, never assumed BT.709
  color_primaries   text,                     -- 'bt709','bt2020', ...
  color_transfer    text,                     -- 'bt709','smpte2084','arib-std-b67', ...
  color_matrix      text,
  color_range       text,                     -- 'tv','pc'
  hdr_format        text,                     -- 'sdr','hdr10','hlg','hdr10plus','dolby_vision'
  mastering_display jsonb,
  content_light     jsonb,
  -- audio
  channels          int,
  channel_layout    text,
  sample_rate_hz    int,
  -- subtitle
  subtitle_format   text,                     -- 'webvtt','srt','ttml','imsc','cea608','cea708'
  unique (asset_version_id, stream_index)
);
create index on tracks (asset_version_id, kind);

-- ── pipelines ───────────────────────────────────────────────────────────
create table pipelines (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  name          text not null,
  created_at    timestamptz not null default now(),
  unique (tenant_id, name)
);

-- versions are immutable; a job pins the version it ran, so jobs stay reproducible
create table pipeline_versions (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  pipeline_id   uuid not null references pipelines,
  version       int  not null,
  definition    jsonb not null,               -- stages, ladder, packaging, protection
  created_at    timestamptz not null default now(),
  unique (pipeline_id, version)
);

-- ── jobs / tasks / attempts ─────────────────────────────────────────────
create table jobs (
  id                  uuid primary key,
  tenant_id           uuid not null references tenants,
  asset_version_id    uuid not null references asset_versions,
  pipeline_version_id uuid not null references pipeline_versions,
  idempotency_key     text,
  priority            int  not null default 100,
  state               text not null check (state in
                        ('pending','running','succeeded','failed','cancelled')),
  plan                jsonb,                  -- planner output: the concrete decisions
  failure_reason      text,
  created_by          uuid references users,
  created_at          timestamptz not null default now(),
  started_at          timestamptz,
  finished_at         timestamptz,
  unique (tenant_id, idempotency_key)
);
create index on jobs (tenant_id, created_at desc);
create index on jobs (state) where state in ('pending','running');

create table tasks (
  id              uuid primary key,
  tenant_id       uuid not null references tenants,
  job_id          uuid not null references jobs,
  operation       text not null check (operation in
                    ('probe','plan','encode','package','protect','thumbnail',
                     'subtitle','audio','validate')),
  spec            jsonb not null,             -- structured; never a command string
  requirements    jsonb not null,             -- codec, encoder, gpu, memory_bytes, disk_bytes, class
  depends_on      uuid[] not null default '{}',
  priority        int  not null default 100,
  state           text not null check (state in
                    ('pending','queued','leased','running','succeeded','failed','cancelled')),
  attempt_count   int  not null default 0,
  max_attempts    int  not null default 3,
  failure_class   text,                       -- transient|resource|permanent_input|
                                              -- permanent_config|infrastructure|unknown
  failure_reason  text,
  queued_at       timestamptz,
  created_at      timestamptz not null default now(),
  finished_at     timestamptz
);
-- the scheduler's hot path: feasible-task lookup by state + priority + age
create index on tasks (state, priority desc, queued_at)
  where state = 'queued';
create index on tasks (job_id);

create table task_attempts (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  task_id           uuid not null references tasks,
  attempt_number    int  not null,
  worker_id         uuid not null references workers,
  state             text not null check (state in
                      ('leased','running','succeeded','failed','cancelled','expired')),
  lease_expires_at  timestamptz not null,
  schedule_reason   text not null,            -- the persisted scheduling explanation
  progress_pct      real,
  -- resource accounting (cost analysis later; not billing)
  cpu_seconds       double precision,
  wall_seconds      double precision,
  peak_memory_bytes bigint,
  gpu_seconds       double precision,
  bytes_in          bigint,
  bytes_out         bigint,
  failure_class     text,
  failure_reason    text,
  log_storage_key   text,                     -- logs live in object storage, not the DB
  leased_at         timestamptz not null default now(),
  started_at        timestamptz,
  finished_at       timestamptz,
  unique (task_id, attempt_number)
);
-- the expiry sweeper's index
create index on task_attempts (lease_expires_at)
  where state in ('leased','running');

-- ── chunks (P1) ─────────────────────────────────────────────────────────
create table chunks (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  job_id            uuid not null references jobs,
  task_id           uuid references tasks,
  chunk_index       int  not null,
  start_pts_ms      bigint not null,
  end_pts_ms        bigint not null,
  start_is_keyframe boolean not null,
  end_is_keyframe   boolean not null,
  state             text not null check (state in
                      ('pending','running','succeeded','failed')),
  output_key        text,
  unique (job_id, chunk_index)
);

-- ── workers ─────────────────────────────────────────────────────────────
create table workers (
  id                  uuid primary key,
  name                text not null,
  hostname            text not null,
  os                  text not null,
  arch                text not null,          -- 'amd64','arm64'
  cpu_model           text,
  cpu_cores           int  not null,
  memory_bytes        bigint not null,
  disk_bytes          bigint not null,
  gpu_model           text,
  gpu_memory_bytes    bigint,
  ffmpeg_version      text not null,
  protocol_version    int  not null,
  capabilities        jsonb not null,         -- encoders, decoders, pix_fmts, containers,
                                              -- packagers, encryption, operations
  slot_capacity       jsonb not null,         -- {"h264":4,"hevc":2,"av1":1,"probe":8,...}
  state               text not null check (state in
                        ('online','draining','offline','unhealthy')),
  credential_hash     bytea not null,
  last_heartbeat_at   timestamptz not null default now(),
  registered_at       timestamptz not null default now()
);
create index on workers (state, last_heartbeat_at);

-- current utilisation, updated on heartbeat; separate from the slow-changing row above
create table worker_resources (
  worker_id             uuid primary key references workers on delete cascade,
  cpu_pct               real,
  memory_used_bytes     bigint,
  disk_free_bytes       bigint,
  gpu_pct               real,
  slots_in_use          jsonb not null default '{}',
  updated_at            timestamptz not null default now()
);

-- ── artifacts ───────────────────────────────────────────────────────────
create table artifact_sets (
  id            uuid primary key,
  tenant_id     uuid not null references tenants,
  job_id        uuid not null references jobs,
  version       int  not null,
  state         text not null check (state in ('building','validating','complete','failed')),
  storage_prefix text not null,
  created_at    timestamptz not null default now(),
  completed_at  timestamptz,
  unique (job_id, version)
);

create table artifacts (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  artifact_set_id   uuid not null references artifact_sets,
  task_attempt_id   uuid not null references task_attempts,
  kind              text not null check (kind in
                      ('rendition','segment_set','manifest','thumbnail','sprite',
                       'poster','subtitle','drm_metadata','log')),
  label             text not null,            -- '1080p_h264', 'hls_master', ...
  storage_key       text not null,
  size_bytes        bigint not null,
  checksum_algo     text not null,
  checksum          bytea not null,
  media             jsonb,                    -- codec, resolution, bitrate, duration
  created_at        timestamptz not null default now(),
  unique (artifact_set_id, label)             -- immutable: never overwritten
);

create table manifests (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  artifact_set_id   uuid not null references artifact_sets,
  format            text not null check (format in ('hls','dash','cmaf')),
  storage_key       text not null,
  variants          jsonb not null
);

-- ── protection ──────────────────────────────────────────────────────────
create table protection_policies (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  name              text not null,
  level             int  not null check (level between 1 and 4),
  scheme            text check (scheme in ('aes128','sample_aes','cenc','cbcs')),
  drm_systems       text[] not null default '{}',  -- widevine, fairplay, playready
  policy            jsonb not null default '{}',   -- window, geo, concurrency, hdcp,
                                                   -- security_level, offline
  created_at        timestamptz not null default now(),
  unique (tenant_id, name)
);

create table encryption_keys (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  kid               bytea not null,           -- 16 bytes
  version           int  not null default 1,
  provider          text not null,            -- 'local','kms','vendor'
  wrapped_key       bytea,                    -- NEVER plaintext; NULL when vendor-held
  provider_ref      text,                     -- external key handle
  iv                bytea,
  state             text not null check (state in ('active','rotating','retired','revoked')),
  created_at        timestamptz not null default now(),
  rotated_at        timestamptz,
  unique (tenant_id, kid, version)
);

create table drm_metadata (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  artifact_set_id   uuid not null references artifact_sets,
  encryption_key_id uuid not null references encryption_keys,
  drm_system        text not null,
  pssh              bytea,
  signaling         jsonb not null default '{}'
);

-- ── validation / QC ─────────────────────────────────────────────────────
create table validation_results (
  id                uuid primary key,
  tenant_id         uuid not null references tenants,
  artifact_set_id   uuid not null references artifact_sets,
  category          text not null check (category in ('technical','quality')),
  check_name        text not null,            -- 'duration_match','manifest_parses','black_frames'
  status            text not null check (status in ('pass','warn','fail')),
  detail            jsonb not null default '{}',
  checked_at        timestamptz not null default now()
);
create index on validation_results (artifact_set_id, status);

-- ── storage reconciliation ──────────────────────────────────────────────
create table storage_findings (
  id            uuid primary key,
  tenant_id     uuid,
  kind          text not null check (kind in ('orphaned_object','missing_object')),
  storage_bucket text not null,
  storage_key   text not null,
  first_seen_at timestamptz not null default now(),
  last_seen_at  timestamptz not null default now(),
  seen_count    int not null default 1,       -- aged before any deletion; never one-shot
  resolved_at   timestamptz,
  unique (storage_bucket, storage_key, kind)
);

-- ── audit ───────────────────────────────────────────────────────────────
create table audit_events (
  id            uuid primary key,
  tenant_id     uuid,
  actor_type    text not null check (actor_type in ('user','api_key','worker','system')),
  actor_id      uuid,
  action        text not null,                -- 'asset.created','job.cancelled', ...
  subject_type  text not null,
  subject_id    uuid,
  detail        jsonb not null default '{}',  -- never secrets; redacted at write time
  created_at    timestamptz not null default now()
);
create index on audit_events (tenant_id, created_at desc);
create index on audit_events (subject_type, subject_id);
