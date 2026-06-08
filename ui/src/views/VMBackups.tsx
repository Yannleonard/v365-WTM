// ui/src/views/VMBackups.tsx
//
// Scheduled VM BACKUPS (Lot 5B). A VM backup = snapshot -> export disk(s) ->
// store to a storage backend (S3 / Azure / SAN / local), with retention + restore.
//
//   - a table of backups: VM, backend, size, when, status, with Restore + Delete;
//   - a "Back up now" action (pick VM + backend);
//   - a Backup Policies section: scheduled backups (VM + backend + interval +
//     retention), list + delete + run-now.
//
// It mirrors Replication.tsx and reuses the SAME hypervisor-provider/VM hooks and
// the storage-backends hook. The server runs the snapshot/export/upload and the
// scheduler; this view drives the on-demand backup, the policy CRUD and restore.

import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  useVMProviders,
  useVMs,
  useStorageBackends,
  useVMBackups,
  useVMBackupPolicies,
} from "../lib/hooks";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { canAny } from "../lib/rbac";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { SelectField, TextField } from "../components/Field";
import { ActionButton } from "../components/ActionButton";
import { LoadingFill } from "../components/Spinner";
import { StatusDot } from "../components/StatusDot";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { Modal } from "../components/Modal";
import { IconRefresh, IconDownload } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type { VMBackup, VMBackupPolicy } from "../lib/types";

const WRITE_PERMS = ["vm.backup", "*"];
const RESTORE_PERMS = ["vm.backup.restore", "*"];

