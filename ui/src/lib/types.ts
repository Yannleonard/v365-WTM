// ui/src/lib/types.ts
// TypeScript mirrors of the Castor REST + WS contract.
// Field names are EXACT (camelCase) and match ADR-002 struct json tags and the
// locked REST contract. Do not rename fields.

/* ===================== Provider / Workload core (ADR-002) ===================== */

export type OrchestratorKind = "docker" | "swarm" | "kubernetes";

export type WorkloadState =
  | "running"
  | "stopped"
  | "paused"
  | "restarting"
  | "pending"
  | "unknown";

// Capability tokens are Capability.Strings() output from the backend.
export type Capability =
  | "list"
  | "inspect"
  | "logs"
  | "stats"
  | "start"
  | "stop"
  | "restart"
  | "remove"
  | "exec"
  | "events"
  | "images"
  | "networks"
  | "volumes"
  | "readonly";

export interface Port {
  private: number;
  public?: number;
  protocol: string; // "tcp" | "udp" | "sctp"
}

export interface Workload {
  id: string;
  name: string;
  kind: OrchestratorKind;
  providerId: string;
  node?: string;
  state: WorkloadState;
  stateRaw?: string;
  image: string;
  ports?: Port[];
  // For K8s pods, labels include the synthetic io.castor.qos entry (the
  // kubelet-reported QoS class) — read it via podQosClass(labels) / K8S_QOS_LABEL.
  labels?: Record<string, string>;
  createdAt: string; // RFC3339
  group?: string;
  protected: boolean;
}

export interface WorkloadDetail extends Workload {
  // engine-specific inspect document, masked for secret env unless caller has
  // docker.container.inspect.secrets. Opaque JSON.
  raw: unknown;
}

/* ===================== Providers / hosts ===================== */

export interface ProviderInfo {
  id: string;
  kind: OrchestratorKind;
  capabilities: Capability[];
}

export type HostStatus = "connected" | "down" | "pending";

// EngineInfo mirrors docker.EngineInfo (server) — host capacity + inventory from
// `docker info`. Present once the first info poll succeeds.
export interface EngineInfo {
  engineVersion: string;
  apiVersion: string;
  os: string;
  osType: string;
  osVersion: string;
  kernelVersion: string;
  architecture: string;
  ncpu: number;
  memTotalBytes: number;
  containers: number;
  containersRunning: number;
  containersPaused: number;
  containersStopped: number;
  images: number;
  name: string;
  swarmActive: boolean;
}

export interface HostSummaryEntry {
  id: string;
  name: string;
  kind: string;
  connection: string;
  status: HostStatus;
  providerIds: string[];
  degraded: boolean;
  engine?: EngineInfo | null;
}

export interface HostSummaryCounts {
  containers: number;
  running: number;
  images: number;
  networks: number;
  volumes: number;
  swarmTasks: number;
  k8sPods: number;
}

export interface HostDetail extends HostSummaryEntry {
  summary: HostSummaryCounts;
}

/* ===================== Dashboard (BI aggregation) ===================== */

// DashboardMetrics mirrors api.dashboardMetrics (server/internal/api/dashboard.go):
// the aggregated numbers powering the BI dashboard for GET
// /hosts/{hostID}/dashboard/metrics. Counts come from the cache snapshot; cpu/
// memory + the top-N rankings come from a bounded live sample of running
// containers (missing samples are silently skipped, so sums are best-effort).

export interface DashboardContainerCounts {
  total: number;
  running: number;
  stopped: number;
  paused: number;
}

export interface DashboardCpu {
  usedPercent: number; // sum of container cpu% (can exceed 100 across cores)
  cores: number; // engine NCPU
}

export interface DashboardMemory {
  usedBytes: number; // sum over running containers
  totalBytes: number; // engine MemTotal (0 if unknown)
  usedPercent: number; // usedBytes / totalBytes * 100 (0 if total unknown)
}

export interface DashboardStateBucket {
  state: string; // WorkloadState value, e.g. "running"
  count: number;
}

// One ranked container in topByCpu / topByMem.
export interface DashboardTopContainer {
  id: string;
  name: string;
  cpuPercent: number;
  memBytes: number;
}

export interface DashboardEngine {
  version: string;
  ncpu: number;
  memTotalBytes: number;
}

export interface DashboardMetrics {
  containers: DashboardContainerCounts;
  images: number;
  networks: number;
  volumes: number;
  swarmServices: number;
  swarmTasks: number;
  k8sPods: number;
  cpu: DashboardCpu;
  memory: DashboardMemory;
  stateBreakdown: DashboardStateBucket[];
  topByCpu: DashboardTopContainer[];
  topByMem: DashboardTopContainer[];
  engine: DashboardEngine;
}

/* ===================== Auth / RBAC ===================== */

// External SSO sessions add "ldap" / "oidc" (no TOTP second factor — the IdP
// owns the authentication strength). Mirrors authz AMR* constants.
export type Amr = "pwd" | "pwd+totp" | "ldap" | "oidc";

export interface SessionUser {
  id: string;
  username: string;
  email?: string;
  isActive?: boolean;
  mustChangePassword?: boolean;
  totpEnabled?: boolean;
}

export interface RoleRef {
  name: string;
  scopeType: string;
  scopeId: string | null;
}

export interface MeResponse {
  user: SessionUser;
  amr: Amr;
  csrfToken: string;
  permissions: string[];
  roles: RoleRef[];
}

export interface LoginResponse {
  user?: SessionUser;
  amr: Amr;
  csrfToken: string;
  permissions?: string[];
  requiresTotp: boolean;
}

export interface BootstrapStatus {
  required: boolean;
}

export interface HealthzResponse {
  status: string;
  version: string;
  bootstrap: boolean;
}

export interface BootstrapResponse {
  user: SessionUser;
  totpEnrollOffered: boolean;
}

export interface TotpEnrollResponse {
  secret: string;
  otpauthUrl: string;
  qrPngBase64: string;
}

export interface TotpConfirmResponse {
  recoveryCodes: string[];
}

/* ===================== Stats (one-shot REST + WS) ===================== */

// REST one-shot StatSample shape.
export interface StatSample {
  timestamp: string;
  cpuPercent: number;
  memUsageBytes: number;
  memLimitBytes: number;
  netRxBytes: number;
  netTxBytes: number;
  blkReadBytes: number;
  blkWriteBytes: number;
}

// WS stats data.payload shape.
export interface WsStatsPayload {
  cpuPct: number;
  memUsed: number;
  memLimit: number;
  memPct: number;
  netRx: number;
  netTx: number;
  blkRead: number;
  blkWrite: number;
}

/* ===================== Docker resources ===================== */

export interface DockerImage {
  id: string;
  repoTags: string[];
  size: number;
  created: string;
  dangling: boolean;
}

export interface DockerNetwork {
  id: string;
  name: string;
  driver: string;
  scope: string;
  internal: boolean;
}

export interface DockerVolume {
  name: string;
  driver: string;
  mountpoint: string;
  createdAt: string;
}

/* ===================== Swarm (read-only) ===================== */

// Configured per-task CPU/memory limits + reservations on a swarm service.
// Mirrors swarm.ServiceResources. cpu* are CPU cores (NanoCPUs/1e9); memory*
// are bytes. A 0 value means the corresponding knob is not set.
export interface SwarmServiceResources {
  cpuLimit: number;
  memoryLimitBytes: number;
  cpuReservation: number;
  memoryReservationBytes: number;
}

export interface SwarmService {
  id: string;
  name: string;
  mode: string;
  replicas: string;
  image: string;
  createdAt: string;
  resources: SwarmServiceResources;
}

export interface SwarmNode {
  id: string;
  hostname: string;
  role: string;
  availability: string;
  state: string;
  addr: string;
}

/* ----- Swarm secrets & configs ----- */

// Swarm secret summary. Mirrors swarm.SwarmSecretInfo. SECURITY: there is NO
// data field — secret values are write-only and never returned on a read.
export interface SwarmSecretInfo {
  id: string;
  name: string;
  createdAt: string;
  updatedAt: string;
}

// Swarm config summary. Mirrors swarm.SwarmConfigInfo (list = metadata only).
export interface SwarmConfigInfo {
  id: string;
  name: string;
  createdAt: string;
  updatedAt: string;
}

// Single swarm config WITH its (non-secret) payload. Mirrors
// swarm.SwarmConfigDetail (returned only by the single-config GET).
export interface SwarmConfigDetail extends SwarmConfigInfo {
  data: string;
}

// Body for POST /hosts/{hostID}/swarm/secrets. The value is a UTF-8 string and
// is the only place a secret's data is ever transmitted (write-only after).
export interface SwarmSecretCreateInput {
  name: string;
  data: string;
}

// Body for POST /hosts/{hostID}/swarm/configs.
export interface SwarmConfigCreateInput {
  name: string;
  data: string;
}

/* ----- Swarm mutations (mirror swarm.ServiceCreateSpec / ServiceUpdateInput) ----- */

// One published port mapping for a service. Mirrors swarm.SwarmPort.
export interface SwarmPort {
  published: number;
  target: number;
  protocol: string; // "tcp" (default) | "udp" | "sctp"
}

