# ADR-UNIHV-002 — HypervisorProvider seam (one VM contract for KVM/Hyper-V/Xen/ESXi)

- **Status:** Accepted
- **Date:** 2026-06-08
- **Deciders:** Solutions Architect (autonomous)
- **Scope:** Prompt §3.1–§3.4. The VM-domain counterpart of ADR-CASTOR-002.

## Context

UniHV must present four hypervisors (KVM/libvirt, Microsoft Hyper-V, Xen/XAPI,
VMware ESXi/vSphere), standalone hosts AND clusters, under one console — coexisting
with the existing container domain. The backend needs a single stable seam so the
API, the unified inventory and the React UI never branch on hypervisor kind.

Two realities (mirroring the container side):
1. **Asymmetric capabilities.** Not every hypervisor supports every op. The UI must
   know — declaratively, before any call — what is supported, and grey out the rest
   (prompt §3.4: no action fails silently due to an absent capability).
2. **Multi-host is the norm.** Each host/connection is just another provider instance
   in a Registry; aggregation is by `ProviderID`. No API/UI rework to add a host.

## Decision

A single Go interface **`HypervisorProvider`** in `internal/vprovider`, implemented by
`vprovider/{kvm,hyperv,xen,esxi}`, mirroring the prompt §3.2 method set
(connect/health/capabilities; inventory; VM lifecycle; snapshots/clones; migration;
cluster/HA; observability).

- A declarative **`CapabilityMatrix`** (uint64 bitset, `Strings()` → wire tokens) the
  API serializes so the UI greys actions out pre-flight.
- Normalized entities — `VM`, `Host`, `Cluster`, `StoragePool`, `Network`, `Snapshot`,
  `Task`, `MetricSample`, `Event` — independent of any hypervisor; engine-native data
  is preserved opaquely in `VMDetail.Raw`.
- Uniform errors: `ErrUnsupported` (405), `ErrNotFound` (404), `ErrConflict` (409),
  `ErrInvalidSpec` (422). A capability that is NOT declared MUST return `ErrUnsupported`.
- A **`Registry`** keyed by `Provider.ID()` (multi-host ready, like the container side).
- A **single conformance suite** (`vprovider/conformance.RunConformance`) that validates
  ANY provider against the contract AND its declared capabilities. This is the
  auto-verifiable DoD for all four providers (prompt §3.3) — no human sign-off.
- A reference **in-memory simulator** (`vprovider/sim`) that implements the whole
  contract, used for zero-hardware CI and to prove the suite + contract are coherent.

## Why a separate seam from the container `Provider` (not one union)

VMs and containers have genuinely different lifecycles and entities. A union interface
would be mostly `ErrUnsupported` on each side. Two seams + one unified inventory/UI on
top keeps each contract honest and small. (D-002.)

## Conformance design

The suite adapts to each provider's `CapabilityMatrix`:
- declared capability → the method must actually work (and return well-formed entities
  whose `ProviderID`/`Kind` match the provider);
- undeclared capability → the method must return `ErrUnsupported`.
This enforces §3.4 by construction. Each real provider supplies a `Factory` wired to its
own simulator (libvirt `test://`, `vcsim`, WMI mock, XAPI mock) and calls
`RunConformance`. Passing at 100% is the gate.

## Consequences

- One seam: API/inventory/UI never branch on hypervisor kind.
- Capabilities are declarative and pre-flight → correct greying, never "click then 405".
- Adding a hypervisor = implement `HypervisorProvider` + pass conformance. Adding a host
  = register another instance. Symmetric with the container domain.
- Trade-off: the normalized `VM` hides engine-specific richness; mitigated by `Raw`.
- Status: contract + matrix + registry + conformance + sim are implemented and the suite
  passes (full-caps and read-only-caps). Gate met for Phase 1's contract deliverable.
