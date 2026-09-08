# ADR-011 — External sources: bring-your-own-storage, one internal model

Status: accepted

## Context
Third-party consumers use the platform in two ways:

1. **Assets + jobs** — they upload to us, we store the source, they run jobs
   against it and we keep the asset for future jobs.
2. **Jobs only** — the media already lives in their own bucket or behind their
   own URL. They want processing, not storage. The source is not ours and must
   not be treated as ours.

The second case must not force a parallel code path through probe, planning,
encoding, packaging and artifacts.

## Decision
A **job-only request still creates an asset and an asset version**, implicitly.
The caller never names one; the API accepts either an existing
`asset_version_id` **or** an inline `source`, never both.

Assets carry a `lifecycle`:

- `managed` — we hold the source, we are responsible for its retention and
  deletion. Created through the upload API.
- `ephemeral` — created implicitly for a job-only request. Not listed in the
  asset API by default, garbage-collected on its own schedule, and **its source
  object is never deleted by us** because it is not ours.

Source files carry an `origin`:

- `managed` — an object in our bucket, uploaded and verified by us.
- `external_url` / `external_s3` — a customer-owned location, referenced with a
  stored credential or a signed URL they supply.

Outputs follow the same shape in reverse: an artifact destination may be our
bucket (default) or a customer-owned bucket.

## Why
- Downstream of "fetch the bytes", every stage is identical. Two source kinds
  should differ in one resolver, not in nine task implementations.
- Keeping the asset model for job-only callers preserves Asset ≠ Job: probe
  results, tracks, jobs and artifacts all still hang off an asset version, and
  a job stays reproducible.
- Making the lifecycle explicit is what stops us deleting a customer's source
  object during garbage collection. A boolean would not have.

## Consequences
- **SSRF becomes a first-class threat.** Fetching a customer-supplied URL from
  inside our network is the classic path to cloud metadata endpoints and
  internal services. Mitigations are mandatory before external sources ship:
  scheme allowlist (https, s3), resolve DNS then pin the IP for the connection
  (defeats rebinding), reject private, loopback, link-local and CGNAT ranges
  including after every redirect, cap redirects, cap response size, and run the
  fetch from a worker with egress restricted to public destinations.
- Customer storage credentials are secrets: encrypted at rest, scoped to one
  tenant, never logged, never returned by the API, and delivered to a worker
  only as a short-lived presigned URL where the provider allows it, rather than
  as raw credentials.
- Verification differs by origin. A managed source is verified at upload
  completion. An external source cannot be, so it is probed and size-checked at
  fetch time, and a job may fail with `permanent_input` at the first task rather
  than being rejected at submission. That asymmetry is documented in the API.
- Quotas differ: external sources consume no storage quota but do consume
  processing quota and egress.

## Rejected
- **A separate "processing-only" API and pipeline** — two of everything, for a
  difference that ends after the first fetch.
- **Requiring a copy into our bucket first** — simple, but it silently doubles
  storage cost and turns a processing product into a storage product the
  customer did not ask for. May be offered later as an explicit `cache_source`
  option.
