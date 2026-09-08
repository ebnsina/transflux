-- Artifacts: the immutable outputs of a job.
--
-- Nothing is ever overwritten. A re-run produces a new artifact set version
-- alongside the old one, so a delivery URL that worked yesterday still resolves
-- to the bytes it resolved to yesterday.

create table artifact_sets (
    id             uuid primary key,
    tenant_id      uuid not null references tenants,
    job_id         uuid not null references jobs,
    version        int  not null,
    state          text not null check (state in
                     ('building', 'validating', 'complete', 'failed')),
    storage_prefix text not null,
    created_at     timestamptz not null default now(),
    completed_at   timestamptz,
    unique (job_id, version)
);
create index artifact_sets_tenant on artifact_sets (tenant_id, created_at desc);

create table artifacts (
    id              uuid primary key,
    tenant_id       uuid not null references tenants,
    artifact_set_id uuid not null references artifact_sets on delete cascade,
    task_attempt_id uuid not null references task_attempts,
    kind            text not null check (kind in
                      ('rendition', 'segment_set', 'manifest', 'thumbnail', 'sprite',
                       'poster', 'subtitle', 'transcript', 'clip', 'drm_metadata', 'log')),
    label           text not null,
    storage_key     text not null,
    size_bytes      bigint not null,
    checksum_algo   text,
    checksum        bytea,
    -- Codec, resolution, bitrate and duration of this output, so a caller can
    -- choose between renditions without fetching any of them.
    media           jsonb,
    created_at      timestamptz not null default now(),
    -- Immutability, enforced rather than intended: a second artifact with the
    -- same label in the same set is a conflict, not an update.
    unique (artifact_set_id, label)
);
create index artifacts_set on artifacts (artifact_set_id);
create unique index artifacts_storage_key on artifacts (storage_key);
