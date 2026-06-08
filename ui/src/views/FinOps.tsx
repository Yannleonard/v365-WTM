// ui/src/views/FinOps.tsx
//
// UniHV's unified FinOps cost dashboard — the demo wow-factor. It prices every
// VM AND container across every hypervisor AND orchestrator from one rate card,
// then surfaces total spend, by-hypervisor / by-cluster breakdowns, the top
// spenders, and rightsizing recommendations with projected $ savings. No
// competitor does this across both domains in one pane.

import { useMemo, useState } from "react";
import {
  ResponsiveContainer,
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  PieChart,
  Pie,
  Cell,
} from "recharts";
import { useAuth } from "../lib/auth";
import { useFinOpsSummary, useFinOpsRightsizing, useRateCard, qk } from "../lib/hooks";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { toast } from "../lib/toast";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { DataTable, type Column } from "../components/DataTable";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { Modal } from "../components/Modal";
import { IconStats, IconVM, IconWorkloads, IconScale, IconSettings, IconCheck } from "../components/icons";
import type { EntityCost, GroupCost, Recommendation, RateCard } from "../lib/types";

function money(v: number | undefined, currency: string, digits = 2): string {
  if (v === undefined || Number.isNaN(v)) return "—";
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency: currency || "USD",
      maximumFractionDigits: digits,
      minimumFractionDigits: digits,
    }).format(v);
  } catch {
    return `${(currency || "USD")} ${v.toFixed(digits)}`;
  }
}

const PALETTE = ["#2496ED", "#13A688", "#7C6BD0", "#E08A00", "#8B5E3C", "#326CE5"];

export function FinOps() {
  const { can } = useAuth();
  const summaryQ = useFinOpsSummary();
  const rsQ = useFinOpsRightsizing();
  const [showRateCard, setShowRateCard] = useState(false);

  const s = summaryQ.data;
  const cur = s?.currency ?? "USD";
  const canEdit = can("settings.update");

  if (summaryQ.isLoading && !s) {
    return <LoadingFill label="Crunching cost data…" />;
  }

  const recs = rsQ.data?.recommendations ?? [];

  return (
    <div className="page">
      <PageHeader
        title="Cost (FinOps)"
        subtitle="Unified spend, breakdowns and rightsizing across hypervisors and orchestrators"
        actions={
          canEdit ? (
            <button className="btn" onClick={() => setShowRateCard(true)}>
              <IconSettings size={16} /> Rate card
            </button>
          ) : null
        }
      />

      {summaryQ.isError ? (
        <div className="banner danger">Failed to load cost data.</div>
      ) : null}

      {/* ---- headline KPIs ---- */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(200px, 1fr))",
          gap: "var(--sp-4)",
        }}
      >
        <StatCard
          label="Projected monthly spend"
          icon={<IconStats size={18} />}
          accent="var(--accent)"
          value={money(s?.totalMonthly, cur, 0)}
          sub={`${money(s?.totalHourly, cur, 3)} / hour`}
        />
        <StatCard
          label="VM spend / month"
          icon={<IconVM size={18} />}
          accent="var(--chart-1)"
          value={money((s?.vmHourly ?? 0) * 730, cur, 0)}
          sub="across all hypervisors"
        />
        <StatCard
          label="Container spend / month"
          icon={<IconWorkloads size={18} />}
          accent="var(--success)"
          value={money((s?.containerHourly ?? 0) * 730, cur, 0)}
          sub="across all orchestrators"
        />
        <StatCard
          label="Potential savings / month"
          icon={<IconScale size={18} />}
          accent="var(--warning)"
          value={money(s?.potentialMonthlySavings, cur, 0)}
          sub={`${s?.recommendations ?? 0} rightsizing actions`}
        />
        <StatCard
          label="Priced entities"
          icon={<IconStats size={18} />}
          value={s?.entities ?? 0}
          sub={`${s?.runningEntities ?? 0} running`}
        />
      </div>

      {/* ---- breakdown charts ---- */}
      <div className="dash-charts" style={{ marginTop: "var(--sp-4)" }}>
        <div className="dash-card">
          <div className="dash-card-head">
            <span className="dash-card-title">Spend by hypervisor / orchestrator</span>
            <span className="dash-card-hint">{cur}/mo</span>
          </div>
          <div className="dash-card-body">
            <GroupBars groups={s?.byHypervisor ?? []} currency={cur} />
          </div>
        </div>

        <div className="dash-card">
          <div className="dash-card-head">
            <span className="dash-card-title">VMs vs containers</span>
            <span className="dash-card-hint">share of spend</span>
          </div>
          <div className="dash-card-body">
            <DomainDonut groups={s?.byDomain ?? []} currency={cur} />
          </div>
        </div>

        <div className="dash-card">
          <div className="dash-card-head">
            <span className="dash-card-title">Spend by cluster</span>
            <span className="dash-card-hint">{cur}/mo</span>
          </div>
          <div className="dash-card-body">
            <GroupBars groups={s?.byCluster ?? []} currency={cur} />
          </div>
        </div>
      </div>

      {/* ---- top spenders ---- */}
      <div className="card" style={{ marginTop: "var(--sp-4)" }}>
        <div className="dash-card-head">
          <span className="dash-card-title">Top spenders</span>
          <span className="dash-card-hint">monthly cost, descending</span>
        </div>
        <TopSpenders rows={s?.topSpenders ?? []} currency={cur} />
      </div>

      {/* ---- rightsizing recommendations ---- */}
      <div className="card" style={{ marginTop: "var(--sp-4)" }}>
        <div className="dash-card-head">
          <span className="dash-card-title">Rightsizing recommendations</span>
          <span className="dash-card-hint">
            {recs.length > 0
              ? `${money(rsQ.data?.potentialMonthlySavings, cur, 0)} / month reclaimable`
              : "no waste detected"}
          </span>
        </div>
        <RightsizingTable rows={recs} currency={cur} loading={rsQ.isLoading} />
      </div>

      {showRateCard ? <RateCardModal currency={cur} onClose={() => setShowRateCard(false)} /> : null}
    </div>
  );
}

