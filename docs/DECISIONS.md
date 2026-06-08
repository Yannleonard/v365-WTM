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
