# UniHV — vSphere Functional-Parity Matrix

> Senior Virtualization Product Architect deliverable. The owner's goal: **"strictly the same
> functionalities as vSphere"** in UniHV (the unified multi-hypervisor manager). This document maps
> every vSphere VM / host / cluster / storage / network capability to UniHV's **current, verified
> code state** and defines exactly what is needed to reach parity.
>
> **Method.** Each row is grounded in the actual source, not the contract's aspiration. Verified files:
> `vprovider/{vprovider,types,extended,capability}.go` (the seam + optional interfaces),
> `vprovider/kvm/{live_libvirt,kvm,libvirt,cloudinit}.go` (the only REAL live backend; the reference
> implementation for "Have"), `internal/{migrate,replication,storage,finops,insights}/`, and
> `api/vm_routes.go` (the exposed REST surface). It refines `docs/VM-FEATURE-GAPS.md` specifically for
> **vSphere** parity (the prior doc benchmarked against vCenter + Proxmox + Prism + XO).
>
> **Legend.**
> - **Status**: `Have` (shipped + wired end-to-end in the live KVM backend) · `Partial` (contract/field
>   or half-wired; not usable end-to-end) · `Missing` (no code).
> - **Priority**: `P0` (must-have to *feel* like vSphere / unblocks real workloads) · `P1` (expected by
>   serious admins) · `P2` (advanced / niche).
> - **Effort**: `S` = days · `M` = 1–2 sprints · `L` = multi-sprint / cross-cutting.
> - **Mechanism**: the concrete libvirt (KVM, the live backend) / govmomi (vSphere passthrough) / WMI
>   (Hyper-V) lever to implement it. UniHV is hypervisor-agnostic, so "parity" = the normalized
>   contract op + at least the KVM live implementation; native vSphere providers map the same op through
>   govmomi.

---

## 0. Verified baseline — what UniHV genuinely has today (do NOT re-scope)

From the code, confirmed end-to-end in the **live libvirt backend** (`LiveCaps = FullCaps | CapConsole |
CapNetworkWrite | CapStorageWrite | CapHotPlug`):

- **Seam & safety**: `HypervisorProvider` + declarative `CapabilityMatrix` (pre-flight greying, never
  "click-then-405"), `ErrUnsupported/NotFound/Conflict/InvalidSpec` sentinels mapped to HTTP, task model,
  conformance suite. Four families (KVM live, Hyper-V, Xen/XAPI, VMware/vSphere).
- **Lifecycle**: create (`DomainDefineXML`+`DomainCreate`), power start/stop/reset/suspend/resume, delete,
  reconfigure (**vCPU + memory only**), clone (full + linked), intra-hyp migrate (live/cold via
  `DomainMigratePerform3Params`), export for V2V (`qemu-img convert -U`).
- **Security/firmware (DONE, verified in `renderDomainXML`)**: **vTPM 2.0** (`<tpm model='tpm-crb'>` +
  swtpm emulator backend), **Secure Boot** (pinned secboot OVMF `<loader secure='yes'>` + per-VM
  MS-keys NVRAM template + `<smm state='on'/>`), q35 machine type always (PCIe hot-plug ready).
- **Guest customization (DONE)**: **cloud-init NoCloud** seed ISO (`cloudinit.go`: user-data/meta-data/
  network-config, xorriso-built `cidata` cdrom). **Windows sysprep = TODO.**
- **Hot-plug (DONE, `DeviceManager`)**: live attach/detach disk + NIC, mount/eject ISO (LIVE|CONFIG flags),
  14 pre-provisioned pcie-root-ports.
- **Optional interfaces**: `ConsoleProvider` (VNC endpoint from `<graphics>`, one-shot ticket),
  `NetworkWriter` (define/destroy libvirt nets), `StorageProvider` (list/create/delete vol, stream ISO upload).
- **V2V** (`migrate`): export→convert→hardware-map→import across any provider pair.
- **DR** (`replication`): scheduled V2V cycles, snapshot-first, measured-RPO, degraded flag, ring-buffer
  history, **one-click failover** (power on replica). **No failback, no CBT.**
- **Storage backends** (`storage`): NFS/iSCSI/SMB (libvirt pools) + Azure Blob + S3 — **connectivity
  `Test()` only**, not yet a VM disk/backup target.
