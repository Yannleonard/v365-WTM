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
import { VMActions } from "../components/VMActions";
import { ProtectedTag } from "../components/ProtectedTag";
import { LoadingFill } from "../components/Spinner";
import { ActionButton } from "../components/ActionButton";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { IconRefresh, IconSearch, IconVM, IconPlus, IconPlay, IconStop, IconSnapshot, IconTrash, IconClose, IconStacks } from "../components/icons";
import { hasVMCap, gateVMAction } from "../lib/rbac";
import { formatBytes, shortId } from "../lib/format";
import { api } from "../lib/api";
import { toast, toastError } from "../lib/toast";
import type { VM, VMState, VMBulkRequest, VMBulkResponse } from "../lib/types";

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

  // --- multi-select + bulk operations ---
  // Selection is keyed by "providerId:vmId" (the same key DataTable rows use).
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkDeleteOpen, setBulkDeleteOpen] = useState(false);
  const vmKey = (v: VM) => `${v.providerId}:${v.id}`;

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

  // The currently-selected VM objects (only those still present after filtering).
  const selectedVMs = useMemo(() => filtered.filter((v) => selected.has(vmKey(v))), [filtered, selected]);
  const allFilteredSelected = filtered.length > 0 && filtered.every((v) => selected.has(vmKey(v)));

  const toggleOne = (v: VM) =>
    setSelected((prev) => {
      const next = new Set(prev);
      const k = vmKey(v);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });

  const toggleAll = () =>
    setSelected((prev) => {
      if (filtered.every((v) => prev.has(vmKey(v)))) {
        // deselect everything currently visible
        const next = new Set(prev);
        for (const v of filtered) next.delete(vmKey(v));
        return next;
      }
      const next = new Set(prev);
      for (const v of filtered) next.add(vmKey(v));
      return next;
    });

  const clearSelection = () => setSelected(new Set());

  // runBulk fans a single action across the selected VMs via POST /vm/bulk. Each
  // target is re-gated server-side by the same permission the single action needs;
  // a per-target result drives the summary toast. The action is only OFFERED for
  // VMs whose provider+RBAC permit it (we filter before sending), so the common
  // case succeeds and only genuine conflicts surface as per-target failures.
  const runBulk = async (req: Omit<VMBulkRequest, "targets">, eligible: VM[]) => {
    if (eligible.length === 0) return;
    setBulkBusy(true);
    try {
      const res: VMBulkResponse = await api.vmBulk({
        ...req,
        targets: eligible.map((v) => ({ providerId: v.providerId, vmId: v.id })),
      });
      const verb = req.op ?? req.action;
      if (res.failed === 0) {
        toast.success(`Bulk ${verb}: ${res.succeeded} succeeded`);
      } else if (res.succeeded === 0) {
        toast.warning(`Bulk ${verb}: all ${res.failed} failed`, res.results.find((r) => r.error)?.error);
      } else {
        toast.warning(`Bulk ${verb}: ${res.succeeded} ok, ${res.failed} failed`, res.results.find((r) => r.error)?.error);
      }
      clearSelection();
      inventoryQ.refetch();
    } catch (err) {
      toastError("Bulk operation failed", err);
    } finally {
      setBulkBusy(false);
    }
  };

  // Eligibility per action: a VM is eligible when its provider advertises the cap
  // AND the user holds the permission (gateVMAction), so we never send a target
  // the backend would 405/403. resume/start share the start gate.
  const eligibleFor = (action: "start" | "stop" | "snapshot" | "delete_vm") =>
    selectedVMs.filter((v) => gateVMAction(action, capsForProvider(v.providerId), permissions).allowed);

  const columns: Column<VM>[] = [
    {
      key: "select",
      header: (
        <span onClick={(e) => e.stopPropagation()} style={{ display: "inline-flex" }}>
          <input
            type="checkbox"
            aria-label="Select all"
            checked={allFilteredSelected}
            ref={(el) => {
              if (el) el.indeterminate = !allFilteredSelected && selectedVMs.length > 0;
            }}
            onChange={toggleAll}
          />
        </span>
      ),
      width: "40px",
      cell: (v) => (
        <span onClick={(e) => e.stopPropagation()} style={{ display: "inline-flex" }}>
          <input type="checkbox" aria-label={`Select ${v.name}`} checked={selected.has(vmKey(v))} onChange={() => toggleOne(v)} />
        </span>
      ),
    },
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
            {v.labels?.["unihv.template"] === "true" ? (
              <span className="chip" title="This VM is a golden-image template (deploy new VMs from it)">
                <IconStacks size={12} /> Template
              </span>
            ) : null}
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
      width: "120px",
      cell: (v) => (
        <VMActions
          layout="menu"
          vm={v}
          caps={capsForProvider(v.providerId)}
          permissions={permissions}
          busy={actions.busyId === v.id}
          onPower={actions.runPower}
          onSnapshot={actions.triggerSnapshot}
          onClone={actions.triggerClone}
          onReconfigure={actions.triggerReconfigure}
          onMigrate={actions.triggerMigrate}
          onAddDisk={actions.triggerAddDisk}
          onAddNic={actions.triggerAddNic}
          onMountIso={actions.triggerMountIso}
          onEjectIso={actions.ejectIso}
          onDelete={actions.triggerDelete}
          onDeploy={actions.triggerDeploy}
          onMarkTemplate={actions.markTemplate}
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

      {selectedVMs.length > 0 ? (
        <div
          className="card card-pad row-wrap"
          style={{ gap: "var(--sp-3)", alignItems: "center", borderColor: "var(--accent)" }}
        >
          <span className="text-sm" style={{ fontWeight: 600 }}>
            {selectedVMs.length} selected
          </span>
          <span className="spacer" />
          <ActionButton
            size="sm"
            variant="ghost"
            loading={bulkBusy}
            disabled={bulkBusy || eligibleFor("start").length === 0}
            tooltip={eligibleFor("start").length === 0 ? "No selected VM can be powered on" : `Power on ${eligibleFor("start").length}`}
            onClick={() => runBulk({ action: "power", op: "start" }, eligibleFor("start"))}
          >
            <IconPlay size={14} />
            Power On ({eligibleFor("start").length})
          </ActionButton>
          <ActionButton
            size="sm"
            variant="ghost"
            loading={bulkBusy}
            disabled={bulkBusy || eligibleFor("stop").length === 0}
            tooltip={eligibleFor("stop").length === 0 ? "No selected VM can be powered off" : `Power off ${eligibleFor("stop").length}`}
            onClick={() => runBulk({ action: "power", op: "stop" }, eligibleFor("stop"))}
          >
            <IconStop size={14} />
            Power Off ({eligibleFor("stop").length})
          </ActionButton>
          <ActionButton
            size="sm"
            variant="ghost"
            loading={bulkBusy}
            disabled={bulkBusy || eligibleFor("snapshot").length === 0}
            tooltip={eligibleFor("snapshot").length === 0 ? "No selected VM supports snapshots" : `Snapshot ${eligibleFor("snapshot").length}`}
            onClick={() => runBulk({ action: "snapshot", name: `bulk-${new Date().toISOString().slice(0, 19)}` }, eligibleFor("snapshot"))}
          >
            <IconSnapshot size={14} />
            Snapshot ({eligibleFor("snapshot").length})
          </ActionButton>
          <ActionButton
            size="sm"
            variant="ghost"
            disabled={bulkBusy || eligibleFor("delete_vm").length === 0}
            tooltip={eligibleFor("delete_vm").length === 0 ? "No selected VM can be deleted" : `Delete ${eligibleFor("delete_vm").length}`}
            onClick={() => setBulkDeleteOpen(true)}
            style={eligibleFor("delete_vm").length > 0 ? { color: "var(--danger)" } : undefined}
          >
            <IconTrash size={14} />
            Delete ({eligibleFor("delete_vm").length})
          </ActionButton>
          <ActionButton size="sm" variant="ghost" iconOnly tooltip="Clear selection" aria-label="Clear selection" onClick={clearSelection}>
            <IconClose size={15} />
          </ActionButton>
        </div>
      ) : null}

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

      <ConfirmDestructiveDialog
        open={bulkDeleteOpen}
        title="Delete selected VMs"
        variant="danger"
        confirmLabel={`Delete ${eligibleFor("delete_vm").length}`}
        description={
          <>
            Permanently delete <strong>{eligibleFor("delete_vm").length}</strong> selected virtual machine(s)? This destroys the guests and cannot be undone.
          </>
        }
        onConfirm={async () => {
          await runBulk({ action: "delete", force: true }, eligibleFor("delete_vm"));
          setBulkDeleteOpen(false);
        }}
        onClose={() => setBulkDeleteOpen(false)}
      />

      {actions.dialogs}
    </div>
  );
}
