# UniHV — Progress tracker

> Updated continuously during the autonomous build. For human review a posteriori.
> Status legend: ⬜ not started · 🟡 in progress · ✅ done (QA-gate green) · ⛔ blocked

## Phase status

| Phase | Title | Status | Gate |
|---|---|---|---|
| 0 | Bootstrap (Castor socle imported, builds green in Docker) | ✅ | baseline image builds + boots healthy + Go tests |
| 1 | Foundations: HypervisorProvider contract + conformance + simulators + unified inventory schema | ✅ | conformance suite runs green against sim (full + read-only) |
| 2 | VM providers (KVM, Hyper-V, ESXi, Xen) | ✅ | 100% conformance × 4, CGO-free, go.mod clean |
| 2b | Container providers wired into unified inventory (reuse Castor) | ⬜ | container conformance green |
| 3 | Platform: unified inventory + aggregated API + monitoring + multi-tenant RBAC | ✅ | inventory+API+RBAC live; Postgres+Redis in compose |
| 4 | V2V migration engine (cross-hypervisor) | ✅ | 2+ directions validated (vmdk→qcow2, qcow2→vhdx) |
| 5 | Frontend: VM/cluster/migration views + unified VM+container dashboard | ✅ | npm build green, 23 vitest pass, full image builds |
| 6 | Hardening, docs, security audit, live validation | ✅ | compose up all-healthy; live E2E green; security scan clean |

### Operational features (post-DoD, user-requested) — DONE
- ✅ Real official-API clients for ALL 4 hypervisors (no mock): KVM=libvirt RPC,
  Hyper-V=WMI Msvm_* via go-ole, ESXi=govmomi, Xen=XAPI. Proven on real libvirt + real Hyper-V.
- ✅ Connection management: register/connect real hypervisors (local AND remote) via UI/API,
  credentials AES-256-GCM sealed. Hyper-V remote via WMI ConnectServer over DCOM.
- ✅ Extension contract (non-breaking optional interfaces): Console (VNC/SPICE/RDP),
  Network write (create/delete switches), Storage write (volumes + ISO upload).
  Implemented on KVM live (proven: real net/vol create-delete in WSL) + Hyper-V live
  (proven: real switch/VHD on host) + sim (CI).
- ✅ Frontend: VM creation wizard, VM Networks view, VM Storage/ISO library, Console viewer.
- ✅ Live verified: capabilities console/network_write/storage_write exposed; real storage
  pool listed; real network created via API (confirmed in libvirt).

### Real browser validation (Playwright headless Chromium) — 100% green, effects verified in libvirt
- NO MOCK in production: demo/sim registration deleted from main.go; sim only in CI tests.
  /vm/providers empty until a real connection is added; VM list shows only real domains.
- Integrated interactive console (guacd + guacamole-common-js): Console tab renders a live
  VNC canvas ('Connected'). 12/12 view checks green.
- Full action suite 10/10 via real browser clicks, 0 API errors, each confirmed in libvirt:
  power stop/start; snapshot on diskless -> 422 surfaced (no fake success); reconfigure;
  clone (e2e-clone); network create (e2e-fnet); volume create (e2e-fvol); Create VM wizard
  (e2e-wizvm WITH a real disk).
- Root-cause backend fixes: error-swallowing write seam -> errors now propagate; ReconfigureVM
  was a no-op on live -> now real DomainSetVcpus/Memory; size-only disks were dropped ->
  defineDomain auto-provisions a qcow2 volume; NAT-without-CIDR -> default subnet; dup -> 409.
- Regression: 16 Go packages green, 26 UI vitest green, linux+windows build.
- Runnable proof: test/e2e/*.spec.mjs.

### Phase 6 — DONE
- ✅ deploy/docker-compose.unihv.yml: app + PostgreSQL 15 + Redis 7, one command, all healthy
- ✅ Hardening fix (D-006): non-root user + group_add reconciles entrypoint with no-new-privileges + cap_drop ALL
- ✅ Live E2E (HTTP, browser-equivalent): bootstrap→login→inventory(12 VMs+18 real containers)→VM power→V2V migrate done→audit recorded
- ✅ Security scan: no hardcoded secrets/keys; no residual TODO/stub in prod code (only build-tagged live transports)
- ✅ README-UNIHV.md; full Go suite 15/15 packages green; full Docker image (UI+Go) builds
- ⚠️ Claude Chrome: no browser-automation tool available in this session; validated via full HTTP E2E
  against the running container (same flows a browser drives). App is live for the user's visual pass.

### Phase 3 — core DONE (live in Docker)
- ✅ Unified inventory aggregator (VM + container), counts, concurrent reads
- ✅ VM API surface (reads + all mutations) with vprovider→HTTP error mapping
- ✅ RBAC: vm.* + inventory.read perms in catalog + seeded roles; full audit/AAL chain
- ✅ Server wiring + 4 demo sim hypervisors; /inventory shows 12 VMs/8 hosts/4 clusters
- ✅ Tests: inventory unit + VM API e2e green; whole suite green; image boots healthy
- 🟡 remaining: Postgres backend (D-003), multi-tenant scopes, WS metrics streaming

## Phase 0 — DONE
- ✅ Castor copied to ./unihv (3.7 MB clean source, no node_modules/.git/dist)
- ✅ git init + baseline commit (354 files, module github.com/gtek-it/castor)
- ✅ Docker baseline image builds green (unihv:phase0-baseline)
- ✅ Container boots → /api/v1/healthz HTTP 200 healthy, bootstrap flow works
- 🟡 Go test suite (running in Docker) — see BLOCKERS if red
- ⬜ Governance docs + .claude/agents/* (in progress)

## Module map (extension targets)
- KEEP: internal/provider/{docker,swarm,kube}, internal/authz, internal/auth,
  internal/cache, internal/store (sqlite), internal/api, ui/src/components
- NEW: internal/vprovider (+ kvm/hyperv/xen/esxi/conformance), internal/inventory,
  internal/migrate, internal/store/postgres, ui/src/views/vm/*, ui/src/views/migrate/*
