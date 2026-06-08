// ui/src/lib/rbac.ts
// Client-side permission + capability gating helpers.
// The permission vocabulary here MUST mirror the backend authz vocabulary
// (the single source of truth in the contract). Wildcard "*" = superuser.
//
// IMPORTANT: this is a UX affordance only (grey-out-before-click). The backend
// re-checks every permission; the UI never relies on this for security.

import type { Capability, OrchestratorKind, VMCapability } from "./types";

/** Returns true if the permission set grants `perm`. */
export function can(permissions: string[] | undefined, perm: string): boolean {
  if (!permissions || permissions.length === 0) return false;
  if (permissions.includes("*")) return true;
  if (permissions.includes(perm)) return true;
  // domain wildcards (e.g. "docker.container.*") are NOT in the seeded vocabulary,
  // but support them defensively if a custom role uses one.
  const parts = perm.split(".");
  for (let i = parts.length - 1; i > 0; i--) {
    const prefix = parts.slice(0, i).join(".") + ".*";
    if (permissions.includes(prefix)) return true;
  }
  return false;
}

/** True if ANY of the listed permissions is granted. */
export function canAny(permissions: string[] | undefined, perms: string[]): boolean {
  return perms.some((p) => can(permissions, p));
}

/** Capability check against a provider's capability list. */
export function hasCap(caps: Capability[] | undefined, want: Capability): boolean {
  return !!caps && caps.includes(want);
}

/** Read-only marker. */
export function isReadOnly(caps: Capability[] | undefined): boolean {
  return hasCap(caps, "readonly");
}

/* ---- Map a workload lifecycle action to its (capability, permission) pair. ---- */

export type WorkloadAction = "start" | "stop" | "restart" | "remove";

const ACTION_CAP: Record<WorkloadAction, Capability> = {
  start: "start",
  stop: "stop",
  restart: "restart",
  remove: "remove",
};

const ACTION_PERM: Record<WorkloadAction, string> = {
  start: "docker.container.start",
  stop: "docker.container.stop",
  restart: "docker.container.restart",
  remove: "docker.container.remove",
};

export interface GateResult {
  allowed: boolean;
  reason: string; // empty when allowed; tooltip text otherwise
}

/**
 * Decide whether a workload lifecycle action should be enabled.
 * Combines provider capability (grey-out per ADR-002) and user permission.
 */
export function gateWorkloadAction(
  action: WorkloadAction,
  kind: OrchestratorKind,
  caps: Capability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  // Swarm / Kubernetes are read-only in V1: no lifecycle buttons at all.
  if (kind !== "docker" || isReadOnly(caps)) {
    return { allowed: false, reason: `${labelKind(kind)} is read-only in this version` };
  }
  if (!hasCap(caps, ACTION_CAP[action])) {
    return { allowed: false, reason: `Provider does not support ${action}` };
  }
  if (!can(permissions, ACTION_PERM[action])) {
    return { allowed: false, reason: `You lack the ${ACTION_PERM[action]} permission` };
  }
  return { allowed: true, reason: "" };
}

/** Logs gate (docker.container.logs + CapLogs). */
export function gateLogs(caps: Capability[] | undefined, permissions: string[] | undefined): GateResult {
  if (!hasCap(caps, "logs")) return { allowed: false, reason: "Provider does not support logs" };
  if (!can(permissions, "docker.container.logs"))
    return { allowed: false, reason: "You lack the docker.container.logs permission" };
  return { allowed: true, reason: "" };
}

/** Stats gate (docker.container.stats + CapStats). k8s has no CapStats. */
export function gateStats(caps: Capability[] | undefined, permissions: string[] | undefined): GateResult {
  if (!hasCap(caps, "stats")) return { allowed: false, reason: "Stats not available for this orchestrator" };
  if (!can(permissions, "docker.container.stats"))
    return { allowed: false, reason: "You lack the docker.container.stats permission" };
  return { allowed: true, reason: "" };
}

/** Exec gate (docker.container.exec + CapExec). */
export function gateExec(caps: Capability[] | undefined, permissions: string[] | undefined): GateResult {
  if (!hasCap(caps, "exec")) return { allowed: false, reason: "Exec not supported by this orchestrator" };
  if (!can(permissions, "docker.container.exec"))
    return { allowed: false, reason: "You lack the docker.container.exec permission" };
  return { allowed: true, reason: "" };
}