// Attach an EXISTING swarm secret to a service as a file under /run/secrets.
// Identify by id (preferred) or name; targetFile defaults to the secret name
// when omitted. Mirrors swarm.SwarmSecretRef. The data is never carried here.
export interface SwarmSecretRef {
  secretId: string;
  secretName: string;
  targetFile: string;
}

// Attach an EXISTING swarm config to a service as a file (default /<configName>).
// Mirrors swarm.SwarmConfigRef.
export interface SwarmConfigRef {
  configId: string;
  configName: string;
  targetFile: string;
}

// Body for POST /hosts/{hostID}/swarm/services. Mirrors swarm.ServiceCreateSpec.
// Resource fields are optional; <=0 / omitted means "unset". cpu* are CPU
// cores, memory* are bytes.
export interface SwarmServiceCreateInput {
  name: string;
  image: string;
  replicas: number;
  env: string[]; // "KEY=VALUE" entries
  ports: SwarmPort[];
  networks: string[];
  restart: string; // "any" (default) | "on-failure" | "none"
  cpuLimit?: number;
  memoryLimitBytes?: number;
  cpuReservation?: number;
  memoryReservationBytes?: number;
  // Optional secret/config attachments (reference existing objects by id/name).
  secrets?: SwarmSecretRef[];
  configs?: SwarmConfigRef[];
}

// Body for PUT /hosts/{hostID}/swarm/services/{id}. Mirrors
// swarm.ServiceUpdateInput: omitted fields are left unchanged; a non-empty env
// array replaces the env entirely. Resource fields are always re-applied by the
// server: a positive value sets that knob, 0 clears it (so omitting them clears
// any previously-set limit — send the current values to preserve them).
export interface SwarmServiceUpdateInput {
  image?: string;
  env?: string[];
  replicas?: number;
  cpuLimit?: number;
  memoryLimitBytes?: number;
  cpuReservation?: number;
  memoryReservationBytes?: number;
  // Omit to leave attachments unchanged; send an array (including []) to REPLACE
  // the full set ([] detaches all). Mirrors swarm.ServiceUpdateInput's
  // pointer-to-slice semantics.
  secrets?: SwarmSecretRef[];
  configs?: SwarmConfigRef[];
}

// Body for POST /hosts/{hostID}/swarm/services/{id}/scale.
export interface SwarmScaleInput {
  replicas: number;
}

// Body for POST /hosts/{hostID}/swarm/nodes/{id}/availability.
export interface NodeAvailabilityInput {
  availability: "active" | "pause" | "drain";
}

/* ===================== Kubernetes (read-only) ===================== */

// Kubernetes QoS class (kubelet rules). Drives the QoS badge color.
export type K8sQosClass = "Guaranteed" | "Burstable" | "BestEffort";

// Per-container CPU/memory requests+limits as declared on a Deployment's pod
// template. Each string is the Quantity form ("100m", "128Mi", …) and is ""
// when that entry is unset. Mirrors kube.K8sContainerResources.
export interface K8sContainerResources {
  name: string;
  cpuRequest: string;
  cpuLimit: string;
  memRequest: string;
  memLimit: string;
}

export interface K8sDeployment {
  namespace: string;
  name: string;
  replicas: number;
  ready: number;
  available: number;
  image: string;
  createdAt: string;
  // Per-container requests/limits from the pod template.
  containers: K8sContainerResources[];
  // QoS class implied by those resources (Guaranteed / Burstable / BestEffort).
  qosClass: K8sQosClass | "";
}

// Synthetic Workload label carrying a pod's kubelet-reported QoS class. K8s pods
// are returned as Workloads; rather than widen the shared Workload schema the
// server surfaces QoS in labels under this key (mirrors kube.LabelQoS). Read it
// from a pod Workload via `workload.labels?.[K8S_QOS_LABEL]`.
export const K8S_QOS_LABEL = "io.castor.qos";

// podQosClass extracts the QoS class label off a pod Workload (undefined when
// the kubelet has not yet reported one).
export function podQosClass(labels: Record<string, string> | undefined): K8sQosClass | undefined {
  const v = labels?.[K8S_QOS_LABEL];
  return v === "Guaranteed" || v === "Burstable" || v === "BestEffort" ? v : undefined;
}

// podContainerNames extracts the container names from a pod WorkloadDetail.raw
// (a marshaled corev1.Pod, so spec.containers[].name in standard Kubernetes
// JSON). Init and ephemeral containers are appended after the regular ones so a
// multi-container pod's full exec/log target set is selectable. Returns [] for
// anything that isn't a pod-shaped object (Docker/Swarm raw, or a not-yet-loaded
// detail).
export function podContainerNames(raw: unknown): string[] {
  if (!raw || typeof raw !== "object") return [];
  const spec = (raw as { spec?: unknown }).spec;
  if (!spec || typeof spec !== "object") return [];
  const names: string[] = [];
  const collect = (list: unknown) => {
    if (!Array.isArray(list)) return;
    for (const c of list) {
      const name = (c as { name?: unknown })?.name;
      if (typeof name === "string" && name && !names.includes(name)) names.push(name);
    }
  };
  const s = spec as { containers?: unknown; initContainers?: unknown; ephemeralContainers?: unknown };
  collect(s.containers);
  collect(s.initContainers);
  collect(s.ephemeralContainers);
  return names;
}

export interface K8sNode {
  name: string;
  status: string;
  roles: string[];
  version: string;
  internalIP: string;
}

/* ----- Kubernetes autoscaling + core cluster objects (Wave 3) ----- */

// HorizontalPodAutoscaler summary. target is the scale-target ref ("Deployment/
// web"); the cpu fields are the configured target and last-observed utilization
// (0 when absent). Mirrors kube.HPAInfo.
export interface HPAInfo {
  namespace: string;
  name: string;
  target: string;
  minReplicas: number;
  maxReplicas: number;
  currentReplicas: number;
  targetCpuPercent: number;
  currentCpuPercent: number;
}

// Body for POST /hosts/{hostID}/k8s/hpas?namespace=<ns>. Creates a CPU-
// utilization HPA targeting a Deployment. Mirrors api.hpaCreateRequest /
// kube.HPACreateSpec.
export interface HPACreateRequest {
  name: string;
  targetDeployment: string;
  minReplicas: number;
  maxReplicas: number;
  cpuPercent: number;
}

// Namespace summary. status is the phase ("Active" / "Terminating"). Mirrors
// kube.NamespaceInfo.
export interface NamespaceInfo {
  name: string;
  status: string;
  createdAt: string; // RFC3339
}

// Service summary. ports are rendered "<port>/<proto>" (or "<port>:<nodePort>/
// <proto>"); externalIP is the first LB ingress / declared external IP ("" when
// none). Mirrors kube.ServiceInfoK8s.
export interface ServiceInfoK8s {
  namespace: string;
  name: string;
  type: string;
  clusterIP: string;
  ports: string[];
  externalIP: string;
}

// ConfigMap summary: key NAMES only (never values). Mirrors kube.ConfigMapInfo.
export interface ConfigMapInfo {
  namespace: string;
  name: string;
  keys: string[];
  createdAt: string; // RFC3339
}

// Secret summary: the secret type + key NAMES only — values are NEVER returned.
// Mirrors kube.SecretInfo.
export interface SecretInfo {
  namespace: string;
  name: string;
  type: string;
  keys: string[];
  createdAt: string; // RFC3339
}

// Event summary. object is the involved object ("<kind>/<name>"); lastSeen is
// the last-occurrence timestamp. Newest-first, capped server-side. Mirrors
// kube.EventInfo.
export interface EventInfo {
  namespace: string;
  type: string;
  reason: string;
  object: string;
  message: string;
  count: number;
  lastSeen: string; // RFC3339
}

// Ingress summary (networking/v1). class is the IngressClass (spec
// ingressClassName, falling back to the legacy annotation, "" when neither set).
// hosts are the distinct rule hosts ("*" for a hostless rule); paths render the
// routing table as "<host><path> -> <service>:<port>". address is the first LB
// ingress IP/hostname ("" until provisioned). Mirrors kube.IngressInfo.
export interface IngressInfo {
  namespace: string;
  name: string;
  class: string;
  hosts: string[];
  paths: string[];
  address: string;
  createdAt: string; // RFC3339
}

/* ----- Kubernetes live metrics (metrics-server) ----- */

// NodeMetric is live per-node usage from metrics-server. cpuMilli is CPU usage
// in millicores (1000 = 1 core); memoryBytes is working-set memory in bytes.
// Mirrors kube.NodeMetric.
export interface NodeMetric {
  name: string;
  cpuMilli: number;
  memoryBytes: number;
  timestamp: string; // RFC3339
}

// PodMetric is live per-pod usage (summed over containers) from metrics-server.
// Mirrors kube.PodMetric.
export interface PodMetric {
  namespace: string;
  name: string;
  cpuMilli: number;
  memoryBytes: number;
  timestamp: string; // RFC3339
}

