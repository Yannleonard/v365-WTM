// ui/src/views/VirtualMachineDetail.tsx
//
// One VM: header (state + hardware), tabs Overview | Snapshots | Metrics |
// Inspect. Overview holds the normalized hardware (disks/NICs tables) + labels;
// Snapshots lists the tree with create/revert (gated); Metrics draws the CPU/
// memory series from .../metrics over the shared StatsChart; Inspect shows the
// hypervisor-native raw JSON. Header carries the lifecycle buttons + reconfigure
// + intra-hypervisor migrate, all greyed-out-before-click.

import { useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { useVM, useVMSnapshots, useVMMetrics, useVMCapabilityLookup } from "../lib/hooks";
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
import { gateVMAction, gateVMConsole, gateVMHotPlug } from "../lib/rbac";
import { formatBytes, formatDateTime, shortId, timeAgo } from "../lib/format";
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
} from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { api } from "../lib/api";
import { useQueryClient } from "@tanstack/react-query";
import type { VMDisk, VMNic, VMSnapshot } from "../lib/types";

type TabKey = "summary" | "monitor" | "snapshots" | "console" | "inspect";

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
          />
        )}
        {tab === "monitor" && <MetricsPanel pid={pid} vmId={vmId} />}
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
              <dd>{detail.guestOs || "—"}</dd>
              <dt>Host</dt>
              <dd className="mono">{detail.hostId || "—"}</dd>
              <dt>Cluster</dt>
              <dd>{detail.clusterId ? <span className="chip">{detail.clusterId}</span> : "—"}</dd>
              <dt>IP addresses</dt>
              <dd>
                {detail.ipAddresses && detail.ipAddresses.length ? (
                  <span className="row-wrap" style={{ gap: 4 }}>
                    {detail.ipAddresses.map((ip) => (
                      <span key={ip} className="chip chip-mono text-xs">{ip}</span>
                    ))}
                  </span>
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

function SnapshotsPanel({
  pid,
  vmId,
  caps,
  permissions,
  onCreate,
}: {
  pid: string;
  vmId: string;
  caps: import("../lib/types").VMCapability[] | undefined;
  permissions: string[] | undefined;
  onCreate: () => void;
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
        <div className="col" style={{ gap: 2 }}>
          <div className="row" style={{ gap: "var(--sp-2)" }}>
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
      width: "120px",
      cell: (s) => (
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

/* ============================ Metrics ============================ */

function MetricsPanel({ pid, vmId }: { pid: string; vmId: string }) {
  const metricsQ = useVMMetrics(pid, vmId);
  const samples = metricsQ.data?.samples ?? [];

  const cpu = useMemo(() => samples.map((s) => s.cpuPercent ?? 0), [samples]);
  const mem = useMemo(
    () =>
      samples.map((s) =>
        s.memLimitBytes && s.memLimitBytes > 0 ? ((s.memUsageBytes ?? 0) / s.memLimitBytes) * 100 : 0,
      ),
    [samples],
  );
  const lastCpu = cpu.length ? cpu[cpu.length - 1]! : undefined;
  const lastMem = mem.length ? mem[mem.length - 1]! : undefined;

  if (metricsQ.isLoading) return <LoadingFill label="Loading metrics…" />;

  if (samples.length === 0) {
    return (
      <div className="card card-pad">
        <EmptyState icon={<IconStats size={32} />} title="No metrics available" message="This hypervisor does not expose metrics for this VM, or none have been collected yet." />
      </div>
    );
  }

  return (
    <div className="col" style={{ gap: "var(--sp-5)" }}>
      <div className="card">
        <div className="card-header">
          <span className="card-title">CPU usage</span>
          <span className="text-xs muted">%</span>
        </div>
        <div className="card-body">
          <StatsChart data={cpu} max={100} color="var(--chart-1)" label="CPU" valueLabel={lastCpu !== undefined ? `${lastCpu.toFixed(1)}%` : undefined} />
        </div>
      </div>
      <div className="card">
        <div className="card-header">
          <span className="card-title">Memory usage</span>
          <span className="text-xs muted">%</span>
        </div>
        <div className="card-body">
          <StatsChart data={mem} max={100} color="var(--chart-2)" label="Memory" valueLabel={lastMem !== undefined ? `${lastMem.toFixed(1)}%` : undefined} />
        </div>
      </div>
    </div>
  );
}