- **FinOps** / **Insights** (cross-domain cost + rules feed). **Observability**: per-VM metric series,
  event stream, audit (`AuditWrap`). **Security**: RBAC (scoped + AAL step-up), TOTP 2FA, OIDC SSO.
- **REST API** (`vm_routes.go`): full per-VM CRUD + power/snapshot/clone/migrate/console/ws + hot-plug
  disks/NICs/ISO + per-provider networks + storage volumes/ISO upload.

---

## 1. VM lifecycle & hardware

| # | Capability (vSphere term) | Status | Pri | Effort | Mechanism to reach parity |
|---|---|---|---|---|---|
| 1.1 | Power ops (on/off/reset/suspend/resume) | **Have** | — | — | libvirt `DomainCreate/Shutdown/Destroy/Reset/Suspend/Resume`; govmomi `VirtualMachine.PowerOn…` |
| 1.2 | Create VM from scratch | **Have** | — | — | `DomainDefineXML`; govmomi `Folder.CreateVM_Task` |
| 1.3 | **VM templates (mark-as-template + deploy-from-template)** | **Missing** | P0 | M | Add `MarkAsTemplate`/`DeployFromTemplate` ops; KVM = a flagged read-only domain + full/linked clone (clone plumbing exists); govmomi `MarkAsTemplate`/`CloneVM_Task` |
| 1.4 | Clone (full + linked) | **Have** | — | — | KVM clone (full/linked) wired; govmomi `CloneVM_Task` w/ `DiskMoveType` |
| 1.5 | **Guest customization — cloud-init (Linux)** | **Have** | — | — | `cloudinit.go` NoCloud seed ISO (hostname/user/keys/runcmd/netcfg) |
| 1.6 | **Guest customization — sysprep (Windows)** | **Missing** | P0 | M | Generate `Autounattend.xml` / cloudbase-init seed cdrom for Windows guests; govmomi `CustomizationSpec` (Sysprep) |
| 1.7 | **CPU topology (sockets/cores/threads)** | **Missing** (flat `<vcpu>` only) | P0 | M | Emit `<cpu><topology sockets/cores/threads/></cpu>`; spec `VMSpec.CPUTopology`; govmomi `numCoresPerSocket` |
| 1.8 | CPU model / host-passthrough selection | **Missing** (no `<cpu>`) | P0 | M | `<cpu mode='host-passthrough'\|'host-model'\|custom>`; needed for perf + live-migrate EVC compat |
| 1.9 | CPU hot-add | **Partial** (live `DomainSetVcpusFlags`, no guest guarantee/UI/cap) | P1 | S | Surface explicit `CapCPUHotAdd`; require pre-set maximum vCPUs; govmomi `cpuHotAddEnabled` |
| 1.10 | Memory hot-add | **Partial** (live `DomainSetMemoryFlags`, same caveats) | P1 | S | Pre-set `<maxMemory slots=…>`; `CapMemHotAdd`; govmomi `memoryHotAddEnabled` |
| 1.11 | **Memory reservation / limit / shares** | **Missing** | P1 | M | libvirt `<memtune>` (hard_limit/soft_limit) + `<cputune><shares>`; govmomi `ResourceAllocationInfo` (reservation/limit/shares) — core vSphere resource-control primitive |
| 1.12 | CPU reservation / limit / shares | **Missing** | P1 | M | `<cputune><shares/period/quota>`; pairs with 1.11 for resource-pool semantics |
| 1.13 | NUMA / vNUMA topology | **Missing** | P1 | L | `<cpu><numa>` + `<numatune>`; govmomi `numa.vcpu.maxPerVirtualNode` |
| 1.14 | CPU pinning / affinity | **Missing** | P2 | M | `<cputune><vcpupin>`; govmomi `LatencySensitivity` + affinity |
| 1.15 | **vTPM 2.0** | **Have** | — | — | `<tpm model='tpm-crb'><backend type='emulator' version='2.0'/>` |
| 1.16 | **Secure Boot** | **Have** | — | — | secboot OVMF `<loader secure='yes'>` + MS-keys NVRAM + `<smm on>` |
| 1.17 | **GPU / PCI passthrough (DirectPath / vGPU)** | **Missing** | P0 | L | `<hostdev mode='subsystem' type='pci'>` (+ mediated `<hostdev type='mdev'>` for vGPU); host IOMMU prep; govmomi `VirtualPCIPassthrough` |
| 1.18 | USB device passthrough / redirect | **Missing** | P2 | M | `<hostdev type='usb'>` / `<redirdev>`; govmomi `VirtualUSB` |
| 1.19 | Serial console (text, browser xterm) | **Missing** (only `<console type='pty'>`, no `<serial>` + no xterm bridge) | P1 | M | Add `<serial type='tcp'>`/`<console>` + xterm.js over the existing guacd/ws bridge; govmomi serial-over-network |
| 1.20 | Boot options (per-device order, firmware, boot delay) | **Partial** (only BootISO forces `order=1`; no UI/per-device) | P1 | S | Emit `<boot order=N>` per disk/NIC + `<bootmenu>`; govmomi `BootOptions` |
| 1.21 | **VMware-Tools-equivalent (qemu guest agent)** | **Missing** (no `<channel org.qemu.guest_agent>`; `VM.IPAddresses` never populated) | P0 | M | Add the virtio guest-agent channel to `renderDomainXML` + `DomainQemuAgentCommand` for IP/FS/clean-shutdown; govmomi VMware Tools status/heartbeat |
| 1.22 | Disk bus selection (virtio/SCSI/SATA/NVMe) | **Missing** (virtio hardcoded) | P1 | S | Parametrize `<target bus=…>`; needed for Windows installs (virtio needs drivers); govmomi controller type |
| 1.23 | NIC model selection (virtio/e1000/vmxnet3) | **Partial** (model field exists, no UI/validation) | P2 | S | Already in `NICSpec.Model`; expose selector + validate |
| 1.24 | Video / display adapter choice | **Partial** (hardcoded `cirrus`) | P2 | S | Parametrize `<video><model>` (qxl/virtio/vga); cirrus is legacy |
| 1.25 | Watchdog / auto-restart device | **Missing** | P2 | S | `<watchdog model='i6300esb' action='reset'>` |