// Response of GET /hosts/{hostID}/k8s/metrics/nodes. available is false (with an
// empty items array) when metrics-server is not installed — not an HTTP error.
// Mirrors api.nodeMetricsResponse.
export interface NodeMetricsResponse {
  available: boolean;
  items: NodeMetric[];
}

// Response of GET /hosts/{hostID}/k8s/metrics/pods. available is false (with an
// empty items array) when metrics-server is not installed. Mirrors
// api.podMetricsResponse.
export interface PodMetricsResponse {
  available: boolean;
  items: PodMetric[];
}

/* ----- Kubernetes storage (PV / PVC / StorageClass) ----- */

// PVInfo mirrors kube.PVInfo: a cluster-scoped PersistentVolume summary.
// capacity is the Quantity string ("10Gi", …), "" when unset; claim is the bound
// PVC as "<namespace>/<name>" ("" when unbound).
export interface PVInfo {
  name: string;
  capacity: string;
  accessModes: string[];
  reclaimPolicy: string;
  status: string;
  storageClass: string;
  claim: string;
}

// PVCInfo mirrors kube.PVCInfo: a PersistentVolumeClaim summary. capacity is the
// bound (status) capacity, "" until bound; volume is the bound PV name ("" while
// Pending).
export interface PVCInfo {
  namespace: string;
  name: string;
  status: string;
  volume: string;
  capacity: string;
  accessModes: string[];
  storageClass: string;
}

// StorageClassInfo mirrors kube.StorageClassInfo. isDefault reflects the
// is-default-class annotation (GA or beta).
export interface StorageClassInfo {
  name: string;
  provisioner: string;
  reclaimPolicy: string;
  volumeBindingMode: string;
  isDefault: boolean;
}

// Body for POST /hosts/{hostID}/k8s/pvcs (mirrors api.pvcCreateRequest). name +
// a positive requestBytes are required; namespace defaults to "default";
// storageClass "" uses the cluster default; accessModes default to
// ["ReadWriteOnce"] when empty. requestBytes is the requested storage in bytes.
export interface PVCCreateRequest {
  name: string;
  namespace?: string;
  storageClass?: string;
  accessModes?: string[];
  requestBytes: number;
}

/* ----- Kubernetes mutations ----- */

// Body for POST /hosts/{hostID}/k8s/deployments/{ns}/{name}/scale.
export interface K8sScaleRequest {
  replicas: number;
}

// Body for POST /hosts/{hostID}/k8s/apply.
export interface K8sApplyRequest {
  yaml: string;
}

// One CPU+memory pair (a request OR a limit) for a Deployment container.
// cpuMilli is millicores (1000 = 1 core); memoryBytes is bytes. A 0 entry is
// "unset" and is left unchanged server-side. Mirrors kube.ResourceSpec.
export interface K8sResourceSpec {
  cpuMilli: number;
  memoryBytes: number;
}

// Body for POST /hosts/{hostID}/k8s/deployments/{ns}/{name}/resources. Sets the
// CPU/memory requests+limits on a Deployment container (containerName omitted =>
// the first container). Mirrors api.setResourcesRequest.
export interface K8sSetResourcesRequest {
  containerName?: string;
  requests: K8sResourceSpec;
  limits: K8sResourceSpec;
}

// One per-document outcome from POST /hosts/{hostID}/k8s/apply. `action` is the
// classification of the server-side apply; `error` is present only when
// action="error". Field names mirror kube.ApplyResult.
export type K8sApplyAction = "created" | "configured" | "unchanged" | "error";

export interface K8sApplyResult {
  group: string;
  version: string;
  kind: string;
  namespace: string;
  name: string;
  action: K8sApplyAction;
  error?: string;
}

// Response of POST /hosts/{hostID}/k8s/apply (mirrors api.applyResponse).
export interface K8sApplyResponse {
  results: K8sApplyResult[];
}

// Friendly aliases used by the UI layer (api.ts / views). Same wire shape as the
// request types above — kept so call sites read as "...Input".
export type K8sScaleInput = K8sScaleRequest;
export type K8sApplyInput = K8sApplyRequest;
export type K8sSetResourcesInput = K8sSetResourcesRequest;

/* ===================== Helm (charts + releases) ===================== */

// One configured chart repository. Mirrors helm.RepoInfo.
export interface HelmRepo {
  name: string;
  url: string;
}

// One chart hit from a repository-index search. version/appVersion are the
// latest version in the cached index. Mirrors helm.ChartInfo.
export interface HelmChart {
  name: string; // chart name, without the "<repo>/" prefix
  repo: string; // owning repo name
  version: string;
  appVersion: string;
  description: string;
}

// Helm release status (lower-case, as Helm reports it).
export type HelmReleaseStatus =
  | "unknown"
  | "deployed"
  | "uninstalled"
  | "superseded"
  | "failed"
  | "uninstalling"
  | "pending-install"
  | "pending-upgrade"
  | "pending-rollback";

// One Helm release summary (list + detail). chart is "<chartName>-<version>".
// Mirrors helm.ReleaseInfo.
export interface HelmRelease {
  name: string;
  namespace: string;
  revision: number;
  status: HelmReleaseStatus | string;
  chart: string;
  appVersion: string;
  updated: string; // RFC3339 ("" if unknown)
}

// One entry in a release's revision history. Mirrors helm.ReleaseRevision.
export interface HelmReleaseRevision {
  revision: number;
  status: HelmReleaseStatus | string;
  chart: string;
  appVersion: string;
  updated: string; // RFC3339 ("" if unknown)
  description: string; // Helm's per-revision note
}

// Body for POST .../helm/repos.
export interface HelmRepoRequest {
  name: string;
  url: string;
}

// Body for POST .../helm/releases (install). namespace defaults to "default"
// server-side; version "" => latest; values are optional overrides.
export interface HelmInstallRequest {
  release: string;
  chart: string; // "repo/name"
  namespace?: string;
  version?: string;
  values?: Record<string, unknown>;
}

// Body for POST .../helm/releases/{ns}/{name}/upgrade. The release name +
// namespace come from the path.
export interface HelmUpgradeRequest {
  chart: string;
  version?: string;
  values?: Record<string, unknown>;
}

// Body for POST .../helm/releases/{ns}/{name}/rollback. revision 0 => the
// immediately previous revision.
export interface HelmRollbackRequest {
  revision: number;
}

/* ===================== Audit ===================== */

export type AuditResult = "success" | "denied" | "error";

export interface AuditEntry {
  id: string;
  ts: string; // RFC3339
  tsEpoch: number;
  actorId: string;
  actorName: string;
  actorIp: string;
  action: string;
  targetType: string;
  targetId: string;
  targetName: string;
  scopeType: string;
  scopeId: string | null;
  result: AuditResult;
  httpStatus: number;
  detail: unknown; // sanitized JSON, never secrets
  requestId: string;
}

export interface AuditPage {
  items: AuditEntry[];
  nextCursor: string | null;
}

export interface AuditQuery {
  actorId?: string;
  action?: string;
  targetType?: string;
  targetId?: string;
  result?: AuditResult | "";
  from?: number;
  to?: number;
  limit?: number;
  cursor?: string;
}

/* ===================== Users / roles ===================== */

export interface UserRoleBinding {
  bindingId: string;
  roleId: string;
  roleName: string;
  scopeType: string;
  scopeId: string | null;
}

export interface UserRecord {
  id: string;
  username: string;
  email?: string;
  isActive: boolean;
  totpEnabled: boolean;
  lastLoginAt: string | null;
  createdAt: string;
  roles: UserRoleBinding[];
}

export interface RoleRecord {
  id: string;
  name: string;
  description?: string;
  isBuiltin: boolean;
  permissions: string[];
  createdAt: string;
}

/* ===================== Settings ===================== */

export interface SettingsResponse {
  "bootstrap.completed": boolean;
  "instance.id": string;
  "security.totp_required_for_mutations": boolean;
  "session.ttl_seconds": number;
  "security.protected_labels": string[];
}

export type SettingsPatch = Partial<
  Pick<
    SettingsResponse,
    "security.totp_required_for_mutations" | "security.protected_labels" | "session.ttl_seconds"
  >
>;

/* ===================== Backups (volume tar) ===================== */

export type BackupStatus = "pending" | "completed" | "failed";

// Backup mirrors store.Backup. filePath is server-side only and never returned.
export interface Backup {
  id: string;
  kind: string; // "volume" (V1)
  hostId: string;
  targetName: string; // volume name backed up
  sizeBytes: number;
  status: BackupStatus;
  error?: string;
  createdBy?: string;
  createdAt: number; // unix epoch seconds
}

// Body for POST /hosts/{hostID}/backups.
export interface CreateBackupRequest {
  kind?: "volume";
  target: string; // volume name
}

// Body for POST /hosts/{hostID}/backups/{id}/restore.
export interface RestoreBackupRequest {
  target?: string; // defaults to the originally backed-up volume
}

/* ===================== Stacks (compose) ===================== */

export type StackStatus = "pending" | "running" | "partial" | "stopped" | "error";

