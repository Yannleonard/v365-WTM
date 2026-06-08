# UniHV — VM / Hypervisor Feature-Gap Analysis

> Product Architect backlog. Rigorous, segment-by-segment comparison of UniHV's VM/hypervisor
> management against VMware vCenter, Proxmox VE, Nutanix Prism, and XCP-ng / Xen Orchestra (XO).
> This drives the next build waves. **Status reflects what is actually in the code today**
> (not the contract's aspiration). Effort: S = days, M = 1–2 sprints, L = multi-sprint / cross-cutting.

## What UniHV already has (baseline — do NOT re-scope these)

Verified from the code:

- **Provider seam** (`vprovider`): hypervisor-agnostic `HypervisorProvider` with a declarative
  `CapabilityMatrix` (pre-flight greying, never "click then 405"), `ErrUnsupported`/`ErrNotFound`/
  `ErrConflict`/`ErrInvalidSpec` sentinels, normalized entities, a task model, and a conformance suite.
  Four families: KVM/libvirt (live, pure-Go RPC), Hyper-V, Xen/XAPI, VMware/vSphere.
- **Lifecycle**: create, power (start/stop/reset/suspend/resume), delete, reconfigure
  (**vCPUs + memory only**), snapshot create/list/revert, clone (full + linked), intra-hypervisor
  migrate (live/cold), export for V2V.
- **Optional interfaces** (`extended.go`): `ConsoleProvider` (VNC/SPICE/RDP endpoint), `NetworkWriter`
  (create/delete virtual networks), `StorageProvider` (list/create/delete volumes, upload ISO),
  `DeviceManager` **hot-plug** (live attach/detach disk + NIC, mount/eject ISO; q35 + pcie-root-ports
  pre-provisioned so hot-plug works).
- **KVM live backend**: real libvirt — domain define/start, set vCPU/mem (config+live), snapshots,
  network define, storage volume create + ISO stream-upload, `qemu-img` disk export, migrate.
- **V2V engine** (`migrate`): cross-hypervisor export → convert (qemu-img) → import, with progress.
- **DR** (`replication`): scheduled V2V cycles, snapshot-first, measured-RPO tracking, degraded flag,
  cycle ring-buffer, one-click failover (power on replica). **No failback, no CBT/incremental.**
- **Storage backends** (`storage`): NFS / iSCSI / SMB (as libvirt pools) + Azure Blob + S3, each with
  a connectivity `Test`. **Connectivity-probe only — not yet a VM disk/backup target.**
- **FinOps** (`finops`): per-VM + per-container cost (CPU/RAM/storage breakdown), rate card, rightsizing.
- **Insights** (`insights`): cross-domain rules feed (resilience/reclaim/housekeeping/health).
- **Observability**: per-VM metric series (CPU/mem/net/disk), event stream, audit (`AuditWrap`).
- **Security**: RBAC at provider scope, AAL/step-up on mutations, 2FA (TOTP), SSO/OIDC.
- **Console**: noVNC via guacd websocket bridge (VNC/RDP).
- **UI**: VMs list, VM detail (overview/snapshots/metrics/console/inspect tabs), VM create wizard
  (4 steps), networks, storage, clusters, replication, finops, insights, hypervisor connections,
  storage backends, migration. A `Drawer` component **exists** but VM action UIs use **Modals**.

---

## 1. VM Lifecycle & Hardware

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Lifecycle/HW | CPU topology (sockets/cores/threads) in spec + UI | missing (renderDomainXML emits flat `<vcpu>` only) | P0 | vCenter, Proxmox, Prism all expose it | M |
| Lifecycle/HW | NUMA topology / vNUMA | missing | P1 | vCenter (vNUMA), Proxmox | L |
| Lifecycle/HW | CPU pinning (cputune/affinity) | missing | P1 | Proxmox, vCenter (latency-sensitive) | M |
| Lifecycle/HW | Hugepages backing | missing | P2 | Proxmox, vCenter | M |
| Lifecycle/HW | Memory ballooning toggle / min-max | missing (single `<memory>`=`<currentMemory>`) | P1 | Proxmox, vCenter, XO | M |
| Lifecycle/HW | CPU model / host-passthrough selection | missing (no `<cpu>` element) | P0 | all (perf + live-migration compat) | M |
| Lifecycle/HW | vTPM 2.0 | missing (no `<tpm>`) | P0 | Win11/secure workloads — vCenter, Proxmox, Prism | M |
| Lifecycle/HW | Secure Boot (UEFI + SB keys) | partial (UEFI firmware yes; no SB enrollment/loader_secure) | P0 | Win11 — vCenter, Proxmox | M |
| Lifecycle/HW | GPU / PCI passthrough (`<hostdev>`) | missing | P0 | vCenter (DirectPath/vGPU), Proxmox, Prism | L |
| Lifecycle/HW | USB device redirect / passthrough | missing | P2 | Proxmox, vCenter | M |
| Lifecycle/HW | Serial / text console (`<serial>`/`<console>`) | missing (no serial device; only graphical) | P1 | Proxmox (xterm.js), XO, vCenter | M |
| Lifecycle/HW | Guest-agent channel (`org.qemu.guest_agent`) + integration | missing (not in create XML; no IP/FS/shutdown via agent) | P0 | qemu-ga everywhere; VMware Tools; Prism | M |
| Lifecycle/HW | Boot device order UI (`<boot order>` per device) | partial (only BootISO forces order=1; no UI, no per-disk/NIC order) | P1 | all | S |
| Lifecycle/HW | VM templates (mark VM as template, deploy-from-template) | missing (no template concept; clone exists but no template lifecycle) | P0 | vCenter, Proxmox, Prism, XO | M |
| Lifecycle/HW | Cloud-init / sysprep guest customization | missing (no nocloud ISO, no Windows sysprep) | P0 | Proxmox, vCenter (customization specs), XO | M |
| Lifecycle/HW | Reconfigure: change firmware/topology/devices live or offline | partial (vCPU+mem only) | P0 | all | M |
| Lifecycle/HW | RAM/CPU hot-add as explicit capability (not just config set) | partial (config+live set, no guest-side guarantee/UI) | P1 | vCenter, Proxmox | S |
| Lifecycle/HW | Watchdog device / auto-restart | missing | P2 | Proxmox, vCenter | S |
| Lifecycle/HW | Disk bus selection (virtio/scsi/sata/nvme) | missing (hardcoded virtio in create + hot-add) | P1 | Proxmox, vCenter | S |

## 2. Storage

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Storage | Thin vs thick provisioning choice | missing (qcow2 default; no allocation policy) | P1 | vCenter (thin/thick/EZT), Proxmox, Prism | M |
| Storage | Online (running-VM) disk resize / grow | missing (no resize op in contract or KVM) | P0 | all | M |
| Storage | Storage migration / live storage vMotion | partial (MigrateOptions.TargetStorage exists; KVM migrate ignores it) | P0 | vCenter (svMotion), Proxmox move-disk, XO | L |
| Storage | Datastore / volume browser (files in a pool) | partial (ListVolumes per pool; no tree/file browse) | P1 | vCenter datastore browser, Proxmox | M |
| Storage | Multipath (iSCSI/FC) | missing | P2 | vCenter, Prism | L |
| Storage | Disk QoS / IOPS + bandwidth limits (`<iotune>`) | missing | P1 | vCenter (SIOC), Proxmox, Prism | M |
| Storage | TRIM / discard (`discard='unmap'`) | missing | P1 | Proxmox, vCenter | S |
| Storage | Shared storage awareness for live-migrate | partial (pools list HostIDs; no shared-vs-local gating) | P1 | all | M |
| Storage | Snapshot tree visualization + branch merge/delete | partial (flat list w/ ParentID field; KVM lists name only, no delete-single, no merge UI) | P0 | vCenter snapshot manager, Proxmox, XO delta tree | M |
| Storage | Delete a single snapshot (not just revert) | missing (no DeleteSnapshot op) | P0 | all | S |
| Storage | Storage backends as real VM disk / backup targets | partial (backends only Test connectivity today) | P1 | n/a (UniHV differentiator) | M |

## 3. Networking

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Networking | Distributed virtual switch (cross-host) | missing (per-host libvirt nets only) | P1 | vCenter vDS, Proxmox SDN, Prism | L |
| Networking | VLAN trunking / tagged port groups on NIC | partial (NetworkSpec.VLAN at net level; no per-NIC trunk/tag) | P1 | vCenter, Proxmox | M |
| Networking | SR-IOV VF assignment | missing | P2 | vCenter, Prism | L |
| Networking | NIC bonding / teaming (host uplinks) | missing | P2 | vCenter, Proxmox | M |
| Networking | Network QoS / rate limit per NIC (`<bandwidth>`) | missing | P1 | vCenter NIOC, Proxmox, XO | M |
| Networking | Firewall / security groups per VM/NIC | missing | P0 | Proxmox firewall, vCenter NSX, Prism Flow | L |
| Networking | SDN overlay (VXLAN/Geneve) | missing | P2 | Proxmox SDN, NSX, Prism | L |
| Networking | IPAM (IP pool mgmt, assignment, reservation) | missing | P1 | vCenter (IP pools), Prism, XO | M |
| Networking | Port mirroring / SPAN | missing | P2 | vCenter vDS, Proxmox | M |
| Networking | Explicit NIC model UI (virtio/e1000/vmxnet3) | partial (model field in spec; no UI selector) | P2 | all | S |

## 4. Console & Guest

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Console | noVNC graphical console | have (guacd bridge) | — | — | — |
| Console | SPICE console (audio/USB/multi-monitor) | partial (endpoint Kind reports spice; no SPICE web client) | P1 | Proxmox, RHV | M |
| Console | Serial / text console in browser | missing (no serial device, no xterm bridge) | P1 | Proxmox, XO, vCenter | M |
| Console | Console copy/paste (clipboard sync) | missing | P2 | vCenter, Proxmox | M |
| Console | File upload into guest (via agent) | missing | P2 | VMware Tools, qemu-ga write-file | M |
| Guest | Guest-agent graceful shutdown/restart | partial (PowerStop tries shutdown then destroy; no agent ack) | P1 | all | S |
| Guest | Guest IP / hostname / FS report via agent | partial (VM.IPAddresses field exists; KVM does not populate from agent) | P0 | all | M |
| Guest | VM screenshot (DomainScreenshot) | missing | P2 | vCenter, Proxmox, XO | S |
| Guest | Multi-user / shared console session | missing | P2 | vCenter | M |
| Guest | Agent presence / version surfaced in UI | missing | P1 | all | S |

## 5. Backup / Snapshot / DR

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Backup | Scheduled snapshots with retention/pruning | missing (snapshots are manual; replication scheduler exists but is V2V, not snapshot retention) | P0 | Proxmox, vCenter (via SDDC), Prism, XO | M |
| Backup | Application-consistent quiescing | partial (SnapshotOptions.Quiesce flag exists; KVM does not wire qemu-ga fsfreeze) | P0 | vCenter (VSS), Proxmox QGA, Prism | M |
| Backup | Incremental / CBT (changed-block tracking) backups | missing (export is full qcow convert each time) | P0 | vCenter CBT, Proxmox dirty-bitmap, XO CBT | L |
| Backup | Backup to cloud storage backends (S3/Azure) | partial (backends exist; not wired as backup destination for VM disks) | P1 | XO (S3), Veeam, Prism | M |
| Backup | Restore granularity (full VM / single disk / file-level) | missing (no VM restore path; container-volume restore only) | P0 | Veeam, vCenter, XO file-level | L |
| Backup | VM-level backup job (separate from V2V) | missing | P0 | Proxmox vzdump, XO, Veeam | M |
| DR | Replication w/ measured RPO + degraded flag | have | — | — | — |
| DR | One-click failover | have (power on replica) | — | — | — |
| DR | Failback (reverse replication after failover) | missing | P1 | vCenter SRM, Zerto, XO CR | M |
| DR | Scheduled + RPO dashboards (fleet view) | partial (per-policy State w/ RPO; no fleet dashboard widget) | P1 | SRM, Zerto | S |
| DR | Test failover (sandbox, non-disruptive) | missing | P2 | SRM, Zerto | M |

## 6. Clusters & Scheduling

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Cluster | HA auto-restart on host failure | missing (Cluster.HAEnabled is read-only flag; no restart orchestration) | P0 | vCenter HA, Proxmox HA, Prism | L |
| Cluster | DRS-style load balancing | missing (Cluster.DRSEnabled read-only; no balancer) | P1 | vCenter DRS, Prism ADS | L |
| Cluster | Affinity / anti-affinity rules | missing | P1 | vCenter, Proxmox HA groups, Prism | M |
| Cluster | Maintenance mode + auto-evacuation | partial (NodeMaintenance state exists; no enter-maintenance op or evacuation) | P0 | all | M |
| Cluster | Resource pools / quotas (cluster compute carving) | missing | P1 | vCenter resource pools, Prism | M |
| Cluster | Live-migration orchestration (batch / rolling) | partial (single-VM migrate; no batch/rolling-host evacuate) | P1 | vCenter, Proxmox bulk-migrate | M |
| Cluster | Capacity-aware placement (where-to-put recommender) | missing | P1 | vCenter DRS initial placement, Prism | M |
| Cluster | Cluster topology read | have (GetClusterTopology, NodeState) | — | — | — |

## 7. Observability

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Observability | Per-VM metric history | have | — | — | — |
| Observability | Alerting / thresholds (rules + notify) | missing (Insights is rules feed, not threshold alerts w/ notification) | P0 | vCenter alarms, Prism, Proxmox+ext | M |
| Observability | Events timeline per VM/host | partial (StreamEvents exists; no persisted, queryable timeline UI) | P1 | all | M |
| Observability | Performance charts (multi-metric, host roll-up) | partial (single CPU/mem chart; no host aggregate, no custom range UI) | P1 | vCenter perf charts, Prism | M |
| Observability | Topology / relationship map | missing | P2 | vCenter maps, Prism, XO | L |
| Observability | Audit log | have (AuditWrap) | — | — | — |
| Observability | Notification channels (email/Slack/webhook) | missing | P1 | all (or via integration) | M |

## 8. Security / RBAC / Multi-tenant

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Security | RBAC (scoped, AAL step-up) | have | — | — | — |
| Security | 2FA / SSO | have (TOTP, OIDC) | — | — | — |
| Security | Per-tenant resource quotas (CPU/RAM/storage caps) | missing | P0 | vCenter (via vCloud), Prism, OpenStack | M |
| Security | Project / folder organization hierarchy | missing (flat provider scope only) | P1 | vCenter folders, Prism projects | M |
| Security | Tag-based policy / dynamic groups | partial (Labels exist for filter; no policy engine on tags) | P1 | vCenter tags+policy, Prism categories | M |
| Security | Encryption at rest for VM disks (LUKS/`<encryption>`) | missing | P1 | vCenter VM encryption, Proxmox, Prism | L |
| Security | Secrets vault integration (KMS/Vault) | missing (sealed secrets for backends only) | P2 | vCenter KMS, Prism | M |
| Security | Per-tenant network isolation guarantees | missing | P1 | NSX, Prism Flow | L |

## 9. Automation / API / IaC

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| Automation | REST API | have | — | — | — |
| Automation | Terraform provider | missing | P0 | vSphere, Proxmox, Nutanix providers | M |
| Automation | Ansible modules / collection | missing | P1 | community.vmware, community.general (proxmox), nutanix.ncp | M |
| Automation | Webhooks (event → external) | missing | P1 | vCenter (via vRO), Prism | M |
| Automation | Scheduled tasks (cron-style ops) | partial (replication scheduler only; no general task scheduler) | P1 | vCenter scheduled tasks, Proxmox | M |
| Automation | Bulk operations (multi-VM power/migrate/tag) | missing (single-entity endpoints; no batch API) | P0 | all | M |
| Automation | Tags + saved searches | partial (Labels filter in ListOptions; no saved searches) | P1 | vCenter, Prism | S |
| Automation | API tokens (non-interactive auth) | missing (session-based; no PAT/service account tokens) | P0 | all | M |

## 10. UX / Operability

| Segment | Feature | Status | Priority | Competitor ref | Effort |
|---|---|---|---|---|---|
| UX | Right-side action drawers (owner request) | missing for VMs (Drawer.tsx exists; VM actions use Modals) | P0 | XO, modern Prism | S |
| UX | Rich edit-settings panel (all hardware, not just vCPU/mem) | missing (reconfigure = vCPU+mem) | P0 | vCenter Edit Settings, Proxmox | M |
| UX | CD/DVD media icons + inline mount/eject affordance | partial (hot-plug mount/eject API exists; no CD/DVD icon UI) | P1 | all | S |
| UX | Bulk select + act in list | missing | P0 | all | M |
| UX | Global search (cross-provider) | missing | P1 | vCenter, Prism | M |
| UX | Dark / light theme | partial (tokens.css present; no toggle verified) | P2 | all | S |
| UX | Keyboard shortcuts | missing | P2 | XO, vCenter | M |
| UX | Guided VM-create wizard improvements (topology, cloud-init, template step) | partial (4-step wizard; lacks topology/cloud-init/template) | P0 | Proxmox wizard, vCenter, XO | M |
| UX | Per-segment dashboards (capacity, cost, DR health) | partial (FinOps + Insights pages; no consolidated ops dashboard) | P1 | vCenter, Prism | M |

---

## Top 15 to Build Next (ordered by impact ÷ effort)

Ranked for maximum best-in-class movement per unit of effort. Items chosen because they unblock
real-world VM workloads (Windows 11, GPU, automation) and convert existing half-built capability
into shipped value.

| # | Feature | Segment | Pri | Effort | Why now |
|---|---|---|---|---|---|
| 1 | **Right-side action drawers + rich Edit-Settings** | UX | P0 | S→M | Owner's explicit ask; `Drawer.tsx` already exists. Becomes the home for every new hardware knob below. |
| 2 | **Delete-single-snapshot + snapshot tree UI** | Storage | P0 | S→M | One missing op + a tree view; today snapshots only revert. High daily-ops value, low cost. |
| 3 | **Guest-agent integration (channel + IP/FS report + graceful shutdown ack)** | Guest | P0 | M | Field already exists (`VM.IPAddresses`); wire qemu-ga. Unblocks quiescing, IP display, clean shutdown. |
| 4 | **Online disk resize (grow)** | Storage | P0 | M | Single new contract op + libvirt `blockResize`. Universally expected, currently absent. |
| 5 | **vTPM + Secure Boot enrollment** | Lifecycle/HW | P0 | M | Hard requirement for Windows 11 guests; q35 base already in place. |
| 6 | **CPU topology + CPU model selection** | Lifecycle/HW | P0 | M | Flat `<vcpu>` today hurts perf + live-migration compat; add `<cpu>` + sockets/cores/threads to spec + drawer. |
| 7 | **Cloud-init / sysprep customization** | Lifecycle/HW | P0 | M | Turns clone-from-template into real provisioning. Table-stakes vs Proxmox/vCenter/XO. |
| 8 | **VM templates (mark-as-template + deploy-from-template)** | Lifecycle/HW | P0 | M | Pairs with #7; clone plumbing exists, add template lifecycle + a wizard step. |
| 9 | **API tokens + bulk operations API** | Automation | P0 | M | Prereq for Terraform/Ansible/CI; bulk power/migrate/tag is a frequent ask. |
| 10 | **Serial/text console in browser** | Console | P1 | M | Add `<serial>` device + xterm bridge (guacd path already proven). Critical for headless Linux. |
| 11 | **Scheduled VM backups w/ retention (app-consistent via #3)** | Backup | P0 | M | Reuse replication scheduler + snapshot + storage backends as targets; biggest data-protection gap. |
| 12 | **Disk QoS (IOPS/BW) + TRIM/discard** | Storage | P1 | S→M | Small `<iotune>`/`discard` XML additions surfaced in the new Edit-Settings drawer. |
| 13 | **GPU / PCI passthrough** | Lifecycle/HW | P0 | L | High-value workloads (AI/VDI). Larger, but a marquee differentiator and frequently demanded. |
| 14 | **Maintenance mode + host evacuation (rolling live-migrate)** | Cluster | P0 | M | `NodeMaintenance` state exists; add enter-maintenance op + batch migrate. Foundation for HA/DRS. |
| 15 | **Terraform provider** | Automation | P0 | M | Built on #9 tokens + bulk API; unlocks IaC adoption and enterprise procurement. |

### Strategic notes

- **Sequence**: #1 (drawers) is the UX substrate; #3 (guest agent) and #2/#4 (snapshot/disk ops)
  are cheap wins that immediately lift the daily-driver experience. #5–#8 close the "can I actually
  run modern Windows + provision at scale" gap. #9 → #15 unlock automation/IaC.
- **Leverage already-built assets**: storage backends (S3/Azure/SAN) are connectivity-only today —
  #11 turns them into backup targets, a near-free differentiator. Replication scheduler is reusable
  for backup scheduling and for #11/failback.
- **Biggest pure-greenfield gaps** (defer unless demanded): distributed switch / SDN overlay,
  HA/DRS automation, multipath, SR-IOV, encryption at rest, per-tenant quotas/isolation. These are
  L-effort and where the established players' moats are deepest; prioritize by concrete deal pressure.
