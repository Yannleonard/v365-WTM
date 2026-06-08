// ui/src/components/RecentTasksBar.tsx
//
// Lot 1 — a slim, collapsible bottom bar (vSphere "Recent Tasks / Alarms"), mounted
// in AppShell. Two tabs:
//   • Recent Tasks — recent api.audit() rows (action humanized, target, actor, time,
//     status), refreshed live by the existing events WS (parent invalidates the
//     ["audit","recent"] query on each event).
//   • Alarms — degraded/disconnected hosts+providers and recent error/denied audit
//     rows (the same single source as the TopBar "Degraded" pill).
//
// Reuses existing chrome only: StatusDot, the icon set, surfaces/tokens. No new deps.

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useHosts } from "../lib/hooks";
import { StatusDot } from "./StatusDot";
import { Spinner } from "./Spinner";
import { humanizeAction, timeAgo } from "../lib/format";
import {
  IconCheck,
  IconAlert,
  IconRefresh,
  IconChevronDown,
} from "./icons";
import type { AuditEntry } from "../lib/types";

const COLLAPSE_KEY = "castor.tasksbar.collapsed";

type TaskFilter = "all" | "running" | "failed";

function statusGlyph(result: string) {
  if (result === "success") return <span style={{ color: "var(--success)" }}><IconCheck size={14} /></span>;
  if (result === "error" || result === "denied") return <span style={{ color: "var(--danger)" }}><IconAlert size={14} /></span>;
  return <span style={{ color: "var(--text-muted)" }}><IconRefresh size={14} /></span>;
}

export function RecentTasksBar() {
  const navigate = useNavigate();
  const [collapsed, setCollapsed] = useState<boolean>(() => localStorage.getItem(COLLAPSE_KEY) === "1");
  const [tab, setTab] = useState<"tasks" | "alarms">("tasks");
  const [filter, setFilter] = useState<TaskFilter>("all");

  // Recent tasks from the audit log. The events WS in AppShell invalidates this
  // key, so the bar reflects in-flight actions within ~1s. Cheap, no new backend.
  const auditQ = useQuery({
    queryKey: ["audit", "recent"],
    queryFn: () => api.audit({ limit: 25 }),
    refetchInterval: 15_000,
  });
  const { data: hosts } = useHosts();

  const rows: AuditEntry[] = (auditQ.data?.items ?? []).filter((r) => r.targetType !== "auth");
  const tasks = rows.filter((r) => {
    if (filter === "failed") return r.result === "error" || r.result === "denied";
    if (filter === "running") return false; // audit rows are completed; in-flight surfaced via WS refresh
    return true;
  });

  // Alarms: degraded/disconnected hosts + recent error/denied audit rows.
  const hostAlarms = (hosts ?? []).filter((h) => h.degraded || h.status !== "connected");
  const auditAlarms = rows.filter((r) => r.result === "error" || r.result === "denied").slice(0, 12);
  const criticalCount = hostAlarms.filter((h) => h.status !== "connected").length;
  const alarmTotal = hostAlarms.length + auditAlarms.length;

  const toggle = () => {
    setCollapsed((v) => {
      const next = !v;
      localStorage.setItem(COLLAPSE_KEY, next ? "1" : "0");
      return next;
    });
  };

  const jumpToTask = (r: AuditEntry) => {
    if (r.targetType === "vm" && r.targetId.includes(":")) {
      const [pid, id] = r.targetId.split(":");
      navigate(`/vms/${encodeURIComponent(pid!)}/${encodeURIComponent(id!)}`);
    }
  };

  return (
    <div className={`tasksbar${collapsed ? " collapsed" : ""}`}>
      <div className="tasksbar-head">
        <button className={`tasksbar-tab${tab === "tasks" ? " active" : ""}`} onClick={() => { setTab("tasks"); setCollapsed(false); }}>
          {tab === "tasks" && auditQ.isFetching ? <Spinner /> : <IconRefresh size={13} />}
          Recent Tasks
          {tasks.length ? <span className="tasksbar-count">{tasks.length}</span> : null}
        </button>
        <button className={`tasksbar-tab${tab === "alarms" ? " active" : ""}`} onClick={() => { setTab("alarms"); setCollapsed(false); }}>
          <IconAlert size={13} style={{ color: alarmTotal ? "var(--warning)" : undefined }} />
          Alarms
          {alarmTotal ? (
            <span className={`tasksbar-count ${criticalCount ? "danger" : "warn"}`}>{alarmTotal}</span>
          ) : null}
        </button>
        <span className="tasksbar-spacer" />
        <button className="tasksbar-toggle" onClick={toggle} aria-label={collapsed ? "Expand tasks" : "Collapse tasks"} title={collapsed ? "Expand" : "Collapse"}>
          <IconChevronDown size={16} style={{ transform: collapsed ? "rotate(0deg)" : "rotate(180deg)" }} />
        </button>
      </div>

      {!collapsed ? (
        tab === "tasks" ? (
          <div className="tasksbar-body">
            <div className="tasksbar-chips">
              {(["all", "running", "failed"] as TaskFilter[]).map((f) => (
                <button key={f} className={`tasksbar-chip${filter === f ? " active" : ""}`} onClick={() => setFilter(f)}>
                  {f === "all" ? "All" : f === "running" ? "Running" : "Failed"}
                </button>
              ))}
            </div>
            {auditQ.isLoading ? (
              <div className="tasksbar-empty"><Spinner /> Loading tasks…</div>
            ) : tasks.length === 0 ? (
              <div className="tasksbar-empty">No recent tasks.</div>
            ) : (
              tasks.map((r) => (
                <button key={r.id} className="task-row" onClick={() => jumpToTask(r)} title="Open object">
                  {statusGlyph(r.result)}
                  <span className="task-name">{humanizeAction(r.action)}</span>
                  <span className="task-target truncate">{r.targetName || r.targetId || r.targetType}</span>
                  <span className="task-actor">{r.actorName}</span>
                  <span className="task-time">{timeAgo(r.tsEpoch || r.ts)}</span>
                </button>
              ))
            )}
          </div>
        ) : (
          <div className="tasksbar-body">
            {alarmTotal === 0 ? (
              <div className="tasksbar-empty">No active alarms.</div>
            ) : (
              <>
                {hostAlarms.map((h) => {
                  const critical = h.status !== "connected";
                  return (
                    <div key={`host-${h.id}`} className="task-row">
                      <span style={{ color: critical ? "var(--danger)" : "var(--warning)" }}><IconAlert size={14} /></span>
                      <span className="task-name">{h.name}</span>
                      <span className="task-target">
                        {critical ? `Host ${h.status}` : "Provider degraded"}
                      </span>
                      <span className="task-actor"><StatusDot hostStatus={h.status} /></span>
                      <span className="task-time">{critical ? "critical" : "warning"}</span>
                    </div>
                  );
                })}
                {auditAlarms.map((r) => (
                  <div key={`audit-${r.id}`} className="task-row">
                    <span style={{ color: "var(--danger)" }}><IconAlert size={14} /></span>
                    <span className="task-name">{humanizeAction(r.action)}</span>
                    <span className="task-target truncate">
                      {(r.targetName || r.targetId || r.targetType)} — {r.result}
                    </span>
                    <span className="task-actor">{r.actorName}</span>
                    <span className="task-time">{timeAgo(r.tsEpoch || r.ts)}</span>
                  </div>
                ))}
              </>
            )}
          </div>
        )
      ) : null}
    </div>
  );
}