// Stack mirrors store.Stack: a deployed multi-container compose stack.
// composeYaml is the validated source document; projectName is the compose
// project label (com.docker.compose.project) used to enumerate/teardown.
export interface Stack {
  id: string;
  name: string;
  projectName: string;
  hostId: string;
  composeYaml: string;
  status: StackStatus;
  serviceCount: number;
  createdBy: string;
  createdAt: number; // unix epoch seconds
  updatedAt: number; // unix epoch seconds
}

// One live container of a stack (StackDetail.containers), enumerated by the
// compose project label.
export interface StackContainer {
  id: string;
  name: string;
  service: string;
  state: string;
}

export interface StackDetail extends Stack {
  containers: StackContainer[];
}

// One normalized service in a validate/summary response. environment is the
// "KEY=VALUE" list form; ports/volumes are the raw compose strings.
export interface StackServiceView {
  name: string;
  image: string;
  containerName: string;
  ports: string[];
  environment: string[];
  volumes: string[];
  networks: string[];
  restart: string;
  command: string[];
  dependsOn: string[];
}

// Response of POST /hosts/{hostID}/stacks/validate (200 on success; a 422
// validation_failed envelope on a bad document).
export interface StackValidateResponse {
  valid: boolean;
  serviceCount: number;
  services: StackServiceView[];
  deployOrder: string[]; // service names in topological deploy order
}

// Body for POST /hosts/{hostID}/stacks/validate.
export interface ValidateStackRequest {
  composeYaml: string;
}

// Body for POST /hosts/{hostID}/stacks (create + up).
export interface CreateStackRequest {
  name: string;
  composeYaml: string;
  // Admin-only opt-in to permit host bind mounts declared in the compose volumes.
  // Non-admins are rejected with 403 if any service declares a host bind; the
  // always-blocked host paths stay rejected for everyone. Omit for named volumes.
  allowHostMounts?: boolean;
}

/* ----- compose builder (POST /stacks/builder/generate) ----- */

export interface BuilderPort {
  host: number; // 0 => ephemeral host port
  container: number;
  proto: string; // "tcp" | "udp" | "sctp" ("" treated as tcp)
}

export interface BuilderEnv {
  key: string;
  value: string;
}

export interface BuilderVolume {
  source: string; // "" => anonymous volume; abs path => bind; else named volume
  target: string;
}

export interface BuilderService {
  name: string;
  image: string;
  containerName?: string;
  ports?: BuilderPort[];
  env?: BuilderEnv[];
  volumes?: BuilderVolume[];
  networks?: string[];
  restart?: string;
  command?: string[];
  dependsOn?: string[];
}

// Body for POST /stacks/builder/generate.
export interface BuilderRequest {
  projectName: string;
  services: BuilderService[];
}

// Response of POST /stacks/builder/generate: the generated compose YAML.
export interface BuilderResponse {
  yaml: string;
}

/* ===================== Virtual machines / hypervisors ===================== */
//
// The VM/hypervisor domain mirrors the backend vm provider contract. A VM is a
// normalized guest across hypervisor kinds (vSphere/ESXi, Proxmox, libvirt/KVM,
// Hyper-V, …); the `kind` is the provider kind token. Capability tokens
// (vmCapability) gate write affordances the same way ADR-002 caps gate
// container actions — power_start / snapshot / clone / migrate / create_vm /
// delete_vm / export / metrics …

// Normalized VM lifecycle state. stateRaw carries the hypervisor-native string.
export type VMState =
  | "running"
  | "stopped"
  | "suspended"
  | "paused"
  | "unknown";

// ----- Hypervisor connections (register/connect a live provider) -----

// HvConnKind selects which hypervisor a connection targets.
export type HvConnKind = "kvm" | "hyperv" | "vmware" | "xen";

// HvConnStatus reflects the live connect/register state of a connection.
export type HvConnStatus = "pending" | "connected" | "error";

// HvConn is the SAFE projection from GET /vm/connections: it carries hasSecret
// (whether a credential is stored) but NEVER the secret itself. Field names match
// the api hvConnView json tags.
export interface HvConn {
  id: string;
  name: string;
  kind: HvConnKind;
  endpoint: string;
  username: string;
  hasSecret: boolean;
  insecureTls: boolean;
  enabled: boolean;
  status: HvConnStatus;
  lastError?: string;
  lastSeenAt?: string; // RFC3339
  createdAt: string; // RFC3339
}

// HvConnInput is the body for POST /vm/connections (create) and
// POST /vm/connections/test (probe). `secret` is the plaintext credential sent
// once; it is sealed server-side and never returned. `enabled` is omitted by the
// test probe (it does not persist anything).
export interface HvConnInput {
  name: string;
  kind: HvConnKind;
  endpoint: string;
  username: string;
  secret: string;
  insecureTls: boolean;
  enabled?: boolean;
}

// StorageBackendType selects which pluggable storage backend a row targets:
// SAN/NAS (nfs|iscsi|smb, realized as a libvirt pool) or a cloud object store
// (azureblob|s3, for images/ISO/backups).
export type StorageBackendType = "nfs" | "iscsi" | "smb" | "azureblob" | "s3";

// StorageBackendStatus reflects the last connectivity probe result.
export type StorageBackendStatus = "pending" | "connected" | "error";

// StorageBackend is the SAFE projection from GET /storage/backends: it carries
// hasSecret (whether a credential is stored) but NEVER the secret itself. Field
// names match the api storageBackendView json tags.
export interface StorageBackend {
  id: string;
  name: string;
  type: StorageBackendType;
  endpoint: string;
  target: string;
  username: string;
  hasSecret: boolean;
  region?: string;
  providerId?: string;
  options?: string;
  enabled: boolean;
  status: StorageBackendStatus;
  lastError?: string;
  lastSeenAt?: string;
  createdAt: string;
}

// StorageBackendInput is the body for POST /storage/backends (create) and
// POST /storage/backends/test (probe). `secret` is the plaintext credential sent
// once; it is sealed server-side and never returned.
export interface StorageBackendInput {
  name: string;
  type: StorageBackendType;
  endpoint: string;
  target: string;
  username: string;
  secret: string;
  region: string;
  providerId: string;
  options: string;
  enabled: boolean;
}

// VM provider capability tokens. Open string union so an unknown token from a
// newer backend still type-checks (we only branch on known ones).
export type VMCapability =
  | "list_hosts"
  | "list_vms"
  | "get_vm"
  | "list_clusters"
  | "list_storage"
  | "list_networks"
  | "create_vm"
  | "power_start"
  | "power_stop"
  | "power_reset"
  | "power_suspend" // resume shares this bit (no separate power_resume token)
  | "delete_vm"
  | "reconfigure_vm"
  | "snapshot"
  | "revert_snapshot"
  | "clone"
  | "migrate"
  | "export"
  | "cluster_topology"
  | "node_state"
  | "metrics"
  | "events"
  | "console"
  | "network_write"
  | "storage_write"
  | "hotplug"
  | "disk_resize"
  | "guest_agent"
  | "maintenance"
  | "templates"
  | "resource_control" // Lot 5A: CPU/mem reservation/limit/shares (<cputune>/<memtune>)
  | "disk_qos" // Lot 5A: per-disk IOPS/bandwidth throttle (<iotune>)
  | "storage_migrate" // Lot 5A: live storage migration (DomainBlockCopy + pivot)
  | "readonly"
  | (string & {});

// One virtual disk attached to a VM (matches vprovider.Disk).
export interface VMDisk {
  id: string;
  label?: string;
  format?: string;
  capacityGb: number;
  storageId?: string;
  path?: string;
}

// One virtual NIC attached to a VM (matches vprovider.NIC).
export interface VMNic {
  id: string;
  mac?: string;
  networkId?: string;
  model?: string;
  connected?: boolean;
}

// VM is the normalized guest summary (list rows + inventory.vms).
export interface VM {
  id: string;
  name: string;
  kind: string; // provider kind token (e.g. "vsphere", "proxmox", "libvirt")
  providerId: string;
  hostId?: string;
  clusterId?: string;
  state: VMState;
  stateRaw?: string;
  vcpus: number;
  memoryMb: number;
  guestOs?: string;
  firmware?: string;
  disks?: VMDisk[];
  nics?: VMNic[];
  ipAddresses?: string[];
  labels?: Record<string, string>;
  snapshotCount: number;
  createdAt?: string; // RFC3339
  protected: boolean;
}

// VMDetail adds the hypervisor-native inspect document (opaque JSON).
export interface VMDetail extends VM {
  raw: unknown;
}

// One VM provider (hypervisor connection) + its capability list.
export interface VMProvider {
  id: string;
  kind: string;
  capabilities: VMCapability[];
}

// One snapshot in a VM's snapshot tree (matches vprovider.Snapshot).
export interface VMSnapshot {
  id: string;
  vmId?: string;
  name: string;
  description?: string;
  createdAt?: string; // RFC3339
  parentId?: string;
  hasMemory?: boolean;
  isCurrent?: boolean;
}

// A hypervisor host (physical node running the hypervisor). Matches vprovider.Host:
// memory is reported in MB (memoryMb), not bytes.
export interface VMHost {
  id: string;
  name: string;
  kind?: string;
  providerId: string;
  clusterId?: string;
  state?: string;
  cpuCores?: number;
  cpuMhz?: number;
  memoryMb?: number;
  memUsedMb?: number;
  vmCount?: number;
  version?: string;
}

