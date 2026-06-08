// ui/src/views/Alarms.tsx
//
// vSphere-style ALARMS — threshold-driven, stateful health rules over the unified
// VM/host/datastore inventory + metrics. Three sections:
//   • Active alarms — the alarms currently FIRING (object, severity, metric/value,
//     when raised), polled live (the engine evaluates server-side on a ticker);
//   • Alarm definitions — user rules (target + metric + comparator + threshold +
//     duration + severity + channel), with enable/disable + delete;
//   • Notification channels — webhook / email-stub destinations, with a Test button.
//
// Mirrors Insights.tsx / Replication.tsx and reuses existing chrome only
// (PageHeader, StatCard, DataTable, Modal, Field, ActionButton, badges, tokens).

import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useActiveAlarms, useAlarmDefinitions, useAlarmChannels } from "../lib/hooks";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { canAny } from "../lib/rbac";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { DataTable, type Column } from "../components/DataTable";
import { SelectField, TextField } from "../components/Field";
import { ActionButton } from "../components/ActionButton";
import { LoadingFill } from "../components/Spinner";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { IconAlert, IconCheck, IconShield, IconRefresh } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type {
  AlarmSeverity,
  AlarmTarget,
  AlarmMetric,
  AlarmComparator,
  AlarmChannelType,
  AlarmDefinition,
  AlarmDefinitionInput,
  AlarmInstance,
  AlarmChannel,
} from "../lib/types";

const WRITE_PERMS = ["alarms.write", "*"];

const SEV_COLOR: Record<AlarmSeverity, string> = {
  critical: "var(--danger)",
  warning: "var(--warning)",
  info: "var(--accent)",
};
const SEV_LABEL: Record<AlarmSeverity, string> = {
  critical: "Critical",
  warning: "Warning",
  info: "Info",
};

const METRIC_LABEL: Record<AlarmMetric, string> = {
  cpu: "CPU %",
  memory: "Memory %",
  disk: "Disk I/O (bytes/s)",
  storage_pct: "Datastore used %",
  state: "State",
};
const CMP_LABEL: Record<AlarmComparator, string> = { gt: ">", lt: "<", eq: "=" };

function metricValueText(a: AlarmInstance): string {
  if (a.metric === "state") return `state = ${a.stateRaw ?? "?"}`;
  if (a.metric === "cpu" || a.metric === "memory" || a.metric === "storage_pct")
    return `${METRIC_LABEL[a.metric]}: ${a.value.toFixed(1)}%`;
  return `${METRIC_LABEL[a.metric]}: ${a.value.toFixed(0)}`;
}

function SevBadge({ sev }: { sev: AlarmSeverity }) {
  return (
    <span className="badge" style={{ background: SEV_COLOR[sev], color: "#fff" }}>
      {SEV_LABEL[sev]}
    </span>
  );
}

