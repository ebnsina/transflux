-- Assets, asset versions, uploads and source files.
--
-- An asset is media; a job is an operation against it. An asset version pins
-- one immutable source, and jobs reference a version rather than a bare asset,
-- so a job stays reproducible after the source is replaced.

create table assets (
    id          uuid primary key,
    tenant_id   uuid not null references tenants,
    external_id text,                          -- the caller's own id, optional
    name        text,
    status      text not null check (status in ('draft', 'ready', 'deleted')),
    -- 'managed'   : we hold the source and own its retention and deletion.
    -- 'ephemeral' : created implicitly for a jobs-only caller who brought their
    --               own storage. Garbage collection must never delete its
    --               source object — it is not ours (ADR-011).
    lifecycle   text not null default 'managed'
                  check (lifecycle in ('managed', 'ephemeral')),
    metadata    jsonb not null default '{}',
    created_at  timestamptz not null default now(),
    deleted_at  timestamptz,
    unique (tenant_id, external_id)
);
create index assets_tenant_time on assets (tenant_id, created_at desc);

create table asset_versions (
    id         uuid primary key,
    tenant_id  uuid not null references tenants,
    asset_id   uuid not null references assets,
    version    int  not null,
    status     text not null check (status in
                 ('pending_source', 'source_ready', 'probing', 'ready', 'failed')),
    created_at timestamptz not null default now(),
    unique (asset_id, version)
);
create index asset_versions_tenant on asset_versions (tenant_id);

create table source_files (
    id               uuid primary key,
    tenant_id        uuid not null references tenants,
    asset_version_id uuid not null references asset_versions unique,
    -- Managed sources live in our bucket and are verified at upload completion.
    -- External sources cannot be verified in advance; they are checked at fetch
    -- time and may fail the first task instead (ADR-011).
    origin           text not null default 'managed'
                       check (origin in ('managed', 'external_url', 'external_s3')),
    storage_key      text,                     -- managed only
    external_url     text,                     -- external_url only
    external_ref     jsonb,                    -- external_s3: endpoint, bucket, key
    size_bytes       bigint,
    checksum_algo    text,
    checksum         bytea,
    content_type     text,
    -- NULL means no job may run against it. This is the gate that stops
    -- expensive work starting on a half-uploaded file.
    verified_at      timestamptz,
    created_at       timestamptz not null default now(),
    check ((origin = 'managed') = (storage_key is not null))
);

create table uploads (
    id                uuid primary key,
    tenant_id         uuid not null references tenants,
    asset_version_id  uuid not null references asset_versions,
    storage_key       text not null,
    storage_upload_id text not null,           -- the provider's multipart id
    size_bytes        bigint not null,
    part_size         int    not null,
    part_count        int    not null,
    content_type      text,
    checksum_algo     text,
    checksum          bytea,                   -- declared by the client, verified at probe
    status            text not null check (status in
                        ('in_progress', 'completed', 'aborted', 'expired')),
    expires_at        timestamptz not null,
    created_at        timestamptz not null default now(),
    completed_at      timestamptz
);
create index uploads_expiry on uploads (status, expires_at) where status = 'in_progress';
create index uploads_tenant on uploads (tenant_id);

-- There is deliberately no upload_parts table. The storage provider's ListParts
-- is the authority on what was actually received: a client can upload a part
-- and die before telling us, and our own record would then be wrong in the
-- direction that causes data loss.