## 2. Storage

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 2.1 | Thin vs thick (lazy/eager-zeroed) provisioning | **Missing** (qcow2 default, no policy) | P1 | M | qcow2=thin / raw `preallocation=full`=thick in `renderVolumeXML`; govmomi `thinProvisioned`/`eagerlyScrub` |
| 2.2 | **Online disk resize (grow, running VM)** | **Missing** (no resize op in contract or KVM) | P0 | M | New `ResizeDisk` op → libvirt `DomainBlockResize` (live) + `StorageVolResize`; govmomi reconfigure disk capacity |
| 2.3 | **Storage migration (live Storage vMotion)** | **Partial** (`MigrateOptions.TargetStorage` exists; KVM migrate ignores it) | P0 | L | libvirt `DomainBlockCopy`/`virsh blockcopy` or migrate with disk relocation; govmomi `RelocateVM_Task` (datastore change) |
| 2.4 | Datastore browser (file tree in a pool) | **Partial** (`ListVolumes` flat per pool; no tree) | P1 | M | `StoragePoolListAllVolumes` + a path-tree view; govmomi `HostDatastoreBrowser` |
| 2.5 | Disk QoS — IOPS / bandwidth limits (SIOC) | **Missing** | P1 | M | `<iotune><total_iops_sec/total_bytes_sec>`; govmomi `StorageIOAllocationInfo` |
| 2.6 | TRIM / discard | **Missing** | P1 | S | `<driver discard='unmap'>` on disks; reclaims thin space |
| 2.7 | Multi-disk VMs | **Have** | — | — | create/hot-add already iterate `Disks[]` |
| 2.8 | Shared-datastore awareness (gate live-migrate) | **Partial** (pools list `HostIDs`; no shared-vs-local gating) | P1 | M | Compare pool `HostIDs` set before allowing same-storage live-migrate; govmomi datastore host-mounts |
| 2.9 | Multipath (iSCSI/FC) | **Missing** | P2 | L | Host-side dm-multipath + libvirt iscsi-direct pool; govmomi PSA/SATP |
| 2.10 | **Snapshot tree (parent/child) + delete-single + consolidate** | **Partial** (`Snapshot.ParentID` field exists but KVM `listSnapshots` returns name only; no `DeleteSnapshot`, no consolidate) | P0 | M | Populate parent from `DomainListAllSnapshots`/snapshot XML; add `DeleteSnapshot` (`DomainSnapshotDelete`); consolidate via `blockcommit`; govmomi `RemoveSnapshot_Task`/`ConsolidateVMDisks_Task` |
| 2.11 | Storage backends as real VM disk/backup targets | **Partial** (backends only `Test()` connectivity) | P1 | M | Mount SAN/NAS pool → provision VM disks; stream backups to S3/Azure (UniHV differentiator) |