function fmtBytes(n: number): string {
  if (!n) return "—";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${u[i]}`;
}

function fmtSecs(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  if (s < 86400) return `${(s / 3600).toFixed(1)}h`;
  return `${(s / 86400).toFixed(1)}d`;
}

function statusColor(s: string): string {
  switch (s) {
    case "completed":
    case "idle":
      return "var(--success)";
    case "pending":
    case "running":
      return "var(--accent)";
    case "error":
      return "var(--danger)";
    default:
      return "var(--muted)";
  }
}

export function VMBackups() {
  const queryClient = useQueryClient();
  const { permissions } = useAuth();
  const canWrite = canAny(permissions, WRITE_PERMS);
  const canRestore = canAny(permissions, RESTORE_PERMS);

  const backupsQ = useVMBackups();
  const backups = backupsQ.data ?? [];
  const backendsQ = useStorageBackends();
  const backends = backendsQ.data ?? [];

  const backendName = (id: string) => backends.find((b) => b.id === id)?.name ?? id;

  const [backingUp, setBackingUp] = useState(false);
  const [restoreTarget, setRestoreTarget] = useState<VMBackup | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<VMBackup | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["vm-backups"] });

  const doDelete = async () => {
    if (!deleteTarget) return;
    try {
      await api.vmBackupDelete(deleteTarget.id);
      toast.success("Backup deleted", deleteTarget.vmName || deleteTarget.vmId);
      invalidate();
    } catch (err) {
      toastError("Delete failed", err);
    }
  };

  const columns: Column<VMBackup>[] = [
    {
      key: "vm",
      header: "VM",
      sortValue: (b) => b.vmName || b.vmId,
      cell: (b) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{b.vmName || b.vmId}</span>
          <span className="mono text-xs muted">{b.vmId}</span>
        </div>
      ),
    },
    {
      key: "backend",
      header: "Backend",
      cell: (b) => <span className="chip chip-mono text-xs">{backendName(b.backendId)}</span>,
    },
    {
      key: "size",
      header: "Size",
      sortValue: (b) => b.sizeBytes,
      cell: (b) => <span className="text-xs mono muted">{fmtBytes(b.sizeBytes)}</span>,
    },
    {
      key: "when",
      header: "When",
      sortValue: (b) => b.createdAt,
      cell: (b) => (
        <span className="text-xs muted nowrap">{timeAgo(new Date(b.createdAt * 1000).toISOString())}</span>
      ),
    },
    {
      key: "status",
      header: "Status",
      sortValue: (b) => b.status,
      cell: (b) => (
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          <StatusDot color={statusColor(b.status)} pulse={b.status === "pending"} />
          {b.status}
          {b.policyId ? <span className="chip text-xs">scheduled</span> : null}
        </span>
      ),
    },
    {
      key: "actions",
      header: "",
      width: "200px",
      cell: (b) => (
        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton
            variant="ghost"
            disabled={!canRestore || b.status !== "completed"}
            tooltip={b.status !== "completed" ? "Only completed backups can be restored" : "Restore as a new VM"}
            onClick={() => setRestoreTarget(b)}
          >
            Restore
          </ActionButton>
          <ActionButton
            variant="ghost"
            disabled={!canWrite}
            tooltip="Delete backup + artifacts"
            onClick={() => setDeleteTarget(b)}
          >
            Delete
          </ActionButton>
        </div>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="VM Backups"
        subtitle="Snapshot + export a VM's disks to a storage backend (S3 / Azure / SAN / local), with retention and one-click restore as a new VM."
        actions={
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => backupsQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
            <ActionButton variant="primary" disabled={!canWrite} onClick={() => setBackingUp(true)}>
              <IconDownload size={15} />
              Back up now
            </ActionButton>
          </div>
        }
      />

      <div className="card">
        <div className="card-header">
          <span className="card-title">Backups</span>
          <span className="text-xs muted">{backups.length}</span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {backupsQ.isLoading ? (
            <LoadingFill label="Loading backups…" />
          ) : (
            <DataTable
              columns={columns}
              rows={backups}
              rowKey={(b) => b.id}
              defaultSortKey="when"
              defaultSortDir="desc"
              emptyIcon={<IconDownload size={32} />}
              emptyTitle="No backups yet"
              emptyMessage="Back up a VM on demand, or create a backup policy to schedule them."
            />
          )}
        </div>
      </div>

      <BackupPoliciesCard canWrite={canWrite} backendName={backendName} />

      {backingUp ? (
        <BackupNowModal
          onClose={() => setBackingUp(false)}
          onDone={() => {
            setBackingUp(false);
            invalidate();
          }}
        />
      ) : null}

      {restoreTarget ? (
        <RestoreModal
          backup={restoreTarget}
          onClose={() => setRestoreTarget(null)}
          onDone={() => {
            setRestoreTarget(null);
            queryClient.invalidateQueries({ queryKey: ["inventory"] });
          }}
        />
      ) : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Delete backup?"
        confirmLabel="Delete"
        description={
          <span>
            This permanently deletes the backup of <strong>{deleteTarget?.vmName || deleteTarget?.vmId}</strong> and
            its artifacts from the storage backend. This cannot be undone.
          </span>
        }
        onConfirm={doDelete}
        onClose={() => setDeleteTarget(null)}
      />
    </div>
  );
}

/* ============================ back up now ============================ */

function BackupNowModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const providersQ = useVMProviders();
  const providers = providersQ.data ?? [];
  const backendsQ = useStorageBackends();
  const backends = backendsQ.data ?? [];

  const [providerId, setProviderId] = useState("");
  const [vmId, setVmId] = useState("");
  const [backendId, setBackendId] = useState("");
  const [busy, setBusy] = useState(false);

  const vmsQ = useVMs(providerId, !!providerId);
  const vms = vmsQ.data ?? [];

  const valid = providerId !== "" && vmId !== "" && backendId !== "";

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.vmBackupRun({ providerId, vmId, backendId });
      toast.success("Backup complete", "Artifact stored on the backend.");
      onDone();
    } catch (err) {
      toastError("Backup failed", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="Back up a VM now" onClose={onClose} wide>
      <div className="col" style={{ gap: "var(--sp-4)" }}>
        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="Hypervisor"
              value={providerId}
              onChange={(e) => {
                setProviderId(e.target.value);
                setVmId("");
              }}
            >
              <option value="">Select a provider…</option>
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id} ({p.kind})
                </option>
              ))}
            </SelectField>
          </div>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="VM"
              value={vmId}
              disabled={!providerId || vmsQ.isLoading}
              onChange={(e) => setVmId(e.target.value)}
            >
              <option value="">{vmsQ.isLoading ? "Loading…" : "Select a VM…"}</option>
              {vms.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name}
                </option>
              ))}
            </SelectField>
          </div>
        </div>

        <SelectField label="Storage backend" value={backendId} onChange={(e) => setBackendId(e.target.value)}>
          <option value="">Select a backend…</option>
          {backends.map((b) => (
            <option key={b.id} value={b.id}>
              {b.name} ({b.type})
            </option>
          ))}
        </SelectField>

        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" onClick={onClose}>
            Cancel
          </ActionButton>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Back up now
          </ActionButton>
        </div>
      </div>
    </Modal>
  );
}

/* ============================ restore ============================ */

function RestoreModal({ backup, onClose, onDone }: { backup: VMBackup; onClose: () => void; onDone: () => void }) {
  const providersQ = useVMProviders();
  const providers = providersQ.data ?? [];

  const [targetProviderId, setTargetProviderId] = useState(backup.providerId);
  const [targetName, setTargetName] = useState(`${backup.vmName || backup.vmId}-restored`);
  const [powerOn, setPowerOn] = useState(false);
  const [busy, setBusy] = useState(false);

  const valid = targetProviderId !== "";

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const res = await api.vmBackupRestore(backup.id, {
        targetProviderId,
        targetName: targetName.trim() || undefined,
        powerOn,
      });
      toast.success("Restore complete", `New VM ${res.vmId} created.`);
      onDone();
    } catch (err) {
      toastError("Restore failed", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title={`Restore — ${backup.vmName || backup.vmId}`} onClose={onClose} wide>
      <div className="col" style={{ gap: "var(--sp-4)" }}>
        <div className="row-wrap" style={{ gap: "var(--sp-2)" }}>
          <span className="chip text-xs">size: {fmtBytes(backup.sizeBytes)}</span>
          <span className="chip text-xs">disks: {backup.diskCount}</span>
          <span className="chip text-xs">taken: {timeAgo(new Date(backup.createdAt * 1000).toISOString())}</span>
        </div>

        <SelectField
          label="Target hypervisor"
          value={targetProviderId}
          onChange={(e) => setTargetProviderId(e.target.value)}
        >
          <option value="">Select a provider…</option>
          {providers.map((p) => (
            <option key={p.id} value={p.id}>
              {p.id} ({p.kind})
            </option>
          ))}
        </SelectField>

        <TextField label="New VM name" value={targetName} onChange={(e) => setTargetName(e.target.value)} />

        <label className="checkbox-row">
          <input type="checkbox" checked={powerOn} onChange={(e) => setPowerOn(e.target.checked)} />
          <span>Power on after restore</span>
        </label>

        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" onClick={onClose}>
            Cancel
          </ActionButton>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Restore as new VM
          </ActionButton>
        </div>
      </div>
    </Modal>
  );
}

/* ============================ policies ============================ */

function BackupPoliciesCard({
  canWrite,
  backendName,
}: {
  canWrite: boolean;
  backendName: (id: string) => string;
}) {
  const queryClient = useQueryClient();
  const policiesQ = useVMBackupPolicies();
  const policies = policiesQ.data ?? [];

  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<VMBackupPolicy | null>(null);

  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ["vm-backup-policies"] });
    queryClient.invalidateQueries({ queryKey: ["vm-backups"] });
  };

  const runNow = async (p: VMBackupPolicy) => {
    setBusyId(p.id);
    try {
      await api.vmBackupPolicyRun(p.id);
      toast.success("Backup complete", p.name);
      invalidate();
    } catch (err) {
      toastError("Run failed", err);
    } finally {
      setBusyId(null);
    }
  };

  const doDelete = async () => {
    if (!deleteTarget) return;
    try {
      await api.vmBackupPolicyDelete(deleteTarget.id);
      toast.success("Policy deleted", deleteTarget.name);
      invalidate();
    } catch (err) {
      toastError("Delete failed", err);
    }
  };

  const columns: Column<VMBackupPolicy>[] = [
    {
      key: "name",
      header: "Policy",
      sortValue: (p) => p.name,
      cell: (p) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{p.name}</span>
          <span className="mono text-xs muted">{p.vmId}</span>
        </div>
      ),
    },
    {
      key: "backend",
      header: "Backend",
      cell: (p) => <span className="chip chip-mono text-xs">{backendName(p.backendId)}</span>,
    },
    {
      key: "schedule",
      header: "Every",
      sortValue: (p) => p.intervalSeconds,
      cell: (p) => <span className="text-xs mono muted">{fmtSecs(p.intervalSeconds)}</span>,
    },
    {
      key: "retention",
      header: "Keep",
      sortValue: (p) => p.retentionCount,
      cell: (p) => <span className="text-xs muted">{p.retentionCount}</span>,
    },
    {
      key: "last",
      header: "Last run",
      sortValue: (p) => p.lastRunAt ?? 0,
      cell: (p) =>
        p.lastRunAt ? (
          <span className="text-xs muted nowrap">{timeAgo(new Date(p.lastRunAt * 1000).toISOString())}</span>
        ) : (
          <span className="text-xs muted">never</span>
        ),
    },
    {
      key: "status",
      header: "Status",
      sortValue: (p) => p.status,
      cell: (p) => (
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          <StatusDot color={statusColor(p.status)} pulse={p.status === "running"} />
          {p.status}
          {p.enabled ? null : <span className="chip text-xs">disabled</span>}
        </span>
      ),
    },
    {
      key: "actions",
      header: "",
      width: "180px",
      cell: (p) => (
        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton
            variant="ghost"
            loading={busyId === p.id}
            disabled={!canWrite}
            tooltip="Run this backup now"
            onClick={() => runNow(p)}
          >
            Run now
          </ActionButton>
          <ActionButton variant="ghost" disabled={!canWrite} tooltip="Delete policy" onClick={() => setDeleteTarget(p)}>
            Delete
          </ActionButton>
        </div>
      ),
    },
  ];

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Backup policies</span>
        <div className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
          <span className="text-xs muted">{policies.length}</span>
          <ActionButton size="sm" variant="ghost" disabled={!canWrite} onClick={() => setCreating(true)}>
            New policy
          </ActionButton>
        </div>
      </div>
      <div className="card-body" style={{ padding: 0 }}>
        {policiesQ.isLoading ? (
          <LoadingFill label="Loading policies…" />
        ) : (
          <DataTable
            columns={columns}
            rows={policies}
            rowKey={(p) => p.id}
            defaultSortKey="name"
            defaultSortDir="asc"
            emptyIcon={<IconRefresh size={32} />}
            emptyTitle="No backup policies"
            emptyMessage="Create a policy to back up a VM on a schedule with retention."
          />
        )}
      </div>

      {creating ? (
        <CreatePolicyModal
          onClose={() => setCreating(false)}
          onCreated={() => {
            setCreating(false);
            invalidate();
          }}
        />
      ) : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Delete backup policy?"
        confirmLabel="Delete"
        description={
          <span>
            This removes the policy <strong>{deleteTarget?.name}</strong> and stops its schedule. Existing backups
            are NOT deleted.
          </span>
        }
        onConfirm={doDelete}
        onClose={() => setDeleteTarget(null)}
      />
    </div>
  );
}

function CreatePolicyModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const providersQ = useVMProviders();
  const providers = providersQ.data ?? [];
  const backendsQ = useStorageBackends();
  const backends = backendsQ.data ?? [];

  const [name, setName] = useState("");
  const [providerId, setProviderId] = useState("");
  const [vmId, setVmId] = useState("");
  const [backendId, setBackendId] = useState("");
  const [intervalHours, setIntervalHours] = useState("24");
  const [retentionCount, setRetentionCount] = useState("7");
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);

  const vmsQ = useVMs(providerId, !!providerId);
  const vms = vmsQ.data ?? [];

  const valid = useMemo(
    () =>
      name.trim() !== "" &&
      providerId !== "" &&
      vmId !== "" &&
      backendId !== "" &&
      Number(intervalHours) > 0,
    [name, providerId, vmId, backendId, intervalHours],
  );

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.vmBackupPolicyCreate({
        name: name.trim(),
        providerId,
        vmId,
        backendId,
        intervalSeconds: Math.round(Number(intervalHours) * 3600),
        retentionCount: Number(retentionCount) || 7,
        enabled,
      });
      toast.success("Policy created", name.trim());
      onCreated();
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="New backup policy" onClose={onClose} wide>
      <div className="col" style={{ gap: "var(--sp-4)" }}>
        <TextField label="Policy name" placeholder="e.g. nightly db backup" value={name} onChange={(e) => setName(e.target.value)} />

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="Hypervisor"
              value={providerId}
              onChange={(e) => {
                setProviderId(e.target.value);
                setVmId("");
              }}
            >
              <option value="">Select a provider…</option>
              {providers.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.id} ({p.kind})
                </option>
              ))}
            </SelectField>
          </div>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="VM"
              value={vmId}
              disabled={!providerId || vmsQ.isLoading}
              onChange={(e) => setVmId(e.target.value)}
            >
              <option value="">{vmsQ.isLoading ? "Loading…" : "Select a VM…"}</option>
              {vms.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name}
                </option>
              ))}
            </SelectField>
          </div>
        </div>

        <SelectField label="Storage backend" value={backendId} onChange={(e) => setBackendId(e.target.value)}>
          <option value="">Select a backend…</option>
          {backends.map((b) => (
            <option key={b.id} value={b.id}>
              {b.name} ({b.type})
            </option>
          ))}
        </SelectField>

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-end" }}>
          <div style={{ flex: "1 1 180px" }}>
            <TextField
              label="Interval (hours)"
              type="number"
              value={intervalHours}
              onChange={(e) => setIntervalHours(e.target.value)}
            />
          </div>
          <div style={{ flex: "1 1 140px" }}>
            <TextField
              label="Retain (backups)"
              type="number"
              value={retentionCount}
              onChange={(e) => setRetentionCount(e.target.value)}
            />
          </div>
          <label className="checkbox-row" style={{ paddingBottom: 8 }}>
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            <span>Enabled (schedule immediately)</span>
          </label>
        </div>

        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" onClick={onClose}>
            Cancel
          </ActionButton>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Create policy
          </ActionButton>
        </div>
      </div>
    </Modal>
  );
}
