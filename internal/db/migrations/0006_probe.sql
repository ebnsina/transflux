-- Probe results and media tracks.
--
-- Colour and HDR are modelled from the start even though P0 encodes SDR H.264.
-- Defaulting the schema to BT.709 is the mistake that is expensive to undo:
-- once HDR sources have been recorded as SDR, the only way back is re-probing
-- every asset.

create table probes (
    id               uuid primary key,
    tenant_id        uuid not null references tenants,
    asset_version_id uuid not null references asset_versions unique,
    container        text,
    duration_ms      bigint,
    bitrate_bps      bigint,
    size_bytes       bigint,
    -- The tool's own output, kept verbatim. A later change of mind about which
    -- fields matter should not mean re-probing every asset.
    raw              jsonb not null,
    probed_at        timestamptz not null default now()
);

create table tracks (
    id               uuid primary key,
    tenant_id        uuid not null references tenants,
    asset_version_id uuid not null references asset_versions,
    probe_id         uuid not null references probes on delete cascade,
    kind             text not null check (kind in ('video', 'audio', 'subtitle', 'data')),
    stream_index     int  not null,
    codec            text,
    duration_ms      bigint,
    bitrate_bps      bigint,
    language         text,                       -- BCP-47 where the source gives one
    title            text,
    is_default       boolean not null default false,
    is_forced        boolean not null default false,
    role             text,                       -- main, commentary, description, captions

    -- video
    width            int,
    height           int,
    fps_num          int,
    fps_den          int,
    is_vfr           boolean,
    pixel_format     text,
    bit_depth        int,
    rotation_degrees int,

    -- colour and HDR
    color_primaries  text,                       -- bt709, bt2020, ...
    color_transfer   text,                       -- bt709, smpte2084, arib-std-b67, ...
    color_matrix     text,
    color_range      text,                       -- tv, pc
    -- A video property, so NULL on audio and subtitle tracks rather than a
    -- meaningless 'sdr'.
    hdr_format       text check (hdr_format in ('sdr','hdr10','hlg','hdr10plus','dolby_vision')),
    mastering_display jsonb,
    content_light     jsonb,

    -- audio
    channels         int,
    channel_layout   text,
    sample_rate_hz   int,

    -- subtitle
    subtitle_format  text,
    -- ASR output must be labelled: presenting machine captions as authored
    -- ones is an accessibility problem, not a cosmetic one.
    is_machine_generated boolean not null default false,
    asr_model        text,

    unique (asset_version_id, stream_index)
);
create index tracks_version_kind on tracks (asset_version_id, kind);
