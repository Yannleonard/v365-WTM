// ui/src/components/InventoryTree.tsx
//
// Lot 2A — the left Inventory Tree (vSphere's defining navigation), rendered in
// the Castor look. A hierarchical, collapsible object inventory with two roots:
//
//   • Hypervisors  →  provider → cluster → host → VM   (the genuine vSphere
//     Datacenter→Cluster→Host→VM shape; the *provider* plays the vCenter role
//     because UniHV federates several hypervisors).
//   • Containers   →  orchestrator host → workload.
//
// Built entirely from existing data sources — useInventory() (the unified
// VMs+hosts+clusters+workloads snapshot) and useVMProviders() (the hypervisor
// provider list). No new endpoints.
//
// Styling reuses the Sidebar nav vocabulary verbatim: each row is a .nav-item
// (hover/active/left-rail), the icon sits in .nav-icon, the label in .nav-label.
// Indentation, density, tokens and the type-icon set all match the module nav, so
// the tree reads as part of the same app. Selecting a VM routes to its detail
// view (/vms/:pid/:id) and the open object is highlighted as the active node.

import { useMemo, useState, type ReactNode } from "react";
import { useNavigate, useLocation } from "react-router-dom";
import { useInventory, useVMProviders } from "../lib/hooks";
import { StatusDot } from "./StatusDot";
import { Spinner } from "./Spinner";
import {
  IconShield,
  IconNetworks,
  IconHosts,
  IconVM,
  IconWorkloads,
  IconChevronDown,
  IconSearch,
  IconLock,
  IconAlert,
} from "./icons";
import type {
  VM,
  VMState,
  VMHost,
  Workload,
  WorkloadState,
} from "../lib/types";

const EXPAND_KEY = "castor.inventorytree.expanded";

// VMState/WorkloadState → the StatusDot palette (--state-* tokens). suspended has
// no container analogue; reuse the pending color (same mapping as VMStateBadge).
const VM_DOT: Record<VMState, string> = {
  running: "var(--state-running)",
  stopped: "var(--state-stopped)",
  suspended: "var(--state-pending)",
  paused: "var(--state-paused)",
  unknown: "var(--state-unknown)",
};
const WK_DOT: Record<WorkloadState, string> = {
  running: "var(--state-running)",
  stopped: "var(--state-stopped)",
  paused: "var(--state-paused)",
  restarting: "var(--state-restarting)",
  pending: "var(--state-pending)",
  unknown: "var(--state-unknown)",
};

// One tree node, already flattened into render order with a depth for indentation.
interface TreeNode {
  key: string;
  label: string;
  title?: string;
  icon: ReactNode;
  depth: number;
  /** dot color (state) on the right, when relevant */
  dot?: string;
  pulse?: boolean;
  protected?: boolean;
  /** alarm count pill (degraded / disconnected) */
  alarm?: number;
  /** has children → render a chevron */
  expandable: boolean;
  expanded?: boolean;
  /** route to navigate to on click (VM nodes + containers); undefined → just toggles */
  to?: string;
  /** stable id used for the active-row highlight test */
  routeKey?: string;
  onToggle?: () => void;
}

function cap(s: string): string {
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : s;
}

