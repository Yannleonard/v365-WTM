// ui/src/views/Insights.tsx
//
// UniHV's cross-domain insights feed — actionable drift / health / best-practice
// findings spanning VMs (every hypervisor) AND containers (every orchestrator),
// each with a severity and a concrete next step. Rendered as severity-grouped
// cards with a filterable severity/category bar. No competitor offers one feed
// over both domains.

import { useMemo, useState } from "react";
import { useInsights } from "../lib/hooks";
import { PageHeader } from "../components/PageHeader";
import { StatCard } from "../components/StatCard";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { timeAgo } from "../lib/format";
import { IconAlert, IconCheck, IconShield, IconRefresh } from "../components/icons";
import type { Insight, InsightSeverity, InsightCategory } from "../lib/types";

const SEV_COLOR: Record<InsightSeverity, string> = {
  critical: "var(--danger)",
  warn: "var(--warning)",
  info: "var(--accent)",
};

const SEV_LABEL: Record<InsightSeverity, string> = {
  critical: "Critical",
  warn: "Warning",
  info: "Info",
};

const CAT_LABEL: Record<InsightCategory, string> = {
  resilience: "Resilience",
  reclaim: "Reclaim",
  housekeeping: "Housekeeping",
  health: "Health",
};

export function Insights() {
  const q = useInsights();
  const [sevFilter, setSevFilter] = useState<InsightSeverity | "all">("all");
  const [catFilter, setCatFilter] = useState<InsightCategory | "all">("all");

  const feed = q.data;
  const counts = feed?.counts ?? { critical: 0, warn: 0, info: 0 };

  const filtered = useMemo(() => {
    const items = feed?.insights ?? [];
    return items.filter(
      (i) =>
        (sevFilter === "all" || i.severity === sevFilter) &&
        (catFilter === "all" || i.category === catFilter),
    );
  }, [feed, sevFilter, catFilter]);

  if (q.isLoading && !feed) {
    return <LoadingFill label="Scanning your fleet…" />;
  }

  const total = (feed?.insights ?? []).length;

  return (
    <div className="page">
      <PageHeader
        title="Insights"
        subtitle="Drift, health and best-practice findings across VMs and containers"
        actions={
          <button className="btn" onClick={() => q.refetch()} disabled={q.isFetching}>
            <IconRefresh size={16} /> {q.isFetching ? "Scanning…" : "Rescan"}
          </button>
        }
      />

      {q.isError ? <div className="banner danger">Failed to load insights.</div> : null}

      {/* ---- severity KPIs (also act as filters) ---- */}
      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(180px, 1fr))",
          gap: "var(--sp-4)",
        }}
      >
        <StatCard
          label="Critical"
          icon={<IconAlert size={18} />}
          accent="var(--danger)"
          value={counts.critical}
          sub="act now"
          onClick={() => setSevFilter(sevFilter === "critical" ? "all" : "critical")}
        />
        <StatCard
          label="Warnings"
          icon={<IconShield size={18} />}
          accent="var(--warning)"
          value={counts.warn}
          sub="should review"
          onClick={() => setSevFilter(sevFilter === "warn" ? "all" : "warn")}
        />
        <StatCard
          label="Info"
          icon={<IconCheck size={18} />}
          accent="var(--accent)"
          value={counts.info}
          sub="opportunities"
          onClick={() => setSevFilter(sevFilter === "info" ? "all" : "info")}
        />
        <StatCard
          label="Total findings"
          icon={<IconShield size={18} />}
          value={total}
          sub={feed ? `scanned ${timeAgo(feed.generatedAt)}` : ""}
        />
      </div>

      {/* ---- filter bar ---- */}
      <div className="row" style={{ gap: "var(--sp-2)", marginTop: "var(--sp-4)", flexWrap: "wrap" }}>
        <FilterPill active={sevFilter === "all"} onClick={() => setSevFilter("all")} label="All severities" />
        {(["critical", "warn", "info"] as InsightSeverity[]).map((s) => (
          <FilterPill key={s} active={sevFilter === s} onClick={() => setSevFilter(s)} label={SEV_LABEL[s]} color={SEV_COLOR[s]} />
        ))}
        <span style={{ width: 1, height: 20, background: "var(--border)", margin: "0 var(--sp-1)" }} />
        <FilterPill active={catFilter === "all"} onClick={() => setCatFilter("all")} label="All categories" />
        {(Object.keys(CAT_LABEL) as InsightCategory[]).map((c) => (
          <FilterPill key={c} active={catFilter === c} onClick={() => setCatFilter(c)} label={CAT_LABEL[c]} />
        ))}
      </div>

      {/* ---- feed ---- */}
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)", marginTop: "var(--sp-4)" }}>
        {filtered.length === 0 ? (
          <EmptyState
            icon={<IconCheck size={36} />}
            title={total === 0 ? "All clear" : "No findings match the filter"}
            message={total === 0 ? "No drift or best-practice issues detected across your fleet." : undefined}
          />
        ) : (
          filtered.map((i) => <InsightCard key={i.id} insight={i} />)
        )}
      </div>
    </div>
  );
}

function FilterPill({
  active,
  onClick,
  label,
  color,
}: {
  active: boolean;
  onClick: () => void;
  label: string;
  color?: string;
}) {
  return (
    <button
      className={active ? "btn primary btn-sm" : "btn btn-sm"}
      onClick={onClick}
      style={color && active ? { background: color, borderColor: color } : undefined}
    >
      {label}
    </button>
  );
}

function InsightCard({ insight }: { insight: Insight }) {
  const color = SEV_COLOR[insight.severity];
  return (
    <div
      className="card card-pad"
      style={{
        display: "flex",
        gap: "var(--sp-3)",
        borderLeft: `4px solid ${color}`,
      }}
    >
      <span style={{ color, marginTop: 2 }}>
        <IconAlert size={20} />
      </span>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div className="row" style={{ justifyContent: "space-between", gap: "var(--sp-2)" }}>
          <span style={{ fontWeight: 700 }}>{insight.title}</span>
          <span className="row" style={{ gap: "var(--sp-1)" }}>
            <span className="badge" style={{ background: color, color: "#fff" }}>
              {SEV_LABEL[insight.severity]}
            </span>
            <span className="badge">{CAT_LABEL[insight.category] ?? insight.category}</span>
          </span>
        </div>
        <div className="text-sm" style={{ marginTop: "var(--sp-1)" }}>{insight.detail}</div>
        <div
          className="text-sm"
          style={{
            marginTop: "var(--sp-2)",
            padding: "var(--sp-2)",
            background: "var(--accent-soft)",
            borderRadius: 6,
          }}
        >
          <strong>Suggested action:</strong> {insight.suggestion}
        </div>
        <div className="row text-xs muted" style={{ gap: "var(--sp-2)", marginTop: "var(--sp-2)", flexWrap: "wrap" }}>
          {insight.entityName ? <span>Entity: <span className="mono">{insight.entityName}</span></span> : null}
          {insight.entityType ? <span>· {insight.entityType}</span> : null}
          {insight.kind ? <span>· {insight.kind}</span> : null}
          {insight.providerId ? <span>· {insight.providerId}</span> : null}
          <span>· rule <span className="mono">{insight.rule}</span></span>
        </div>
      </div>
    </div>
  );
}