// A cluster of hypervisor hosts (matches vprovider.Cluster). The backend returns
// the member host ids (hostIds), not pre-computed counts.
export interface VMCluster {
  id: string;
  name: string;
  kind?: string;
  providerId: string;
  hostIds?: string[];
  haEnabled?: boolean;
  drsEnabled?: boolean;
}

// One node in a cluster topology (matches vprovider.NodeState).
export interface VMClusterNode {
  nodeId: string;
  state?: string;
  message?: string;
  vmCount?: number;
  updatedAt?: string; // RFC3339
}

// Cluster topology (matches vprovider.Topology): nodes[] + a vmId->nodeId
// placement map.
export interface VMClusterTopology {
  clusterId: string;
  nodes: VMClusterNode[];
  placement?: Record<string, string>;
}

// A datastore / storage pool exposed by a VM provider (matches vprovider.StoragePool).
// Capacity is reported in GB (capacityGb/freeGb), not bytes.
export interface VMStorage {
  id: string;
  name: string;
  kind?: string;
  providerId: string;
  type?: string;
  capacityGb?: number;
  freeGb?: number;
  hostIds?: string[];
  accessible?: boolean;
}

// A virtual network / port-group exposed by a VM provider (matches vprovider.Network).
export interface VMNetwork {
  id: string;
  name: string;
  kind?: string;
  providerId: string;
  type?: string;
  vlan?: number;
}

// VMNetworkType is the accepted set for a created virtual network.
export type VMNetworkType = "bridge" | "nat" | "vlan" | "isolated";

// Body for POST /vm/providers/{pid}/networks (create a virtual network). bridge
// is meaningful for type=bridge; vlan for type=vlan; cidr for nat/isolated.
export interface VMNetworkCreateRequest {
  name: string;
  type: VMNetworkType;
  bridge?: string;
  vlan?: number;
  cidr?: string;
  hostId?: string;
}

// One storage volume / virtual disk in a pool (GET /storage/{id}/volumes).
// isIso marks an uploaded ISO image (selectable as a boot medium in the wizard).
export interface Volume {
  id: string;
  name: string;
  storageId: string;
  format?: string;
  capacityGb: number;
  allocGb?: number;
  isIso: boolean;
  path?: string;
}

// Body for POST /vm/providers/{pid}/storage/{storageID}/volumes (create a disk).
export interface VolumeCreateRequest {
  name: string;
  capacityGb: number;
  format: string;
}

// A graphical console endpoint for a VM (GET .../console). For vnc/spice the
// host:port connect a remote-framebuffer client (a one-shot password may be
// present); for rdp the host:port feed a generated .rdp file. tlsPort/path are
// hypervisor-specific (e.g. a websocket bridge path).
export interface ConsoleEndpoint {
  kind: "vnc" | "spice" | "rdp";
  host: string;
  port: number;
  password?: string;
  tlsPort?: number;
  path?: string;
}

// One metrics sample for a VM (matches vprovider.MetricSample). Memory is
// reported as used/limit byte counts (not a precomputed percentage).
export interface VMMetricSample {
  timestamp: string; // RFC3339
  cpuPercent?: number;
  memUsageBytes?: number;
  memLimitBytes?: number;
  netRxBytes?: number;
  netTxBytes?: number;
  diskReadBytes?: number;
  diskWriteBytes?: number;
}

// Response of GET /vm/providers/{pid}/vms/{vmId}/metrics.
export interface VMMetricsResponse {
  entityId: string;
  samples: VMMetricSample[];
}

// A long-running hypervisor task returned by power/lifecycle mutations.
export interface VMTask {
  id: string;
  state?: string;
  progress?: number;
  message?: string;
  error?: string;
}

// Body for POST /vm/providers/{pid}/vms/{vmId}/snapshots.
export interface VMSnapshotCreateRequest {
  name: string;
  description?: string;
  memory?: boolean;
  quiesce?: boolean;
}

// Body for POST /vm/providers/{pid}/vms/{vmId}/clone.
export interface VMCloneRequest {
  name: string;
  hostId?: string;
  storageId?: string;
  linked?: boolean;
  powerOn?: boolean;
}

// Body for POST /vm/providers/{pid}/vms/{vmId}/migrate (intra-hypervisor).
export interface VMMigrateRequest {
  targetHost: string;
  live?: boolean;
  targetStorage?: string;
}

// Body for POST /vm/providers/{pid}/vms/{vmId}/reconfigure.
export interface VMReconfigureRequest {
  vcpus?: number;
  memoryMb?: number;
  resources?: ResourceSpec; // Lot 5A: CPU/mem reservation/limit/shares
}

// CPU/memory resource allocation (Lot 5A) — vSphere-style reservation/limit/shares
// (libvirt <cputune>/<memtune>). Zero/omitted fields mean "unset / hypervisor
// default". POST .../vms/{vmId}/resources, and also accepted inside a reconfigure.
export interface ResourceSpec {
  cpuShares?: number;
  cpuReservationMhz?: number;
  cpuLimitMhz?: number;
  memoryShares?: number;
  memoryReservationMb?: number;
  memoryLimitMb?: number;
}

// Per-disk QoS (Lot 5A) — IOPS / bandwidth throttling (libvirt <iotune>). 0 =
// unlimited. POST .../vms/{vmId}/disks/{diskId}/qos. total* take precedence over the
// read/write pair when both are set.
export interface DiskQoS {
  readIops?: number;
  writeIops?: number;
  totalIops?: number;
  readBytesSec?: number;
  writeBytesSec?: number;
  totalBytesSec?: number;
}

// Body for POST .../vms/{vmId}/disks/{diskId}/migrate — live storage migration of a
// running VM's disk to another storage pool (no downtime; DomainBlockCopy + pivot).
export interface VMStorageMigrateRequest {
  targetStorageId: string;
}

// A resource pool (Lot 5A): a named, provider-scoped grouping of VMs with an
// AGGREGATE CPU/memory shares + limit budget. Members join via the unihv.pool=<id>
// label. The budget is an advisory/reported allocation contract (plain libvirt has no
// native parent-cgroup pool). The view adds live member count + aggregate usage.
export interface ResourcePool {
  id: string;
  name: string;
  providerId: string;
  cpuShares: number;
  cpuLimitMhz: number;
  memShares: number;
  memLimitMb: number;
  notes?: string;
  createdAt: number;
  updatedAt: number;
  memberVmIds: string[];
  memberCount: number;
  usedVcpus: number;
  usedMemoryMb: number;
  note?: string;
}

// Create/update body for a resource pool.
export interface ResourcePoolInput {
  name: string;
  providerId: string;
  cpuShares?: number;
  cpuLimitMhz?: number;
  memShares?: number;
  memLimitMb?: number;
  notes?: string;
}

// Body for POST /vm/providers/{pid}/vms/{vmId}/disks/{diskId}/resize. Online
// disk grow only — the backend returns 422 if capacityGb is <= the current size.
export interface VMDiskResizeRequest {
  capacityGb: number;
}

// Guest-agent reported info (GET .../guest). agentConnected is false on demo /
// agent-less VMs — the UI then shows a subtle "not connected" hint rather than
// an error, and hostname / ipAddresses are best-effort (may be empty).
export interface GuestInfo {
  hostname?: string;
  osName?: string;
  agentConnected: boolean;
  ipAddresses?: string[];
}

// One disk in a VMSpec (wizard). capacityGb is required; format/storageId pick
// the on-disk format + target pool; sourcePath clones from an existing image.
export interface VMSpecDisk {
  capacityGb: number;
  format?: string;
  storageId?: string;
  sourcePath?: string;
  provisioning?: string; // "thin" | "thick"
  discard?: boolean;     // TRIM/UNMAP passthrough
}

// One NIC in a VMSpec (wizard). networkId references a GET /networks entry.
export interface VMSpecNic {
  networkId: string;
  model?: string;
  mac?: string;
}

// Body for POST /vm/providers/{pid}/vms (create a VM; admin). Mirrors the
// backend VMSpec contract: structured disks[] + nics[] plus an optional boot ISO
// (a volume path/id from the storage library).
export interface VMSpec {
  name: string;
  hostId?: string;
  clusterId?: string;
  vcpus: number;
  memoryMb: number;
  guestOs?: string;
  firmware?: "bios" | "uefi";
  disks: VMSpecDisk[];
  nics: VMSpecNic[];
  bootIso?: string;
  // tpm requests an emulated TPM 2.0 device (vTPM) — required for Windows 11.
  tpm?: boolean;
  // secureBoot enables UEFI Secure Boot (implies UEFI firmware + signed OVMF).
  secureBoot?: boolean;
  // cloudInit, when set, generates a NoCloud seed ISO (cloud-init guest
  // customization) attached to the VM. Requires a cloud-init-enabled guest image.
  cloudInit?: CloudInitSpec;
  // cpu, when set, pins an explicit CPU topology (sockets/cores/threads) + model.
  // vcpus is then derived from sockets*cores*threads. Omit for a flat vCPU count.
  cpu?: CPUSpec;
  // isTemplate marks the created VM as a golden-image TEMPLATE (not run as-is; the
  // source for clone-from-template).
  isTemplate?: boolean;
  // sysprep, when set, generates a Windows autounattend.xml seed ISO (the Windows
  // analogue of cloud-init) attached to the VM. Requires a Windows guest image.
  sysprep?: SysprepSpec;
}

