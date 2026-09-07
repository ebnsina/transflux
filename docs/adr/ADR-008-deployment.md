# ADR-008 — Deployment: Docker Compose first, orchestrator-agnostic binaries

Status: accepted

## Context
Initial scale is one control plane, one Postgres, one bucket, and a handful of
workers — some on other machines, some with GPUs. The system must reach a
multi-machine worker fleet without a rewrite.

## Decision
Docker Compose for the control plane stack (proxy, control plane, Postgres,
MinIO). Remote workers run the same image under systemd or plain Docker,
configured with a control-plane URL and a bootstrap token. No orchestrator API
is ever called from application code.

## Why
- Compose covers stage one and two (one machine → several worker machines,
  including GPU and ARM hosts) with no scheduler to operate.
- Worker registration is self-service: the control plane learns capabilities
  from the worker, so scaling out changes no configuration and no schema.
- Because deployment is not in the domain model, moving to Nomad or Kubernetes
  later is a packaging change, not a rewrite.

## Consequences
- No automatic worker autoscaling initially. Adding a worker is a manual, cheap
  action. Autoscaling is a P3 concern and needs cost data we do not have.
- Control-plane HA is a later concern; a restart is safe because all state is in
  Postgres, and workers reconnect.

## Rejected
- **Kubernetes now** — an orchestrator, a networking model and a control plane
  to operate before we have earned any of them. Being distributed is not by
  itself a reason.
- **Nomad** — closer fit and genuinely simpler than k8s; still an extra system
  we do not yet need. The most likely next step if manual worker management
  becomes the bottleneck.
- **PaaS (Coolify/Dokploy/Kamal)** — poor fit for GPU and specialised hosts.
