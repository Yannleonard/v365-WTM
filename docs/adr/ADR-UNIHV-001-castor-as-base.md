# ADR-UNIHV-001 — Castor as the UniHV base; add a parallel VM domain

- **Status:** Accepted
- **Date:** 2026-06-08
- **Deciders:** Tech Lead Orchestrator (autonomous) + Solutions Architect
- **Scope:** Whole-project foundation. Supersedes the "Node/TS everywhere" stack constraint
  on the backend, per the prompt's overriding "reuse rather than rewrite" directive.

## Context

UniHV's value is a single agnostic console above BOTH virtual machines (Hyper-V, Xen, KVM,
ESXi) AND containers (Docker, Swarm, Kubernetes). The orchestration prompt imposes a Node/TS
backend but issues an imperative directive to reuse the existing internal app **Castor**
(`D:\Castor`) for the container domain, and explicitly authorizes Go per provider.

A thorough inventory of Castor revealed it is not a fragment to scavenge but a near-complete,
production-grade implementation of the container domain plus the hard cross-cutting concerns:

- A provider-abstraction seam (`Provider` interface + declarative `Capability` bitset +
  `ReadOnlyMutations` + `Registry`) that is, conceptually, exactly the prompt's
  `OrchestratorProvider` with a pre-flight capability matrix.
- Working Docker (full r/w), Swarm (r/o), Kubernetes (r/o) providers.
- RBAC (hierarchical dot-notation permissions, global/host scopes), append-only audit log,
  local/OIDC/LDAP auth, TOTP 2FA with AES-256-GCM-sealed secrets, signed sessions with AAL.
- An in-memory snapshot cache fed by pollers + event watcher (the read path the API/UI use).
- A React 18 + TS + Vite + Zustand + TanStack Query UI with a reusable design system
  (DataTable, xterm Terminal, LogViewer, Recharts StatsChart, CapabilityGate, dialogs,
  status badges) and a unified Workloads view.
- A multi-stage Dockerfile producing a pure-Go, CGO-free, distroless, non-root static image.

## Decision

**Adopt Castor as the UniHV foundation.** Copy it into `./unihv` (D:\Castor untouched), keep
its Go backend and React/TS frontend, and EXTEND it:

1. Add a parallel `HypervisorProvider` seam (`internal/vprovider`) mirroring the prompt §3.2,
   reusing Castor's proven patterns (Capability matrix, ErrUnsupported, Registry, normalized
   entity). Container `Provider` stays untouched. (See ADR-UNIHV-002.)
2. Add a unified inventory aggregator over both seams, a V2V migration engine, a multi-tenant
   layer, and a Postgres store backend alongside the existing SQLite (D-003).
3. Reuse RBAC/audit/auth/secrets/cache/UI as-is, extending where the VM domain requires.

The Go module path stays `github.com/gtek-it/castor` (internal identifier; renaming 350+ files
buys nothing and risks breakage). "UniHV" is the product name.

## Alternatives rejected

- **Strict Node/TS rewrite.** Discards ~70% of working, tested code and the hardest
  cross-cutting concerns; weeks to reach parity; directly contradicts the prompt's reuse
  directive. The prompt's stack constraint and reuse directive conflict; the reuse directive
  is the explicit "impérative" one and wins.
- **Polyglot gateway (Node front of a Castor Go service).** Adds an IPC seam and a second
  runtime for zero functional benefit; the Go seam already exists in-process.

## Consequences

- **Positive:** Container domain + RBAC/audit/auth/secrets/cache/UI/Docker-build are
  effectively done on day one. Effort concentrates where the real new value is: the 4 VM
  providers, the unified inventory, and the V2V engine.
- **Trade-off:** Backend is Go, not Node/TS — the single conscious deviation from the imposed
  stack, justified above and logged as D-001.
- **Neutral:** Frontend already matches the prompt's React/TS/Vite/Zustand/TanStack stack.
