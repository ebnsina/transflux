-- Worker registry additions.
--
-- Workers are fleet infrastructure, not tenant resources: one worker serves
-- every tenant, so there is deliberately no tenant_id here. Tenant isolation is
-- enforced when work is leased, not by partitioning the fleet.

-- A worker that restarts re-registers under the same name and keeps its id,
-- which also rotates its credential. Without this the fleet list would grow a
-- new row on every deploy.
create unique index workers_name on workers (name);

-- Credentials are looked up by hash, so a worker authenticates with the secret
-- alone and never has to send its id alongside.
create unique index workers_credential on workers (credential_hash);

-- Fast-changing utilisation, kept apart from the slow-changing worker row so a
-- heartbeat every ten seconds does not rewrite the capability blob.
create table worker_resources (
    worker_id         uuid primary key references workers on delete cascade,
    cpu_pct           real,
    memory_used_bytes bigint,
    disk_free_bytes   bigint,
    gpu_pct           real,
    slots_in_use      jsonb not null default '{}',
    healthy           boolean not null default true,
    updated_at        timestamptz not null default now()
);
