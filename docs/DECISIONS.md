# UniHV — Decision log (ADR-style)

> Autonomous build. Every non-trivial technical ambiguity is decided here, not asked.
> Format per entry: Context / Decision / Alternatives rejected / Consequences.
> Founding ADRs live in `docs/adr/`. This file is the running, lightweight log.

---

## D-001 — Castor as the UniHV base (socle), not a rewrite

- **Context.** The orchestration prompt mandates building UniHV (unified VM + container
  management console). It imposes a Node/TS stack but ALSO issues an imperative directive:
  "reuse rather than rewrite", and explicitly authorizes Go per provider. Inventory of
  `D:\Castor` showed a production-grade Go + React/TS platform that ALREADY implements the
  container domain (Docker/Swarm/K8s providers), a provider-abstraction seam identical in
  spirit to the prompt's `OrchestratorProvider`, plus RBAC, audit, secrets (AES-256-GCM),
  OIDC/LDAP/2FA, an in-memory snapshot cache, and a single-pane React UI.
- **Decision.** Adopt Castor as the foundation. Keep its Go backend and React/TS frontend.
  EXTEND it with the VM/hypervisor domain, a unified inventory, and a V2V engine. Copy
  Castor into `./unihv` (D:\Castor is never modified).
- **Alternatives rejected.**
  - *Rewrite everything in Node/TS (strict prompt stack).* Throws away ~70% working, tested
    code; weeks of effort to reach parity; contradicts the prompt's own "reuse" directive.
  - *Polyglot (Node gateway delegating to Castor Go binary).* Adds an inter-process seam and
    a second language runtime for no functional gain; the Go seam already exists.
- **Consequences.** Backend stays Go (module `github.com/gtek-it/castor`, kept to avoid a
  noisy mechanical rename; UniHV is the product name, the Go module path is an internal
  identifier). The imposed Node/TS-everywhere constraint is consciously not met on the
  backend; this is the single largest deviation and is justified by the reuse mandate.
  Frontend already React 18 + TS + Vite + Zustand + TanStack Query (matches the prompt).

## D-002 — Two parallel provider contracts, one console

- **Context.** Castor's `provider.Provider` is container-only (`Workload` = container/task/pod).
  The VM domain needs different entities (Host/VM/Cluster/Datastore/Snapshot/Migration).
- **Decision.** Add a parallel `internal/vprovider.HypervisorProvider` seam mirroring the
  prompt §3.2, built with the SAME patterns Castor already proved: a `Capability` bitset
  (CapabilityMatrix), `ErrUnsupported` for unsupported ops, a `Registry`, and a normalized
  `VM` struct (mirroring the normalized `Workload`). Container `Provider` is left untouched.
- **Alternatives rejected.** *One mega-interface for VMs and containers.* The two domains
  have genuinely different lifecycles; a union interface would be mostly `ErrUnsupported`.
- **Consequences.** Two seams, one unified inventory/UI layer on top. Adding a hypervisor =
  implement `HypervisorProvider`; adding an orchestrator = implement `Provider`. Symmetric.

## D-003 — Persistence: keep SQLite, add Postgres for the unified multi-domain inventory

- **Context.** Castor uses pure-Go SQLite (modernc) — great for self-host single-binary.
  The prompt requires Postgres for unified inventory + RBAC + audit + billing across both
  domains and 500+ tenants. SQLite does not fit multi-tenant 500+ concurrent.
- **Decision.** Keep SQLite as the default embedded store for the existing auth/RBAC/audit
  (zero-config self-host path preserved). Introduce a `store` backend abstraction with a
  PostgreSQL 15 implementation for the unified inventory + tenants + billing, selected by
  config (`UNIHV_DB_DRIVER=postgres|sqlite`). Time-series metrics: Postgres + a lightweight
  rollup table in V1 (TimescaleDB extension optional, deferred — see future ADR if needed).
- **Alternatives rejected.** *Postgres-only, drop SQLite.* Kills the single-binary self-host
  story that is one of Castor's strengths and the easiest demo/dev path.
- **Consequences.** Dual-backend store layer. Docker-compose for UniHV ships a Postgres
  service; the standalone single-binary path still works on SQLite.

## D-004 — Build & run strictly in Docker; Go absent from host

- **Context.** Host has Node/npm/pnpm/Docker/Git but NO Go toolchain. The user requires the
  app to run in a Docker container (Docker Desktop already running, v28.3.2).
