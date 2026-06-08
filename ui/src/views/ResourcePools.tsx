// ui/src/views/ResourcePools.tsx
//
// Resource pools (Lot 5A): a persisted, provider-scoped grouping of VMs with an
// AGGREGATE CPU/memory shares + limit budget. VMs join a pool via the unihv.pool
// label (assigned from the VM's Edit-settings / actions). The pool budget is an
// advisory/reported allocation contract — plain libvirt has no native parent-cgroup
// pool — so each row reports the pool's budget vs. its live members' used vCPU/memory.
//
// Reuses PageHeader / DataTable / Modal / Field / ActionButton + existing tokens.
// List is read-grade; create/update/delete are gated on vm.resource (cap
// "resource_control" on at least one provider).

import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useVMPools, useVMProviders, useVMs } from "../lib/hooks";
import { qk } from "../lib/hooks";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { can } from "../lib/rbac";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { SelectField, TextField } from "../components/Field";
import { ActionButton } from "../components/ActionButton";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { IconRefresh, IconScale, IconPlus, IconEdit, IconTrash } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { formatBytes } from "../lib/format";
import type { ResourcePool, ResourcePoolInput } from "../lib/types";

interface PoolForm extends ResourcePoolInput {
  id?: string; // present when editing
}

const emptyForm = (providerId: string): PoolForm => ({
  name: "",
  providerId,
  cpuShares: undefined,
  cpuLimitMhz: undefined,
  memShares: undefined,
  memLimitMb: undefined,
  notes: "",
});

