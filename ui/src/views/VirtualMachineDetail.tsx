// ui/src/views/VirtualMachineDetail.tsx
//
// One VM: header (state + hardware), tabs Summary | Monitor | Configure |
// Permissions | Snapshots | Console | Inspect (canonical vSphere set per
// docs/plan/01-ux-navigation-icons-actions.md §4). Summary holds the gauges +
// hardware overview; Monitor draws CPU/Memory/Network/Disk series over the
// shared StatsChart plus this VM's audit events; Configure is the read-form of
// "Edit settings" (hardware + options) with Edit affordances opening the
// existing Reconfigure drawer; Permissions surfaces effective RBAC bindings;
// Snapshots/Console/Inspect unchanged. Header carries the lifecycle buttons.

import { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "../lib/auth";
import { useVM, useVMSnapshots, useVMMetrics, useVMGuest, useVMCapabilityLookup, useRoles, useUsers } from "../lib/hooks";
import { useVMActions } from "./useVMActions";
import { PageHeader } from "../components/PageHeader";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { VMStateBadge } from "../components/VMStateBadge";
import { VMActions } from "../components/VMActions";
import { ProtectedTag } from "../components/ProtectedTag";
import { ActionButton } from "../components/ActionButton";
import { CapabilityGate } from "../components/CapabilityGate";
import { DataTable, type Column } from "../components/DataTable";
import { StatsChart } from "../components/StatsChart";
import { ResourceGauge } from "../components/ResourceGauge";
import { ConsolePanel } from "../components/ConsolePanel";
import { InspectTab } from "./workload/InspectTab";
import { gateVMAction, gateVMConsole, gateVMHotPlug, gateVMDiskResize, gateVMGuestAgent } from "../lib/rbac";
import { formatBytes, formatDateTime, shortId, timeAgo, humanizeAction } from "../lib/format";
import {
  IconVM,
  IconDashboard,
  IconSnapshot,
  IconStats,
  IconInspect,
  IconRestart,
  IconConsole,
  IconCpu,
  IconMemory,
  IconDisc,
  IconDisk,
  IconNic,
  IconTrash,
  IconNetworks,
  IconHosts,
  IconShield,
  IconEdit,
  IconUsers,
  IconRoles,
  IconExternal,
  IconAudit,
} from "../components/icons";
import { StatusDot } from "../components/StatusDot";
import { toast, toastError } from "../lib/toast";
import { api } from "../lib/api";
import { useQueryClient } from "@tanstack/react-query";
import type { VMDisk, VMNic, VMSnapshot, VMDetail, AuditEntry, AuditResult } from "../lib/types";

type TabKey = "summary" | "monitor" | "configure" | "permissions" | "snapshots" | "console" | "inspect";

export function VirtualMachineDetail() {
  const params = useParams<{ pid: string; id: string }>();
  const navigate = useNavigate();
  const { permissions } = useAuth();
  const { capsForProvider } = useVMCapabilityLookup();

  const pid = decodeURIComponent(params.pid ?? "");
  const vmId = decodeURIComponent(params.id ?? "");

  const query = useVM(pid, vmId);
  const detail = query.data;
  const caps = capsForProvider(pid);

  const actions = useVMActions();
  const [tab, setTab] = useState<TabKey>("summary");

  if (query.isLoading) return <LoadingFill label="Loading virtual machine…" />;

  if (query.isError || !detail) {
    return (
      <div className="page">
        <PageHeader title="Virtual machine" />
        <EmptyState
          icon={<IconVM size={40} />}
          title="Virtual machine not found"
          message="It may have been removed, or you may not have access."
          action={
            <ActionButton variant="ghost" onClick={() => navigate("/vms")}>
              Back to virtual machines
            </ActionButton>
          }
        />
      </div>
    );
  }

  // The graphical console tab is shown only when the provider exposes a console
  // AND the user holds vm.console (same gate as the old modal button).
  const consoleGate = gateVMConsole(caps, permissions);

  const tabs: { key: TabKey; label: string; icon: JSX.Element }[] = [
    { key: "summary", label: "Summary", icon: <IconDashboard size={15} /> },
    { key: "monitor", label: "Monitor", icon: <IconStats size={15} /> },
    { key: "configure", label: "Configure", icon: <IconEdit size={15} /> },
    { key: "permissions", label: "Permissions", icon: <IconShield size={15} /> },
    { key: "snapshots", label: "Snapshots", icon: <IconSnapshot size={15} /> },
    ...(consoleGate.allowed
      ? [{ key: "console" as const, label: "Console", icon: <IconConsole size={15} /> }]
      : []),
    { key: "inspect", label: "Inspect", icon: <IconInspect size={15} /> },
  ];

  return (
    <div className="page">
      <PageHeader
        title={
          <span className="row" style={{ gap: "var(--sp-3)" }}>
            <span className="truncate" style={{ maxWidth: 520 }}>
              {detail.name}
            </span>
            <VMStateBadge state={detail.state} raw={detail.stateRaw} />
          </span>
        }
        subtitle={
          <span className="row" style={{ gap: "var(--sp-2)" }}>
            <span className="text-xs muted">{detail.kind}</span>
            <span className="mono text-xs">{shortId(detail.id)}</span>
            {detail.hostId ? <span className="text-xs muted">· {detail.hostId}</span> : null}
            {detail.protected ? <ProtectedTag /> : null}
          </span>
        }
        actions={
          <VMActions
            layout="bar"
            vm={detail}
            caps={caps}
            permissions={permissions}
            busy={actions.busyId === detail.id}
            size="md"
            onPower={actions.runPower}
            onSnapshot={actions.triggerSnapshot}
            onManageSnapshots={() => setTab("snapshots")}
            onClone={actions.triggerClone}
            onReconfigure={actions.triggerReconfigure}
            onMigrate={actions.triggerMigrate}
            onAddDisk={actions.triggerAddDisk}
            onAddNic={actions.triggerAddNic}
            onMountIso={actions.triggerMountIso}
            onEjectIso={actions.ejectIso}
            onDelete={actions.triggerDelete}
            onConsole={consoleGate.allowed ? () => setTab("console") : undefined}
            onRefresh={() => query.refetch()}
          />
        }
      />

      <div className="tabs">
        {tabs.map((t) => (
          <button key={t.key} className={`tab${tab === t.key ? " active" : ""}`} onClick={() => setTab(t.key)}>
            <span className="row" style={{ gap: 6 }}>
              {t.icon}
              {t.label}
            </span>
          </button>
        ))}
      </div>

      <div>
        {tab === "summary" && (
          <VMSummary
            pid={pid}
            vmId={vmId}
            detail={detail}
            guestAgent={gateVMGuestAgent(caps, permissions)}
            consoleAllowed={consoleGate.allowed}
            onLaunchConsole={() => setTab("console")}
            hotPlug={gateVMHotPlug(caps, permissions)}
            onDetachDisk={(id) => actions.detachDisk(detail, id)}
            onDetachNic={(id) => actions.detachNic(detail, id)}
            detachBusyId={actions.detachBusyId}
          />
        )}
        {tab === "snapshots" && (
          <SnapshotsPanel
            pid={pid}
            vmId={vmId}
            caps={caps}
            permissions={permissions}
            onCreate={() => actions.triggerSnapshot(detail)}
            onDelete={(snap) => actions.triggerDeleteSnapshot(detail, snap)}
          />
        )}
        {tab === "monitor" && <MetricsPanel pid={pid} vmId={vmId} detail={detail} />}
        {tab === "configure" && (
          <ConfigurePanel
            detail={detail}
            hotPlug={gateVMHotPlug(caps, permissions)}
            diskResize={gateVMDiskResize(caps, permissions)}
            onReconfigure={() => actions.triggerReconfigure(detail)}
            onAddDisk={() => actions.triggerAddDisk(detail)}
            onAddNic={() => actions.triggerAddNic(detail)}
            onMountIso={() => actions.triggerMountIso(detail)}
            onResizeDisk={(disk) => actions.triggerResizeDisk(detail, disk)}
          />
        )}
        {tab === "permissions" && <PermissionsPanel detail={detail} />}
        {/* Mounted only when active so no console socket opens in the background. */}
        {tab === "console" && consoleGate.allowed && <ConsolePanel pid={pid} vmId={vmId} />}
        {tab === "inspect" && <InspectTab raw={detail.raw} />}
      </div>

      {actions.dialogs}
    </div>
  );
}

/* ============================ Summary ============================ */
//
// vSphere "Summary" rendered in Castor cards: a console vignette, three resource
// gauges (CPU / Memory / Storage), a VM Hardware card, a General card and a
// Related Objects card — restructured from the old Overview content. Gauges read
// CPU/mem from the latest GetMetrics sample and storage from the disk capacities.

function VMSummary({
  pid,
  vmId,
  detail,
  guestAgent,
  consoleAllowed,
  onLaunchConsole,
  hotPlug,
  onDetachDisk,
  onDetachNic,
  detachBusyId,
}: {
  pid: string;
  vmId: string;
  detail: import("../lib/types").VMDetail;
  guestAgent: import("../lib/rbac").GateResult;
  consoleAllowed: boolean;
  onLaunchConsole: () => void;
  hotPlug: import("../lib/rbac").GateResult;
  onDetachDisk: (diskId: string) => void;
  onDetachNic: (nicId: string) => void;
  detachBusyId: string | null;
}) {
  const disks = detail.disks ?? [];
  const nics = detail.nics ?? [];
  const labels = Object.entries(detail.labels ?? {});

  // Guest-agent info (hostname / IPs / OS). Only fetched when the provider
  // advertises the guest_agent capability; agentConnected may be false on demo
  // VMs, in which case we show a subtle hint instead of an error.
  const guestQ = useVMGuest(pid, vmId, guestAgent.allowed);
  const guest = guestQ.data;
  const guestConnected = !!guest?.agentConnected;
  // Prefer the live guest-agent IPs; fall back to the normalized VMDetail list.
  const guestIps = guest?.ipAddresses?.length ? guest.ipAddresses : detail.ipAddresses ?? [];

  // Latest metrics sample drives the CPU + memory gauges (best-effort: 0 if none).
  const metricsQ = useVMMetrics(pid, vmId);
  const samples = metricsQ.data?.samples ?? [];
  const last = samples.length ? samples[samples.length - 1] : undefined;
  const cpuPct = last?.cpuPercent ?? 0;
  const memUsed = last?.memUsageBytes ?? 0;
  const memCap = last?.memLimitBytes && last.memLimitBytes > 0 ? last.memLimitBytes : detail.memoryMb * 1024 * 1024;
  const memPct = memCap > 0 ? (memUsed / memCap) * 100 : 0;

  // Storage gauge from the sum of disk capacities (used/free are not reported per
  // disk, so we show provisioned capacity as the gauge total — honest and useful).
  const storageBytes = disks.reduce((s, d) => s + d.capacityGb * 1024 ** 3, 0);
  // Best-effort "used": qcow2 thin disks rarely report usage here, so we present
  // total provisioned capacity as the gauge value (100% provisioned) when unknown.
  const storagePct = storageBytes > 0 ? 100 : 0;

  const diskColumns: Column<VMDisk>[] = [
    { key: "label", header: "Disk", cell: (d) => <span className="mono text-xs">{d.label || d.id}</span> },
    { key: "size", header: "Size", align: "right", sortValue: (d) => d.capacityGb, cell: (d) => <span className="mono text-xs">{formatBytes(d.capacityGb * 1024 ** 3, 0)}</span> },
    { key: "format", header: "Format", cell: (d) => (d.format ? <span className="chip">{d.format}</span> : <span className="muted">—</span>) },
    { key: "storage", header: "Storage", cell: (d) => (d.storageId ? <span className="mono text-xs muted">{d.storageId}</span> : <span className="muted">—</span>) },
    { key: "path", header: "Path", cell: (d) => (d.path ? <span className="mono text-xs muted truncate" style={{ maxWidth: 280, display: "inline-block" }} title={d.path}>{d.path}</span> : <span className="muted">—</span>) },
    ...(hotPlug.allowed
      ? [
          {
            key: "actions",
            header: "",
            align: "right" as const,
            width: "90px",
            cell: (d: VMDisk) => (
              <ActionButton
                size="sm"
                variant="ghost"
                tooltip="Detach disk (live)"
                aria-label="Detach disk"
                loading={detachBusyId === d.id}
                onClick={() => onDetachDisk(d.id)}
              >
                <IconTrash size={14} />
              </ActionButton>
            ),
          },
        ]
      : []),
  ];

  const nicColumns: Column<VMNic>[] = [
    { key: "id", header: "NIC", cell: (n) => <span className="mono text-xs">{n.id}</span> },
    { key: "network", header: "Network", cell: (n) => (n.networkId ? <span className="chip">{n.networkId}</span> : <span className="muted">—</span>) },
    { key: "model", header: "Model", cell: (n) => (n.model ? <span className="text-xs">{n.model}</span> : <span className="muted">—</span>) },
    { key: "mac", header: "MAC", cell: (n) => (n.mac ? <span className="mono text-xs">{n.mac}</span> : <span className="muted">—</span>) },
    { key: "connected", header: "Connected", cell: (n) => <span className="text-xs">{n.connected ? "Yes" : "No"}</span> },
    ...(hotPlug.allowed
      ? [
          {
            key: "actions",
            header: "",
            align: "right" as const,
            width: "90px",
            cell: (n: VMNic) => (
              <ActionButton
                size="sm"
                variant="ghost"
                tooltip="Detach adapter (live)"
                aria-label="Detach adapter"
                loading={detachBusyId === n.id}
                onClick={() => onDetachNic(n.id)}
              >
                <IconTrash size={14} />
              </ActionButton>
            ),
          },
        ]
      : []),
  ];

  const networks = Array.from(new Set(nics.map((n) => n.networkId).filter(Boolean))) as string[];
  const storageIds = Array.from(new Set(disks.map((d) => d.storageId).filter(Boolean))) as string[];

  return (
    <div className="col" style={{ gap: "var(--sp-5)" }}>
      {/* Console vignette — a "Launch console" card linking to the Console tab. */}
      <button
        className="console-vignette"
        disabled={!consoleAllowed}
        onClick={onLaunchConsole}
        title={consoleAllowed ? "Open the graphical console" : "Console not available for this VM"}
      >
        <span className="cv-screen"><IconConsole size={24} /></span>
        <span className="cv-main">
          <span className="cv-title">{consoleAllowed ? "Launch console" : "Console unavailable"}</span>
          <span className="cv-sub">
            {consoleAllowed
              ? `Graphical console · ${detail.name}`
              : "This hypervisor does not expose a console for this guest."}
          </span>
        </span>
      </button>

      {/* Top row: General + Resource gauges */}
      <div className="summary-grid">
        <div className="card">
          <div className="card-header">
            <span className="card-title">General</span>
            <VMStateBadge state={detail.state} raw={detail.stateRaw} />
          </div>
          <div className="card-body">
            <dl className="dl">
              <dt>Name</dt>
              <dd>{detail.name}</dd>
              <dt>ID</dt>
              <dd className="mono">{shortId(detail.id, 24)}</dd>
              <dt>Guest OS</dt>
              <dd>{guest?.osName || detail.guestOs || "—"}</dd>
              <dt>Host</dt>
              <dd className="mono">{detail.hostId || "—"}</dd>
              <dt>Cluster</dt>
              <dd>{detail.clusterId ? <span className="chip">{detail.clusterId}</span> : "—"}</dd>
              {guestAgent.allowed ? (
                <>
                  <dt>Guest agent</dt>
                  <dd>
                    <span className="row" style={{ gap: 6 }}>
                      <StatusDot color={guestConnected ? "var(--success)" : "var(--state-unknown)"} />
                      <span className="text-sm" style={{ color: guestConnected ? "var(--success)" : "var(--text-muted)" }}>
                        {guestConnected ? "Connected" : "Not connected"}
                      </span>
                    </span>
                  </dd>
                  <dt>Guest hostname</dt>
                  <dd>
                    {guest?.hostname ? (
                      <span className="mono">{guest.hostname}</span>
                    ) : (
                      <span className="muted text-xs">{guestConnected ? "—" : "guest agent not connected"}</span>
                    )}
                  </dd>
                </>
              ) : null}
              <dt>IP addresses</dt>
              <dd>
                {guestIps.length ? (
                  <span className="row-wrap" style={{ gap: 4 }}>
                    {guestIps.map((ip) => (
                      <span key={ip} className="chip chip-mono text-xs">{ip}</span>
                    ))}
                  </span>
                ) : guestAgent.allowed && !guestConnected ? (
                  <span className="muted text-xs">guest agent not connected</span>
                ) : (
                  <span className="muted">—</span>
                )}
              </dd>
              <dt>Created</dt>
              <dd>
                {detail.createdAt ? (
                  <>
                    {formatDateTime(detail.createdAt)} <span className="text-xs muted">({timeAgo(detail.createdAt)})</span>
                  </>
                ) : (
                  "—"
                )}
              </dd>
            </dl>
          </div>
        </div>

        <div className="card">
          <div className="card-header">
            <span className="card-title">Resources</span>
            <span className="text-xs muted">latest</span>
          </div>
          <div className="card-body">
            <div className="gauge-stack">
              <ResourceGauge
                label="CPU"
                icon={<IconCpu size={15} />}
                percent={cpuPct}
                usedLabel={`${cpuPct.toFixed(0)}% used`}
                capacityLabel={`${detail.vcpus} vCPU`}
                baseColor="var(--accent)"
              />
              <ResourceGauge
                label="Memory"
                icon={<IconMemory size={15} />}
                percent={memPct}
                usedLabel={`${formatBytes(memUsed, 0)} used`}
                capacityLabel={`${formatBytes(memCap, 0)} total`}
                baseColor="var(--success)"
              />
              <ResourceGauge
                label="Storage"
                icon={<IconDisk size={15} />}
                percent={storagePct}
                usedLabel={`${disks.length} disk${disks.length === 1 ? "" : "s"}`}
                capacityLabel={`${formatBytes(storageBytes, 0)} provisioned`}
                baseColor="var(--accent)"
              />
            </div>
          </div>
        </div>
      </div>

      {/* VM Hardware */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">VM Hardware</span>
          <span className="text-xs muted">{detail.kind}</span>
        </div>
        <div className="card-body">
          <dl className="dl">
            <dt><span className="row" style={{ gap: 6 }}><IconCpu size={14} /> CPU</span></dt>
            <dd className="mono">{detail.vcpus} vCPU</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconMemory size={14} /> Memory</span></dt>
            <dd className="mono">{formatBytes(detail.memoryMb * 1024 * 1024, 0)}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconDisk size={14} /> Hard disks</span></dt>
            <dd>{disks.length}{storageBytes > 0 ? <span className="text-xs muted"> · {formatBytes(storageBytes, 0)}</span> : null}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconNic size={14} /> Network adapters</span></dt>
            <dd>{nics.length}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconDisc size={14} /> CD/DVD</span></dt>
            <dd className="muted text-sm">Virtual drive (mount via Actions ▸ Storage)</dd>
            <dt>Firmware</dt>
            <dd>{detail.firmware || "—"}</dd>
            <dt>Guest OS</dt>
            <dd>{detail.guestOs || "—"}</dd>
            <dt>Snapshots</dt>
            <dd className="mono">{detail.snapshotCount}</dd>
          </dl>
        </div>
      </div>

      {/* Hard disks table */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Hard disks</span>
          <span className="text-xs muted">{disks.length}</span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          <DataTable
            columns={diskColumns}
            rows={disks}
            rowKey={(d) => d.id}
            emptyIcon={<IconDisk size={32} />}
            emptyTitle="No disks reported"
          />
        </div>
      </div>

      {/* Network adapters table */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Network adapters</span>
          <span className="text-xs muted">{nics.length}</span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          <DataTable
            columns={nicColumns}
            rows={nics}
            rowKey={(n) => n.id}
            emptyIcon={<IconNic size={32} />}
            emptyTitle="No network interfaces reported"
          />
        </div>
      </div>

      {/* Related Objects */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Related Objects</span>
        </div>
        <div className="card-body">
          <dl className="dl">
            <dt><span className="row" style={{ gap: 6 }}><IconShield size={14} /> Provider</span></dt>
            <dd className="mono">{detail.providerId}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconHosts size={14} /> Host</span></dt>
            <dd className="mono">{detail.hostId || "—"}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconNetworks size={14} /> Networks</span></dt>
            <dd>
              {networks.length ? (
                <span className="row-wrap" style={{ gap: 4 }}>
                  {networks.map((nw) => <span key={nw} className="chip">{nw}</span>)}
                </span>
              ) : (
                <span className="muted">—</span>
              )}
            </dd>
            <dt><span className="row" style={{ gap: 6 }}><IconDisk size={14} /> Storage</span></dt>
            <dd>
              {storageIds.length ? (
                <span className="row-wrap" style={{ gap: 4 }}>
                  {storageIds.map((st) => <span key={st} className="chip chip-mono text-xs">{st}</span>)}
                </span>
              ) : (
                <span className="muted">—</span>
              )}
            </dd>
          </dl>
        </div>
      </div>

      {/* Labels */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Labels</span>
          <span className="text-xs muted">{labels.length}</span>
        </div>
        <div className="card-body">
          {labels.length === 0 ? (
            <span className="muted text-sm">No labels.</span>
          ) : (
            <div className="col" style={{ gap: "var(--sp-1)" }}>
              {labels.map(([k, v]) => (
                <div key={k} className="row" style={{ gap: "var(--sp-2)", alignItems: "baseline" }}>
                  <span className="mono text-xs" style={{ color: "var(--text-link)" }}>{k}</span>
                  <span className="mono text-xs muted">=</span>
                  <span className="mono text-xs secondary truncate">{v}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

/* ============================ Snapshots ============================ */

// snapDepth computes a snapshot's depth in the parent tree (for indentation),
// walking parentId links with a cycle guard.
function snapDepth(s: VMSnapshot, all: VMSnapshot[]): number {
  let depth = 0;
  let cur: VMSnapshot | undefined = s;
  const byId = new Map(all.map((x) => [x.id, x]));
  const seen = new Set<string>();
  while (cur?.parentId && byId.has(cur.parentId) && !seen.has(cur.parentId)) {
    seen.add(cur.parentId);
    cur = byId.get(cur.parentId);
    depth++;
    if (depth > 50) break;
  }
  return depth;
}

function SnapshotsPanel({
  pid,
  vmId,
  caps,
  permissions,
  onCreate,
  onDelete,
}: {
  pid: string;
  vmId: string;
  caps: import("../lib/types").VMCapability[] | undefined;
  permissions: string[] | undefined;
  onCreate: () => void;
  onDelete?: (snap: VMSnapshot) => void;
}) {
  const queryClient = useQueryClient();
  const snapsQ = useVMSnapshots(pid, vmId);
  const [revertingId, setRevertingId] = useState<string | null>(null);

  const revert = async (snap: VMSnapshot) => {
    setRevertingId(snap.id);
    try {
      await api.vmSnapshotRevert(pid, vmId, snap.id);
      toast.success("Revert requested", snap.name);
      queryClient.invalidateQueries({ queryKey: ["vm", "snapshots", pid, vmId] });
      queryClient.invalidateQueries({ queryKey: ["vm", pid, vmId] });
    } catch (err) {
      toastError("Revert failed", err);
    } finally {
      setRevertingId(null);
    }
  };

  const columns: Column<VMSnapshot>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (s) => s.name,
      cell: (s) => (
        <div className="col" style={{ gap: 2, paddingLeft: snapDepth(s, snapsQ.data ?? []) * 18 }}>
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            {snapDepth(s, snapsQ.data ?? []) > 0 ? <span className="muted" aria-hidden>└</span> : null}
            <IconSnapshot size={14} />
            <span style={{ fontWeight: 600 }}>{s.name}</span>
            {s.isCurrent ? <span className="chip">current</span> : null}
          </div>
          {s.description ? <span className="text-xs muted truncate" style={{ maxWidth: 320 }}>{s.description}</span> : null}
        </div>
      ),
    },
    { key: "created", header: "Created", sortValue: (s) => s.createdAt ?? "", cell: (s) => <span className="text-xs muted nowrap">{s.createdAt ? timeAgo(s.createdAt) : "—"}</span> },
    { key: "memory", header: "Memory", cell: (s) => <span className="text-xs">{s.hasMemory ? "Included" : "—"}</span> },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "210px",
      cell: (s) => (
        <div className="row" style={{ gap: "var(--sp-1)", justifyContent: "flex-end" }}>
          <CapabilityGate gate={gateVMAction("snapshot_revert", caps, permissions)}>
            {(allowed, reason) => (
              <ActionButton
                size="sm"
                variant="ghost"
                disabled={!allowed}
                loading={revertingId === s.id}
                tooltip={allowed ? "Revert to this snapshot" : reason}
                onClick={() => revert(s)}
              >
                <IconRestart size={14} />
                Revert
              </ActionButton>
            )}
          </CapabilityGate>
          {onDelete ? (
            <CapabilityGate gate={gateVMAction("snapshot", caps, permissions)}>
              {(allowed, reason) => (
                <ActionButton
                  size="sm"
                  variant="ghost"
                  disabled={!allowed}
                  tooltip={allowed ? "Delete this snapshot" : reason}
                  onClick={() => onDelete(s)}
                >
                  <IconTrash size={14} />
                  Delete
                </ActionButton>
              )}
            </CapabilityGate>
          ) : null}
        </div>
      ),
    },
  ];

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Snapshots</span>
        <CapabilityGate gate={gateVMAction("snapshot", caps, permissions)}>
          {(allowed, reason) => (
            <ActionButton size="sm" variant="primary" disabled={!allowed} tooltip={allowed ? undefined : reason} onClick={onCreate}>
              <IconSnapshot size={14} />
              Create snapshot
            </ActionButton>
          )}
        </CapabilityGate>
      </div>
      <div className="card-body" style={{ padding: 0 }}>
        {snapsQ.isLoading ? (
          <LoadingFill label="Loading snapshots…" />
        ) : (
          <DataTable
            columns={columns}
            rows={snapsQ.data ?? []}
            rowKey={(s) => s.id}
            defaultSortKey="created"
            defaultSortDir="desc"
            emptyIcon={<IconSnapshot size={32} />}
            emptyTitle="No snapshots"
            emptyMessage="Create a snapshot to capture the current state."
          />
        )}
      </div>
    </div>
  );
}

/* ============================ Monitor ============================ */
//
// vSphere "Monitor": performance charts (CPU / Memory / Network rx+tx / Disk
// read+write) over the metric window from GetMetrics, with a time-range selector
// that trims the sample window client-side, then an Events table of this VM's
// recent audit entries (humanized action, actor, time, result).

// Net/disk samples are cumulative byte counters → render per-interval deltas
// (clamped at 0 to absorb counter resets) as a throughput series.
function deltas(values: number[]): number[] {
  const out: number[] = [];
  for (let i = 1; i < values.length; i++) out.push(Math.max(0, values[i]! - values[i - 1]!));
  return out;
}

const RANGES: { key: string; label: string; n: number }[] = [
  { key: "30", label: "30m", n: 30 },
  { key: "60", label: "1h", n: 60 },
  { key: "all", label: "All", n: Number.MAX_SAFE_INTEGER },
];

const RESULT_COLOR: Record<AuditResult, string> = {
  success: "var(--success)",
  denied: "var(--warning)",
  error: "var(--danger)",
};

function MetricsPanel({ pid, vmId, detail }: { pid: string; vmId: string; detail: VMDetail }) {
  const metricsQ = useVMMetrics(pid, vmId);
  const allSamples = metricsQ.data?.samples ?? [];
  const [range, setRange] = useState("all");

  const samples = useMemo(() => {
    const n = RANGES.find((r) => r.key === range)?.n ?? Number.MAX_SAFE_INTEGER;
    return n >= allSamples.length ? allSamples : allSamples.slice(allSamples.length - n);
  }, [allSamples, range]);

  const cpu = useMemo(() => samples.map((s) => s.cpuPercent ?? 0), [samples]);
  const mem = useMemo(
    () =>
      samples.map((s) =>
        s.memLimitBytes && s.memLimitBytes > 0 ? ((s.memUsageBytes ?? 0) / s.memLimitBytes) * 100 : 0,
      ),
    [samples],
  );
  const netRx = useMemo(() => deltas(samples.map((s) => s.netRxBytes ?? 0)), [samples]);
  const netTx = useMemo(() => deltas(samples.map((s) => s.netTxBytes ?? 0)), [samples]);
  const diskR = useMemo(() => deltas(samples.map((s) => s.diskReadBytes ?? 0)), [samples]);
  const diskW = useMemo(() => deltas(samples.map((s) => s.diskWriteBytes ?? 0)), [samples]);

  const lastCpu = cpu.length ? cpu[cpu.length - 1]! : undefined;
  const lastMem = mem.length ? mem[mem.length - 1]! : undefined;
  const lastRx = netRx.length ? netRx[netRx.length - 1]! : undefined;
  const lastTx = netTx.length ? netTx[netTx.length - 1]! : undefined;
  const lastDr = diskR.length ? diskR[diskR.length - 1]! : undefined;
  const lastDw = diskW.length ? diskW[diskW.length - 1]! : undefined;

  const hasNet = netRx.some((v) => v > 0) || netTx.some((v) => v > 0);
  const hasDisk = diskR.some((v) => v > 0) || diskW.some((v) => v > 0);

  return (
    <div className="col" style={{ gap: "var(--sp-5)" }}>
      {/* Performance charts */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Performance</span>
          <span className="row" style={{ gap: 4 }}>
            {RANGES.map((r) => (
              <ActionButton
                key={r.key}
                size="sm"
                variant={range === r.key ? "primary" : "ghost"}
                onClick={() => setRange(r.key)}
              >
                {r.label}
              </ActionButton>
            ))}
          </span>
        </div>
        <div className="card-body">
          {metricsQ.isLoading ? (
            <LoadingFill label="Loading metrics…" />
          ) : allSamples.length === 0 ? (
            <EmptyState
              icon={<IconStats size={32} />}
              title="No metrics available"
              message="This hypervisor does not expose metrics for this VM, or none have been collected yet."
            />
          ) : (
            <div className="summary-grid">
              <ChartCard title="CPU usage">
                <StatsChart data={cpu} max={100} color="var(--chart-1)" label="CPU %" valueLabel={lastCpu !== undefined ? `${lastCpu.toFixed(1)}%` : undefined} />
              </ChartCard>
              <ChartCard title="Memory usage">
                <StatsChart data={mem} max={100} color="var(--chart-2)" label="Memory %" valueLabel={lastMem !== undefined ? `${lastMem.toFixed(1)}%` : undefined} />
              </ChartCard>
              <ChartCard title="Network — receive">
                <StatsChart data={netRx} color="var(--chart-3)" label="rx" valueLabel={hasNet && lastRx !== undefined ? `${formatBytes(lastRx)}/interval` : "no traffic"} />
              </ChartCard>
              <ChartCard title="Network — transmit">
                <StatsChart data={netTx} color="var(--chart-4)" label="tx" valueLabel={hasNet && lastTx !== undefined ? `${formatBytes(lastTx)}/interval` : "no traffic"} />
              </ChartCard>
              <ChartCard title="Disk — read">
                <StatsChart data={diskR} color="var(--chart-5)" label="read" valueLabel={hasDisk && lastDr !== undefined ? `${formatBytes(lastDr)}/interval` : "no I/O"} />
              </ChartCard>
              <ChartCard title="Disk — write">
                <StatsChart data={diskW} color="var(--chart-6)" label="write" valueLabel={hasDisk && lastDw !== undefined ? `${formatBytes(lastDw)}/interval` : "no I/O"} />
              </ChartCard>
            </div>
          )}
        </div>
      </div>

      {/* Events for this VM (filtered audit) */}
      <VMEventsCard detail={detail} />
    </div>
  );
}

function ChartCard({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="col" style={{ gap: "var(--sp-1)" }}>
      <span className="text-xs muted" style={{ fontWeight: 600 }}>{title}</span>
      {children}
    </div>
  );
}

// Recent audit events scoped to THIS VM. The audit API has no "any-id" filter so
// we pull the recent VM-target page and match this VM by id OR name (audit rows
// carry whichever the handler recorded). Honest fallback: a clear empty state.
function VMEventsCard({ detail }: { detail: VMDetail }) {
  const auditQ = useQuery({
    queryKey: ["audit", "vm", detail.id],
    queryFn: () => api.audit({ targetType: "vm", limit: 200 }),
    staleTime: 15_000,
  });

  const rows = useMemo(() => {
    const items = auditQ.data?.items ?? [];
    const idShort = shortId(detail.id, 64);
    return items
      .filter(
        (a) =>
          a.targetId === detail.id ||
          a.targetId === idShort ||
          a.targetName === detail.name ||
          (a.targetId && detail.id.includes(a.targetId)),
      )
      .slice(0, 50);
  }, [auditQ.data, detail.id, detail.name]);

  const columns: Column<AuditEntry>[] = [
    {
      key: "ts",
      header: "Time",
      width: "150px",
      sortValue: (a) => a.tsEpoch,
      cell: (a) => (
        <div className="col" style={{ gap: 0 }}>
          <span className="text-sm">{timeAgo(a.tsEpoch)}</span>
          <span className="text-xs muted nowrap">{formatDateTime(a.ts)}</span>
        </div>
      ),
    },
    {
      key: "result",
      header: "Result",
      width: "100px",
      sortValue: (a) => a.result,
      cell: (a) => (
        <span className="row" style={{ gap: 6 }}>
          <StatusDot color={RESULT_COLOR[a.result]} />
          <span className="text-sm" style={{ color: RESULT_COLOR[a.result], textTransform: "capitalize" }}>{a.result}</span>
        </span>
      ),
    },
    { key: "action", header: "Event", sortValue: (a) => a.action, cell: (a) => <span className="text-sm" style={{ fontWeight: 600 }}>{humanizeAction(a.action)}</span> },
    {
      key: "actor",
      header: "Actor",
      sortValue: (a) => a.actorName || a.actorId,
      cell: (a) => <span className="text-sm">{a.actorName || a.actorId || "—"}</span>,
    },
  ];

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Events</span>
        <span className="text-xs muted">{rows.length} recent</span>
      </div>
      <div className="card-body" style={{ padding: 0 }}>
        {auditQ.isLoading ? (
          <LoadingFill label="Loading events…" />
        ) : (
          <DataTable
            columns={columns}
            rows={rows}
            rowKey={(a) => a.id}
            defaultSortKey="ts"
            defaultSortDir="desc"
            emptyIcon={<IconAudit size={32} />}
            emptyTitle="No events for this VM"
            emptyMessage="Mutating actions and access decisions on this VM will appear here."
          />
        )}
      </div>
    </div>
  );
}

/* ============================ Configure ============================ */
//
// vSphere "Configure" rendered read-form: VM Hardware (CPU/memory/disks/NICs/
// CD-DVD/video) + VM Options (firmware, secure boot, TPM, boot, guest OS), each
// section with an Edit affordance opening the EXISTING Reconfigure / hot-plug
// drawers (no new mutation logic — pure wiring over useVMActions). Reuses the
// same card + .dl + DataTable chrome as Summary.

function ConfigurePanel({
  detail,
  hotPlug,
  diskResize,
  onReconfigure,
  onAddDisk,
  onAddNic,
  onMountIso,
  onResizeDisk,
}: {
  detail: VMDetail;
  hotPlug: import("../lib/rbac").GateResult;
  diskResize?: import("../lib/rbac").GateResult;
  onReconfigure: () => void;
  onAddDisk: () => void;
  onAddNic: () => void;
  onMountIso: () => void;
  onResizeDisk?: (disk: VMDisk) => void;
}) {
  const disks = detail.disks ?? [];
  const nics = detail.nics ?? [];

  // CPU topology is not modeled in the normalized VMDetail; surface it only if the
  // hypervisor-native raw doc exposes sockets/cores (best-effort, honest "—" else).
  const topology = useMemo(() => {
    const raw = detail.raw as Record<string, unknown> | null | undefined;
    if (!raw || typeof raw !== "object") return undefined;
    const sockets = (raw.sockets ?? raw.cpuSockets) as number | undefined;
    const cores = (raw.coresPerSocket ?? raw.cores) as number | undefined;
    if (typeof sockets === "number" || typeof cores === "number") {
      return `${sockets ?? "?"} socket(s) × ${cores ?? "?"} core(s)`;
    }
    return undefined;
  }, [detail.raw]);

  const diskColumns: Column<VMDisk>[] = [
    { key: "label", header: "Disk", cell: (d) => <span className="mono text-xs">{d.label || d.id}</span> },
    { key: "size", header: "Size", align: "right", sortValue: (d) => d.capacityGb, cell: (d) => <span className="mono text-xs">{formatBytes(d.capacityGb * 1024 ** 3, 0)}</span> },
    { key: "format", header: "Format", cell: (d) => (d.format ? <span className="chip">{d.format}</span> : <span className="muted">—</span>) },
    { key: "bus", header: "Bus / Storage", cell: (d) => (d.storageId ? <span className="mono text-xs muted">{d.storageId}</span> : <span className="muted">—</span>) },
    { key: "path", header: "Path", cell: (d) => (d.path ? <span className="mono text-xs muted truncate" style={{ maxWidth: 260, display: "inline-block" }} title={d.path}>{d.path}</span> : <span className="muted">—</span>) },
    ...(onResizeDisk
      ? [{
          key: "resize",
          header: "",
          align: "right" as const,
          width: "110px",
          cell: (d: VMDisk) => (
            <CapabilityGate gate={diskResize ?? hotPlug}>
              {(allowed: boolean, reason?: string) => (
                <ActionButton size="sm" variant="ghost" disabled={!allowed} tooltip={allowed ? "Resize disk (grow)" : reason} onClick={() => onResizeDisk(d)}>
                  <IconEdit size={14} />
                  Resize
                </ActionButton>
              )}
            </CapabilityGate>
          ),
        }]
      : []),
  ];

  const nicColumns: Column<VMNic>[] = [
    { key: "id", header: "Adapter", cell: (n) => <span className="mono text-xs">{n.id}</span> },
    { key: "network", header: "Network", cell: (n) => (n.networkId ? <span className="chip">{n.networkId}</span> : <span className="muted">—</span>) },
    { key: "model", header: "Model", cell: (n) => (n.model ? <span className="text-xs">{n.model}</span> : <span className="muted">—</span>) },
    { key: "mac", header: "MAC", cell: (n) => (n.mac ? <span className="mono text-xs">{n.mac}</span> : <span className="muted">—</span>) },
    { key: "connected", header: "Connected", cell: (n) => <span className="text-xs">{n.connected ? "Yes" : "No"}</span> },
  ];

  const editBtn = (label: string, onClick: () => void, gated = false) =>
    gated && !hotPlug.allowed ? (
      <ActionButton size="sm" variant="ghost" disabled tooltip={hotPlug.reason}>
        <IconEdit size={14} /> {label}
      </ActionButton>
    ) : (
      <ActionButton size="sm" variant="ghost" onClick={onClick}>
        <IconEdit size={14} /> {label}
      </ActionButton>
    );

  return (
    <div className="col" style={{ gap: "var(--sp-5)" }}>
      {/* VM Hardware */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">VM Hardware</span>
          {editBtn("Edit Settings", onReconfigure)}
        </div>
        <div className="card-body">
          <dl className="dl">
            <dt><span className="row" style={{ gap: 6 }}><IconCpu size={14} /> CPU</span></dt>
            <dd className="mono">{detail.vcpus} vCPU{topology ? <span className="text-xs muted"> · {topology}</span> : null}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconMemory size={14} /> Memory</span></dt>
            <dd className="mono">{formatBytes(detail.memoryMb * 1024 * 1024, 0)}</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconDisc size={14} /> CD/DVD drive</span></dt>
            <dd className="text-sm muted">Virtual drive — mount or eject an ISO via Actions ▸ Storage</dd>
            <dt>Video card</dt>
            <dd className="text-sm muted">Default display adapter</dd>
          </dl>
        </div>
      </div>

      {/* Disks */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Hard disks</span>
          <span className="row" style={{ gap: "var(--sp-2)" }}>
            <span className="text-xs muted">{disks.length}</span>
            {editBtn("Add disk", onAddDisk, true)}
          </span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          <DataTable columns={diskColumns} rows={disks} rowKey={(d) => d.id} emptyIcon={<IconDisk size={32} />} emptyTitle="No disks reported" />
        </div>
      </div>

      {/* NICs */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Network adapters</span>
          <span className="row" style={{ gap: "var(--sp-2)" }}>
            <span className="text-xs muted">{nics.length}</span>
            {editBtn("Add adapter", onAddNic, true)}
          </span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          <DataTable columns={nicColumns} rows={nics} rowKey={(n) => n.id} emptyIcon={<IconNic size={32} />} emptyTitle="No network interfaces reported" />
        </div>
      </div>

      {/* VM Options */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">VM Options</span>
          {editBtn("Edit Settings", onReconfigure)}
        </div>
        <div className="card-body">
          <dl className="dl">
            <dt>Guest OS</dt>
            <dd>{detail.guestOs || "—"}</dd>
            <dt>Firmware</dt>
            <dd>{detail.firmware ? <span className="chip">{detail.firmware}</span> : "—"}</dd>
            <dt>Secure Boot</dt>
            <dd className="text-sm muted">{detail.firmware && /uefi/i.test(detail.firmware) ? "Available (UEFI)" : "Not applicable"}</dd>
            <dt>TPM</dt>
            <dd className="text-sm muted">Reported in hypervisor inspect (see Inspect tab)</dd>
            <dt>Boot order</dt>
            <dd className="text-sm muted">Disk → Network (hypervisor default)</dd>
            <dt><span className="row" style={{ gap: 6 }}><IconDisc size={14} /> Boot ISO</span></dt>
            <dd>{editBtn("Mount ISO", onMountIso, true)}</dd>
          </dl>
        </div>
      </div>
    </div>
  );
}

/* ============================ Permissions ============================ */
//
// vSphere "Permissions": the principals/roles that can act on this object. RBAC
// in UniHV is GLOBAL- or HOST-scoped today (no per-object ACL), so we honestly
// surface the role bindings that APPLY to this VM's scope — global bindings plus
// any binding scoped to this VM's host/cluster — as (Principal | Role | Scope |
// Permissions summary), with a link to the Users / Roles admin to manage them.

function PermissionsPanel({ detail }: { detail: VMDetail }) {
  const usersQ = useUsers();
  const rolesQ = useRoles();
  const navigate = useNavigate();

  const roleById = useMemo(() => {
    const m = new Map<string, import("../lib/types").RoleRecord>();
    for (const r of rolesQ.data ?? []) m.set(r.id, r);
    return m;
  }, [rolesQ.data]);

  type Row = {
    key: string;
    principal: string;
    roleName: string;
    scopeType: string;
    scopeId: string | null;
    perms: number;
    permList: string[];
  };

  // A binding is "effective" on this VM when it is global, or scoped to this VM's
  // host or cluster (the granularity RBAC actually supports today).
  const scopeApplies = (scopeType: string, scopeId: string | null): boolean => {
    if (scopeType === "global" || !scopeId) return true;
    if (scopeType === "host") return scopeId === detail.hostId;
    if (scopeType === "cluster") return scopeId === detail.clusterId;
    return false;
  };

  const rows = useMemo<Row[]>(() => {
    const out: Row[] = [];
    for (const u of usersQ.data ?? []) {
      for (const b of u.roles ?? []) {
        if (!scopeApplies(b.scopeType, b.scopeId)) continue;
        const role = roleById.get(b.roleId);
        out.push({
          key: `${u.id}:${b.bindingId}`,
          principal: u.username,
          roleName: b.roleName || role?.name || b.roleId,
          scopeType: b.scopeType,
          scopeId: b.scopeId,
          perms: role?.permissions.length ?? 0,
          permList: role?.permissions ?? [],
        });
      }
    }
    return out.sort((a, b) => a.principal.localeCompare(b.principal));
  }, [usersQ.data, roleById, detail.hostId, detail.clusterId]);

  const columns: Column<Row>[] = [
    {
      key: "principal",
      header: "Principal",
      sortValue: (r) => r.principal,
      cell: (r) => (
        <span className="row" style={{ gap: 6 }}>
          <IconUsers size={14} />
          <span className="text-sm" style={{ fontWeight: 600 }}>{r.principal}</span>
        </span>
      ),
    },
    {
      key: "role",
      header: "Role",
      sortValue: (r) => r.roleName,
      cell: (r) => (
        <span className="row" style={{ gap: 6 }}>
          <IconRoles size={14} />
          <span className="chip">{r.roleName}</span>
        </span>
      ),
    },
    {
      key: "scope",
      header: "Scope",
      sortValue: (r) => r.scopeType,
      cell: (r) => (
        <span className="text-xs">
          {r.scopeType === "global" ? (
            <span className="chip">global</span>
          ) : (
            <span className="mono">{r.scopeType}{r.scopeId ? `:${shortId(r.scopeId, 16)}` : ""}</span>
          )}
        </span>
      ),
    },
    {
      key: "perms",
      header: "Permissions",
      sortValue: (r) => r.perms,
      cell: (r) => (
        <span className="text-xs muted" title={r.permList.slice(0, 40).join("\n")}>
          {r.perms} permission{r.perms === 1 ? "" : "s"}
        </span>
      ),
    },
  ];

  const loading = usersQ.isLoading || rolesQ.isLoading;
  const denied = usersQ.isError || rolesQ.isError;

  return (
    <div className="col" style={{ gap: "var(--sp-5)" }}>
      <div className="card card-pad">
        <div className="row" style={{ gap: 8, alignItems: "flex-start" }}>
          <IconShield size={18} />
          <span className="text-sm muted">
            Access to this VM is granted through <strong>role bindings</strong>. UniHV scopes RBAC
            globally or per host/cluster (there is no per-VM ACL today), so the table below shows the
            bindings that are <strong>effective on this VM</strong> — global bindings plus any scoped to
            its host{detail.hostId ? <> (<span className="mono">{detail.hostId}</span>)</> : null}
            {detail.clusterId ? <> or cluster (<span className="mono">{detail.clusterId}</span>)</> : null}.
            Manage principals and roles in the admin area.
          </span>
        </div>
      </div>

      <div className="card">
        <div className="card-header">
          <span className="card-title">Effective role bindings</span>
          <span className="row" style={{ gap: "var(--sp-2)" }}>
            <ActionButton size="sm" variant="ghost" onClick={() => navigate("/users")}>
              <IconUsers size={14} /> Users <IconExternal size={12} />
            </ActionButton>
            <ActionButton size="sm" variant="ghost" onClick={() => navigate("/roles")}>
              <IconRoles size={14} /> Roles <IconExternal size={12} />
            </ActionButton>
          </span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {loading ? (
            <LoadingFill label="Loading permissions…" />
          ) : denied ? (
            <div className="card-pad">
              <EmptyState
                icon={<IconShield size={32} />}
                title="Permissions not visible"
                message="You do not have access to the RBAC directory. Ask an administrator to review who can act on this VM."
              />
            </div>
          ) : (
            <DataTable
              columns={columns}
              rows={rows}
              rowKey={(r) => r.key}
              defaultSortKey="principal"
              emptyIcon={<IconShield size={32} />}
              emptyTitle="No effective bindings"
              emptyMessage="No role bindings apply to this VM's scope. Grant a role in Users to allow access."
            />
          )}
        </div>
      </div>
    </div>
  );
}