// CPUSpec pins an explicit CPU topology + model. Mirrors vprovider.CPUSpec. An
// empty model means the hypervisor default (KVM: host-passthrough).
export interface CPUSpec {
  sockets?: number;
  coresPerSocket?: number;
  threadsPerCore?: number;
  model?: string;
}

// SysprepSpec is Windows guest customization applied via an autounattend.xml seed
// ISO. Mirrors vprovider.SysprepSpec. All fields optional; requires a Windows guest.
export interface SysprepSpec {
  computerName?: string;
  adminPassword?: string;
  productKey?: string;
  orgName?: string;
  timeZone?: string;
  locale?: string;
  // unattendXmlExtra is optional raw extra unattend settings appended (advanced).
  unattendXmlExtra?: string;
}

// CloudInitSpec is guest customization applied via a NoCloud seed ISO. Mirrors the
// backend vprovider.CloudInitSpec. All fields are optional; an empty spec seeds a
// minimal datasource. Requires a cloud-init-enabled guest image (e.g. an Ubuntu/
// Debian/Fedora/RHEL cloud image) for any of this to take effect on first boot.
export interface CloudInitSpec {
  // hostname sets the guest hostname / fqdn (local-hostname in meta-data).
  hostname?: string;
  // username creates a sudo user on first boot.
  username?: string;
  // password is the plaintext password for username (the guest hashes it).
  password?: string;
  // sshAuthorizedKeys are public keys installed for username.
  sshAuthorizedKeys?: string[];
  // runCmd is a list of shell commands run once on first boot.
  runCmd?: string[];
  // networkConfig is optional raw cloud-init network-config v2 YAML ("" = DHCP).
  networkConfig?: string;
  // userDataExtra is optional raw extra #cloud-config appended (advanced).
  userDataExtra?: string;
}

// VM power operation tokens (path segment for the power endpoint).
export type VMPowerOp = "start" | "stop" | "reset" | "suspend" | "resume";

/* ===================== Bulk operations (Lot 4B) ===================== */

// One VM in a bulk request: its owning provider + vm id.
export interface VMBulkTarget {
  providerId: string;
  vmId: string;
}

// POST /vm/bulk body. action is the verb; op qualifies a power action.
export interface VMBulkRequest {
  action: "power" | "snapshot" | "delete";
  op?: VMPowerOp;
  name?: string; // optional snapshot name
  force?: boolean; // delete: force-destroy a running VM
  targets: VMBulkTarget[];
}

// Per-target outcome.
export interface VMBulkResult {
  providerId: string;
  vmId: string;
  ok: boolean;
  error?: string;
  taskId?: string;
}

// Aggregated bulk fan-out response.
export interface VMBulkResponse {
  action: string;
  op?: string;
  succeeded: number;
  failed: number;
  results: VMBulkResult[];
}

/* ===================== API tokens (scoped PATs, Lot 4B) ===================== */

// A persisted API-token record (metadata only; the raw value is NEVER returned
// after creation). expiresAt/lastUsedAt are unix epoch seconds.
export interface ApiTokenRecord {
  id: string;
  name: string;
  userId: string;
  permissions: string[];
  expiresAt?: number;
  lastUsedAt?: number;
  createdAt: number;
}

// POST /tokens body.
export interface ApiTokenCreateRequest {
  name: string;
  scopes: string[];
  expiresInDays?: number;
}

// POST /tokens response: the record PLUS the raw token shown exactly ONCE.
export interface ApiTokenCreateResponse extends ApiTokenRecord {
  token: string;
}

/* ===================== Unified inventory (single pane of glass) ===================== */

// Aggregated counts across the VM + container worlds. Field names match the
// backend Unified.counts struct verbatim.
export interface InventoryCounts {
  vms: number;
  vmsRunning: number;
  hosts: number;
  clusters: number;
  containers: number;
  containersUp: number;
  hypervisorProviders: number;
  containerHosts: number;
}

// One degraded provider/host entry on the unified inventory (best-effort
// surfaced; fields are advisory).
export interface InventoryDegraded {
  id: string;
  kind?: string;
  message?: string;
}

// Response of GET /inventory — the unified single-pane snapshot.
export interface Inventory {
  vms: VM[];
  hosts: VMHost[];
  clusters: VMCluster[];
  storage: VMStorage[];
  networks: VMNetwork[];
  workloads: Workload[];
  counts: InventoryCounts;
  degraded: InventoryDegraded[];
  generatedAt: string; // RFC3339
}

/* ===================== FinOps (unified cost & rightsizing) ===================== */

// The configurable rate card driving cost estimates (GET/PUT /finops/ratecard).
// All rates are per-hour except storage, which is per-GiB per-month. Field names
// match the backend finops.RateCard json tags verbatim.
export interface RateCard {
  currency: string;
  vcpuHour: number;
  gbRamHour: number;
  gbStorageMonth: number;
  containerVcpuHour: number;
  containerGbRamHour: number;
}

// Per-dimension hourly cost split for one entity.
export interface CostBreakdown {
  cpuHour: number;
  ramHour: number;
  storageHour: number;
}

// One priced entity (a VM or a container) in the cost view.
export interface EntityCost {
  id: string;
  name: string;
  domain: "vm" | "container";
  kind: string;
  providerId: string;
  hostId?: string;
  clusterId?: string;
  running: boolean;
  vcpus: number;
  memoryGb: number;
  storageGb: number;
  breakdown: CostBreakdown;
  hourlyCost: number;
  monthlyCost: number;
}

// A rolled-up cost for a grouping key (domain / hypervisor / cluster / host).
export interface GroupCost {
  key: string;
  label?: string;
  count: number;
  hourlyCost: number;
  monthlyCost: number;
}

// Response of GET /finops/summary — the cost overview.
export interface FinOpsSummary {
  currency: string;
  totalHourly: number;
  totalMonthly: number;
  vmHourly: number;
  containerHourly: number;
  entities: number;
  runningEntities: number;
  byDomain: GroupCost[];
  byHypervisor: GroupCost[];
  byCluster: GroupCost[];
  byHost: GroupCost[];
  topSpenders: EntityCost[];
  potentialMonthlySavings: number;
  recommendations: number;
}

// Observed utilization summary backing a rightsizing recommendation.
export interface RightsizeUtilization {
  samples: number;
  avgCpuPercent: number;
  peakCpuPercent: number;
  avgMemPercent: number;
  peakMemPercent: number;
}

// One rightsizing recommendation with projected savings.
export interface Recommendation {
  entityId: string;
  name: string;
  domain: "vm" | "container";
  kind: string;
  providerId: string;
  hostId?: string;
  clusterId?: string;
  recommendation: "idle" | "oversized";
  severity: "info" | "warn" | "critical";
  currentVcpus: number;
  currentMemGb: number;
  suggestedVcpus: number;
  suggestedMemGb: number;
  utilization: RightsizeUtilization;
  currentMonthly: number;
  projectedMonthly: number;
  monthlySavings: number;
  rationale: string;
}

// Response of GET /finops/rightsizing.
export interface RightsizingResponse {
  recommendations: Recommendation[];
  potentialMonthlySavings: number;
  currency: string;
}

/* ===================== Insights (drift / health / best-practice feed) ===================== */

export type InsightSeverity = "critical" | "warn" | "info";
export type InsightCategory = "resilience" | "reclaim" | "housekeeping" | "health";

// One finding in the insights feed.
export interface Insight {
  id: string;
  rule: string;
  severity: InsightSeverity;
  category: InsightCategory;
  title: string;
  detail: string;
  suggestion: string;
  entityId?: string;
  entityName?: string;
  entityType?: string;
  providerId?: string;
  kind?: string;
}

// Response of GET /insights.
export interface InsightsFeed {
  insights: Insight[];
  counts: Record<InsightSeverity, number>;
  generatedAt: string; // RFC3339
}

/* ===================== Alarms (vSphere-style threshold rules) ===================== */

export type AlarmSeverity = "info" | "warning" | "critical";
export type AlarmTarget = "vm" | "host" | "datastore";
export type AlarmMetric = "cpu" | "memory" | "disk" | "storage_pct" | "state";
export type AlarmComparator = "gt" | "lt" | "eq";
export type AlarmChannelType = "webhook" | "email-stub";

// A user-defined alarm definition (GET/POST /alarms/definitions).
export interface AlarmDefinition {
  id: string;
  name: string;
  target: AlarmTarget;
  metric: AlarmMetric;
  comparator: AlarmComparator;
  threshold: number;
  stateValue?: string;
  durationSec: number;
  severity: AlarmSeverity;
  enabled: boolean;
  notifyChannelIds: string[];
  createdAt: number;
  updatedAt: number;
}

