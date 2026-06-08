// ui/src/views/VMClusters.tsx
//
// Hypervisor clusters across every VM provider (from the unified inventory), with
// a per-cluster topology drill-in (GET .../clusters/{cid}/topology) showing each
// node (host) state + the VMs placed on it. Reuses DataTable for both the cluster
// list and each node's VM placement table.

import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useInventory, useVMClusterTopology } from "../lib/hooks";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { VMStateBadge } from "../components/VMStateBadge";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { ActionButton } from "../components/ActionButton";
import { StatusDot } from "../components/StatusDot";
import { IconRefresh, IconNetworks, IconChevronDown, IconVM } from "../components/icons";
import { formatBytes } from "../lib/format";
import type { VM, VMCluster } from "../lib/types";

export function VMClusters() {
  const inventoryQ = useInventory();
  const clusters = inventoryQ.data?.clusters ?? [];
  const allVms = inventoryQ.data?.vms ?? [];

  const [expanded, setExpanded] = useState<string | null>(null);

  if (inventoryQ.isLoading) return <LoadingFill label="Loading clusters…" />;

  return (
    <div className="page">
      <PageHeader
        title="Clusters"
        subtitle="Hypervisor clusters and their node placement."
        actions={
          <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => inventoryQ.refetch()}>
            <IconRefresh size={16} />
          </ActionButton>
        }
      />

      {clusters.length === 0 ? (
        <EmptyState icon={<IconNetworks size={40} />} title="No clusters" message="No hypervisor clusters are reported by any connected provider." />
      ) : (
        <div className="col" style={{ gap: "var(--sp-4)" }}>
          {clusters.map((c) => (
            <ClusterCard
              key={`${c.providerId}:${c.id}`}
              cluster={c}
              vms={allVms}
              open={expanded === `${c.providerId}:${c.id}`}
              onToggle={() =>
                setExpanded((cur) => (cur === `${c.providerId}:${c.id}` ? null : `${c.providerId}:${c.id}`))
              }
            />
          ))}
        </div>
      )}
    </div>
  );
}

function ClusterCard({
  cluster,
  vms,
  open,
  onToggle,
}: {
  cluster: VMCluster;
  vms: VM[];
  open: boolean;
  onToggle: () => void;
}) {
  const hostCount = cluster.hostIds?.length ?? 0;
  const vmCount = vms.filter((v) => v.providerId === cluster.providerId && v.clusterId === cluster.id).length;
  return (
    <div className="card">
      <div
        className="card-header"
        style={{ cursor: "pointer" }}
        onClick={onToggle}
        role="button"
        aria-expanded={open}
      >
        <div className="row" style={{ gap: "var(--sp-3)" }}>
          <span className="muted" style={{ transform: open ? "rotate(0deg)" : "rotate(-90deg)", transition: "transform 120ms" }}>
            <IconChevronDown size={16} />
          </span>
          <div className="col" style={{ gap: 0 }}>
            <span className="card-title">{cluster.name}</span>
            <span className="text-xs muted mono">{cluster.providerId}</span>
          </div>
        </div>
        <div className="row-wrap" style={{ gap: "var(--sp-2)" }}>
          {cluster.haEnabled ? <span className="chip">HA</span> : null}
          {cluster.drsEnabled ? <span className="chip">DRS</span> : null}
          <span className="chip">{hostCount} hosts</span>
          <span className="chip">{vmCount} VMs</span>
        </div>
      </div>
      {open ? (
        <div className="card-body" style={{ padding: 0 }}>
          <ClusterTopology pid={cluster.providerId} cid={cluster.id} vms={vms} />
        </div>
      ) : null}
    </div>
  );
}

