# UniHV — Castor reuse map

> Per prompt §3.6 / §4 (Castor Integration Engineer). Castor (`D:\Castor`, Go + React/TS)
> already implements the container domain. This maps what UniHV reuses as-is, adapts, or
> must build new. Every borrowed module keeps its traceability here.
> Castor was COPIED into ./unihv (it IS the base), so "reuse" here = "keep & build on".

## Reused AS-IS (kept, container domain + cross-cutting)

| Castor module | Path | Role in UniHV |
|---|---|---|
| Provider seam (container) | `server/internal/provider/provider.go` | The model for the new VM seam (D-002); container side unchanged |
| Docker provider (full r/w) | `server/internal/provider/docker/` | Container domain Docker support; feeds unified inventory |
| Swarm provider (r/o) | `server/internal/provider/swarm/` | Container domain Swarm support |
| Kubernetes provider (r/o) | `server/internal/provider/kube/` | Container domain K8s support |
| Snapshot cache + pollers | `server/internal/cache/` | Pattern extended to VM snapshots; container side reused |
| RBAC (dot-notation, scopes) | `server/internal/authz/rbac.go` | Extended with VM + tenant permissions |
| Audit log (append-only) | `server/internal/authz/audit.go` | Reused verbatim for VM + migration actions |
| Session/auth (local/OIDC/LDAP/TOTP) | `server/internal/auth/`, `authz/session.go` | Reused verbatim |
| Secrets sealing (AES-256-GCM) | `server/internal/...` (CASTOR_SECRET_KEY) | Reused for hypervisor credentials at rest |
| SQLite store + migrations | `server/internal/store/` | Kept; Postgres backend added alongside (D-003) |
| chi API router + error mapping | `server/internal/api/` | Extended with /vm, /migrate, /inventory routes |
| WebSocket hub | `server/internal/api/` (ws) | Reused for VM events/metrics streaming |
| React design system | `ui/src/components/` | Reused: DataTable, Terminal, LogViewer, StatsChart, CapabilityGate, Modal, badges |
| Front state/query | Zustand + TanStack Query | Reused as-is (matches prompt stack) |
| Docker build (multi-stage, distroless) | `Dockerfile` | Extended for UniHV image |

## ADAPTED (small rework)

| Item | Why |
|---|---|
| Compose parser `server/internal/compose/` | Docker-specific; generalize toward a stack-template notion if needed for unified deploy |
| UI routing `ui/src/routes.tsx` | Add VM/cluster/migration routes; move toward capability-driven view registration |
| Cache `Snapshot` struct | Add VM/host/cluster sections alongside container sections |

## NEW (built by UniHV, no Castor equivalent)

- `internal/vprovider` — HypervisorProvider seam + VM/Host/Cluster/Datastore model
- `internal/vprovider/{kvm,hyperv,xen,esxi}` — the 4 hypervisor providers
- `internal/vprovider/conformance` — single conformance suite + simulators (libvirt test://, vcsim, WMI mock, XAPI mock)
- `internal/inventory` — unified VM + container aggregator
- `internal/migrate` — V2V engine (export/convert VMDK↔qcow2↔VHDX, pre-flight, progress)
- `internal/store/postgres` — Postgres backend for unified inventory + tenants + billing
- `internal/tenant` — multi-tenant isolation layer (extends RBAC scopes)
- `ui/src/views/vm/*`, `ui/src/views/migrate/*`, unified dashboard

## Traceability rule
Any code lifted from a Castor module into a new UniHV package cites the source path in a
top-of-file comment: `// adapted from Castor server/internal/<...> (see CASTOR-REUSE.md)`.