export function Alarms() {
  const queryClient = useQueryClient();
  const { permissions } = useAuth();
  const canWrite = canAny(permissions, WRITE_PERMS);

  const activeQ = useActiveAlarms();
  const defsQ = useAlarmDefinitions();
  const channelsQ = useAlarmChannels();

  const active = activeQ.data ?? [];
  const defs = defsQ.data ?? [];
  const channels = channelsQ.data ?? [];

  const [creatingDef, setCreatingDef] = useState(false);
  const [creatingCh, setCreatingCh] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [deleteDef, setDeleteDef] = useState<AlarmDefinition | null>(null);
  const [deleteCh, setDeleteCh] = useState<AlarmChannel | null>(null);

  const invalidateAll = () => {
    queryClient.invalidateQueries({ queryKey: ["alarms"] });
  };

  const counts = useMemo(() => {
    const c = { critical: 0, warning: 0, info: 0 };
    for (const a of active) c[a.severity]++;
    return c;
  }, [active]);

  const channelName = (id: string) => channels.find((c) => c.id === id)?.name ?? id;

  const toggleEnabled = async (d: AlarmDefinition) => {
    setBusyId(d.id);
    try {
      await api.alarmDefinitionUpdate(d.id, defToInput({ ...d, enabled: !d.enabled }));
      toast.success(d.enabled ? "Alarm disabled" : "Alarm enabled", d.name);
      invalidateAll();
    } catch (err) {
      toastError("Update failed", err);
    } finally {
      setBusyId(null);
    }
  };

  const doDeleteDef = async () => {
    if (!deleteDef) return;
    try {
      await api.alarmDefinitionDelete(deleteDef.id);
      toast.success("Definition deleted", deleteDef.name);
      invalidateAll();
    } catch (err) {
      toastError("Delete failed", err);
    }
  };

  const doDeleteCh = async () => {
    if (!deleteCh) return;
    try {
      await api.alarmChannelDelete(deleteCh.id);
      toast.success("Channel deleted", deleteCh.name);
      invalidateAll();
    } catch (err) {
      toastError("Delete failed", err);
    }
  };

  const testChannel = async (c: AlarmChannel) => {
    setBusyId(c.id);
    try {
      await api.alarmChannelTest(c.id);
      toast.success("Test notification sent", c.type === "webhook" ? "Webhook POSTed." : "Logged (email stub).");
    } catch (err) {
      toastError("Test failed", err);
    } finally {
      setBusyId(null);
    }
  };

  /* ---------- active alarms table ---------- */
  const activeCols: Column<AlarmInstance>[] = [
    {
      key: "object",
      header: "Object",
      sortValue: (a) => a.objectName,
      cell: (a) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{a.objectName}</span>
          <span className="text-xs muted">{a.objectType}</span>
        </div>
      ),
    },
    {
      key: "severity",
      header: "Severity",
      sortValue: (a) => a.severity,
      cell: (a) => <SevBadge sev={a.severity} />,
    },
    { key: "definition", header: "Alarm", sortValue: (a) => a.definitionName, cell: (a) => a.definitionName },
    {
      key: "value",
      header: "Metric / value",
      cell: (a) => <span className="mono text-xs">{metricValueText(a)}</span>,
    },
    {
      key: "raised",
      header: "Raised",
      sortValue: (a) => a.raisedAt,
      cell: (a) => <span className="text-xs muted nowrap">{timeAgo(a.raisedAt)}</span>,
    },
  ];

  /* ---------- definitions table ---------- */
  const defCols: Column<AlarmDefinition>[] = [
    {
      key: "name",
      header: "Definition",
      sortValue: (d) => d.name,
      cell: (d) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{d.name}</span>
          <span className="text-xs muted">{d.target}</span>
        </div>
      ),
    },
    {
      key: "rule",
      header: "Rule",
      cell: (d) => (
        <span className="chip chip-mono text-xs">
          {d.metric === "state"
            ? `state = ${d.stateValue}`
            : `${METRIC_LABEL[d.metric]} ${CMP_LABEL[d.comparator]} ${d.threshold}`}
          {d.durationSec > 0 ? ` for ${d.durationSec}s` : ""}
        </span>
      ),
    },
    { key: "severity", header: "Severity", sortValue: (d) => d.severity, cell: (d) => <SevBadge sev={d.severity} /> },
    {
      key: "channels",
      header: "Notify",
      cell: (d) =>
        d.notifyChannelIds.length === 0 ? (
          <span className="text-xs muted">—</span>
        ) : (
          <span className="text-xs">{d.notifyChannelIds.map(channelName).join(", ")}</span>
        ),
    },
    {
      key: "enabled",
      header: "Enabled",
      sortValue: (d) => (d.enabled ? 1 : 0),
      cell: (d) =>
        d.enabled ? (
          <span className="badge" style={{ background: "var(--success)", color: "#fff" }}>On</span>
        ) : (
          <span className="badge">Off</span>
        ),
    },
    {
      key: "actions",
      header: "",
      width: "220px",
      cell: (d) => (
        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" loading={busyId === d.id} disabled={!canWrite} onClick={() => toggleEnabled(d)}>
            {d.enabled ? "Disable" : "Enable"}
          </ActionButton>
          <ActionButton variant="ghost" disabled={!canWrite} tooltip="Delete definition" onClick={() => setDeleteDef(d)}>
            Delete
          </ActionButton>
        </div>
      ),
    },
  ];

  /* ---------- channels table ---------- */
  const chCols: Column<AlarmChannel>[] = [
    { key: "name", header: "Channel", sortValue: (c) => c.name, cell: (c) => <span style={{ fontWeight: 600 }}>{c.name}</span> },
    { key: "type", header: "Type", sortValue: (c) => c.type, cell: (c) => <span className="chip text-xs">{c.type}</span> },
    { key: "config", header: "Destination", cell: (c) => <span className="mono text-xs truncate">{c.config}</span> },
    {
      key: "actions",
      header: "",
      width: "180px",
      cell: (c) => (
        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" loading={busyId === c.id} disabled={!canWrite} onClick={() => testChannel(c)}>
            Test
          </ActionButton>
          <ActionButton variant="ghost" disabled={!canWrite} tooltip="Delete channel" onClick={() => setDeleteCh(c)}>
            Delete
          </ActionButton>
        </div>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="Alarms"
        subtitle="Threshold-driven, stateful health alarms across VMs, hosts and datastores — with notification channels."
        actions={
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => activeQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
            <ActionButton variant="primary" disabled={!canWrite} onClick={() => setCreatingDef(true)}>
              <IconAlert size={15} /> New definition
            </ActionButton>
          </div>
        }
      />

      {/* severity KPIs */}
      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))", gap: "var(--sp-4)" }}>
        <StatCard label="Critical" icon={<IconAlert size={18} />} accent="var(--danger)" value={counts.critical} sub="firing now" />
        <StatCard label="Warnings" icon={<IconShield size={18} />} accent="var(--warning)" value={counts.warning} sub="firing now" />
        <StatCard label="Info" icon={<IconCheck size={18} />} accent="var(--accent)" value={counts.info} sub="firing now" />
        <StatCard label="Definitions" icon={<IconShield size={18} />} value={defs.length} sub={`${defs.filter((d) => d.enabled).length} enabled`} />
      </div>

      {/* active alarms */}
      <div className="card" style={{ marginTop: "var(--sp-4)" }}>
        <div className="card-header">
          <span className="card-title">Active alarms</span>
          <span className="text-xs muted">{active.length}</span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {activeQ.isLoading ? (
            <LoadingFill label="Evaluating alarms…" />
          ) : (
            <DataTable
              columns={activeCols}
              rows={active}
              rowKey={(a) => a.id}
              defaultSortKey="severity"
              defaultSortDir="asc"
              emptyIcon={<IconCheck size={32} />}
              emptyTitle="All clear"
              emptyMessage="No alarms are currently firing."
            />
          )}
        </div>
      </div>

      {/* definitions */}
      <div className="card" style={{ marginTop: "var(--sp-4)" }}>
        <div className="card-header">
          <span className="card-title">Alarm definitions</span>
          <span className="text-xs muted">{defs.length}</span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {defsQ.isLoading ? (
            <LoadingFill label="Loading definitions…" />
          ) : (
            <DataTable
              columns={defCols}
              rows={defs}
              rowKey={(d) => d.id}
              defaultSortKey="name"
              defaultSortDir="asc"
              emptyIcon={<IconAlert size={32} />}
              emptyTitle="No alarm definitions yet"
              emptyMessage="Create a definition to start watching a metric/state threshold."
            />
          )}
        </div>
      </div>

      {/* channels */}
      <div className="card" style={{ marginTop: "var(--sp-4)" }}>
        <div className="card-header">
          <span className="card-title">Notification channels</span>
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            <span className="text-xs muted">{channels.length}</span>
            <ActionButton variant="ghost" disabled={!canWrite} onClick={() => setCreatingCh(true)}>
              New channel
            </ActionButton>
          </div>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {channelsQ.isLoading ? (
            <LoadingFill label="Loading channels…" />
          ) : (
            <DataTable
              columns={chCols}
              rows={channels}
              rowKey={(c) => c.id}
              defaultSortKey="name"
              defaultSortDir="asc"
              emptyIcon={<IconRefresh size={32} />}
              emptyTitle="No channels yet"
              emptyMessage="Add a webhook to be notified when an alarm fires or clears."
            />
          )}
        </div>
      </div>

      {creatingDef ? (
        <CreateDefinitionModal
          channels={channels}
          onClose={() => setCreatingDef(false)}
          onCreated={() => {
            setCreatingDef(false);
            invalidateAll();
          }}
        />
      ) : null}

      {creatingCh ? (
        <CreateChannelModal
          onClose={() => setCreatingCh(false)}
          onCreated={() => {
            setCreatingCh(false);
            invalidateAll();
          }}
        />
      ) : null}

      <ConfirmDestructiveDialog
        open={!!deleteDef}
        title="Delete alarm definition?"
        confirmLabel="Delete"
        description={<span>This removes <strong>{deleteDef?.name}</strong> and clears any alarms it raised.</span>}
        onConfirm={doDeleteDef}
        onClose={() => setDeleteDef(null)}
      />
      <ConfirmDestructiveDialog
        open={!!deleteCh}
        title="Delete notification channel?"
        confirmLabel="Delete"
        description={<span>This removes <strong>{deleteCh?.name}</strong>. Definitions still referencing it simply skip it.</span>}
        onConfirm={doDeleteCh}
        onClose={() => setDeleteCh(null)}
      />
    </div>
  );
}

