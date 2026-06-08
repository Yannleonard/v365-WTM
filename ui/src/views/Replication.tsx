// ui/src/views/Replication.tsx
//
// Cross-hypervisor VM REPLICATION (disaster recovery). It continuously/periodically
// replicates a VM from one hypervisor to a DIFFERENT one (e.g. KVM -> ESXi) on a
// schedule (the RPO target), and offers a one-click failover that powers on the
// replica on the target.
//
//   - a table of policies: source -> target, RPO target vs. measured RPO, last sync,
//     status badge, replica id;
//   - a create-policy wizard (source provider + VM, target provider + host, interval,
//     retain);
//   - per-policy actions: Run now, Failover (confirm dialog), and a history drawer.
//
// It mirrors Migration.tsx and reuses the SAME hypervisor-provider/VM hooks. The
// server schedules/runs the cycles (reusing the V2V engine) — this view only drives
// the policy CRUD + the run/failover actions and renders the live DR state.

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useVMProviders,
  useVMs,
  useReplicationPolicies,
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
import { IconRefresh, IconMigrate, IconAlert, IconCheck } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type { ReplicationPolicyView, ReplicationStatus, VMHost } from "../lib/types";

const WRITE_PERMS = ["replication.write", "*"];

function statusColor(s: ReplicationStatus | string): string {
  switch (s) {
    case "idle":
      return "var(--success)";
    case "syncing":
      return "var(--accent)";
    case "degraded":
      return "var(--warning)";
    case "error":
      return "var(--danger)";
    case "failed_over":
      return "var(--accent)";
    default:
      return "var(--muted)";
  }
}

function statusLabel(s: ReplicationStatus | string): string {
  return s === "failed_over" ? "failed over" : s;
}

// rpoText renders measured vs. target RPO, flagging a breach.
function rpoText(p: ReplicationPolicyView): { text: string; breach: boolean } {
  const target = p.intervalSeconds;
  const measured = p.state?.measuredRpoSeconds;
  if (measured == null || p.state?.lastSyncAt == null) {
    return { text: `target ${fmtSecs(target)} · never synced`, breach: false };
  }
  return {
    text: `${fmtSecs(measured)} / ${fmtSecs(target)}`,
    breach: measured > target && target > 0,
  };
}