## 3. Networking

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 3.1 | Standard switch + port group | **Have** (per-host libvirt net via `NetworkWriter`) | — | — | `NetworkDefineXML` bridge/nat/isolated; govmomi `HostNetworkSystem` vSwitch/portgroup |
| 3.2 | **Distributed virtual switch (vDS, cross-host)** | **Missing** (per-host nets only) | P1 | L | OVS + controller, or libvirt+SDN overlay; govmomi `DistributedVirtualSwitch` — a deep vSphere moat |
| 3.3 | VLAN tagging (port-group level) | **Have** (`NetworkSpec.VLAN`) | — | — | `<vlan><tag id=N>` in net XML; govmomi portgroup `VlanId` |
| 3.4 | VLAN trunk / per-NIC tagging | **Partial** (net-level only) | P1 | M | `<vlan trunk='yes'>` + per-NIC `<vlan>`; govmomi `VmwareDistributedVirtualSwitchTrunkVlanSpec` |
| 3.5 | NIC teaming / uplink bonding | **Missing** | P2 | M | Host bond + bridge; govmomi `NicTeamingPolicy` (load-balance/failover) |
| 3.6 | SR-IOV VF assignment | **Missing** | P2 | L | `<interface type='hostdev'>` to a VF; host SR-IOV enable; govmomi `VirtualSriovEthernetCard` |
| 3.7 | Traffic shaping / QoS per NIC (NIOC) | **Missing** | P1 | M | `<bandwidth><inbound/outbound>` on `<interface>`; govmomi `DVSTrafficShapingPolicy` |
| 3.8 | Distributed firewall / security groups (NSX) | **Missing** | P0 | L | nftables/ebtables `<filterref>` (libvirt nwfilter) per NIC; govmomi/NSX DFW — a large greenfield |
| 3.9 | Port mirroring / SPAN | **Missing** | P2 | M | OVS mirror; govmomi vDS port-mirroring session |
| 3.10 | IPAM (IP pools / reservation) | **Missing** | P1 | M | DHCP ranges in net XML + reservation store; govmomi IP pools |

## 4. Snapshots / backup / DR

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 4.1 | Snapshot manager (tree view) | **Partial** (see 2.10) | P0 | M | parent/child from snapshot XML; tree UI; govmomi `snapshot.rootSnapshotList` |
| 4.2 | Snapshot create / revert | **Have** | — | — | `DomainSnapshotCreateXML` / `DomainRevertToSnapshot` |
| 4.3 | Delete single snapshot / consolidate | **Missing** | P0 | S | `DomainSnapshotDelete`; `blockcommit` to consolidate; govmomi `RemoveSnapshot_Task` |
| 4.4 | Scheduled snapshots + retention/pruning | **Missing** (snapshots are manual; replication scheduler is V2V) | P0 | M | Reuse the `replication` scheduler loop → periodic `Snapshot` + prune by `Retain`; govmomi scheduled task |
| 4.5 | **App-consistent quiescing (guest agent)** | **Partial** (`SnapshotOptions.Quiesce` flag exists; KVM does NOT call qemu-ga fsfreeze) | P0 | M | Depends on 1.21: `DomainFSFreeze`/`DomainFSThaw` (or `guest-fsfreeze` agent cmd) around snapshot; govmomi VSS quiesce |
| 4.6 | Incremental / CBT (changed-block tracking) | **Missing** (export is full convert each pass) | P0 | L | qcow2 dirty-bitmaps + `DomainBackupBegin` (incremental backup API); govmomi CBT `QueryChangedDiskAreas` |
| 4.7 | VM-level backup job (separate from V2V) | **Missing** | P0 | M | Backup engine = snapshot + export disk → storage backend (reuse `migrate` export + `storage` S3/Azure) |
| 4.8 | Backup to cloud/SAN backends | **Partial** (backends `Test()`-only) | P1 | M | Wire S3/Azure/SAN as backup destinations (pairs with 4.7 / 2.11) |
| 4.9 | Restore (full VM / single disk / file-level) | **Missing** (no VM restore path) | P0 | L | Import backed-up disk → `CreateVM`; file-level via guest-agent mount; govmomi restore |
| 4.10 | Replication w/ measured RPO + degraded flag | **Have** | — | — | `replication` engine |
| 4.11 | One-click failover | **Have** (power on replica) | — | — | `Failover` powers on the target replica |
| 4.12 | **Failback (reverse replication)** | **Missing** | P1 | M | Reverse the policy direction post-failover (swap source/target); vSphere SRM reprotect |
| 4.13 | Test failover (sandbox, non-disruptive) | **Missing** | P2 | M | Clone replica into isolated net + power on; SRM test recovery |