/* ===================== Swarm + Kubernetes write gates ===================== */
//
// Swarm services and Kubernetes deployments/pods are mutated through dedicated
// endpoints (not the generic container Provider interface). The provider drops
// CapReadOnly once writable, so the gate opens when the user holds the matching
// permission and the orchestrator is no longer read-only. We still grey-out when
// CapReadOnly is present (e.g. an engine that only advertises a read-only swarm).

/** Swarm service lifecycle permissions, keyed by action. */
export const SWARM_SERVICE_PERM = {
  create: "swarm.service.create",
  scale: "swarm.service.scale",
  update: "swarm.service.update",
  restart: "swarm.service.update", // forced no-op spec update reuses update
  remove: "swarm.service.remove",
} as const;

export type SwarmServiceAction = keyof typeof SWARM_SERVICE_PERM;

/** Gate a swarm service write (create/scale/update/restart/remove). */
export function gateSwarmService(
  action: SwarmServiceAction,
  caps: Capability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (isReadOnly(caps)) return { allowed: false, reason: "Swarm is read-only on this host" };
  const perm = SWARM_SERVICE_PERM[action];
  if (!can(permissions, perm)) return { allowed: false, reason: `You lack the ${perm} permission` };
  return { allowed: true, reason: "" };
}

/** Gate a swarm node availability change (active/pause/drain). */
export function gateSwarmNode(
  caps: Capability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (isReadOnly(caps)) return { allowed: false, reason: "Swarm is read-only on this host" };
  if (!can(permissions, "swarm.node.update"))
    return { allowed: false, reason: "You lack the swarm.node.update permission" };
  return { allowed: true, reason: "" };
}

/* ----- Swarm secrets & configs ----- */
//
// Secrets and configs are managed through dedicated endpoints. Reads list
// metadata only (values are never returned for secrets); a single write
// permission covers both create AND delete in each domain, mirroring the backend
// router (swarm.secret.write / swarm.config.write). Like the service gates, the
// gate stays closed while the provider advertises read-only.

/** Swarm secret/config write permissions, keyed by domain. */
export const SWARM_SECRET_WRITE_PERM = {
  secret: "swarm.secret.write", // secret create + delete
  config: "swarm.config.write", // config create + delete
} as const;

export type SwarmSecretDomain = keyof typeof SWARM_SECRET_WRITE_PERM;

/** Gate a swarm secret/config write (create + delete share one permission). */
export function gateSwarmSecret(
  domain: SwarmSecretDomain,
  caps: Capability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (isReadOnly(caps)) return { allowed: false, reason: "Swarm is read-only on this host" };
  const perm = SWARM_SECRET_WRITE_PERM[domain];
  if (!can(permissions, perm)) return { allowed: false, reason: `You lack the ${perm} permission` };
  return { allowed: true, reason: "" };
}

/** Kubernetes write permissions, keyed by action. */
export const K8S_PERM = {
  scale: "k8s.deployment.scale",
  restart: "k8s.deployment.restart",
  resources: "k8s.deployment.resources", // set CPU/memory requests+limits
  delete: "k8s.workload.delete", // deployment + pod deletion share this perm
  apply: "k8s.manifest.apply",
} as const;

export type K8sAction = keyof typeof K8S_PERM;

/** Gate a Kubernetes write (scale/restart/delete/apply). */
export function gateK8s(
  action: K8sAction,
  caps: Capability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (isReadOnly(caps)) return { allowed: false, reason: "Kubernetes is read-only on this host" };
  const perm = K8S_PERM[action];
  if (!can(permissions, perm)) return { allowed: false, reason: `You lack the ${perm} permission` };
  return { allowed: true, reason: "" };
}

/* ----- Kubernetes storage / autoscaling / namespaces (Wave 3) ----- */
//
// These mutate dedicated cluster objects (PVCs, HPAs, Namespaces). PVC + HPA
// writes share one "*.write" permission for create AND delete; Namespace
// create/delete is cluster-level (admin-only) but uses the same single write
// permission. Like gateK8s, the gate stays closed while the provider advertises
// read-only.

/** Kubernetes cluster-object write permissions, keyed by domain. */
export const K8S_CLUSTER_WRITE_PERM = {
  storage: "k8s.storage.write", // PVC create + delete
  hpa: "k8s.hpa.write", // HPA create + delete
  namespace: "k8s.namespace.write", // Namespace create + delete (admin-only)
  ingress: "k8s.ingress.write", // Ingress delete (create/update via manifest apply)
} as const;