export function ResourcePools() {
  const qc = useQueryClient();
  const { permissions } = useAuth();
  const poolsQ = useVMPools();
  const providersQ = useVMProviders();
  const pools = poolsQ.data ?? [];
  const providers = providersQ.data ?? [];

  // The pool-management permission (create/update/delete). Provider capability is
  // enforced server-side; here we only gate on the user's permission.
  const canManage = can(permissions, "vm.resource");

  const [form, setForm] = useState<PoolForm | null>(null);
  const [busy, setBusy] = useState(false);
  const [del, setDel] = useState<ResourcePool | null>(null);
  const [assign, setAssign] = useState<{ pool: ResourcePool; vmId: string } | null>(null);

  const refresh = () => qc.invalidateQueries({ queryKey: qk.vmPools() });

  const confirmAssign = async () => {
    if (!assign || !assign.vmId) return;
    setBusy(true);
    try {
      await api.vmPoolAssign(assign.pool.providerId, assign.vmId, assign.pool.id);
      toast.success("VM assigned to pool", assign.pool.name);
      refresh();
      setAssign(null);
    } catch (err) {
      toastError("Assign VM failed", err);
    } finally {
      setBusy(false);
    }
  };

  const openCreate = () => {
    const first = providers[0]?.id ?? "";
    setForm(emptyForm(first));
  };
  const openEdit = (p: ResourcePool) =>
    setForm({
      id: p.id,
      name: p.name,
      providerId: p.providerId,
      cpuShares: p.cpuShares || undefined,
      cpuLimitMhz: p.cpuLimitMhz || undefined,
      memShares: p.memShares || undefined,
      memLimitMb: p.memLimitMb || undefined,
      notes: p.notes ?? "",
    });

  const submit = async () => {
    if (!form || !form.name.trim() || !form.providerId) return;
    setBusy(true);
    try {
      const body: ResourcePoolInput = {
        name: form.name.trim(),
        providerId: form.providerId,
        cpuShares: form.cpuShares,
        cpuLimitMhz: form.cpuLimitMhz,
        memShares: form.memShares,
        memLimitMb: form.memLimitMb,
        notes: form.notes?.trim() || undefined,
      };
      if (form.id) {
        await api.vmPoolUpdate(form.id, body);
        toast.success("Pool updated", form.name);
      } else {
        await api.vmPoolCreate(body);
        toast.success("Pool created", form.name);
      }
      refresh();
      setForm(null);
    } catch (err) {
      toastError("Save pool failed", err);
    } finally {
      setBusy(false);
    }
  };

  const confirmDelete = async () => {
    if (!del) return;
    try {
      await api.vmPoolDelete(del.id);
      toast.success("Pool deleted", del.name);
      refresh();
      setDel(null);
    } catch (err) {
      toastError("Delete pool failed", err);
      throw err; // keep the dialog open on failure
    }
  };

  const columns = useMemo<Column<ResourcePool>[]>(
    () => [
      {
        key: "name",
        header: "Pool",
        cell: (p: ResourcePool) => (
          <div className="col" style={{ gap: 2 }}>
            <span className="device-title">{p.name}</span>
            <span className="mono text-xs muted">{p.providerId}</span>
          </div>
        ),
      },
      {
        key: "members",
        header: "Members",
        cell: (p: ResourcePool) => (
          <span>
            {p.memberCount} VM{p.memberCount === 1 ? "" : "s"}
          </span>
        ),
      },
      {
        key: "cpu",
        header: "CPU budget",
        cell: (p: ResourcePool) => (
          <span className="device-meta">
            {p.cpuShares ? <span className="chip">{p.cpuShares} sh</span> : null}
            {p.cpuLimitMhz ? <span className="chip">{p.cpuLimitMhz} MHz cap</span> : null}
            <span className="muted">used {p.usedVcpus} vCPU</span>
          </span>
        ),
      },
      {
        key: "mem",
        header: "Memory budget",
        cell: (p: ResourcePool) => (
          <span className="device-meta">
            {p.memShares ? <span className="chip">{p.memShares} sh</span> : null}
            {p.memLimitMb ? <span className="chip">{formatBytes(p.memLimitMb * 1024 * 1024, 0)} cap</span> : null}
            <span className="muted">used {formatBytes(p.usedMemoryMb * 1024 * 1024, 0)}</span>
          </span>
        ),
      },
      {
        key: "actions",
        header: "",
        align: "right",
        cell: (p: ResourcePool) =>
          canManage ? (
            <span className="row" style={{ gap: "var(--sp-1)", justifyContent: "flex-end" }}>
              <ActionButton size="sm" variant="ghost" onClick={() => setAssign({ pool: p, vmId: "" })}>
                <IconPlus size={13} /> Assign VM
              </ActionButton>
              <ActionButton size="sm" variant="ghost" iconOnly tooltip="Edit budget" aria-label="Edit pool" onClick={() => openEdit(p)}>
                <IconEdit size={14} />
              </ActionButton>
              <ActionButton
                size="sm"
                variant="ghost"
                iconOnly
                tooltip="Delete pool"
                aria-label="Delete pool"
                onClick={() => setDel(p)}
                style={{ color: "var(--danger)" }}
              >
                <IconTrash size={14} />
              </ActionButton>
            </span>
          ) : null,
      },
    ],
    [canManage],
  );

  if (poolsQ.isLoading) return <LoadingFill label="Loading resource pools…" />;

  return (
    <div className="page">
      <PageHeader
        title="Resource pools"
        subtitle="Named CPU & memory allocation budgets that VMs can belong to."
        actions={
          <>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => poolsQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
            {canManage ? (
              <ActionButton variant="primary" onClick={openCreate} disabled={providers.length === 0}>
                <IconPlus size={14} /> New pool
              </ActionButton>
            ) : null}
          </>
        }
      />

      <div className="drawer-banner info" style={{ marginBottom: "var(--sp-4)" }}>
        <IconScale size={15} />
        <span>
          A resource pool aggregates a CPU &amp; memory shares/limit budget for its member VMs. Assign a VM to a pool from
          its Edit-settings drawer. The budget is an <strong>advisory/reported</strong> allocation — plain libvirt has no
          native parent-cgroup pool enforcement — so each row shows the budget alongside its members&apos; live usage.
        </span>
      </div>

      {pools.length === 0 ? (
        <EmptyState
          icon={<IconScale size={40} />}
          title="No resource pools"
          message="Create a pool to group VMs under a shared CPU & memory budget."
        />
      ) : (
        <DataTable columns={columns} rows={pools} rowKey={(p) => p.id} />
      )}

      {/* Create / edit pool */}
      <Modal
        open={form !== null}
        title={form?.id ? "Edit resource pool" : "New resource pool"}
        onClose={() => setForm(null)}
        footer={
          <>
            <button className="btn" onClick={() => setForm(null)} disabled={busy}>
              Cancel
            </button>
            <ActionButton variant="primary" loading={busy} disabled={!form?.name.trim() || !form?.providerId} onClick={submit}>
              {form?.id ? "Save" : "Create"}
            </ActionButton>
          </>
        }
      >
        {form ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <TextField
              label="Name"
              value={form.name}
              autoFocus
              placeholder="e.g. production-tier"
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
            <SelectField
              label="Hypervisor provider"
              value={form.providerId}
              disabled={!!form.id}
              onChange={(e) => setForm({ ...form, providerId: e.target.value })}
            >
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id} ({p.kind})
                </option>
              ))}
            </SelectField>
            <div className="field-grid">
              <TextField
                label="CPU shares"
                type="number"
                min={0}
                value={form.cpuShares ?? ""}
                placeholder="default"
                onChange={(e) => setForm({ ...form, cpuShares: numOrUndef(e.target.value) })}
              />
              <TextField
                label="CPU limit (MHz)"
                type="number"
                min={0}
                value={form.cpuLimitMhz ?? ""}
                placeholder="0 = unlimited"
                onChange={(e) => setForm({ ...form, cpuLimitMhz: numOrUndef(e.target.value) })}
              />
              <TextField
                label="Memory shares"
                type="number"
                min={0}
                value={form.memShares ?? ""}
                placeholder="default"
                onChange={(e) => setForm({ ...form, memShares: numOrUndef(e.target.value) })}
              />
              <TextField
                label="Memory limit (MB)"
                type="number"
                min={0}
                value={form.memLimitMb ?? ""}
                placeholder="0 = unlimited"
                onChange={(e) => setForm({ ...form, memLimitMb: numOrUndef(e.target.value) })}
              />
            </div>
            <TextField
              label="Notes"
              value={form.notes ?? ""}
              placeholder="Optional"
              onChange={(e) => setForm({ ...form, notes: e.target.value })}
            />
          </div>
        ) : null}
      </Modal>

      {/* Assign a VM to a pool (sets the unihv.pool label via a reconfigure) */}
      <Modal
        open={assign !== null}
        title={assign ? `Assign a VM to "${assign.pool.name}"` : ""}
        onClose={() => setAssign(null)}
        footer={
          <>
            <button className="btn" onClick={() => setAssign(null)} disabled={busy}>
              Cancel
            </button>
            <ActionButton variant="primary" loading={busy} disabled={!assign?.vmId} onClick={confirmAssign}>
              Assign
            </ActionButton>
          </>
        }
      >
        {assign ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <PoolVMSelect
              providerId={assign.pool.providerId}
              value={assign.vmId}
              onChange={(v) => setAssign({ ...assign, vmId: v })}
            />
            <span className="field-hint">
              The VM gets the <span className="mono">unihv.pool</span> label set to this pool. Reassign it to another pool
              or clear the label from the VM&apos;s Edit-settings drawer.
            </span>
          </div>
        ) : null}
      </Modal>

      <ConfirmDestructiveDialog
        open={del !== null}
        title="Delete resource pool"
        description={
          del
            ? `Delete the pool "${del.name}"? Member VMs keep running; their pool assignment label is left in place.`
            : ""
        }
        confirmLabel="Delete pool"
        onClose={() => setDel(null)}
        onConfirm={confirmDelete}
      />
    </div>
  );
}

function numOrUndef(s: string): number | undefined {
  const n = Number(s);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

// PoolVMSelect lists the VMs on a provider for assignment to a pool.
function PoolVMSelect({
  providerId,
  value,
  onChange,
}: {
  providerId: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const vmsQ = useVMs(providerId, !!providerId);
  const vms = vmsQ.data ?? [];
  return (
    <SelectField label="Virtual machine" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Select a VM…</option>
      {vms.map((v) => (
        <option key={v.id} value={v.id}>
          {v.name} ({v.state})
        </option>
      ))}
    </SelectField>
  );
}
