-- Tenancy, credentials and audit.
--
-- The platform serves both internal applications and third-party customers, so
-- tenant_id is a security boundary rather than an accounting column: every
-- tenant-scoped table carries it and indexes it first.

create extension if not exists citext;

create table tenants (
    id         uuid primary key,
    name       text not null,
    status     text not null check (status in ('active', 'suspended')),
    quotas     jsonb not null default '{}',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table users (
    id         uuid primary key,
    tenant_id  uuid not null references tenants,
    email      citext not null,
    role       text not null check (role in ('owner', 'admin', 'operator', 'viewer')),
    created_at timestamptz not null default now(),
    unique (tenant_id, email)
);

create table api_keys (
    id           uuid primary key,
    tenant_id    uuid not null references tenants,
    name         text not null,
    -- key_hash is sha256 of the whole token, and is the lookup key. A password
    -- KDF would be wrong here: argon2 exists to slow brute force of low-entropy
    -- secrets, and these are 256 bits of CSPRNG output, so it would only add
    -- latency to every authenticated request. key_prefix is for display in a UI
    -- and is deliberately not unique.
    key_prefix   text not null,
    key_hash     bytea not null,
    scopes       text[] not null default '{}',
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    created_at   timestamptz not null default now()
);
create unique index api_keys_hash on api_keys (key_hash);
create index api_keys_prefix on api_keys (key_prefix);
create index api_keys_tenant on api_keys (tenant_id);

create table audit_events (
    id           uuid primary key,
    tenant_id    uuid references tenants,
    actor_type   text not null check (actor_type in ('user', 'api_key', 'worker', 'system')),
    actor_id     uuid,
    action       text not null,
    subject_type text not null,
    subject_id   uuid,
    -- never secrets: redacted at write time, not by convention downstream
    detail       jsonb not null default '{}',
    created_at   timestamptz not null default now()
);
create index audit_events_tenant_time on audit_events (tenant_id, created_at desc);
create index audit_events_subject on audit_events (subject_type, subject_id);