## 5. Clusters / scheduling

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 5.1 | Cluster topology read | **Have** (`GetClusterTopology`, `NodeState`) | — | — | — |
| 5.2 | **HA (auto-restart on host failure)** | **Missing** (`Cluster.HAEnabled` is a read-only flag; no orchestration) | P0 | L | Host-failure detector + restart orchestrator (re-`CreateVM`/start on surviving host w/ shared storage); govmomi `ClusterDasConfigInfo` (vSphere HA) |
| 5.3 | **DRS (load balancing)** | **Missing** (`Cluster.DRSEnabled` read-only) | P1 | L | Balancer reading `GetMetrics` → live-migrate to even load; govmomi `ClusterDrsConfigInfo` + `PlaceVm` |
| 5.4 | Affinity / anti-affinity rules | **Missing** | P1 | M | Placement constraint store consulted by 5.3/5.5; govmomi `ClusterAffinityRuleSpec` |
| 5.5 | Resource pools (compute carving + reservation/limit/shares) | **Missing** | P1 | M | Builds on 1.11/1.12; cgroup hierarchy per pool; govmomi `ResourcePool` tree |
| 5.6 | **Maintenance mode + evacuation** | **Partial** (`NodeMaintenance` state exists; no enter-maintenance op, no auto-evacuate) | P0 | M | `EnterMaintenance` op → batch live-migrate all VMs off the host (reuse `MigrateVM` in a loop); govmomi `EnterMaintenanceMode_Task` |
| 5.7 | EVC (cross-CPU-gen migration compat) | **Missing** | P1 | M | Depends on 1.8: pin a baseline `<cpu mode='custom'>` feature mask cluster-wide; govmomi `EVCMode` |
| 5.8 | Capacity-aware initial placement | **Missing** | P1 | M | Recommender over host free CPU/RAM at create time; govmomi DRS `PlaceVm` |
| 5.9 | Batch / rolling live-migration | **Partial** (single-VM migrate; no batch) | P1 | M | Orchestrate `MigrateVM` across a host set (foundation for 5.6) |

## 6. Host management

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 6.1 | Host summary (CPU/mem/VM count/version) | **Have** (`ListHosts` → `Host` w/ cores/MHz/mem/used/vmCount/version) | — | — | `NodeGetInfo` + `ConnectGetHostname`/`GetLibVersion` |
| 6.2 | Host health / state | **Partial** (`NodeState` up/down/maintenance/degraded; no sensor detail) | P1 | M | Host sensors (IPMI/`nodedev`); govmomi `HostSystem.runtime.healthSystemRuntime` |
| 6.3 | Hardware status (fans/PSU/temp/disks) | **Missing** | P2 | M | IPMI/redfish or `lm-sensors`; govmomi `numericSensorInfo` |
| 6.4 | Host services (start/stop/restart) | **Missing** | P2 | M | systemd over SSH/agent; govmomi `HostServiceSystem` |
| 6.5 | Host firewall config | **Missing** | P2 | M | nftables/ufw mgmt; govmomi `HostFirewallSystem` |
| 6.6 | NTP / time config | **Missing** | P2 | S | chrony/timesyncd; govmomi `HostDateTimeSystem` |
| 6.7 | Host certificates mgmt | **Missing** | P2 | M | cert rotation; govmomi `HostCertificateManager` |
| 6.8 | Host maintenance mode | **Partial** (see 5.6) | P0 | M | enter/exit maintenance op |