function defToInput(d: AlarmDefinition): AlarmDefinitionInput {
  return {
    name: d.name,
    target: d.target,
    metric: d.metric,
    comparator: d.comparator,
    threshold: d.threshold,
    stateValue: d.stateValue,
    durationSec: d.durationSec,
    severity: d.severity,
    enabled: d.enabled,
    notifyChannelIds: d.notifyChannelIds,
  };
}

/* ============================ create definition ============================ */

function CreateDefinitionModal({
  channels,
  onClose,
  onCreated,
}: {
  channels: AlarmChannel[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const [name, setName] = useState("");
  const [target, setTarget] = useState<AlarmTarget>("vm");
  const [metric, setMetric] = useState<AlarmMetric>("cpu");
  const [comparator, setComparator] = useState<AlarmComparator>("gt");
  const [threshold, setThreshold] = useState("90");
  const [stateValue, setStateValue] = useState("error");
  const [durationSec, setDurationSec] = useState("0");
  const [severity, setSeverity] = useState<AlarmSeverity>("warning");
  const [enabled, setEnabled] = useState(true);
  const [channelIds, setChannelIds] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  const isState = metric === "state";

  // Metric options depend on the target.
  const metricOptions: AlarmMetric[] =
    target === "datastore" ? ["storage_pct"] : target === "host" ? ["cpu", "memory", "state"] : ["cpu", "memory", "disk", "state"];

  const valid = useMemo(
    () => name.trim() !== "" && (isState ? stateValue.trim() !== "" : threshold.trim() !== ""),
    [name, isState, stateValue, threshold],
  );

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.alarmDefinitionCreate({
        name: name.trim(),
        target,
        metric,
        comparator,
        threshold: Number(threshold) || 0,
        stateValue: isState ? stateValue.trim() : undefined,
        durationSec: Number(durationSec) || 0,
        severity,
        enabled,
        notifyChannelIds: channelIds,
      });
      toast.success("Definition created", name.trim());
      onCreated();
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setBusy(false);
    }
  };

  const toggleChannel = (id: string) =>
    setChannelIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));

  return (
    <Modal open title="New alarm definition" onClose={onClose} wide>
      <div className="col" style={{ gap: "var(--sp-4)" }}>
        <TextField label="Name" placeholder="e.g. VM CPU saturated" value={name} onChange={(e) => setName(e.target.value)} />

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
          <div style={{ flex: "1 1 180px" }}>
            <SelectField
              label="Target"
              value={target}
              onChange={(e) => {
                const t = e.target.value as AlarmTarget;
                setTarget(t);
                setMetric(t === "datastore" ? "storage_pct" : "cpu");
              }}
            >
              <option value="vm">VM</option>
              <option value="host">Host</option>
              <option value="datastore">Datastore</option>
            </SelectField>
          </div>
          <div style={{ flex: "1 1 180px" }}>
            <SelectField label="Metric" value={metric} onChange={(e) => setMetric(e.target.value as AlarmMetric)}>
              {metricOptions.map((m) => (
                <option key={m} value={m}>
                  {METRIC_LABEL[m]}
                </option>
              ))}
            </SelectField>
          </div>
        </div>

        {isState ? (
          <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
            <div style={{ flex: "1 1 200px" }}>
              <TextField
                label="State equals"
                placeholder="error"
                value={stateValue}
                onChange={(e) => setStateValue(e.target.value)}
              />
            </div>
          </div>
        ) : (
          <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
            <div style={{ flex: "1 1 120px" }}>
              <SelectField label="Comparator" value={comparator} onChange={(e) => setComparator(e.target.value as AlarmComparator)}>
                <option value="gt">greater than</option>
                <option value="lt">less than</option>
                <option value="eq">equals</option>
              </SelectField>
            </div>
            <div style={{ flex: "1 1 140px" }}>
              <TextField label="Threshold" type="number" value={threshold} onChange={(e) => setThreshold(e.target.value)} />
            </div>
          </div>
        )}

        <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-end" }}>
          <div style={{ flex: "1 1 160px" }}>
            <TextField
              label="Duration (seconds)"
              type="number"
              value={durationSec}
              onChange={(e) => setDurationSec(e.target.value)}
            />
          </div>
          <div style={{ flex: "1 1 160px" }}>
            <SelectField label="Severity" value={severity} onChange={(e) => setSeverity(e.target.value as AlarmSeverity)}>
              <option value="info">Info</option>
              <option value="warning">Warning</option>
              <option value="critical">Critical</option>
            </SelectField>
          </div>
          <label className="checkbox-row" style={{ paddingBottom: 8 }}>
            <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
            <span>Enabled</span>
          </label>
        </div>

        {channels.length > 0 ? (
          <div className="col" style={{ gap: "var(--sp-2)" }}>
            <span className="text-sm" style={{ fontWeight: 600 }}>Notify channels</span>
            <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
              {channels.map((c) => (
                <label key={c.id} className="checkbox-row">
                  <input type="checkbox" checked={channelIds.includes(c.id)} onChange={() => toggleChannel(c.id)} />
                  <span>{c.name} <span className="text-xs muted">({c.type})</span></span>
                </label>
              ))}
            </div>
          </div>
        ) : (
          <span className="text-xs muted">No channels yet — add one below to get notified on raise/clear.</span>
        )}

        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" onClick={onClose}>Cancel</ActionButton>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Create definition
          </ActionButton>
        </div>
      </div>
    </Modal>
  );
}