export type K8sClusterDomain = keyof typeof K8S_CLUSTER_WRITE_PERM;

/**
 * Gate a Kubernetes cluster-object write (PVC / HPA / Namespace create+delete).
 * One permission covers both create and delete in each domain, mirroring the
 * backend router (k8s.storage.write / k8s.hpa.write / k8s.namespace.write).
 */
export function gateK8sCluster(
  domain: K8sClusterDomain,
  caps: Capability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (isReadOnly(caps)) return { allowed: false, reason: "Kubernetes is read-only on this host" };
  const perm = K8S_CLUSTER_WRITE_PERM[domain];
  if (!can(permissions, perm)) return { allowed: false, reason: `You lack the ${perm} permission` };
  return { allowed: true, reason: "" };
}

/* ===================== Virtual machine (hypervisor) write gates ===================== */
//
// VM write affordances follow the same grey-out-before-click rule as containers:
// a button is enabled only when BOTH the owning hypervisor provider advertises
// the required VM capability AND the user holds the matching permission. The
// backend re-checks. A provider that advertises "readonly" disables every write.

/** VM capability membership check. */
export function hasVMCap(caps: VMCapability[] | undefined, want: VMCapability): boolean {
  return !!caps && caps.includes(want);
}

/** VM write actions -> (capability token, permission) pairs. */
export const VM_ACTION: Record<
  string,
  { cap: VMCapability; perm: string }
> = {
  start: { cap: "power_start", perm: "vm.power" },
  stop: { cap: "power_stop", perm: "vm.power" },
  reset: { cap: "power_reset", perm: "vm.power" },
  suspend: { cap: "power_suspend", perm: "vm.power" },
  // resume shares the suspend capability bit server-side (PowerOpCapability).
  resume: { cap: "power_suspend", perm: "vm.power" },
  snapshot: { cap: "snapshot", perm: "vm.snapshot" },
  snapshot_revert: { cap: "revert_snapshot", perm: "vm.snapshot" },
  clone: { cap: "clone", perm: "vm.clone" },
  migrate: { cap: "migrate", perm: "vm.migrate" },
  reconfigure: { cap: "reconfigure_vm", perm: "vm.reconfigure" },
  create_vm: { cap: "create_vm", perm: "vm.create" },
  delete_vm: { cap: "delete_vm", perm: "vm.delete" },
};

export type VMActionKey = keyof typeof VM_ACTION;

/**
 * Gate a VM write action. Combines the provider's advertised capability with the
 * user's permission; a read-only provider disables everything.
 */
export function gateVMAction(
  action: VMActionKey,
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  const spec = VM_ACTION[action];
  if (!spec) return { allowed: false, reason: "Unknown action" };
  if (hasVMCap(caps, "readonly")) {
    return { allowed: false, reason: "This hypervisor is read-only" };
  }
  if (!hasVMCap(caps, spec.cap)) {
    return { allowed: false, reason: `Provider does not support ${action.replace(/_/g, " ")}` };
  }
  if (!can(permissions, spec.perm)) {
    return { allowed: false, reason: `You lack the ${spec.perm} permission` };
  }
  return { allowed: true, reason: "" };
}

/* ----- VM infrastructure write gates (networks / storage / console) ----- */
//
// These mutate dedicated VM-provider objects (virtual networks, storage volumes,
// ISO library) or open the graphical console. Each combines a provider
// capability token advertised by GET /vm/providers with the matching user
// permission. A read-only provider disables every write. One write permission
// covers both create AND delete in each domain, mirroring the backend router.

/** Gate creating/deleting a virtual network (cap "network_write" + vm.network.write). */
export function gateVMNetworkWrite(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "network_write"))
    return { allowed: false, reason: "Provider does not support network management" };
  if (!can(permissions, "vm.network.write"))
    return { allowed: false, reason: "You lack the vm.network.write permission" };
  return { allowed: true, reason: "" };
}