/* ----------------------------- breakdown charts ----------------------------- */

function GroupBars({ groups, currency }: { groups: GroupCost[]; currency: string }) {
  const rows = useMemo(
    () =>
      groups
        .slice(0, 8)
        .map((g) => ({ name: g.label || g.key || "—", value: g.monthlyCost, count: g.count })),
    [groups],
  );
  if (rows.length === 0) {
    return <EmptyState icon={<IconStats size={28} />} title="No cost data" />;
  }
  return (
    <div className="dash-chart-frame" style={{ height: Math.max(160, rows.length * 38 + 24) }}>
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={rows} layout="vertical" margin={{ top: 4, right: 16, bottom: 4, left: 4 }}>
          <CartesianGrid horizontal={false} stroke="var(--chart-grid)" />
          <XAxis
            type="number"
            tick={{ fill: "var(--chart-axis)", fontSize: 11 }}
            tickLine={false}
            tickFormatter={(v: number) => money(v, currency, 0)}
          />
          <YAxis
            type="category"
            dataKey="name"
            width={120}
            tick={{ fill: "var(--chart-axis)", fontSize: 11 }}
            tickLine={false}
          />
          <Tooltip
            formatter={(v: number) => money(v, currency, 0)}
            contentStyle={{ background: "var(--bg-surface)", border: "1px solid var(--border)", borderRadius: 8 }}
          />
          <Bar dataKey="value" fill="#2496ED" radius={[0, 4, 4, 0]} maxBarSize={22} />
        </BarChart>
      </ResponsiveContainer>
    </div>
  );
}

