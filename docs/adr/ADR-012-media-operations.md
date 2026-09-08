# ADR-012 — Media operations catalogue, and ASR on workers

Status: accepted

## Context
Beyond encode and package, a media platform is expected to produce thumbnails,
posters, sprites, generated captions, normalised audio, clips and previews.
"Add a feature" must not mean "add a pipeline".

Transcription (ASR) is the first operation that adds a genuinely new dimension:
it needs a multi-gigabyte model on the worker, and its output must be
reproducible across model versions.

## Decision

### Operations are the extension point
Every capability is a **task operation**: a structured spec, a worker
capability gate, and an artifact kind. Adding one touches the operation enum,
one worker executor and the planner — never the scheduler, the lease protocol,
the state machine or the artifact model.

| Operation | Phase | Notes |
|---|---|---|
| `probe` | P0 | ffprobe → tracks, colour/HDR |
| `encode` | P0 | structured config → FFmpeg |
| `validate` | P0 | outputs are not trusted on exit code 0 |
| `thumbnail` | P1 | stills at times or intervals, posters, sprite sheets + the WebVTT index a player needs |
| `audio` | P1 | per-track renditions, loudness normalisation to EBU R128 (broadcast-standard target, not a guess) |
| `subtitle` | P1 | convert and preserve existing tracks: WebVTT, SRT, TTML, IMSC, CEA-608/708 |
| `package` | P1 | CMAF, HLS, DASH |
| `transcribe` | P1 | ASR → WebVTT/SRT + word-timed transcript |
| `protect` | P2 | encryption and DRM signalling |
| `clip` | P2 | subclips, concatenation, animated previews, audio-only extraction |
| `watermark` | P2 | visible overlay; forensic watermarking stays an integration boundary |

Explicitly integration boundaries, not operations we implement: content
moderation, object and face recognition, viewer analytics.

### ASR engine: whisper.cpp as a subprocess
Same pattern as ADR-004 — a sandboxed subprocess with structured arguments, not
a library in our address space, and no Python runtime on worker hosts.

- The model is a **worker capability** (`asr_models: ["whisper-large-v3"]`) and
  the scheduler gates on it exactly as it gates on an encoder.
- The task spec **pins the model version**, so a transcript is reproducible and
  a model upgrade is a visible change rather than silent drift.
- Models are baked into the worker image, or fetched once into a cache
  directory and verified by checksum. They are never fetched per task.
- ASR gets its own slot class. It is GPU-hungry and must not be counted against
  encode slots.
- Outputs: WebVTT and SRT artifacts plus a JSON transcript with word-level
  timings, and a detected language recorded on the track.

## Why
- Thumbnails, captions and normalisation are table stakes; discovering that
  each needs bespoke plumbing would mean the task abstraction failed.
- Loudness normalisation is named as EBU R128 rather than left to taste because
  "sounds about right" is not reproducible and not what broadcasters check.
- Transcription is the operation most likely to tempt a special case (models,
  GPUs, Python). Making it prove the operation abstraction is the point.

## Consequences
- Worker images diverge: an ASR-capable worker is much larger. That is fine —
  capabilities are declared, not assumed, so a small encode-only worker simply
  never receives transcription work.
- Generated captions are marked as machine-generated in track metadata.
  Presenting ASR output as authored captions is an accessibility problem, not a
  cosmetic one.
- Transcription cost per minute is recorded like any other operation, so its
  expense is visible rather than buried in encode cost.

## Rejected
- **A separate transcription service** — same fetch, same leases, same retries,
  same artifacts. It is a task.
- **A hosted ASR API as the only option** — a reasonable provider to support
  later behind the same operation, but it sends customer media to a third party,
  which some tenants cannot accept.