/* ============================ create channel ============================ */

function CreateChannelModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState<AlarmChannelType>("webhook");
  const [config, setConfig] = useState("");
  const [busy, setBusy] = useState(false);

  const valid = name.trim() !== "" && (type !== "webhook" || config.trim() !== "");

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.alarmChannelCreate({ name: name.trim(), type, config: config.trim() });
      toast.success("Channel created", name.trim());
      onCreated();
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="New notification channel" onClose={onClose}>
      <div className="col" style={{ gap: "var(--sp-4)" }}>
        <TextField label="Name" placeholder="e.g. ops-webhook" value={name} onChange={(e) => setName(e.target.value)} />
        <SelectField label="Type" value={type} onChange={(e) => setType(e.target.value as AlarmChannelType)}>
          <option value="webhook">Webhook (HTTP POST)</option>
          <option value="email-stub">Email (stub / logged)</option>
        </SelectField>
        <TextField
          label={type === "webhook" ? "Webhook URL" : "Email address"}
          placeholder={type === "webhook" ? "https://hooks.example.com/alarms" : "ops@example.com"}
          value={config}
          onChange={(e) => setConfig(e.target.value)}
        />
        <div className="row" style={{ gap: "var(--sp-2)", justifyContent: "flex-end" }}>
          <ActionButton variant="ghost" onClick={onClose}>Cancel</ActionButton>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Create channel
          </ActionButton>
        </div>
      </div>
    </Modal>
  );
}
