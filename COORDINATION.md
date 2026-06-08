# UniHV — Coordination document (for fresh agents joining the work)

> **Purpose:** onboard new contributors/agents fast. What UniHV is, what's REAL vs not,
> exactly where mocks/fake data still hide, how to build/test/deploy, and the rules.
> **Owner's #1 rule (non-negotiable): NO mocks, no fabricated data, no false-success in
> the production path.** A real hypervisor API or a clear error — never invented numbers.

Last updated: 2026-06-08. Repo: `Yannleonard/v365-WTM` (branch `main`, ~50 commits).
Working dir: `C:\Users\yleon\.vscode\v360-leonard\unihv`. Never modify `D:\Castor` (the base Castor app this was forked from).

---

## 1. What UniHV is

A unified, vendor-agnostic console over BOTH virtual machines (Hyper-V, KVM, ESXi, Xen —
standalone hosts AND clusters) AND containers (Docker, Swarm, K8s). Built ON TOP of the
existing **Castor** app (Go backend + React/TS frontend). Differentiators vs competitors:
unified inventory/monitoring cross-hypervisor + cross-orchestrator, V2V migration, RBAC
multi-tenant, single-pane VM+container view, FinOps, DR replication. UX target: **vSphere
clarity** in **Castor's graphic identity**.

## 2. Architecture (where things live)

- `server/` — Go backend (single binary `castor`). `cmd/castor/main.go` = wiring.
- `server/internal/vprovider/` — the VM provider seam. **`vprovider.go`/`types.go` = FROZEN
  core contract (22 ops) — do NOT change.** `extended.go` = optional interfaces (Console,
  NetworkWriter, StorageProvider, DeviceManager hot-plug, GuestAgent, SnapshotManager,
  DiskResizer, TemplateManager, MaintenanceProvider, ResourceController, DiskQoSManager,
  StorageMigrator, metricsBackend, eventBackend). `capability.go` = CapabilityMatrix bits.
- `server/internal/vprovider/kvm/` — **THE ONLY ACTIVE REAL BACKEND.** Live libvirt over
  go-libvirt RPC (`live_libvirt.go`, `live_metrics.go`, `cloudinit.go`, `sysprep.go`,
  `resource_qos.go`, `kvm_maintenance.go`). `sim_backend.go` = in-memory fake for TESTS ONLY.
- `server/internal/vprovider/{esxi,hyperv,xen}/` — real clients exist (govmomi / WMI-COM /
  XAPI) but are **NOT exercised** in the current deployment (no real ESXi/Xen/Hyper-V host
  wired). Their sim backends are test-only.
- `server/internal/{migrate,replication,storage,finops,insights,alarms,backup}/` — engines.
- `server/internal/api/` — REST + WS handlers + router. `server/internal/store/` — Postgres
  (migrations in `store/migrations/`). `server/internal/authz/` — auth/RBAC/sessions/tokens.
- `ui/` — React/TS (Vite). Key: `views/VirtualMachineDetail.tsx`, `views/useVMActions.tsx`,
  `components/{VMActions,Drawer,InventoryTree,RecentTasksBar,ConsolePanel,icons}.tsx`,
  `lib/{api,hooks,types,rbac}.ts`, `styles/{tokens,global}.css`.

## 3. How to build / test / deploy (Docker-only; no Go/Node assumed on host)

**Go build + test (in Docker, both OS + tests):**
```
MSYS_NO_PATHCONV=1 docker run --rm -v "/c/Users/yleon/.vscode/v360-leonard/unihv":/src \
  -w /src -v unihv-gomod:/go/pkg/mod golang:1.25.11-alpine sh -c \
  "CGO_ENABLED=0 go build ./server/... && CGO_ENABLED=0 GOOS=windows go build ./server/... \
   && CGO_ENABLED=0 go vet ./server/... && CGO_ENABLED=0 go test ./server/..."
```
**UI build + test:** `cd ui && npm run build && npm test` (must stay green; ~26 tests).

**Rebuild + redeploy the local container** (Docker Desktop):
```
docker build -t unihv:latest --build-arg VERSION=<tag> .
docker rm -f unihv
export CASTOR_SECRET_KEY=$(cat /tmp/unihv_secret.txt)
docker compose -f deploy/docker-compose.unihv.yml up -d
# wait for http://localhost:8080/api/v1/healthz == 200
```
App: **http://localhost:8080**, login `admin` / `Admin1234567`. The app runs in a container;
**real libvirt runs in WSL** and the app reaches it via `tcp://host.docker.internal:16509`
(a persisted hypervisor connection named "WSL KVM"). guacd runs as `unihv-guacd`.

**Real libvirt access (for proofs):** `wsl -d Ubuntu -u root -- bash -lc "virsh -c qemu:///system <cmd>"`.
Real demo VMs: `web-server-01`, `linux-server`, `db-server-01`, `Alpine`, `windows-11`
(real libvirt domains with real disks; Alpine boots to login).

**Probe (real-hardware proofs):** `server/cmd/kvmprobe` — `wsl ... go run ./server/cmd/kvmprobe tcp://127.0.0.1:16509`.

**CRITICAL gotcha:** in Git-Bash, ALWAYS prefix `docker run` with `MSYS_NO_PATHCONV=1` or the
`-w /work/...` path gets mangled to `C:/Program Files/Git/...` and the container fails to start.

**Browser E2E:** Playwright headless in Docker (`mcr.microsoft.com/playwright:v1.49.0-noble`,
`--add-host=host.docker.internal:host-gateway`, `BASE=http://host.docker.internal:8080`).
Scripts in `test/e2e/*.mjs`, screenshots to `test/e2e/shots/`. Note: a static VNC console
shows black until a keypress forces a redraw — that's normal VNC, not a bug.

