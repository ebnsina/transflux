-- Validation results.
--
-- A successful encode is not a successful output. Exit code zero says the tool
-- did not crash; it says nothing about whether the file plays, has the right
-- resolution, or contains the audio that was asked for.

create table validation_results (
    id              uuid primary key,
    tenant_id       uuid not null references tenants,
    artifact_set_id uuid not null references artifact_sets on delete cascade,
    artifact_label  text,
    -- 'technical' is "is this file what we said it is". 'quality' is "does it
    -- look and sound right", which is a different question with different
    -- answers, and conflating them makes both unactionable.
    category        text not null check (category in ('technical', 'quality')),
    check_name      text not null,
    status          text not null check (status in ('pass', 'warn', 'fail')),
    detail          jsonb not null default '{}',
    checked_at      timestamptz not null default now()
);
create index validation_results_set on validation_results (artifact_set_id, status);
