# ADR-009 — Protection: DRM-aware platform, pluggable providers, not a DRM vendor

Status: accepted

## Context
Customers need protection ranging from "private bucket" to "Widevine L1 with
HDCP". Building a license server means owning device attestation, robustness
rules, vendor certification and a very high-value secret store.

## Decision
Model protection as four explicit levels (private storage, signed delivery,
media encryption, full DRM). Own the encryption workflow, KIDs, key lifecycle
and rotation, PSSH construction, protected packaging, manifest signalling and
the protection policy record. Integrate with external DRM/key providers behind
a `KeyProvider` / `DRMProvider` interface. Do not build a license server.

## Why
- Protection is not a boolean, and retrofitting the levels onto a boolean schema
  is expensive.
- License issuance is a certification and liability boundary better bought than
  built; the packaging and signalling side is where our value is.
- Provider interfaces keep Widevine/FairPlay/PlayReady and CENC/CBCS from
  leaking into the packaging code.

## Consequences
- Content keys are wrapped at rest, delivered to workers through a scoped,
  attempt-bound endpoint, held in memory only, and redacted in the log layer.
- Playback authorization (entitlement) stays a separate decision from license
  issuance (DRM policy enforcement).
- Offline/persistent licenses and forensic watermarking are deferred but have
  reserved places in the policy model and pipeline respectively.

## Rejected
- **`drm_enabled` boolean** — cannot express the levels or the policies.
- **Own license server** — out of scope, permanently, absent a specific demand.