/** Gate creating/deleting a storage volume or uploading an ISO (cap "storage_write" + vm.storage.write). */
export function gateVMStorageWrite(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "storage_write"))
    return { allowed: false, reason: "Provider does not support storage management" };
  if (!can(permissions, "vm.storage.write"))
    return { allowed: false, reason: "You lack the vm.storage.write permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate live hot-plug device management — attach/detach disk & NIC, mount/eject ISO
 * on a RUNNING VM (cap "hotplug" + vm.hotplug). A read-only provider disables it.
 */
export function gateVMHotPlug(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "hotplug"))
    return { allowed: false, reason: "Provider does not support live device hot-plug" };
  if (!can(permissions, "vm.hotplug"))
    return { allowed: false, reason: "You lack the vm.hotplug permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate an online disk resize (grow) — cap "disk_resize" + vm.disk.resize. A
 * read-only provider disables it. The backend additionally rejects a shrink
 * (422), so the UI only ever offers growing the disk.
 */
export function gateVMDiskResize(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "disk_resize"))
    return { allowed: false, reason: "Provider does not support disk resize" };
  if (!can(permissions, "vm.disk.resize"))
    return { allowed: false, reason: "You lack the vm.disk.resize permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate marking/unmarking a VM as a TEMPLATE (Lot 4A) — cap "templates" +
 * vm.template (admin-grade). A read-only provider disables it. Deploy-from-template
 * reuses gateVMAction("clone").
 */
export function gateVMTemplate(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "templates"))
    return { allowed: false, reason: "Provider does not support VM templates" };
  if (!can(permissions, "vm.template"))
    return { allowed: false, reason: "You lack the vm.template permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate applying CPU/memory resource control (Lot 5A) — cap "resource_control" +
 * vm.resource. Also gates resource-pool create/update/delete/assign (same perm).
 * A read-only provider disables it.
 */
export function gateVMResource(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "resource_control"))
    return { allowed: false, reason: "Provider does not support resource control" };
  if (!can(permissions, "vm.resource"))
    return { allowed: false, reason: "You lack the vm.resource permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate per-disk QoS (Lot 5A) — cap "disk_qos" + vm.disk.qos. A read-only provider
 * disables it.
 */
export function gateVMDiskQoS(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "disk_qos"))
    return { allowed: false, reason: "Provider does not support per-disk QoS" };
  if (!can(permissions, "vm.disk.qos"))
    return { allowed: false, reason: "You lack the vm.disk.qos permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate live storage migration (Lot 5A) — cap "storage_migrate" + vm.storage.migrate.
 * A read-only provider disables it.
 */
export function gateVMStorageMigrate(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "storage_migrate"))
    return { allowed: false, reason: "Provider does not support live storage migration" };
  if (!can(permissions, "vm.storage.migrate"))
    return { allowed: false, reason: "You lack the vm.storage.migrate permission" };
  return { allowed: true, reason: "" };
}

/**
 * Gate showing guest-agent info — cap "guest_agent" + vm.read (anyone who can
 * see the VM). Read-only does NOT disable it (it is a read affordance).
 */
export function gateVMGuestAgent(
  caps: VMCapability[] | undefined,
  _permissions: string[] | undefined,
): GateResult {
  if (!hasVMCap(caps, "guest_agent"))
    return { allowed: false, reason: "Provider does not expose guest-agent info" };
  return { allowed: true, reason: "" };
}

/**
 * Gate entering/exiting host maintenance mode (cap "maintenance" +
 * vm.host.maintenance, admin-grade). A read-only provider disables it.
 */
export function gateVMMaintenance(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (hasVMCap(caps, "readonly")) return { allowed: false, reason: "This hypervisor is read-only" };
  if (!hasVMCap(caps, "maintenance"))
    return { allowed: false, reason: "Provider does not support host maintenance" };
  if (!can(permissions, "vm.host.maintenance"))
    return { allowed: false, reason: "You lack the vm.host.maintenance permission" };
  return { allowed: true, reason: "" };
}

/** Gate opening the graphical console (cap "console" + vm.console). */
export function gateVMConsole(
  caps: VMCapability[] | undefined,
  permissions: string[] | undefined,
): GateResult {
  if (!hasVMCap(caps, "console"))
    return { allowed: false, reason: "Provider does not expose a console" };
  if (!can(permissions, "vm.console"))
    return { allowed: false, reason: "You lack the vm.console permission" };
  return { allowed: true, reason: "" };
}

function labelKind(kind: OrchestratorKind): string {
  switch (kind) {
    case "docker":
      return "Docker";
    case "swarm":
      return "Swarm";
    case "kubernetes":
      return "Kubernetes";
    default:
      return kind;
  }
}