// Body for create/update an alarm definition.
export interface AlarmDefinitionInput {
  name: string;
  target: AlarmTarget;
  metric: AlarmMetric;
  comparator: AlarmComparator;
  threshold: number;
  stateValue?: string;
  durationSec: number;
  severity: AlarmSeverity;
  enabled: boolean;
  notifyChannelIds: string[];
}

// A currently-firing alarm instance (GET /alarms/active).
export interface AlarmInstance {
  id: string;
  definitionId: string;
  definitionName: string;
  objectId: string;
  objectName: string;
  objectType: AlarmTarget;
  severity: AlarmSeverity;
  metric: AlarmMetric;
  state: "active" | "cleared";
  value: number;
  stateRaw?: string;
  raisedAt: string; // RFC3339
  clearedAt?: string;
  lastNotifiedAt?: string;
}

// A notification channel (GET/POST /alarms/channels).
export interface AlarmChannel {
  id: string;
  name: string;
  type: AlarmChannelType;
  config: string;
  createdAt: number;
  updatedAt: number;
}

export interface AlarmChannelInput {
  name: string;
  type: AlarmChannelType;
  config: string;
}

/* ===================== V2V migration (cross-hypervisor) ===================== */

// Body for POST /v2v/preflight and POST /v2v/migrate. The same shape drives
// both: preflight validates it, migrate enqueues the job.
export interface V2VRequest {
  sourceProviderId: string;
  sourceVmId: string;
  targetProviderId: string;
  targetHostId?: string;
  targetStorageId?: string;
  targetName?: string;
  // Matches the backend migrateRequestBody.powerOnAfter json tag.
  powerOnAfter?: boolean;
}

// Response of POST /v2v/preflight. ok=false with a populated issues[] means the
// migration is blocked (not an HTTP error). source/target Format and Kind
// describe the disk-format conversion that will happen.
export interface V2VPreflightResult {
  ok: boolean;
  issues: string[];
  sourceFormat: string;
  targetFormat: string;
  sourceKind: string;
  targetKind: string;
}

// Response of POST /v2v/migrate (the enqueued job id).
export interface V2VMigrateResponse {
  id: string;
}

// V2V job phases (lower-case, as the backend reports them). Open union for
// forward-compat.
export type V2VPhase =
  | "queued"
  | "export"
  | "convert"
  | "transfer"
  | "import"
  | "finalize"
  | "done"
  | "failed"
  | (string & {});

// Progress of a V2V job (GET /v2v/jobs and /v2v/jobs/{id}).
export interface V2VProgress {
  id: string;
  phase: V2VPhase;
  percent: number;
  message?: string;
  sourceProviderId?: string;
  sourceVmId?: string;
  targetProviderId?: string;
  targetVmId?: string;
  error?: string;
  startedAt?: string; // RFC3339
  updatedAt?: string; // RFC3339
}

/* ===================== Replication (cross-hypervisor DR) ===================== */

// Live DR status of a policy (mirrors replication.Status).
export type ReplicationStatus = "idle" | "syncing" | "error" | "degraded" | "failed_over";

// One replication cycle outcome (ring-buffer history entry).
export interface ReplicationCycle {
  startedAt: string; // RFC3339
  finishedAt: string; // RFC3339
  durationMs: number;
  bytesTransferred: number;
  replicaVmId?: string;
  ok: boolean;
  error?: string;
  firstCycle: boolean;
}

// The engine's live, observable DR state for a policy.
export interface ReplicationState {
  status: ReplicationStatus;
  lastSyncAt?: string; // RFC3339 (zero-value omitted)
  lastDurationMs: number;
  bytesTransferred: number; // cumulative
  replicaVmId?: string;
  lastError?: string;
  measuredRpoSeconds: number; // achieved RPO (now - lastSyncAt)
  rpoTargetSeconds: number; // policy interval
  cycleCount: number;
  history: ReplicationCycle[];
}

// The create-policy request body.
export interface ReplicationPolicyInput {
  name: string;
  sourceProviderId: string;
  sourceVmId: string;
  targetProviderId: string;
  targetHostId?: string;
  intervalSeconds: number; // RPO target
  retain: number;
  enabled: boolean;
}

// The durable policy row merged with the live engine state.
export interface ReplicationPolicyView {
  id: string;
  name: string;
  sourceProviderId: string;
  sourceVmId: string;
  targetProviderId: string;
  targetHostId?: string;
  intervalSeconds: number;
  retain: number;
  enabled: boolean;
  status: string;
  lastSyncAt?: number; // unix seconds
  replicaVmId?: string;
  lastError?: string;
  createdAt: number;
  updatedAt: number;
  state?: ReplicationState;
}

/* ===================== Scheduled VM backups (Lot 5B) ===================== */

// One stored disk image inside a backup.
export interface VMBackupDisk {
  key: string;
  sizeBytes: number;
  format?: string;
}

// VMBackup is a produced backup artifact set (GET /vm-backups): a point-in-time
// snapshot of a VM whose disk(s) were exported (qcow2) and pushed to a storage
// backend under keyPrefix. status is pending|completed|error.
export interface VMBackup {
  id: string;
  vmId: string;
  vmName?: string;
  providerId: string;
  backendId: string;
  policyId?: string;
  keyPrefix: string;
  sizeBytes: number;
  diskCount: number;
  disks?: VMBackupDisk[];
  guestOs?: string;
  firmware?: string;
  status: "pending" | "completed" | "error" | (string & {});
  error?: string;
  createdAt: number;
}

// Body for POST /vm-backups/run ("Back up now").
export interface VMBackupRunInput {
  providerId: string;
  vmId: string;
  backendId: string;
}

// Body for POST /vm-backups/{id}/restore (import a backup as a NEW VM).
export interface VMBackupRestoreInput {
  targetProviderId: string;
  targetHostId?: string;
  targetName?: string;
  powerOn?: boolean;
}

// VMBackupPolicy is a scheduled-backup definition (GET /vm-backup-policies).
export interface VMBackupPolicy {
  id: string;
  name: string;
  providerId: string;
  vmId: string;
  backendId: string;
  intervalSeconds: number;
  retentionCount: number;
  enabled: boolean;
  status: string;
  lastRunAt?: number;
  lastError?: string;
  createdAt: number;
  updatedAt: number;
}

// Body for POST /vm-backup-policies (create).
export interface VMBackupPolicyInput {
  name: string;
  providerId: string;
  vmId: string;
  backendId: string;
  intervalSeconds: number;
  retentionCount: number;
  enabled: boolean;
}

/* ===================== Error envelope ===================== */

export type ApiErrorCode =
  | "bootstrap_required"
  | "unauthenticated"
  | "aal_required"
  | "forbidden"
  | "csrf_failed"
  | "not_found"
  | "method_not_allowed"
  | "protected_resource"
  | "conflict"
  | "validation_failed"
  | "rate_limited"
  | "internal"
  | "account_locked";

export interface ApiErrorEnvelope {
  error: {
    code: ApiErrorCode | string;
    message: string;
    requestId: string;
  };
}

/* ===================== Generic action result ===================== */

export interface ActionResult {
  ok: boolean;
}

/* ===================== WS envelope (ADR-001) ===================== */

export type WsType = "subscribe" | "unsubscribe" | "data" | "ack" | "error" | "end";
export type WsChannel = "stats" | "logs" | "events" | "exec";
export type WsRefKind = "container" | "service" | "node" | "task" | "pod";

export interface WsRef {
  kind: WsRefKind;
  id: string;
}

export interface WsExecSubscribePayload {
  cmd: string[];
  tty: boolean;
  env?: string[];
  workingDir?: string;
  // K8s pods: which container in a multi-container pod to exec into ("" / omitted
  // => the pod's default/first container). Ignored by the Docker provider.
  container?: string;
}

// Optional payload for a logs subscribe frame. tail bounds the initial backlog;
// container selects which container's logs to stream for a K8s multi-container
// pod ("" / omitted => first/default container; Docker ignores it).
export interface WsLogsSubscribePayload {
  tail?: number;
  container?: string;
}

export interface WsLogsPayload {
  stream: "stdout" | "stderr";
  line: string;
}

export interface WsEventsPayload {
  action: string;
  kind: "container" | "network" | "volume";
  id: string;
  snapshotDelta?: unknown;
}

export interface WsExecOutPayload {
  stream?: "stdout" | "stderr";
  data?: string;
  exitCode?: number;
}

export interface WsErrorPayload {
  code: string;
  message: string;
}

export interface WsEnvelope<P = unknown> {
  v: 1;
  type: WsType;
  channel?: WsChannel;
  subId?: string;
  hostId?: string;
  ref?: WsRef;
  ts?: string;
  payload?: P | null;
}

/* ===================== Marketplace: app templates + deploy ===================== */

// TemplateEnvVar is one environment variable in a marketplace template.
export interface TemplateEnvVar {
  key: string;
  value: string;
  required: boolean;
}

// TemplateSource marks where a marketplace template comes from.
export type TemplateSource = "builtin" | "custom";

