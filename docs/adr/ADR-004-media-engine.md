# ADR-004 — Media engine: FFmpeg as a subprocess

Status: accepted

## Context
Candidates: FFmpeg CLI subprocess, libav via cgo bindings, GStreamer, custom
C/C++, Rust media crates.

## Decision
FFmpeg (and ffprobe) invoked as subprocesses with explicit argv, from the
worker binary only.

## Why
- **Isolation is the point.** Media parsers are the largest attack surface in
  the system and we run untrusted input through them. A subprocess can be
  sandboxed, resource-limited, network-denied and killed. A cgo library crash
  takes the worker down with it and puts a CVE inside our address space.
- FFmpeg has the codec/container/filter ecosystem we need for H.264, HEVC, AV1,
  AAC, Opus, subtitles, HDR metadata and CMAF packaging. Reimplementing any of
  it is unjustifiable.
- No cgo keeps cross-compilation and static binaries simple.

## Consequences
- Progress and metrics come from parsing `-progress` output — structured
  key/value, not stderr scraping.
- Per-invocation process overhead is negligible against encode durations.
- Version pinning matters: the worker reports its FFmpeg version and build
  configuration as a capability, and the control plane can require a minimum.
- Where FFmpeg's packager proves insufficient (some DRM/CMAF signalling), a
  dedicated packager may be added as another subprocess behind the same task
  interface. Not a rewrite.

## Rejected
- **libav via cgo** — finer control, unacceptable blast radius, cgo cost.
- **GStreamer** — heavier deployment, weaker fit for batch VOD.
- **Custom codec work** — out of scope, permanently.