function DomainDonut({ groups, currency }: { groups: GroupCost[]; currency: string }) {
  const data = groups.filter((g) => g.monthlyCost > 0).map((g) => ({ name: g.label || g.key, value: g.monthlyCost }));
  if (data.length === 0) {
    return <EmptyState icon={<IconStats size={28} />} title="No cost data" />;
  }
  return (
    <div className="dash-chart-frame" style={{ height: 220 }}>
      <ResponsiveContainer width="100%" height="100%">
        <PieChart>
          <Pie data={data} dataKey="value" nameKey="name" cx="50%" cy="50%" innerRadius="55%" outerRadius="90%" paddingAngle={2}>
            {data.map((_, i) => (
              <Cell key={i} fill={PALETTE[i % PALETTE.length]} />
            ))}
          </Pie>
          <Tooltip
            formatter={(v: number) => money(v, currency, 0)}
            contentStyle={{ background: "var(--bg-surface)", border: "1px solid var(--border)", borderRadius: 8 }}
          />
        </PieChart>
      </ResponsiveContainer>
    </div>
  );
}

/* ----------------------------- tables ----------------------------- */

function TopSpenders({ rows, currency }: { rows: EntityCost[]; currency: string }) {
  const columns: Column<EntityCost>[] = [
    {
      key: "name",
      header: "Entity",
      cell: (r) => (
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          {r.domain === "vm" ? <IconVM size={16} /> : <IconWorkloads size={16} />}
          <span className="truncate" style={{ fontWeight: 600 }}>{r.name || r.id}</span>
        </span>
      ),
      sortValue: (r) => r.name,
    },
    { key: "kind", header: "Platform", cell: (r) => <span className="badge">{r.kind || "—"}</span>, sortValue: (r) => r.kind },
    {
      key: "alloc",
      header: "Allocation",
      cell: (r) => (
        <span className="text-xs muted">
          {r.vcpus} vCPU · {r.memoryGb.toFixed(1)} GiB{r.storageGb > 0 ? ` · ${r.storageGb.toFixed(0)} GiB disk` : ""}
        </span>
      ),
    },
    {
      key: "state",
      header: "State",
      cell: (r) => (
        <span className={`badge ${r.running ? "ok" : ""}`}>{r.running ? "running" : "stopped"}</span>
      ),
      sortValue: (r) => (r.running ? 1 : 0),
    },
    {
      key: "monthly",
      header: `${currency}/mo`,
      align: "right",
      cell: (r) => <strong>{money(r.monthlyCost, currency, 0)}</strong>,
      sortValue: (r) => r.monthlyCost,
    },
  ];
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(r) => `${r.providerId}:${r.domain}:${r.id}`}
      defaultSortKey="monthly"
      defaultSortDir="desc"
      emptyIcon={<IconStats size={32} />}
      emptyTitle="No priced entities"
    />
  );
}

function RightsizingTable({
  rows,
  currency,
  loading,
}: {
  rows: Recommendation[];
  currency: string;
  loading: boolean;
}) {
  if (loading && rows.length === 0) return <LoadingFill label="Analyzing utilization…" />;
  const columns: Column<Recommendation>[] = [
    {
      key: "name",
      header: "Entity",
      cell: (r) => (
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          {r.domain === "vm" ? <IconVM size={16} /> : <IconWorkloads size={16} />}
          <span className="truncate" style={{ fontWeight: 600 }}>{r.name || r.entityId}</span>
        </span>
      ),
      sortValue: (r) => r.name,
    },
    {
      key: "kind",
      header: "Finding",
      cell: (r) => (
        <span className={`badge ${r.recommendation === "idle" ? "warn" : ""}`}>
          {r.recommendation === "idle" ? "Idle — reclaim" : "Oversized — downsize"}
        </span>
      ),
      sortValue: (r) => r.recommendation,
    },
    {
      key: "change",
      header: "Suggested change",
      cell: (r) =>
        r.recommendation === "idle" ? (
          <span className="text-xs muted">Power off / decommission</span>
        ) : (
          <span className="text-xs muted">
            {r.currentVcpus}→{r.suggestedVcpus} vCPU · {r.currentMemGb.toFixed(1)}→{r.suggestedMemGb.toFixed(1)} GiB
          </span>
        ),
    },
    {
      key: "util",
      header: "Peak CPU / mem",
      cell: (r) => (
        <span className="text-xs muted">
          {r.utilization.peakCpuPercent.toFixed(0)}% / {r.utilization.peakMemPercent.toFixed(0)}%
        </span>
      ),
      sortValue: (r) => r.utilization.peakCpuPercent,
    },
    {
      key: "savings",
      header: `Savings ${currency}/mo`,
      align: "right",
      cell: (r) => <strong style={{ color: "var(--success)" }}>{money(r.monthlySavings, currency, 0)}</strong>,
      sortValue: (r) => r.monthlySavings,
    },
  ];
  return (
    <DataTable
      columns={columns}
      rows={rows}
      rowKey={(r) => `${r.providerId}:${r.entityId}`}
      defaultSortKey="savings"
      defaultSortDir="desc"
      emptyIcon={<IconCheck size={32} />}
      emptyTitle="No rightsizing opportunities"
      emptyMessage="Every running VM with metrics is appropriately sized."
    />
  );
}

