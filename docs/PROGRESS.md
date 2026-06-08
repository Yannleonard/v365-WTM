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
| 3 | Platform: unified inventory + aggregated API + monitoring + multi-tenant RBAC | ⬜ | API tested, tenant isolation, unified metrics |
| 4 | V2V migration engine (cross-hypervisor) | ⬜ | 2 directions validated on test disks |
| 5 | Frontend: VM/cluster/migration views + unified VM+container dashboard | ⬜ | Playwright e2e green |
| 6 | Hardening, docs, security audit, Claude Chrome validation | ⬜ | global DoD §7 |

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