function fmtSecs(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

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

export function Replication() {
  const queryClient = useQueryClient();
  const { permissions } = useAuth();
  const canWrite = canAny(permissions, WRITE_PERMS);

  const policiesQ = useReplicationPolicies();
  const policies = policiesQ.data ?? [];

  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [failoverTarget, setFailoverTarget] = useState<ReplicationPolicyView | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<ReplicationPolicyView | null>(null);
  const [historyTarget, setHistoryTarget] = useState<ReplicationPolicyView | null>(null);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["replication", "policies"] });

  const runNow = async (p: ReplicationPolicyView) => {
    setBusyId(p.id);
    try {
      await api.replicationRun(p.id);
      toast.success("Replication cycle complete", `${p.name} synced.`);
      invalidate();
    } catch (err) {
      toastError("Run failed", err);
    } finally {
      setBusyId(null);
    }
  };

  const doFailover = async () => {
    if (!failoverTarget) return;
    try {
      await api.replicationFailover(failoverTarget.id);
      toast.warning("Failover triggered", `Replica of ${failoverTarget.name} is now live.`);
      invalidate();
    } catch (err) {
      toastError("Failover failed", err);
    }
  };

  const doDelete = async () => {
    if (!deleteTarget) return;
    try {
      await api.replicationDelete(deleteTarget.id);
      toast.success("Policy deleted", deleteTarget.name);
      invalidate();
    } catch (err) {
      toastError("Delete failed", err);
    }
  };

  const columns: Column<ReplicationPolicyView>[] = [
    {
      key: "name",
      header: "Policy",
      sortValue: (p) => p.name,
      cell: (p) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{p.name}</span>
          <span className="mono text-xs muted">{p.sourceVmId}</span>
        </div>
      ),
    },
    {
      key: "route",
      header: "Source → Target",
      cell: (p) => (
        <span className="chip chip-mono text-xs">
          {p.sourceProviderId} → {p.targetProviderId}
          {p.targetHostId ? `/${p.targetHostId}` : ""}
        </span>
      ),
    },
    {
      key: "rpo",
      header: "RPO (measured / target)",
      cell: (p) => {
        const { text, breach } = rpoText(p);
        return (
          <span className={`text-xs mono ${breach ? "" : "muted"}`} style={breach ? { color: "var(--warning)" } : undefined}>
            {text}
          </span>
        );
      },
    },
    {
      key: "lastSync",
      header: "Last sync",
      sortValue: (p) => p.lastSyncAt ?? 0,
      cell: (p) =>
        p.lastSyncAt ? (
          <span className="text-xs muted nowrap">{timeAgo(new Date(p.lastSyncAt * 1000).toISOString())}</span>
        ) : (
          <span className="text-xs muted">never</span>
        ),
    },
    {
      key: "status",
      header: "Status",
      sortValue: (p) => p.status,
      cell: (p) => {
        const st = p.state?.status ?? (p.status as ReplicationStatus);
        return (
          <span className="row" style={{ gap: "var(--sp-2)" }}>
            <StatusDot color={statusColor(st)} pulse={st === "syncing"} />
            {statusLabel(st)}
          </span>
        );
      },
    },
    {
      key: "replica",
      header: "Replica",
      cell: (p) =>
        p.replicaVmId ?? p.state?.replicaVmId ? (
          <span className="mono text-xs muted">{p.replicaVmId ?? p.state?.replicaVmId}</span>
        ) : (
          <span className="text-xs muted">—</span>
        ),
    },
    {
      key: "actions",
      header: "",
      width: "260px",
      cell: (p) => {
        const failedOver = (p.state?.status ?? p.status) === "failed_over";
        return (
          <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
            <ActionButton
              variant="ghost"
              loading={busyId === p.id}
              disabled={!canWrite || failedOver}
              tooltip={failedOver ? "Policy is failed over" : "Trigger a replication cycle now"}
              onClick={() => runNow(p)}
            >
              Run now
            </ActionButton>
            <ActionButton
              variant="ghost"
              disabled={!canWrite || failedOver || !(p.replicaVmId ?? p.state?.replicaVmId)}
              tooltip={failedOver ? "Already failed over" : "Power on the replica on the target"}
              onClick={() => setFailoverTarget(p)}
            >
              Failover
            </ActionButton>
            <ActionButton variant="ghost" onClick={() => setHistoryTarget(p)} tooltip="View cycle history">
              History
            </ActionButton>
            <ActionButton
              variant="ghost"
              disabled={!canWrite}
              tooltip="Delete policy"
              onClick={() => setDeleteTarget(p)}
            >
              Delete
            </ActionButton>
          </div>
        );
      },
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="Replication (DR)"
        subtitle="Continuously replicate a VM to a DIFFERENT hypervisor for disaster recovery, with RPO tracking and one-click failover."
        actions={
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => policiesQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
            <ActionButton variant="primary" disabled={!canWrite} onClick={() => setCreating(true)}>
              <IconMigrate size={15} />
              New policy
            </ActionButton>
          </div>
        }
      />

      <div className="card">
        <div className="card-header">
          <span className="card-title">Replication policies</span>
          <span className="text-xs muted">{policies.length}</span>
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
              emptyTitle="No replication policies yet"
              emptyMessage="Create a policy to replicate a VM to another hypervisor for DR."
            />
          )}
        </div>
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
        open={!!failoverTarget}
        title="Fail over to replica?"
        variant="primary"
        confirmLabel="Fail over"
        description={
          <span>
            This will power on the replica of <strong>{failoverTarget?.name}</strong> on the target hypervisor and
            PAUSE further replication. Use this when the source is lost. The replica becomes the live VM.
          </span>
        }
        onConfirm={doFailover}
        onClose={() => setFailoverTarget(null)}
      />

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Delete replication policy?"
        confirmLabel="Delete"
        description={
          <span>
            This removes the policy <strong>{deleteTarget?.name}</strong> and stops its schedule. The existing replica
            VM on the target is NOT deleted.
          </span>
        }
        onConfirm={doDelete}
        onClose={() => setDeleteTarget(null)}
      />

      {historyTarget ? (
        <HistoryModal policy={historyTarget} onClose={() => setHistoryTarget(null)} />
      ) : null}
    </div>
  );
}

/* ============================ create wizard ============================ */

function CreatePolicyModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const providersQ = useVMProviders();
  const providers = providersQ.data ?? [];

  const [name, setName] = useState("");
  const [sourceProviderId, setSourceProviderId] = useState("");
  const [sourceVmId, setSourceVmId] = useState("");
  const [targetProviderId, setTargetProviderId] = useState("");
  const [targetHostId, setTargetHostId] = useState("");
  const [intervalSeconds, setIntervalSeconds] = useState("300");
  const [retain, setRetain] = useState("5");
  const [enabled, setEnabled] = useState(true);
  const [busy, setBusy] = useState(false);

  const sourceVmsQ = useVMs(sourceProviderId, !!sourceProviderId);
  const sourceVms = sourceVmsQ.data ?? [];

  const targetHostsQ = useQuery<VMHost[]>({
    queryKey: ["vm", "hosts", targetProviderId],
    queryFn: () => api.vmHosts(targetProviderId),
    enabled: !!targetProviderId,
  });
  const targetHosts = targetHostsQ.data ?? [];

  const valid = useMemo(
    () =>
      name.trim() !== "" &&
      sourceProviderId !== "" &&
      sourceVmId !== "" &&
      targetProviderId !== "" &&
      targetProviderId !== sourceProviderId &&
      Number(intervalSeconds) > 0,
    [name, sourceProviderId, sourceVmId, targetProviderId, intervalSeconds],
  );

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.replicationCreate({
        name: name.trim(),
        sourceProviderId,
        sourceVmId,
        targetProviderId,
        targetHostId: targetHostId || undefined,
        intervalSeconds: Number(intervalSeconds),
        retain: Number(retain) || 5,
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
    <Modal open title="New replication policy" onClose={onClose} wide>
      <div className="col" style={{ gap: "var(--sp-4)" }}>
        <TextField label="Policy name" placeholder="e.g. payroll-db DR" value={name} onChange={(e) => setName(e.target.value)} />

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="Source hypervisor"
              value={sourceProviderId}
              onChange={(e) => {
                setSourceProviderId(e.target.value);
                setSourceVmId("");
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
              label="Source VM"
              value={sourceVmId}
              disabled={!sourceProviderId || sourceVmsQ.isLoading}
              onChange={(e) => setSourceVmId(e.target.value)}
            >
              <option value="">{sourceVmsQ.isLoading ? "Loading…" : "Select a VM…"}</option>
              {sourceVms.map((v) => (
                <option key={v.id} value={v.id}>
                  {v.name}
                </option>
              ))}
            </SelectField>
          </div>
        </div>

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="Target hypervisor (must differ)"
              value={targetProviderId}
              onChange={(e) => {
                setTargetProviderId(e.target.value);
                setTargetHostId("");
              }}
            >
              <option value="">Select a provider…</option>
              {providers
                .filter((p) => p.id !== sourceProviderId)
                .map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.id} ({p.kind})
                  </option>
                ))}
            </SelectField>
          </div>
          <div style={{ flex: "1 1 220px" }}>
            <SelectField
              label="Target host (optional)"
              value={targetHostId}
              disabled={!targetProviderId || targetHostsQ.isLoading}
              onChange={(e) => setTargetHostId(e.target.value)}
            >
              <option value="">Auto / any host</option>
              {targetHosts.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}
                </option>
              ))}
            </SelectField>
          </div>
        </div>

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-end" }}>
          <div style={{ flex: "1 1 180px" }}>
            <TextField
              label="Interval / RPO target (seconds)"
              type="number"
              value={intervalSeconds}
              onChange={(e) => setIntervalSeconds(e.target.value)}
            />
          </div>
          <div style={{ flex: "1 1 140px" }}>
            <TextField label="Retain (cycles)" type="number" value={retain} onChange={(e) => setRetain(e.target.value)} />
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

/* ============================ history drawer ============================ */

function HistoryModal({ policy, onClose }: { policy: ReplicationPolicyView; onClose: () => void }) {
  // The list endpoint already embeds the live state (incl. history); re-read the
  // single policy for the freshest history while open.
  const q = useQuery<ReplicationPolicyView>({
    queryKey: ["replication", "policy", policy.id],
    queryFn: () => api.replicationPolicy(policy.id),
    refetchInterval: 5000,
    initialData: policy,
  });
  const state = q.data?.state;
  const history = [...(state?.history ?? [])].reverse();

  return (
    <Modal open title={`History — ${policy.name}`} onClose={onClose} wide>
      <div className="col" style={{ gap: "var(--sp-3)" }}>
        <div className="row-wrap" style={{ gap: "var(--sp-2)" }}>
          <span className="chip text-xs">cycles: {state?.cycleCount ?? 0}</span>
          <span className="chip text-xs">RPO target: {fmtSecs(policy.intervalSeconds)}</span>
          {state?.measuredRpoSeconds != null && state?.lastSyncAt != null ? (
            <span className="chip text-xs">measured RPO: {fmtSecs(state.measuredRpoSeconds)}</span>
          ) : null}
          <span className="chip text-xs">transferred: {fmtBytes(state?.bytesTransferred ?? 0)}</span>
        </div>

        {history.length === 0 ? (
          <span className="text-sm muted">No cycles have run yet.</span>
        ) : (
          <div className="col" style={{ gap: "var(--sp-2)" }}>
            {history.map((c, i) => (
              <div key={i} className="row" style={{ gap: "var(--sp-3)", alignItems: "center", padding: "6px 0", borderBottom: "1px solid var(--border)" }}>
                <span style={{ color: c.ok ? "var(--success)" : "var(--danger)" }}>
                  {c.ok ? <IconCheck size={15} /> : <IconAlert size={15} />}
                </span>
                <span className="text-xs muted nowrap" style={{ width: 120 }}>
                  {c.startedAt ? timeAgo(c.startedAt) : "—"}
                </span>
                <span className="chip text-xs">{c.firstCycle ? "initial" : "resync"}</span>
                <span className="text-xs muted">{(c.durationMs / 1000).toFixed(1)}s</span>
                <span className="text-xs muted">{fmtBytes(c.bytesTransferred)}</span>
                {c.error ? <span className="text-xs" style={{ color: "var(--danger)" }}>{c.error}</span> : null}
              </div>
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
}