// Template is a unified marketplace catalog entry from GET /templates. Built-in
// entries have an empty `id` and carry their UI logo path in `logo`; custom
// entries carry the row id and the operator-supplied logo URL in `logo`.
// `logo` is "" when there is no logo (UI renders an initials fallback).
export interface Template {
  id: string;
  source: TemplateSource;
  name: string;
  slug: string;
  category: string;
  image: string;
  description: string;
  ports: number[];
  env: TemplateEnvVar[];
  volumes: string[];
  logo: string;
  createdAt?: number; // unix epoch seconds; present for custom only
}

// TemplateWriteRequest is the POST /templates and PUT /templates/{id} body for
// a custom template (admin).
export interface TemplateWriteRequest {
  name: string;
  slug: string;
  category: string;
  image: string;
  description: string;
  ports: number[];
  env: TemplateEnvVar[];
  volumes: string[];
  logoUrl: string;
}

// DeployPortMap is a single host:container port publication. host=0 lets the
// daemon pick an ephemeral host port; proto defaults to "tcp".
export interface DeployPortMap {
  host: number;
  container: number;
  proto: string; // "tcp" | "udp"
}

// DeployVolMount is a single mount: source is a named volume or absolute host
// path (bind); target is the absolute in-container path.
export interface DeployVolMount {
  source: string;
  target: string;
}

// DeployRequest is the POST /hosts/{hostID}/templates/deploy body. Supply either
// templateSlug (resolved against built-in + custom catalogs) or an inline image.
// ports/env/volumes override the template defaults when provided.
export interface DeployRequest {
  templateSlug?: string;
  image?: string;
  name?: string;
  env?: Record<string, string>;
  ports?: DeployPortMap[];
  volumes?: DeployVolMount[];
  labels?: Record<string, string>;
  restartPolicy?: string; // "" | "no" | "always" | "on-failure" | "unless-stopped"

  // Optional resource limits/reservations (<=0 / omitted means "unset"). cpu*
  // are CPU cores (like `docker run --cpus`); memory* are bytes. cpuReservation
  // maps to CPUShares (relative weight), not a hard floor.
  cpuLimit?: number;
  memoryLimitBytes?: number;
  cpuReservation?: number;
  memoryReservationBytes?: number;

  // Admin-only opt-in to permit host bind mounts. Ignored (and rejected with 403)
  // for non-admins; the always-blocked host paths (docker.sock, /, /etc, ...) are
  // rejected even for admins. Omit it for the normal named-volume path.
  allowHostMounts?: boolean;
}

// DeployResponse is returned on a successful one-click deploy.
export interface DeployResponse {
  ok: boolean;
  containerId: string;
  name: string;
  image: string;
}

// DeployTemplateRequest is an alias of DeployRequest — the body for the
// one-click template deploy (POST /hosts/{hostID}/templates/deploy).
export type DeployTemplateRequest = DeployRequest;

/* ===================== Marketplace: registries + remote catalogs (admin) ===================== */

// RegistryType is the accepted set for a registry credential.
export type RegistryType = "dockerhub" | "ghcr" | "gitlab" | "quay" | "ecr" | "custom";

// Registry is the SAFE projection from GET /registries: it carries hasSecret
// (whether a credential is stored) but NEVER the secret itself. Field names match
// the api registryView json tags.
export interface Registry {
  id: string;
  name: string;
  type: RegistryType;
  url: string;
  username: string;
  email: string;
  hasSecret: boolean;
  createdAt: number; // unix epoch seconds
}

// RegistryInput is the POST /registries body. On create, omit/empty `secret`
// stores no credential. On PUT /registries/{id} the same shape is used, but
// `secret` is optional with three-state semantics: undefined => keep the stored
// credential, "" => clear it, a value => replace it.
export interface RegistryInput {
  name: string;
  type: RegistryType;
  url: string;
  username: string;
  secret?: string;
  email: string;
}

// RegistryTestResult is the body of POST /registries/{id}/test (a failed login is
// a normal ok:false result, not an HTTP error).
export interface RegistryTestResult {
  ok: boolean;
  message: string;
}

// RemoteCatalog mirrors store.RemoteCatalog: an external template catalog served
// as JSON at `url`, whose templates are merged into the marketplace as
// source="remote:<name>". lastFetchedAt/lastError are null until the first
// refresh.
export interface RemoteCatalog {
  id: string;
  name: string;
  url: string;
  enabled: boolean;
  lastFetchedAt: number | null; // unix epoch seconds
  templateCount: number;
  lastError: string | null;
  createdAt: number; // unix epoch seconds
}

// CatalogInput is the POST /catalogs and PUT /catalogs/{id} body. `enabled`
// defaults to true on create when omitted.
export interface CatalogInput {
  name: string;
  url: string;
  enabled?: boolean;
}

/* ===================== Enterprise SSO: LDAP + OIDC (Entra ID) ===================== */

// AuthProviderKind selects which subset of fields is meaningful: an LDAP/LDAPS
// directory or an OIDC (Microsoft Entra ID) provider.
export type AuthProviderKind = "ldap" | "oidc";

// LDAPTLSMode is the transport security for an LDAP provider.
export type LDAPTLSMode = "none" | "starttls" | "ldaps";

// PublicAuthProvider is the secret-free projection from GET /auth/providers
// (PUBLIC, pre-auth) used by the login screen to render "Sign in with <name>"
// buttons / an LDAP form. It exposes the bare minimum and never any config.
export interface PublicAuthProvider {
  id: string;
  name: string;
  kind: AuthProviderKind;
}

// AuthProvider is the SAFE admin projection from GET /admin/auth/providers — the
// full configuration EXCEPT the sealed secrets, which are reported only as the
// hasBindPassword / hasClientSecret booleans. Field names mirror authProviderView
// (server/internal/api/admin_auth.go) verbatim.
export interface AuthProvider {
  id: string;
  name: string;
  kind: AuthProviderKind;
  enabled: boolean;
  defaultRoleId: string;

  // LDAP
  ldapHost: string;
  ldapPort: number;
  ldapTls: LDAPTLSMode;
  ldapSkipVerify: boolean;
  ldapBindDn: string;
  hasBindPassword: boolean;
  ldapBaseDn: string;
  ldapUserFilter: string;
  ldapAttrUsername: string;
  ldapAttrEmail: string;
  ldapAttrDisplay: string;
  ldapGroupBaseDn: string;
  ldapGroupFilter: string;
  ldapAttrMember: string;

  // OIDC (Entra ID)
  oidcIssuer: string;
  oidcClientId: string;
  hasClientSecret: boolean;
  oidcRedirectUrl: string;
  oidcScopes: string;
  oidcGroupsClaim: string;
  oidcUsernameClaim: string;
  oidcEmailClaim: string;

  createdAt: number; // unix epoch seconds
  updatedAt: number; // unix epoch seconds
}

// AuthProviderInput is the POST /admin/auth/providers (create) and PUT
// /admin/auth/providers/{id} (update) body. Mirrors authProviderRequest. Secrets
// (ldapBindPassword / oidcClientSecret) use three-state pointer semantics on
// update: undefined => keep the stored secret, "" => clear it, a value => replace
// it. On create a non-empty value is sealed; omit/empty stores none. `kind` is
// immutable after create (the server ignores it on update).
export interface AuthProviderInput {
  name: string;
  kind: AuthProviderKind;
  enabled: boolean;
  defaultRoleId: string;

  // LDAP
  ldapHost: string;
  ldapPort: number;
  ldapTls: LDAPTLSMode;
  ldapSkipVerify: boolean;
  ldapBindDn: string;
  ldapBindPassword?: string;
  ldapBaseDn: string;
  ldapUserFilter: string;
  ldapAttrUsername: string;
  ldapAttrEmail: string;
  ldapAttrDisplay: string;
  ldapGroupBaseDn: string;
  ldapGroupFilter: string;
  ldapAttrMember: string;

  // OIDC (Entra ID)
  oidcIssuer: string;
  oidcClientId: string;
  oidcClientSecret?: string;
  oidcRedirectUrl: string;
  oidcScopes: string;
  oidcGroupsClaim: string;
  oidcUsernameClaim: string;
  oidcEmailClaim: string;
}

// GroupRoleMapping maps an external group (LDAP group DN/CN, or an Entra group
// object-id/name from the token's groups claim) to a Castor role. Mirrors
// groupMappingView. At login the union of a user's groups resolves to roles.
export interface GroupRoleMapping {
  id: string;
  providerId: string;
  externalGroup: string;
  roleId: string;
  createdAt: number; // unix epoch seconds
}

// CreateGroupRoleMappingInput is the POST /admin/auth/providers/{id}/mappings
// body (mirrors createMappingRequest).
export interface CreateGroupRoleMappingInput {
  externalGroup: string;
  roleId: string;
}

// ProviderTestResult is the body of POST /admin/auth/providers/{id}/test. A
// configuration/connectivity problem is a normal ok:false result (not an HTTP
// error). sampleUser is an LDAP sample entry DN when the search matched one.
export interface ProviderTestResult {
  ok: boolean;
  message: string;
  sampleUser?: string;
}