## 7. Monitoring

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 7.1 | Per-VM performance metrics (CPU/mem/disk/net) | **Have** (`GetMetrics` → `MetricSeries`) | — | — | libvirt `DomainGetStats`/`DomainBlockStats`/`DomainInterfaceStats` |
| 7.2 | Performance charts — history, host roll-up, custom range | **Partial** (single CPU/mem chart; no host aggregate / range picker) | P1 | M | Aggregate per-host; range selector UI; govmomi `PerfManager` historical intervals |
| 7.3 | Events / tasks timeline (persisted, queryable) | **Partial** (`StreamEvents` exists; no persisted, filterable timeline) | P1 | M | Persist `Event`s to store + a timeline view; govmomi `EventManager`/`TaskManager` |
| 7.4 | **Alarms / alerting with thresholds + notify** | **Missing** (`insights` is a rules feed, not threshold alarms w/ notification) | P0 | M | Threshold-rule engine over `GetMetrics` → notification channels; govmomi `AlarmManager` |
| 7.5 | Notification channels (email/Slack/webhook) | **Missing** | P1 | M | Channel registry consumed by 7.4 |
| 7.6 | Topology / relationship map | **Missing** | P2 | L | Graph from aggregator (host↔VM↔net↔datastore); govmomi inventory maps |
| 7.7 | Audit log | **Have** (`AuditWrap`) | — | — | — |

## 8. Security / access

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 8.1 | RBAC roles + permissions | **Have** (scoped RBAC + AAL step-up on mutations) | — | — | — |
| 8.2 | **Per-object permissions** | **Partial** (provider-scope today; no per-VM/folder grant) | P1 | M | Extend RBAC scope to entity id / folder; govmomi `AuthorizationManager.SetEntityPermissions` |
| 8.3 | Tags & categories | **Partial** (`Labels` used for filter; no category schema/policy) | P1 | M | Category+tag model + tag-driven policy; govmomi tagging service |
| 8.4 | Sessions (list / terminate) | **Partial** (sessions exist; no admin session-management UI) | P1 | S | Session store list/revoke; govmomi `SessionManager.TerminateSession` |
| 8.5 | MFA | **Have** (TOTP) | — | — | — |
| 8.6 | SSO | **Have** (OIDC) | — | — | — |
| 8.7 | Per-tenant resource quotas | **Missing** | P1 | M | Quota enforcement at create/reconfigure (pairs w/ 1.11/5.5) |
| 8.8 | Folder / project hierarchy | **Missing** (flat provider scope) | P1 | M | Inventory folder tree; govmomi `Folder` hierarchy |
| 8.9 | VM disk encryption at rest | **Missing** | P1 | L | libvirt `<encryption>` (LUKS) + KMS; govmomi VM Encryption + KMS cluster |

## 9. Automation

| # | Capability | Status | Pri | Effort | Mechanism |
|---|---|---|---|---|---|
| 9.1 | REST API | **Have** (`vm_routes.go`, full per-VM surface) | — | — | — |
| 9.2 | **API tokens (non-interactive / service accounts)** | **Missing** (session-based only) | P0 | M | PAT/service-account token issuance + scope binding (prereq for IaC) |
| 9.3 | **Bulk operations (multi-VM power/migrate/tag)** | **Missing** (single-entity endpoints) | P0 | M | Batch endpoints fanning out over existing ops |
| 9.4 | Terraform provider | **Missing** | P0 | M | Go provider over 9.1 + 9.2; mirrors `hashicorp/vsphere` |
| 9.5 | Ansible modules / collection | **Missing** | P1 | M | Collection over REST; mirrors `community.vmware` |
| 9.6 | Scheduled tasks (general cron ops) | **Partial** (replication scheduler only) | P1 | M | Generalize the scheduler to arbitrary ops; govmomi `ScheduledTaskManager` |
| 9.7 | Webhooks (event → external) | **Missing** | P1 | M | Event-stream → HTTP webhook dispatch (pairs w/ 7.5) |
| 9.8 | Saved searches / tag-based queries | **Partial** (`ListOptions.Labels` filter; no saved searches) | P1 | S | Persist named filters |

---

## Parity roadmap — 3 waves

Ordered for **maximum "feels like vSphere" per unit of effort**, building on what is already shipped
(vTPM, Secure Boot, cloud-init, hot-plug, console, clone, migrate, replication, RBAC, MFA/SSO).

### Wave 1 — "It behaves like vSphere for a real VM" (highest impact)
The smallest set that makes a single VM's day-1/day-2 lifecycle indistinguishable from vSphere.

1. **Guest agent (1.21)** — the keystone: unblocks IP display, clean shutdown ack, AND app-consistent
   quiescing (4.5). Field `VM.IPAddresses` already exists; just wire qemu-ga.