- **Decision.** All Go compilation and Go tests run inside Docker (golang:1.25.11-alpine,
  matching Castor's Dockerfile). The app is delivered and validated as a container image.
  No host Go install.
- **Alternatives rejected.** *Install Go on host.* Unnecessary; contradicts the container
  mandate; the multi-stage Dockerfile already compiles Go internally.
- **Consequences.** Slightly slower iterative Go builds (container overhead) but fully
  reproducible and aligned with the deployment target.

## D-005 — VM providers: pure-Go default build, real-SDK paths behind build tags

- **Context.** The 4 hypervisor SDKs have very different runtime constraints:
  - VMware: `govmomi` is pure Go; `vcsim` simulator runs in-process in CI. ✅ clean.
  - Xen: XAPI is an HTTP/XML-RPC API; a pure-Go client + a mock server run in CI. ✅ clean.
  - KVM: the common libvirt Go binding (`libvirt.org/go/libvirt`) needs cgo + libvirt
    system libraries; the `test://` driver needs libvirtd. This breaks Castor's locked
    CGO_ENABLED=0 distroless build. An alternative pure-Go path exists: talk to libvirt
    over its RPC socket (`digitalocean/go-libvirt`, pure Go, no cgo).
  - Hyper-V: WMI/CIM is Windows-only; it cannot execute inside a Linux container at all.
- **Decision.** Every provider normalizes hypervisor-native data into the contract via a
  pure-Go, dependency-light core that is unit/conformance-tested against a **simulator or
  recorded fixtures** in the default (CGO-free, Linux) build. Live-SDK transport code that
  cannot run CGO-free/cross-platform is isolated behind Go build tags
  (`//go:build libvirt_cgo`, `//go:build windows`) so the default image stays distroless
  and CI stays hardware-free. KVM uses the pure-Go `go-libvirt` socket client by default
  (no cgo). The conformance suite (§3.3) is the acceptance gate for all four.
- **Alternatives rejected.**
  - *Require cgo + libvirt libs + libvirtd in the runtime image.* Breaks distroless,
    multi-arch, and the single-static-binary story; needs privileged CI.
  - *Skip Hyper-V because it's Windows-only.* The contract + normalization + conformance
    (against a WMI fixture/mock) are implementable and testable cross-platform; only the
    live WMI transport is `//go:build windows`. The provider is real; its live transport
    is OS-gated, which is correct and honest.
- **Consequences.** Default build & CI: CGO-free, Linux, hardware-free, all four providers
  pass conformance against simulators/fixtures. Production against a live hypervisor uses
  the same normalization with the tagged transport compiled in (KVM live = pure-Go socket,
  no special build). Each provider documents how to exercise it against the real backend.

## D-007 — Providers use each hypervisor's OFFICIAL API; no mocks in the production path

- **Context.** The user requires real hypervisor management (standalone hosts AND clusters),
  with NO mock in the production path. Ref: Microsoft Virtualization API
  (learn.microsoft.com/virtualization/api/). Each hypervisor exposes an official API.
- **Decision.** Every provider's LIVE backend talks to the hypervisor's official API:
  - **Hyper-V** → WMI namespace `root\virtualization\v2` (`Msvm_*` classes) accessed DIRECTLY
    via COM from Go using `github.com/go-ole/go-ole` (the official management surface; this is
    what the Hyper-V PowerShell cmdlets wrap). Manages standalone host AND Failover Cluster
    (`MSCluster_*`). Build tag `//go:build windows`.
  - **KVM** → libvirt RPC API via the pure-Go `github.com/digitalocean/go-libvirt`.
  - **VMware ESXi/vSphere** → vSphere SOAP/REST API via `github.com/vmware/govmomi`.
  - **Xen** → XAPI XML-RPC/JSON-RPC over HTTP.
  Simulators remain ONLY for the CI conformance suite (no hardware); never the real path.
- **CGO note (refines D-005).** go-ole/COM may require cgo. We accept that the **Windows /
  Hyper-V** build target may be cgo-enabled (Hyper-V is Windows-only regardless). The default
  Linux image (KVM/ESXi/Xen + the whole container domain) stays CGO-free and distroless.
- **Consequences.** Real, verifiable management against actual hypervisor APIs. The Hyper-V
  provider is compiled for/run on Windows; the Linux server registers KVM/ESXi/Xen live
  providers. Conformance (sim) still gates correctness in hardware-free CI.

## D-006 — Hardened compose runs the app as non-root directly (no in-container privilege drop)

- **Context.** The inherited entrypoint starts as root (USER 0) and re-execs itself as
  uid 65532 with the docker-socket group (the gosu pattern) so it can read a root-owned
  socket without `--group-add`. Under the hardened compose (`no-new-privileges:true` +
  `cap_drop: ALL`), the kernel forbids that credential-changing re-exec →
  `fork/exec /proc/self/exe: operation not permitted`, crash-looping the app. Caught by
  the live `docker compose up` validation gate.
- **Decision.** In `deploy/docker-compose.unihv.yml` run the container directly as
  `user: "65532:65532"` and grant socket access via `group_add` (the host docker.sock
  group; 0 on Docker Desktop). The entrypoint then takes its already-non-root path and
  runs the server in-process — no re-exec — which is compatible with `no-new-privileges`
  + `cap_drop: ALL`. The named `/data` volume is image-seeded as 65532, so SQLite writes.
- **Alternatives rejected.** *Drop `no-new-privileges`/keep CAP_SETUID+SETGID.* Weakens
  the hardening for no benefit when running as the target user directly works.
- **Consequences.** Stack comes up healthy with full hardening; the server reaches the
  Docker daemon and shows real containers next to the demo VMs. The plain `docker run`
  path (root entrypoint + in-process drop) still works for users who prefer it.