/* ----------------------------- rate card editor ----------------------------- */

function RateCardModal({ currency, onClose }: { currency: string; onClose: () => void }) {
  const rcQ = useRateCard();
  const qc = useQueryClient();
  const [form, setForm] = useState<RateCard | null>(null);
  const [saving, setSaving] = useState(false);

  const rc = form ?? rcQ.data ?? null;

  const setField = (k: keyof RateCard, v: string) => {
    if (!rc) return;
    setForm({ ...rc, [k]: k === "currency" ? v : Number(v) });
  };

  const save = async () => {
    if (!rc) return;
    setSaving(true);
    try {
      await api.finopsRateCardUpdate(rc);
      await qc.invalidateQueries({ queryKey: qk.finopsRateCard });
      await qc.invalidateQueries({ queryKey: qk.finopsSummary });
      await qc.invalidateQueries({ queryKey: qk.finopsRightsizing });
      toast.success("Rate card updated");
      onClose();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to update rate card");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal open title="Cost rate card" onClose={onClose} busy={saving}>
      {!rc ? (
        <LoadingFill />
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)", minWidth: 360 }}>
          <p className="text-sm muted">
            Prices used to estimate spend. Compute is billed per hour; storage per GiB per month.
          </p>
          <Num label={`Currency`} value={rc.currency} onChange={(v) => setField("currency", v)} text />
          <Num label="VM vCPU / hour" value={rc.vcpuHour} onChange={(v) => setField("vcpuHour", v)} />
          <Num label="VM GiB RAM / hour" value={rc.gbRamHour} onChange={(v) => setField("gbRamHour", v)} />
          <Num label="Storage GiB / month" value={rc.gbStorageMonth} onChange={(v) => setField("gbStorageMonth", v)} />
          <Num label="Container vCPU / hour" value={rc.containerVcpuHour} onChange={(v) => setField("containerVcpuHour", v)} />
          <Num label="Container GiB RAM / hour" value={rc.containerGbRamHour} onChange={(v) => setField("containerGbRamHour", v)} />
          <div className="row" style={{ justifyContent: "flex-end", gap: "var(--sp-2)", marginTop: "var(--sp-2)" }}>
            <button className="btn" onClick={onClose} disabled={saving}>Cancel</button>
            <button className="btn primary" onClick={save} disabled={saving}>
              {saving ? "Saving…" : "Save rate card"}
            </button>
          </div>
          <span className="text-xs muted">Display currency: {currency}</span>
        </div>
      )}
    </Modal>
  );
}

function Num({
  label,
  value,
  onChange,
  text,
}: {
  label: string;
  value: string | number;
  onChange: (v: string) => void;
  text?: boolean;
}) {
  return (
    <label style={{ display: "flex", flexDirection: "column", gap: "var(--sp-1)" }}>
      <span className="text-sm">{label}</span>
      <input
        className="input"
        type={text ? "text" : "number"}
        step={text ? undefined : "0.0001"}
        min={text ? undefined : "0"}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </label>
  );
}
