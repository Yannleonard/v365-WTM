# UniHV — Unified Multi-Hypervisor & Multi-Orchestrator Console

> One agnostic console above **virtual machines** (KVM/libvirt · Microsoft Hyper-V ·
> Xen/XAPI · VMware ESXi/vSphere) **and** **containers** (Docker · Swarm · Kubernetes).
> No lock-in. Inventory + monitoring unified across hypervisors *and* orchestrators,
> cross-hypervisor V2V migration, RBAC + audit, single-pane VM + container view.

UniHV is built on the **Castor** container-orchestration platform as its socle
(Go backend + React/TS frontend) and extends it with a parallel VM/hypervisor domain,
a unified inventory, and a V2V migration engine. See `docs/adr/ADR-UNIHV-001` and
`docs/DECISIONS.md` for the design rationale.

## Quick start (Docker, one command)

```bash
export CASTOR_SECRET_KEY=$(openssl rand -hex 32)   # 32 bytes / 64 hex chars
docker compose -f deploy/docker-compose.unihv.yml up -d --build
# open http://localhost:8080  → first-run "create admin" (bootstrap) screen
```

This launches the whole stack: the UniHV app (Go API + embedded React UI, single
distroless non-root static binary), **PostgreSQL 15** (unified inventory / tenants /
billing), and **Redis 7** (sessions / cache / task queue). The local Docker socket is
mounted read-only so the container domain is populated from your real engine; four
in-memory **demo hypervisors** populate the VM domain out of the box
(`UNIHV_DEMO_HYPERVISOR=false` to disable).

The single image alone also runs without compose:

```bash
docker run -d -p 8080:8080 \
  -e CASTOR_SECRET_KEY=$(openssl rand -hex 32) \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  unihv:latest
```

## What you get

- **Unified dashboard** — VMs and containers side by side; counts of VMs/running,
  hosts, clusters, containers, hypervisors (`GET /api/v1/inventory`).
- **Virtual Machines** — list across all hypervisors; detail (hardware, disks, NICs,
  snapshots, live metrics, raw inspect); power/snapshot/clone/reconfigure/delete,
  all greyed out per the hypervisor's declared `CapabilityMatrix` (never "click then 405").
- **Clusters** — unified cluster + node-state + placement topology across backends.
- **Migration (V2V)** — cross-hypervisor wizard: pre-flight → export → disk-format
  conversion (VMDK ↔ qcow2 ↔ raw ↔ VHDX via qemu-img) → import → power-on, with live
  progress. ≥2 directions validated (e.g. ESXi→KVM, KVM→Hyper-V).
- **Containers** — the full Castor surface (Docker/Swarm/K8s, logs, exec, stats,
  stacks, Helm, marketplace), unchanged.
- **Security** — RBAC (hierarchical permissions, provider/host scopes), append-only
  audit, AES-256-GCM-sealed credentials, OIDC/LDAP/local + TOTP, CSRF + Origin checks,
  non-root distroless runtime.

## Architecture (two seams, one console)

```
                    ┌──────────────── UniHV API (Go, chi) ────────────────┐
                    │  unified inventory · RBAC · audit · WS · embedded UI │
                    └───────┬───────────────────────────────┬─────────────┘
            HypervisorProvider seam                 Provider seam (Castor)
        (internal/vprovider, 1 contract)        (internal/provider, 1 contract)
        ┌──────┬──────┬──────┬──────┐            ┌────────┬────────┬────────┐
        │ KVM  │HyperV│ Xen  │ ESXi │            │ Docker │ Swarm  │  K8s   │
        └──────┴──────┴──────┴──────┘            └────────┴────────┴────────┘
        each: normalize + CapabilityMatrix + conformance-tested against a simulator
```

Adding a hypervisor = implement `vprovider.HypervisorProvider` and pass the single
conformance suite (`internal/vprovider/conformance`). Adding an orchestrator =
implement `provider.Provider`. Adding a host = register another instance. Symmetric.

## Registering real hypervisors

The demo providers are in-memory simulators. To manage real infrastructure, register
a connection per hypervisor (KVM via libvirt socket, ESXi/vSphere via govmomi, Xen via
XAPI, Hyper-V via WMI on a Windows host). Live transports are isolated behind build
tags (`libvirt_live`, `vsphere_live`, `xen_live`, `windows`) so the default image stays
CGO-free and distroless; see `docs/DECISIONS.md` D-005 and each provider package.

## Development

Go is **not** required on the host — everything builds in Docker.

```bash
# backend build + test (CGO-free, in the Go container)
docker run --rm -v "$PWD":/src -w /src golang:1.25.11-alpine \
  sh -c "CGO_ENABLED=0 go test ./server/..."

# frontend build + test
cd ui && npm ci && npm run build && npm test

# full image (UI vite + Go, distroless)
docker build -t unihv:dev .
```

## Project docs

- `docs/DECISIONS.md` — running decision log (D-001..D-005)
- `docs/adr/ADR-UNIHV-001` — Castor as base · `ADR-UNIHV-002` — HypervisorProvider seam
- `docs/CASTOR-REUSE.md` — what is reused / adapted / new
- `docs/PROGRESS.md` — phase-by-phase status
