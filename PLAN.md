# Implementation plan

Design is in `ARCHITECTURE.md`, decisions in `docs/adr/`, schema proposal in
`docs/schema.sql`. Nothing below is built yet.

Rule for every slice: it ends with something runnable and a test that fails if
the logic breaks. No slice is "done" because the code compiles.

## Confirmed requirements

Answers that shape the plan, recorded so later slices do not relitigate them.

| | Answer | Consequence |
|---|---|---|
| Callers | **Both** internal apps and third-party customers | `tenant_id` is a security boundary, not accounting. API keys, scopes, quotas and cross-tenant tests are P0. The `/v1` contract is a promise once external callers exist. |
| Deployment | One controlled box for the control plane; GPU nodes later | ADR-008 stands. Slot capacity is declared per worker so a GPU node joins with no control-plane change. |
| DRM | Real, but only once the core is solid | Stays P2. The four-level model, KIDs and key lifecycle are designed now so it is not a rewrite later. |
| Scale | No numbers yet; "millions" eventually | Do not optimise for a number we do not have. Keep the scheduler stateless and the artifact path immutable so scaling out stays a capacity question. |
| Codecs | H.264 is enough; leave room | P0 encodes H.264/AAC. Codec is structured config plus a worker capability, so HEVC and AV1 are new profiles and capability gates, not new code paths. |
| Consumption | Some callers use **assets + jobs**, others **jobs only** against their own storage | One internal model: a jobs-only request creates an ephemeral asset implicitly (ADR-011). Adds SSRF defence and customer-credential handling as hard requirements before external sources ship. |
| Operations | Thumbnails, transcription, audio normalisation and more are expected | Each is a task operation, not a pipeline (ADR-012). ASR adds worker-side model management and its own slot class. |
| Pipelines | Built-in presets now, custom later | `pipeline_versions.definition` is validated against known stages; a custom-pipeline API is P1+. |

## P0 — Foundation

The target is one end-to-end path: upload an MP4, get a validated 720p H.264
rendition back, and survive a worker being killed mid-encode.

| # | Slice | Done when |
|---|---|---|
| 0 | Repo skeleton, config, Postgres migrations, Compose | ✅ Boots against Postgres 17, migrates, serves health endpoints |
| 1 | Tenancy + API keys + auth middleware + audit | ✅ Cross-tenant resolution and revocation fail in an integration test |
| 2 | Storage abstraction over S3/MinIO, presign, multipart | ✅ Round trip, presign expiry, resumed multipart and abort against MinIO |
| 3 | Assets, asset versions, resumable uploads, completion verification (managed sources only) | ✅ Interrupted upload resumes; short parts and size mismatches rejected; no source row exists until verified |
| 4 | Job/task/attempt tables + state machine | ✅ Every ordered state pair checked; illegal moves rejected under row locks; one winner under concurrency |
| 5 | Worker registry, registration, capabilities, heartbeat, lifecycle | ✅ Registers online, survives restart with a rotated credential, swept offline after 30s silent |
| 6 | Worker protocol v1 + lease/heartbeat/complete endpoints | ✅ A stub worker leased, started, reported progress and completed a two-task graph over HTTP |
| 7 | Scheduler: constraints, scoring, leasing, persisted explanations | ✅ Seven constraint classes each proven to exclude; explanation stored per attempt. API exposure lands with the jobs API |
| 8 | Lease expiry sweeper, retry classification, cancellation propagation | ✅ Killed a worker mid-task on the live stack: reclaimed, re-run by another worker, the dead one's late report refused with 409 |
| 9 | Worker binary: task loop, FFmpeg subprocess supervision, progress parsing, process-group kill | ✅ Cancel kills FFmpeg in <1s with no stray processes; real worker registers 206 detected encoders against the live stack |
| 10 | Probe task (ffprobe → tracks, colour/HDR fields populated) | ✅ A BT.2020/PQ source probes as `hdr10` end to end, on both the local and pinned FFmpeg builds |
| 11 | Encode task: structured config → argv, H.264 + AAC | ✅ Vocabulary and injection attempts rejected; a 4K HDR source transcoded to 720p with BT.2020/PQ intact and keyframes on the 2s grid |
| 12 | Artifact registration (idempotent, lease-bound), artifact sets | ✅ A second worker registering on someone else's attempt gets 409; retries are idempotent; delivery URL round-tripped the HDR rendition |
| 13 | Validate task: exists, checksum, probe, codec/resolution/duration/audio match | ✅ A truncated output failed on size, checksum and playability after the encode had exited 0; the set was marked failed |
| 14 | Observability: structured logs with redaction, metrics, per-attempt resource accounting | Content keys never appear in logs (test asserts this) |
| 15 | Failure test suite: worker crash, DB restart, storage failure, duplicate task, expired lease, control-plane restart, interrupted upload | All pass in CI |

## P1 — Production VOD
Chunked encoding (keyframe-aligned, with documented unsafe cases and a
whole-file fallback), parallel encode, HEVC + AV1, encoding ladders, thumbnails
and sprites with their WebVTT index, posters, multiple audio tracks, loudness
normalisation to EBU R128, subtitle conversion, CMAF/HLS/DASH packaging, media
QC beyond technical validation, signed delivery URLs, media encryption,
DRM-aware packaging, HDR preservation, admin UI (ADR-010).

Two P1 items carry their own prerequisites:

- **External sources** (jobs-only callers, ADR-011) do not ship until the SSRF
  defences in ARCHITECTURE §11 exist and are tested against a metadata-endpoint
  target, and customer credentials are encrypted at rest.
- **Transcription** (ADR-012) needs whisper.cpp packaged into an ASR worker
  image, a pinned model version in the task spec, its own slot class, and
  machine-generated labelling on the produced tracks.

## P2 — Premium protection and editing
Clips, subclips, concatenation, animated previews, audio-only extraction,
visible watermarking. Widevine / FairPlay / PlayReady integration, key management and KMS, KID/PSSH
handling, key rotation, DRM policies, security levels and HDCP, offline DRM,
watermarking integration point.

## P3 — Advanced
Per-title encoding, cost optimisation, autoscaling, GPU fleet optimisation,
live (separate domain — never a giant VOD job), LL-HLS, SCTE-35, timed
metadata, multi-region.

## Test corpus
`testdata/` is generated by a script, not committed as binaries: SD/HD/FHD/4K,
30/60fps and VFR, H.264/HEVC/AV1, AAC/Opus, stereo and 5.1, multi-language,
subtitles, SDR/HDR10/HLG, very short and long, high and low bitrate, and
deliberately corrupted files. Media tests skip with a clear message when the
corpus has not been generated.