function ClusterTopology({ pid, cid, vms }: { pid: string; cid: string; vms: VM[] }) {
  const topoQ = useVMClusterTopology(pid, cid, true);

  // The backend Topology carries node states + a vmId->nodeId placement map.
  // Build per-node VM lists from the inventory VMs using that placement (falling
  // back to the VM's hostId when a node has no explicit placement entry).
  const vmsByNode = useMemo(() => {
    const placement = topoQ.data?.placement ?? {};
    const byNode = new Map<string, VM[]>();
    for (const v of vms) {
      if (v.providerId !== pid) continue;
      const nodeId = placement[v.id] ?? v.hostId;
      if (!nodeId) continue;
      const list = byNode.get(nodeId) ?? [];
      list.push(v);
      byNode.set(nodeId, list);
    }
    return byNode;
  }, [topoQ.data, vms, pid]);

  if (topoQ.isLoading) return <LoadingFill label="Loading topology…" />;
  if (topoQ.isError || !topoQ.data) {
    return (
      <div style={{ padding: "var(--sp-4)" }}>
        <EmptyState icon={<IconNetworks size={28} />} title="Topology unavailable" />
      </div>
    );
  }

  const nodes = topoQ.data.nodes ?? [];
  if (nodes.length === 0) {
    return (
      <div style={{ padding: "var(--sp-4)" }}>
        <EmptyState icon={<IconNetworks size={28} />} title="No nodes in this cluster" />
      </div>
    );
  }

  return (
    <div className="col" style={{ gap: "var(--sp-4)", padding: "var(--sp-4)" }}>
      {nodes.map((node) => (
        <NodePanel key={node.nodeId} node={node} vms={vmsByNode.get(node.nodeId) ?? []} />
      ))}
    </div>
  );
}

function NodePanel({ node, vms }: { node: import("../lib/types").VMClusterNode; vms: VM[] }) {
  const navigate = useNavigate();
  const vmColumns: Column<VM>[] = [
    { key: "name", header: "VM", sortValue: (v) => v.name, cell: (v) => <span style={{ fontWeight: 600 }} className="truncate">{v.name}</span> },
    { key: "state", header: "State", sortValue: (v) => v.state, cell: (v) => <VMStateBadge state={v.state} raw={v.stateRaw} /> },
    { key: "vcpus", header: "vCPU", align: "right", sortValue: (v) => v.vcpus, cell: (v) => <span className="mono text-xs">{v.vcpus}</span> },
    { key: "memory", header: "RAM", align: "right", sortValue: (v) => v.memoryMb, cell: (v) => <span className="mono text-xs nowrap">{formatBytes(v.memoryMb * 1024 * 1024, 0)}</span> },
    { key: "guestOs", header: "Guest OS", sortValue: (v) => v.guestOs ?? "", cell: (v) => <span className="text-xs muted">{v.guestOs || "—"}</span> },
  ];

  const healthy = node.state === "up" || node.state === "running" || node.state === "connected";

  return (
    <div className="card" style={{ background: "var(--bg-elevated, var(--bg-surface))" }}>
      <div className="card-header">
        <div className="row" style={{ gap: "var(--sp-3)" }}>
          <StatusDot color={healthy ? "var(--success)" : "var(--state-pending)"} />
          <div className="col" style={{ gap: 0 }}>
            <span className="card-title">{node.nodeId}</span>
            {node.message ? <span className="text-xs muted">{node.message}</span> : null}
          </div>
        </div>
        <div className="row-wrap" style={{ gap: "var(--sp-2)" }}>
          {node.state ? <span className="chip">{node.state}</span> : null}
          <span className="chip">{node.vmCount ?? vms.length} VMs</span>
        </div>
      </div>
      <div className="card-body" style={{ padding: 0 }}>
        <DataTable
          columns={vmColumns}
          rows={vms}
          rowKey={(v) => `${v.providerId}:${v.id}`}
          defaultSortKey="name"
          onRowClick={(v) => navigate(`/vms/${encodeURIComponent(v.providerId)}/${encodeURIComponent(v.id)}`)}
          emptyIcon={<IconVM size={28} />}
          emptyTitle="No VMs on this node"
        />
      </div>
    </div>
  );
}
