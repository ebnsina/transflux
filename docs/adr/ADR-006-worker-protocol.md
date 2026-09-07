# ADR-006 — Worker protocol: worker-initiated HTTPS/JSON, versioned, structured

Status: accepted

## Context
Workers may run anywhere: another cloud, a NAT'd LAN, a spot instance, a
developer laptop. They are heterogeneous and disposable. Candidates: gRPC
streaming, control-plane push over HTTP, message broker, worker-pull HTTP.

## Decision
Worker-initiated HTTPS/JSON. Long-poll lease. Heartbeat carries progress and
returns cancellation. `POST /worker/v1/...`, version in the path, N-1
compatibility.

## Why
- **No inbound reachability to workers.** This is what makes the fleet a
  configuration detail rather than a network topology problem, and it is what
  lets a GPU box in another provider join by setting two env vars.
- JSON over HTTP is debuggable with curl, proxyable, and trivially versioned.
  Task specs are small; the bulk data path is object storage, not the protocol.
- **Structured specs, never shell commands.** The worker renders its own argv
  from an allowlisted vocabulary, so a compromised or spoofed control plane
  cannot achieve remote code execution on the fleet.

## Consequences
- Cancellation latency is bounded by the heartbeat interval (~10s). Acceptable.
- Long-polling holds a connection per idle worker; fine into the thousands, and
  the interval is tunable.
- Protocol changes are additive; the worker's `protocol_version` is registered
  and an unsupported worker is asked to drain rather than failed mid-task.

## Rejected
- **gRPC** — better streaming, but adds codegen and proxy friction for a
  protocol that moves kilobytes; revisit if per-frame telemetry ever matters.
- **Broker (Kafka/RabbitMQ/Redis)** — a whole extra system, and it cannot do the
  capability matching that must happen at lease time anyway.
- **Control-plane push** — requires inbound routes to every worker.
