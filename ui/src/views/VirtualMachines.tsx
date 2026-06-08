// ui/src/views/VirtualMachines.tsx
//
// Unified VM table across every hypervisor provider, sourced from the single-pane
// inventory (GET /inventory -> vms[]). Columns: name, kind (hypervisor badge),
// state, host, cluster, vCPU, RAM, guest OS. Filters: provider, state, search.
// Row actions (power/snapshot/clone/delete) are greyed-out-before-click via the
// VM provider capability list + RBAC. Rows click through to the detail view.

import { useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useAuth } from "../lib/auth";
import { useInventory, useVMCapabilityLookup } from "../lib/hooks";
import { useVMActions } from "./useVMActions";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { VMStateBadge } from "../components/VMStateBadge";
import { VMActionButtons } from "../components/VMActionButtons";
import { ProtectedTag } from "../components/ProtectedTag";
import { LoadingFill } from "../components/Spinner";
import { ActionButton } from "../components/ActionButton";
import { IconRefresh, IconSearch, IconVM, IconPlus } from "../components/icons";
import { hasVMCap } from "../lib/rbac";
import { formatBytes, shortId } from "../lib/format";
import type { VM, VMState } from "../lib/types";

const EMPTY_VMS: VM[] = [];

const STATES: { value: "" | VMState; label: string }[] = [
  { value: "", label: "All states" },
  { value: "running", label: "Running" },
  { value: "stopped", label: "Stopped" },
  { value: "suspended", label: "Suspended" },
  { value: "paused", label: "Paused" },
  { value: "unknown", label: "Unknown" },
];

// A hypervisor-kind badge, styled like OrchestratorBadge but over the open VM
// kind vocabulary (vsphere / proxmox / libvirt / hyperv / …).
function HypervisorBadge({ kind }: { kind: string }) {
  const label = kind ? kind.charAt(0).toUpperCase() + kind.slice(1) : "Hypervisor";
  return (
    <span
      className="pill"
      style={{ color: "var(--accent)", background: "transparent", borderColor: "var(--accent)" }}
      title={label}
    >
      <span
        aria-hidden
        style={{ width: 6, height: 6, borderRadius: "50%", background: "var(--accent)", display: "inline-block" }}
      />
      {label}
    </span>
  );
}