2. **Snapshot manager: tree + delete-single + consolidate (2.10 / 4.1 / 4.3)** — one missing op + parent
   wiring + a tree view. Daily-driver expectation; today snapshots only revert.
3. **Online disk resize (2.2)** — single new op → `DomainBlockResize`. Universally expected, absent today.
4. **CPU topology + CPU model (1.7 / 1.8)** — `<cpu>` element; fixes perf + live-migrate/EVC compat.
5. **VM templates + sysprep (1.3 / 1.6)** — pairs with the already-done cloud-init to make
   deploy-from-template real provisioning (incl. Windows).
6. **Maintenance mode + host evacuation (5.6 / 6.8)** — `EnterMaintenance` + batch live-migrate; the
   foundation every admin reaches for, and the substrate for HA/DRS.
7. **API tokens + bulk ops (9.2 / 9.3)** — prerequisite for Terraform/Ansible and a constant admin ask.

### Wave 2 — "It manages a fleet like vSphere"
Resource control, data protection, alarms, automation surface.

8. **Memory/CPU reservation·limit·shares (1.11 / 1.12)** + **resource pools (5.5)** — vSphere's core
   resource-control model.
9. **Scheduled backups w/ retention (4.4 / 4.7 / 4.8)** — snapshot+export to S3/Azure/SAN backends
   (turns connectivity-only backends into real targets). Biggest data-protection gap.
10. **Alarms / thresholds + notification channels (7.4 / 7.5 / 9.7)** — vCenter alarms equivalent.
11. **Disk QoS + TRIM + thin/thick (2.5 / 2.6 / 2.1)** — small `<iotune>`/`discard`/preallocation knobs.
12. **Storage migration (live svMotion) (2.3)** — honor `TargetStorage` via `DomainBlockCopy`.
13. **Terraform provider (9.4)** + **per-object permissions / folders / tags (8.2 / 8.8 / 8.3)**.
14. **Replication failback (4.12)** + **events timeline + perf roll-up (7.3 / 7.2)**.

### Wave 3 — "Enterprise / marquee parity" (deep, L-effort moats)
15. **GPU / PCI passthrough + vGPU (1.17)** — `<hostdev>`/mdev; AI/VDI workloads.
16. **HA auto-restart (5.2)** + **DRS load balancing (5.3)** + **affinity rules (5.4)** + **EVC (5.7)**.
17. **Distributed virtual switch + distributed firewall (3.2 / 3.8)** + SR-IOV/teaming/NIOC (3.6/3.5/3.7).
18. **CBT / incremental backup + restore (4.6 / 4.9)** — dirty-bitmaps + `DomainBackupBegin`.
19. **VM encryption at rest (8.9)**, NUMA/vNUMA (1.13), multipath (2.9), serial console (1.19),
    USB passthrough (1.18), per-tenant quotas/isolation (8.7).

### Effort summary (realistic)

| Wave | Theme | Items | P0 count | Rough effort |
|------|-------|-------|----------|--------------|
| **1** | Single-VM lifecycle parity | 7 | 7 | ~6 S/M items ≈ **2–3 sprints** (1 keystone M + several S) |
| **2** | Fleet management parity | 7 clusters of work | ~3 | mostly M ≈ **4–6 sprints** |
| **3** | Enterprise moats | 5 clusters | ~2 | mostly L ≈ **2–3 quarters**; sequence by concrete deal pressure |

**Architectural notes.**
- Every Wave-1/2 item is a **normalized contract op + the KVM live implementation** (the only real
  backend today); native govmomi/WMI/XAPI mappings follow per provider. Each new op MUST add a
  `CapabilityMatrix` bit so the UI greys it out pre-flight (never "click-then-405").
- **Leverage already-built assets**: the `replication` scheduler is reusable for scheduled snapshots
  (4.4) and backups (4.7); the `storage` S3/Azure/SAN backends become backup targets (4.8) almost for
  free; the guacd/ws console bridge extends to the serial/xterm console (1.19); the `migrate` export is
  the basis of VM backup (4.7) and restore (4.9).
- **Defer the L-effort moats (Wave 3)** unless demanded — vDS/DFW, HA/DRS automation, CBT, encryption,
  SR-IOV/multipath are exactly where VMware's moat is deepest and the cost/value ratio is worst early.
