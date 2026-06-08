// ui/src/views/Migration.tsx
//
// V2V cross-hypervisor migration wizard:
//   1. pick source provider + VM and a target provider (+ optional name/power-on)
//   2. run POST /v2v/preflight -> show ok / blocking issues + the disk-format
//      conversion (sourceFormat -> targetFormat, sourceKind -> targetKind)
//   3. POST /v2v/migrate -> enqueue, then poll GET /v2v/jobs/{id} and render a
//      progress bar (phase + percent + message) until done/failed.
// A recent-jobs table (GET /v2v/jobs) sits below, polled live.

import { useMemo, useState } from "react";
import { useVMProviders, useVMs, useV2VJobs } from "../lib/hooks";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { SelectField, TextField } from "../components/Field";
import { ActionButton } from "../components/ActionButton";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { StatusDot } from "../components/StatusDot";
import { IconMigrate, IconRefresh, IconCheck, IconAlert } from "../components/icons";
import { toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type { V2VPreflightResult, V2VProgress } from "../lib/types";

// Terminal phases: stop polling the single-job query once reached.
function isTerminal(phase: string): boolean {
  return phase === "done" || phase === "failed";
}

function phaseColor(phase: string): string {
  if (phase === "done") return "var(--success)";
  if (phase === "failed") return "var(--danger)";
  return "var(--accent)";
}

export function Migration() {
  const queryClient = useQueryClient();
  const providersQ = useVMProviders();
  const providers = providersQ.data ?? [];

  const [sourceProviderId, setSourceProviderId] = useState("");
  const [sourceVmId, setSourceVmId] = useState("");
  const [targetProviderId, setTargetProviderId] = useState("");
  const [targetName, setTargetName] = useState("");
  const [powerOn, setPowerOn] = useState(false);

  const [preflight, setPreflight] = useState<V2VPreflightResult | null>(null);
  const [preflightBusy, setPreflightBusy] = useState(false);
  const [migrateBusy, setMigrateBusy] = useState(false);
  const [activeJobId, setActiveJobId] = useState<string | null>(null);

  const sourceVmsQ = useVMs(sourceProviderId, !!sourceProviderId);
  const sourceVms = sourceVmsQ.data ?? [];

  const jobsQ = useV2VJobs(true);

  // Poll the active job until terminal.
  const activeJobQ = useQuery<V2VProgress>({
    queryKey: ["v2v", "job", activeJobId],
    queryFn: () => api.v2vJob(activeJobId as string),
    enabled: !!activeJobId,
    refetchInterval: (q) => (q.state.data && isTerminal(q.state.data.phase) ? false : 2000),
  });

  const request = useMemo(
    () => ({
      sourceProviderId,
      sourceVmId,
      targetProviderId,
      targetName: targetName.trim() || undefined,
      powerOn,
    }),
    [sourceProviderId, sourceVmId, targetProviderId, targetName, powerOn],
  );

  const formValid = !!sourceProviderId && !!sourceVmId && !!targetProviderId;

  const runPreflight = async () => {
    if (!formValid) return;
    setPreflightBusy(true);
    setPreflight(null);
    try {
      const res = await api.v2vPreflight(request);
      setPreflight(res);
    } catch (err) {
      toastError("Preflight failed", err);
    } finally {
      setPreflightBusy(false);
    }
  };

  const runMigrate = async () => {
    if (!formValid) return;
    setMigrateBusy(true);
    try {
      const res = await api.v2vMigrate(request);
      setActiveJobId(res.id);
      queryClient.invalidateQueries({ queryKey: ["v2v", "jobs"] });
    } catch (err) {
      toastError("Migration failed", err);
    } finally {
      setMigrateBusy(false);
    }
  };

  // Reset preflight/job when the selection changes.
  const onSourceProvider = (id: string) => {
    setSourceProviderId(id);
    setSourceVmId("");
    setPreflight(null);
  };

  const jobColumns: Column<V2VProgress>[] = [
    {
      key: "id",
      header: "Job",
      sortValue: (j) => j.id,
      cell: (j) => <span className="mono text-xs">{j.id}</span>,
    },
    {
      key: "source",
      header: "Source",
      cell: (j) => <span className="mono text-xs muted truncate" style={{ maxWidth: 220, display: "inline-block" }}>{j.sourceProviderId ? `${j.sourceProviderId}/${j.sourceVmId ?? ""}` : j.sourceVmId ?? "—"}</span>,
    },
    {
      key: "target",
      header: "Target",
      cell: (j) => <span className="mono text-xs muted truncate" style={{ maxWidth: 220, display: "inline-block" }}>{j.targetProviderId ? `${j.targetProviderId}/${j.targetVmId ?? ""}` : j.targetVmId ?? "—"}</span>,
    },
    {
      key: "phase",
      header: "Phase",
      sortValue: (j) => j.phase,
      cell: (j) => (
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          <StatusDot color={phaseColor(j.phase)} pulse={!isTerminal(j.phase)} />
          {j.phase}
        </span>
      ),
    },
    {
      key: "percent",
      header: "Progress",
      width: "180px",
      cell: (j) => <ProgressBar percent={j.percent} color={phaseColor(j.phase)} />,
    },
    {
      key: "updated",
      header: "Updated",
      sortValue: (j) => j.updatedAt ?? "",
      cell: (j) => <span className="text-xs muted nowrap">{j.updatedAt ? timeAgo(j.updatedAt) : "—"}</span>,
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="Migration (V2V)"
        subtitle="Move a VM between hypervisors — export, convert, transfer, import."
        actions={
          <ActionButton variant="ghost" iconOnly tooltip="Refresh jobs" aria-label="Refresh jobs" onClick={() => jobsQ.refetch()}>
            <IconRefresh size={16} />
          </ActionButton>
        }
      />

      {/* ---- wizard ---- */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">New migration</span>
        </div>
        <div className="card-body col" style={{ gap: "var(--sp-4)" }}>
          <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-start" }}>
            <div style={{ flex: "1 1 240px" }}>
              <SelectField label="Source hypervisor" value={sourceProviderId} onChange={(e) => onSourceProvider(e.target.value)}>
                <option value="">Select a provider…</option>
                {providers.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.id} ({p.kind})
                  </option>
                ))}
              </SelectField>
            </div>
            <div style={{ flex: "1 1 240px" }}>
              <SelectField
                label="Source VM"
                value={sourceVmId}
                disabled={!sourceProviderId || sourceVmsQ.isLoading}
                onChange={(e) => {
                  setSourceVmId(e.target.value);
                  setPreflight(null);
                }}
              >
                <option value="">{sourceVmsQ.isLoading ? "Loading…" : "Select a VM…"}</option>
                {sourceVms.map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.name}
                  </option>
                ))}
              </SelectField>
            </div>
            <div style={{ flex: "1 1 240px" }}>
              <SelectField
                label="Target hypervisor"
                value={targetProviderId}
                onChange={(e) => {
                  setTargetProviderId(e.target.value);
                  setPreflight(null);
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
          </div>

          <div className="row-wrap" style={{ gap: "var(--sp-4)", alignItems: "flex-end" }}>
            <div style={{ flex: "1 1 240px" }}>
              <TextField
                label="Target VM name (optional)"
                placeholder="Defaults to the source name"
                value={targetName}
                onChange={(e) => setTargetName(e.target.value)}
              />
            </div>
            <label className="checkbox-row" style={{ paddingBottom: 8 }}>
              <input type="checkbox" checked={powerOn} onChange={(e) => setPowerOn(e.target.checked)} />
              <span>Power on after import</span>
            </label>
            <span className="spacer" />
            <div className="row" style={{ gap: "var(--sp-2)" }}>
              <ActionButton variant="ghost" loading={preflightBusy} disabled={!formValid} onClick={runPreflight}>
                Run preflight
              </ActionButton>
              <ActionButton
                variant="primary"
                loading={migrateBusy}
                disabled={!formValid || (preflight ? !preflight.ok : false)}
                tooltip={preflight && !preflight.ok ? "Resolve the preflight issues first" : undefined}
                onClick={runMigrate}
              >
                <IconMigrate size={15} />
                Start migration
              </ActionButton>
            </div>
          </div>

          {preflight ? <PreflightResult result={preflight} /> : null}
        </div>
      </div>

      {/* ---- active job progress ---- */}
      {activeJobId && activeJobQ.data ? <ActiveJobCard job={activeJobQ.data} /> : null}

      {/* ---- recent jobs ---- */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Recent jobs</span>
          <span className="text-xs muted">{(jobsQ.data ?? []).length}</span>
        </div>
        <div className="card-body" style={{ padding: 0 }}>
          {jobsQ.isLoading ? (
            <LoadingFill label="Loading jobs…" />
          ) : (
            <DataTable
              columns={jobColumns}
              rows={jobsQ.data ?? []}
              rowKey={(j) => j.id}
              defaultSortKey="updated"
              defaultSortDir="desc"
              onRowClick={(j) => setActiveJobId(j.id)}
              emptyIcon={<IconMigrate size={32} />}
              emptyTitle="No migration jobs yet"
              emptyMessage="Run a migration above to see its progress here."
            />
          )}
        </div>
      </div>
    </div>
  );
}

/* ============================ building blocks ============================ */

function PreflightResult({ result }: { result: V2VPreflightResult }) {
  return (
    <div className={`banner ${result.ok ? "success" : "warning"}`}>
      <span style={{ color: result.ok ? "var(--success)" : "var(--warning)" }}>
        {result.ok ? <IconCheck size={16} /> : <IconAlert size={16} />}
      </span>
      <div className="col" style={{ gap: "var(--sp-2)", flex: 1 }}>
        <span>
          {result.ok ? "Preflight passed — ready to migrate." : "Preflight found blocking issues."}
        </span>
        <div className="row-wrap" style={{ gap: "var(--sp-2)" }}>
          <span className="chip chip-mono text-xs">
            {result.sourceKind || "?"} → {result.targetKind || "?"}
          </span>
          <span className="chip chip-mono text-xs">
            {result.sourceFormat || "?"} → {result.targetFormat || "?"}
          </span>
        </div>
        {result.issues.length ? (
          <ul style={{ margin: 0, paddingLeft: "var(--sp-4)" }}>
            {result.issues.map((issue, i) => (
              <li key={i} className="text-sm">
                {issue}
              </li>
            ))}
          </ul>
        ) : null}
      </div>
    </div>
  );
}

function ActiveJobCard({ job }: { job: V2VProgress }) {
  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">
          <span className="row" style={{ gap: "var(--sp-2)" }}>
            <StatusDot color={phaseColor(job.phase)} pulse={!isTerminal(job.phase)} />
            Migration {job.id}
          </span>
        </span>
        <span className="chip">{job.phase}</span>
      </div>
      <div className="card-body col" style={{ gap: "var(--sp-3)" }}>
        <ProgressBar percent={job.percent} color={phaseColor(job.phase)} showLabel />
        {job.message ? <span className="text-sm secondary">{job.message}</span> : null}
        {job.error ? (
          <div className="banner warning">
            <span style={{ color: "var(--danger)" }}>
              <IconAlert size={16} />
            </span>
            <span>{job.error}</span>
          </div>
        ) : null}
        {job.phase === "done" ? (
          <EmptyState icon={<IconCheck size={28} />} title="Migration complete" message={job.targetVmId ? `Imported as ${job.targetVmId}.` : undefined} />
        ) : null}
      </div>
    </div>
  );
}

function ProgressBar({ percent, color, showLabel }: { percent: number; color: string; showLabel?: boolean }) {
  const pct = Math.max(0, Math.min(100, percent ?? 0));
  return (
    <div className="col" style={{ gap: 4, width: "100%" }}>
      <div
        style={{
          height: showLabel ? 10 : 8,
          borderRadius: 999,
          background: "rgba(110,138,166,0.16)",
          overflow: "hidden",
        }}
      >
        <span
          style={{
            display: "block",
            height: "100%",
            width: `${pct}%`,
            background: color,
            borderRadius: 999,
            transition: "width 240ms",
          }}
        />
      </div>
      {showLabel ? <span className="text-xs muted mono">{pct.toFixed(0)}%</span> : null}
    </div>
  );
}
