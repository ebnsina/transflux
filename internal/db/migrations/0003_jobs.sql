-- Pipelines, jobs, tasks and task attempts.
--
-- A pipeline describes what must happen; a task is one executable unit; a task
-- attempt is one execution of it. Never assume one task equals one execution:
-- attempts carry the debugging, retry and resource-accounting detail.

create table pipelines (
    id         uuid primary key,
    tenant_id  uuid not null references tenants,
    name       text not null,
    created_at timestamptz not null default now(),
    unique (tenant_id, name)
);

-- Versions are immutable. A job pins the version it ran, so a job stays
-- reproducible after the pipeline is edited.
create table pipeline_versions (
    id          uuid primary key,
    tenant_id   uuid not null references tenants,
    pipeline_id uuid not null references pipelines,
    version     int  not null,
    definition  jsonb not null,
    created_at  timestamptz not null default now(),
    unique (pipeline_id, version)
);

-- Workers exist here because task_attempts references them; registration and
-- lifecycle land with the worker registry.
create table workers (
    id                uuid primary key,
    name              text not null,
    hostname          text not null,
    os                text not null,
    arch              text not null,
    cpu_cores         int  not null,
    memory_bytes      bigint not null,
    disk_bytes        bigint not null,
    gpu_model         text,
    gpu_memory_bytes  bigint,
    ffmpeg_version    text not null,
    protocol_version  int  not null,
    capabilities      jsonb not null default '{}',
    -- Capacity per workload class, declared by the worker. A 32-core host is
    -- not 32 concurrent AV1 encodes, so this is never derived from core count.
    slot_capacity     jsonb not null default '{}',
    state             text not null check (state in
                        ('online', 'draining', 'offline', 'unhealthy')),
    credential_hash   bytea not null,
    last_heartbeat_at timestamptz not null default now(),
    registered_at     timestamptz not null default now()
);
create index workers_state on workers (state, last_heartbeat_at);

create table jobs (
    id                  uuid primary key,
    tenant_id           uuid not null references tenants,
    asset_version_id    uuid not null references asset_versions,
    pipeline_version_id uuid not null references pipeline_versions,
    idempotency_key     text,
    priority            int  not null default 100,
    state               text not null check (state in
                          ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    plan                jsonb,
    failure_reason      text,
    created_at          timestamptz not null default now(),
    started_at          timestamptz,
    finished_at         timestamptz,
    unique (tenant_id, idempotency_key)
);
create index jobs_tenant_time on jobs (tenant_id, created_at desc);
create index jobs_active on jobs (state) where state in ('pending', 'running');

create table tasks (
    id             uuid primary key,
    tenant_id      uuid not null references tenants,
    job_id         uuid not null references jobs,
    -- Operations are the extension point: a new capability is a spec, a worker
    -- capability gate and an artifact kind, never a new pipeline.
    operation      text not null check (operation in
                     ('probe', 'plan', 'encode', 'package', 'protect', 'thumbnail',
                      'subtitle', 'audio', 'transcribe', 'clip', 'watermark',
                      'validate')),
    spec           jsonb not null default '{}',
    requirements   jsonb not null default '{}',
    depends_on     uuid[] not null default '{}',
    priority       int  not null default 100,
    state          text not null check (state in
                     ('pending', 'queued', 'leased', 'running',
                      'succeeded', 'failed', 'cancelled')),
    attempt_count  int  not null default 0,
    max_attempts   int  not null default 3,
    failure_class  text check (failure_class in
                     ('transient', 'resource', 'permanent_input',
                      'permanent_config', 'infrastructure', 'unknown')),
    failure_reason text,
    queued_at      timestamptz,
    created_at     timestamptz not null default now(),
    finished_at    timestamptz
);
-- The scheduler's hot path: schedulable tasks, best first.
create index tasks_schedulable on tasks (priority desc, queued_at)
    where state = 'queued';
create index tasks_job on tasks (job_id);

create table task_attempts (
    id                uuid primary key,
    tenant_id         uuid not null references tenants,
    task_id           uuid not null references tasks,
    attempt_number    int  not null,
    worker_id         uuid not null references workers,
    state             text not null check (state in
                        ('leased', 'running', 'succeeded', 'failed',
                         'cancelled', 'expired')),
    lease_expires_at  timestamptz not null,
    -- Why the scheduler chose this worker, kept so "why did this take an hour"
    -- is a query rather than an investigation.
    schedule_reason   text not null default '',
    progress_pct      real,
    -- Resource accounting, for cost-per-asset analysis later. Not billing.
    cpu_seconds       double precision,
    wall_seconds      double precision,
    peak_memory_bytes bigint,
    gpu_seconds       double precision,
    bytes_in          bigint,
    bytes_out         bigint,
    failure_class     text,
    failure_reason    text,
    log_storage_key   text,          -- logs live in object storage, not here
    leased_at         timestamptz not null default now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    unique (task_id, attempt_number)
);
-- The expiry sweeper's index.
create index task_attempts_live on task_attempts (lease_expires_at)
    where state in ('leased', 'running');
create index task_attempts_task on task_attempts (task_id);