## 4. What is REAL and verified (don't redo)

- Lifecycle/power (real DomainCreate/Shutdown/etc), create-from-scratch, snapshot/clone,
  intra-hv migrate, hot-add disk/NIC/ISO (q35 + 14 PCIe root-ports), online disk resize.
- vTPM 2.0 (swtpm), Secure Boot (secboot OVMF + MS keys + SMM), cloud-init (NoCloud seed ISO),
  CPU topology, templates, Windows sysprep — all proven in `kvmprobe` on real libvirt.
- **Metrics: REAL** (`live_metrics.go`) — DomainGetInfo/MemoryStats/Interface/BlockStats;
  **a stopped VM returns ZERO/empty samples** (fixed the "36% on a powered-off VM" bug).
- Integrated console: real VNC via guacd (`ConsolePanel.tsx` — fixed the `?undefined` WS-URL
  bug that hung on "Opening interactive console").
- KVM disk **export** for FILE-backed qcow2 disks is real (qemu-img + libvirt StorageVolDownload
  RPC so it works from the container) → backup/V2V of file-backed VMs is real.
- API tokens (bearer, sealed), bulk ops, maintenance mode, resource control/QoS/thin-thick/TRIM,
  storage migration (DomainBlockCopy), RBAC, 2FA/TOTP, OIDC/LDAP SSO, audit.
- UI: named action bar + Actions menu, inventory tree, 7 VM tabs, Recent Tasks/Alarms bar,
  FinOps/Insights/Alarms/Backups/Replication/StorageBackends views (real data wiring confirmed).

## 5. ⚠️ KNOWN GAPS / not-real-yet (these are HONEST errors, NOT silent mocks anymore)

These now **return a clear error** instead of fabricating data (fixed), but the real
implementation is still TODO. **This is where fresh agents should focus.**

| Area | File | Current behavior | What's needed (real impl) |
|---|---|---|---|
| **ESXi disk export** | `esxi/esxi.go:503` | live path → `ErrUnsupported` "not yet implemented" | Real OVF/VMDK export via HttpNfcLease (govmomi) |
| **Xen disk export** | `xen/xen.go:535` | live path → `ErrUnsupported` | Real XAPI export HTTP handler |
| **Hyper-V disk export** | `hyperv/provider.go:521` | live path → `ErrUnsupported` | Real Export-VM / VHDX streaming (WMI) |
| **KVM export, non-file disks** | `kvm/kvm.go:742` | block/RBD/iSCSI `<source dev/protocol>` → `ErrUnsupported` | Support non-file disk export (NBD/qemu-img over block) |
| **Alarm email channel** | `alarms/alarms.go:116` | `email-stub` = logs only (UI labels it "stub/logged") | Real SMTP sender (webhook channel IS real HTTP POST) |
| **FinOps container default alloc** | `finops/engine.go:189` | assumes 1 vCPU / 0.5GB for containers with no declared limits (documented assumption, not presented as measured) | Source from real cgroup limits where available |
| **ESXi/Xen/Hyper-V live ops generally** | those packages | many ops only verified against API simulators, not a real host | Validate against a real ESXi/XCP-ng/Hyper-V host |

**The placeholder export streams (`KVMEXPORT`/`VSPHEREEXPORT`/`XENEXPORT`/`HYPERVEXPORT`) still
exist in the code but are now GATED to the sim/in-memory backend (tests only)** — they can no
longer reach a live operation. If you touch export, keep that gate (`if _, live := p.backend.(*liveBackend); live { return ErrUnsupported }`).

## 6. Rules for contributors (enforce strictly)

1. **No fabricated/hardcoded/random user-facing data on the live path.** Real API value or a
   clear error. A powered-off/unavailable resource shows 0 / "no data", never invented numbers.
2. **No false success.** If an op can't really do the thing, return an error; downstream
   (backup/migrate/replication) must record FAILED, not completed.
3. Sim/in-memory backends are for **conformance/unit tests only** — never registered in the
   running server (`cmd/castor/main.go` registers only live providers from persisted connections).
4. Every new provider op adds a `CapabilityMatrix` bit so the UI greys it out pre-flight
   (never "click-then-405/500"). Use the optional-interface + capability-bit pattern.
5. Follow the EXISTING Castor design system in UI (reuse components/tokens/CSS classes/icons).
   No new visual language, no new deps without reason.
6. Validate EVERY change: Go build (linux+windows) + `go test ./server/...` + conformance green,
   UI build + tests green, and **prove on real libvirt** (kvmprobe or virsh) for backend
   behavior, **browser** for UI. Don't claim "done" on endpoint-200 alone — test the real effect.
7. Commit per logical change with a descriptive message; push to `main`.
8. Distroless/container caveats: the app container can't see the WSL filesystem — anything
   touching VM disk bytes must go over libvirt RPC, not local file paths.

## 7. How to help right now (suggested next work, by priority)

1. **Re-audit for any remaining live-path mock** beyond §5 (the owner reports "mock partout" —
   trust that and keep hunting: grep live code for constants/ramps/`rand`/placeholder feeding
   user-facing values; cross-check each UI card/chart against its real data source).
2. Implement real **ESXi/Xen/Hyper-V disk export** (unblocks cross-hv backup/V2V for them).
3. Real **SMTP** for alarm email channel.
4. Validate ESXi/Xen/Hyper-V live ops against a real host (currently simulator-verified only).
5. Console UX: confirm the integrated console renders reliably across VMs (VNC redraw timing).