export function VirtualMachines() {
  const navigate = useNavigate();
  const { permissions, can } = useAuth();
  const { capsForProvider, providers } = useVMCapabilityLookup();

  // The "Create VM" button shows only when some provider advertises create_vm and
  // the user can vm.create (the wizard re-checks both per the selected provider).
  const canCreateVM = providers.some((p) => hasVMCap(p.capabilities, "create_vm")) && can("vm.create");

  const inventoryQ = useInventory();
  const vms = inventoryQ.data?.vms ?? EMPTY_VMS;

  const actions = useVMActions();

  const [provider, setProvider] = useState("");
  const [state, setState] = useState<"" | VMState>("");
  const [search, setSearch] = useState("");

  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    return vms.filter((v) => {
      if (provider && v.providerId !== provider) return false;
      if (state && v.state !== state) return false;
      if (s) {
        const hay = `${v.name} ${v.id} ${v.guestOs ?? ""} ${v.hostId ?? ""} ${v.clusterId ?? ""} ${(v.ipAddresses ?? []).join(" ")}`.toLowerCase();
        if (!hay.includes(s)) return false;
      }
      return true;
    });
  }, [vms, provider, state, search]);

  const providerIds = useMemo(() => {
    const set = new Set<string>();
    for (const v of vms) set.add(v.providerId);
    for (const p of providers) set.add(p.id);
    return Array.from(set).sort();
  }, [vms, providers]);

  const columns: Column<VM>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (v) => v.name,
      cell: (v) => (
        <div className="col" style={{ gap: 2 }}>
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            <span style={{ fontWeight: 600 }} className="truncate">
              {v.name}
            </span>
            {v.protected ? <ProtectedTag /> : null}
          </div>
          <span className="text-xs muted mono">{shortId(v.id)}</span>
        </div>
      ),
    },
    {
      key: "kind",
      header: "Hypervisor",
      sortValue: (v) => v.kind,
      cell: (v) => <HypervisorBadge kind={v.kind} />,
    },
    {
      key: "state",
      header: "State",
      sortValue: (v) => v.state,
      cell: (v) => <VMStateBadge state={v.state} raw={v.stateRaw} />,
    },
    {
      key: "host",
      header: "Host",
      sortValue: (v) => v.hostId ?? "",
      cell: (v) => (v.hostId ? <span className="mono text-xs truncate">{v.hostId}</span> : <span className="muted">—</span>),
    },
    {
      key: "cluster",
      header: "Cluster",
      sortValue: (v) => v.clusterId ?? "",
      cell: (v) => (v.clusterId ? <span className="chip">{v.clusterId}</span> : <span className="muted">—</span>),
    },
    {
      key: "vcpus",
      header: "vCPU",
      align: "right",
      sortValue: (v) => v.vcpus,
      cell: (v) => <span className="mono text-xs">{v.vcpus}</span>,
    },
    {
      key: "memory",
      header: "RAM",
      align: "right",
      sortValue: (v) => v.memoryMb,
      cell: (v) => <span className="mono text-xs nowrap">{formatBytes(v.memoryMb * 1024 * 1024, 0)}</span>,
    },
    {
      key: "guestOs",
      header: "Guest OS",
      sortValue: (v) => v.guestOs ?? "",
      cell: (v) => (
        <span className="text-xs muted truncate" style={{ maxWidth: 200, display: "inline-block" }} title={v.guestOs}>
          {v.guestOs || "—"}
        </span>
      ),
    },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "200px",
      cell: (v) => (
        <VMActionButtons
          vm={v}
          caps={capsForProvider(v.providerId)}
          permissions={permissions}
          busy={actions.busyId === v.id}
          onPower={actions.runPower}
          onSnapshot={actions.triggerSnapshot}
          onClone={actions.triggerClone}
          onDelete={actions.triggerDelete}
        />
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="Virtual Machines"
        subtitle="Every guest across your hypervisors, in one pane."
        actions={
          <div className="row">
            {canCreateVM ? (
              <ActionButton variant="primary" onClick={() => navigate("/vms/new")}>
                <IconPlus size={15} />
                Create VM
              </ActionButton>
            ) : null}
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" onClick={() => inventoryQ.refetch()} aria-label="Refresh">
              <IconRefresh size={16} />
            </ActionButton>
          </div>
        }
      />

      <div className="card card-pad">
        <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
          <div className="row" style={{ flex: "1 1 260px", minWidth: 220 }}>
            <span className="muted">
              <IconSearch size={16} />
            </span>
            <input
              className="input"
              placeholder="Search name, id, guest OS, IP…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <select className="select" style={{ width: 200 }} value={provider} onChange={(e) => setProvider(e.target.value)}>
            <option value="">All hypervisors</option>
            {providerIds.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
          <select className="select" style={{ width: 160 }} value={state} onChange={(e) => setState(e.target.value as VMState | "")}>
            {STATES.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
          <span className="spacer" />
          <span className="text-sm muted">
            {filtered.length} of {vms.length}
          </span>
        </div>
      </div>

      {inventoryQ.isLoading ? (
        <LoadingFill label="Loading virtual machines…" />
      ) : (
        <DataTable
          columns={columns}
          rows={filtered}
          rowKey={(v) => `${v.providerId}:${v.id}`}
          defaultSortKey="name"
          onRowClick={(v) => navigate(`/vms/${encodeURIComponent(v.providerId)}/${encodeURIComponent(v.id)}`)}
          emptyIcon={<IconVM size={40} />}
          emptyTitle="No virtual machines match"
          emptyMessage="Adjust the filters above, or connect a hypervisor provider."
        />
      )}

      {actions.dialogs}
    </div>
  );
}
