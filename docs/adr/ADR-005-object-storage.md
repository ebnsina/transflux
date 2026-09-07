# ADR-005 — Object storage: S3-compatible behind a narrow interface

Status: accepted

## Context
Media is large, immutable, and must outlive any single machine. Uploads must be
resumable. We must not be locked to one vendor.

## Decision
An `internal/storage` interface of a handful of methods (Head, Get, Put,
Multipart{Create,UploadPart,Complete,Abort}, Delete, Presign), implemented once
against S3-compatible APIs via `aws-sdk-go-v2` with a configurable endpoint.
MinIO in development; S3/R2/B2/Spaces in production.

## Why
- Multipart upload is the resumable-upload primitive; we should not reinvent it.
- Presigned URLs let clients upload and workers fetch without proxying bytes
  through the control plane.
- No SDK type crosses the interface, so the implementation is replaceable.

## Consequences
- Vendor differences (checksum algorithms, storage classes, conditional writes)
  are handled in the adapter, and capabilities we cannot rely on everywhere are
  not used in core logic.
- Storage is eventually consistent with the database by construction, so
  reconciliation is a designed feature, not a bug fix (ARCHITECTURE §10).

## Rejected
- **Filesystem/NFS as the durable layer** — no multi-machine story.
- **Direct vendor SDK usage throughout** — lock-in for no gain.