export function InventoryTree() {
  const navigate = useNavigate();
  const location = useLocation();
  const inventoryQ = useInventory();
  const providersQ = useVMProviders();

  const [filter, setFilter] = useState("");
  // Expanded state persists across navigations (localStorage), so the tree keeps
  // its shape as the user clicks through objects — same persistence pattern as the
  // tasks bar / host store.
  const [expanded, setExpanded] = useState<Record<string, boolean>>(() => {
    try {
      const raw = localStorage.getItem(EXPAND_KEY);
      return raw ? (JSON.parse(raw) as Record<string, boolean>) : { "root:hv": true, "root:ct": true };
    } catch {
      return { "root:hv": true, "root:ct": true };
    }
  });

  const toggle = (key: string, defOpen: boolean) => {
    setExpanded((cur) => {
      const isOpen = cur[key] ?? defOpen;
      const next = { ...cur, [key]: !isOpen };
      try {
        localStorage.setItem(EXPAND_KEY, JSON.stringify(next));
      } catch {
        /* ignore quota */
      }
      return next;
    });
  };
  const isOpen = (key: string, defOpen: boolean) => expanded[key] ?? defOpen;

  const inv = inventoryQ.data;

  // The route key of the object currently open in the right pane (for the active
  // highlight). VM detail is /vms/:pid/:id.
  const activeRouteKey = useMemo(() => {
    const m = location.pathname.match(/^\/vms\/([^/]+)\/([^/]+)/);
    if (m) return `vm:${decodeURIComponent(m[1]!)}:${decodeURIComponent(m[2]!)}`;
    return "";
  }, [location.pathname]);

  // Build the flattened, depth-tagged node list once per inventory/expand change.
  const nodes = useMemo<TreeNode[]>(() => {
    if (!inv) return [];
    const f = filter.trim().toLowerCase();
    const match = (s: string | undefined) => !f || (s ?? "").toLowerCase().includes(f);

    const vms = inv.vms ?? [];
    const hosts = inv.hosts ?? [];
    const clusters = inv.clusters ?? [];
    const workloads = inv.workloads ?? [];

    // Provider list = the hypervisor providers (capability list) unioned with any
    // providerId that appears on a VM/host/cluster (so a provider with objects but
    // no entry in /vm/providers still shows).
    const providerIds = new Set<string>();
    for (const p of providersQ.data ?? []) providerIds.add(p.id);
    for (const v of vms) if (v.providerId) providerIds.add(v.providerId);
    for (const h of hosts) if (h.providerId) providerIds.add(h.providerId);
    for (const c of clusters) if (c.providerId) providerIds.add(c.providerId);
    const providerKind = new Map<string, string>();
    for (const p of providersQ.data ?? []) providerKind.set(p.id, p.kind);
    for (const v of vms) if (!providerKind.has(v.providerId)) providerKind.set(v.providerId, v.kind);

    // Degraded providers (alarm badge source) — best-effort from inventory.degraded.
    const degraded = new Set<string>();
    for (const d of inv.degraded ?? []) degraded.add(d.id);

    const out: TreeNode[] = [];

    /* ---------------- Hypervisors root ---------------- */
    const hvKey = "root:hv";
    const hvOpen = isOpen(hvKey, true);
    out.push({
      key: hvKey,
      label: "Hypervisors",
      icon: <IconShield size={16} />,
      depth: 0,
      expandable: providerIds.size > 0,
      expanded: hvOpen,
      onToggle: () => toggle(hvKey, true),
    });

    if (hvOpen) {
      for (const pid of Array.from(providerIds).sort()) {
        const pVms = vms.filter((v) => v.providerId === pid);
        const pHosts = hosts.filter((h) => h.providerId === pid);
        const pClusters = clusters.filter((c) => c.providerId === pid);
        const pKey = `prov:${pid}`;
        const pOpen = isOpen(pKey, false);
        const kind = providerKind.get(pid);

        // A provider passes the filter if it (or any descendant) matches.
        const providerMatches =
          match(pid) ||
          match(kind) ||
          pVms.some((v) => match(v.name)) ||
          pHosts.some((h) => match(h.name)) ||
          pClusters.some((c) => match(c.name));
        if (!providerMatches) continue;

        out.push({
          key: pKey,
          label: pid,
          title: kind ? `${pid} · ${cap(kind)}` : pid,
          icon: <IconShield size={16} />,
          depth: 1,
          expandable: pVms.length + pHosts.length + pClusters.length > 0,
          expanded: pOpen,
          alarm: degraded.has(pid) ? 1 : undefined,
          onToggle: () => toggle(pKey, false),
        });
        if (!pOpen) continue;

        // Hosts already grouped under a cluster are not repeated at provider level.
        const clusteredHostIds = new Set<string>();
        for (const c of pClusters) for (const hid of c.hostIds ?? []) clusteredHostIds.add(hid);

        const renderVM = (v: VM, depth: number) => {
          if (!match(v.name) && !match(v.id)) return;
          out.push({
            key: `vm:${pid}:${v.id}`,
            label: v.name,
            title: `${v.name} · ${v.state}`,
            icon: <IconVM size={16} />,
            depth,
            dot: VM_DOT[v.state] ?? VM_DOT.unknown,
            pulse: v.state === "running",
            protected: v.protected,
            expandable: false,
            to: `/vms/${encodeURIComponent(v.providerId)}/${encodeURIComponent(v.id)}`,
            routeKey: `vm:${v.providerId}:${v.id}`,
          });
        };

        const renderHost = (h: VMHost, depth: number) => {
          const hVms = pVms.filter((v) => v.hostId === h.id);
          if (!match(h.name) && !hVms.some((v) => match(v.name))) return;
          const hKey = `host:${pid}:${h.id}`;
          const hOpen = isOpen(hKey, false);
          out.push({
            key: hKey,
            label: h.name || h.id,
            title: h.name || h.id,
            icon: <IconHosts size={16} />,
            depth,
            dot: h.state ? "var(--state-running)" : undefined,
            expandable: hVms.length > 0,
            expanded: hOpen,
            onToggle: () => toggle(hKey, false),
          });
          if (hOpen) for (const v of hVms) renderVM(v, depth + 1);
        };

        // Clusters (with their member hosts), then standalone hosts, then VMs that
        // have no host placement at all (kept visible directly under the provider).
        for (const c of pClusters) {
          const cKey = `cluster:${pid}:${c.id}`;
          const cOpen = isOpen(cKey, false);
          const cHosts = pHosts.filter((h) => (c.hostIds ?? []).includes(h.id) || h.clusterId === c.id);
          const cVms = pVms.filter((v) => v.clusterId === c.id);
          if (!match(c.name) && !cHosts.some((h) => match(h.name)) && !cVms.some((v) => match(v.name))) continue;
          out.push({
            key: cKey,
            label: c.name || c.id,
            title: c.name || c.id,
            icon: <IconNetworks size={16} />,
            depth: 2,
            expandable: cHosts.length + cVms.length > 0,
            expanded: cOpen,
            onToggle: () => toggle(cKey, false),
          });
          if (cOpen) {
            for (const h of cHosts) renderHost(h, 3);
            // VMs attached to the cluster but not to a specific host.
            for (const v of cVms) if (!v.hostId) renderVM(v, 3);
          }
        }

        const standaloneHosts = pHosts.filter((h) => !clusteredHostIds.has(h.id) && !h.clusterId);
        for (const h of standaloneHosts) renderHost(h, 2);

        // VMs with neither a cluster nor a host placement → directly under provider.
        const placedHostIds = new Set(pHosts.map((h) => h.id));
        for (const v of pVms) {
          if (v.clusterId && pClusters.some((c) => c.id === v.clusterId)) continue;
          if (v.hostId && placedHostIds.has(v.hostId)) continue;
          renderVM(v, 2);
        }
      }
    }

    /* ---------------- Containers root ---------------- */
    // Group orchestrator workloads by their engine/host (node, else providerId).
    const byHost = new Map<string, Workload[]>();
    for (const w of workloads) {
      const hk = w.node || w.providerId || "local";
      const arr = byHost.get(hk) ?? [];
      arr.push(w);
      byHost.set(hk, arr);
    }

    const ctKey = "root:ct";
    const ctOpen = isOpen(ctKey, true);
    out.push({
      key: ctKey,
      label: "Containers",
      icon: <IconWorkloads size={16} />,
      depth: 0,
      expandable: byHost.size > 0,
      expanded: ctOpen,
      onToggle: () => toggle(ctKey, true),
    });

    if (ctOpen) {
      for (const hk of Array.from(byHost.keys()).sort()) {
        const list = byHost.get(hk)!;
        if (!match(hk) && !list.some((w) => match(w.name))) continue;
        const ehKey = `engine:${hk}`;
        const ehOpen = isOpen(ehKey, false);
        out.push({
          key: ehKey,
          label: hk,
          title: hk,
          icon: <IconHosts size={16} />,
          depth: 1,
          expandable: list.length > 0,
          expanded: ehOpen,
          onToggle: () => toggle(ehKey, false),
        });
        if (!ehOpen) continue;
        for (const w of list) {
          if (!match(w.name) && !match(w.id)) continue;
          out.push({
            key: `wk:${hk}:${w.id}`,
            label: w.name,
            title: `${w.name} · ${w.state}`,
            icon: <IconWorkloads size={16} />,
            depth: 2,
            dot: WK_DOT[w.state] ?? WK_DOT.unknown,
            pulse: w.state === "running",
            protected: w.protected,
            expandable: false,
            to: `/workloads/${encodeURIComponent(w.providerId)}/${encodeURIComponent(w.id)}`,
          });
        }
      }
    }

    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inv, providersQ.data, expanded, filter]);

  return (
    <aside className="invtree" aria-label="Inventory">
      <div className="invtree-head">
        <span className="invtree-title">Inventory</span>
        {inventoryQ.isFetching ? <Spinner /> : null}
      </div>

      <div className="invtree-search">
        <span className="muted" aria-hidden>
          <IconSearch size={15} />
        </span>
        <input
          className="input"
          placeholder="Filter inventory…"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          aria-label="Filter inventory"
        />
      </div>

      <div className="invtree-body">
        {inventoryQ.isLoading ? (
          <div className="invtree-empty">
            <Spinner /> Loading inventory…
          </div>
        ) : nodes.length === 0 ? (
          <div className="invtree-empty">No objects match.</div>
        ) : (
          nodes.map((n) => {
            const active = !!n.routeKey && n.routeKey === activeRouteKey;
            const onClick = () => {
              if (n.to) navigate(n.to);
              else if (n.onToggle) n.onToggle();
            };
            return (
              <div
                key={n.key}
                role="treeitem"
                aria-expanded={n.expandable ? n.expanded : undefined}
                aria-selected={active}
                className={`nav-item invtree-item${active ? " active" : ""}`}
                style={{ paddingLeft: `calc(var(--sp-2) + ${n.depth * 14}px)` }}
                title={n.title ?? n.label}
                onClick={onClick}
              >
                <span
                  className={`invtree-chev${n.expandable ? "" : " ghost"}`}
                  onClick={(e) => {
                    if (!n.expandable) return;
                    e.stopPropagation();
                    n.onToggle?.();
                  }}
                  aria-hidden
                >
                  {n.expandable ? (
                    <IconChevronDown
                      size={14}
                      style={{ transform: n.expanded ? "rotate(0deg)" : "rotate(-90deg)" }}
                    />
                  ) : null}
                </span>
                <span className="nav-icon">{n.icon}</span>
                <span className="nav-label truncate">{n.label}</span>
                {n.protected ? (
                  <span className="invtree-badge protected" title="Protected" aria-label="Protected">
                    <IconLock size={12} />
                  </span>
                ) : null}
                {n.alarm ? (
                  <span className="invtree-badge alarm" title="Degraded">
                    <IconAlert size={11} />
                    {n.alarm}
                  </span>
                ) : null}
                {n.dot ? <StatusDot color={n.dot} pulse={n.pulse} /> : null}
              </div>
            );
          })
        )}
      </div>
    </aside>
  );
}
