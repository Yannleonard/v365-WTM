// ui/src/components/AppShell.tsx
//
// The authenticated layout: sidebar + topbar + routed <Outlet>. Also hosts the
// fleet-wide events WS subscription that invalidates React Query caches so lists
// stay reactive within ~1s (ADR-001 events channel).

import { useEffect, useState } from "react";
import { Outlet, useLocation } from "react-router-dom";
import { useQueryClient } from "@tanstack/react-query";
import { Sidebar } from "./Sidebar";
import { TopBar } from "./TopBar";
import { RecentTasksBar } from "./RecentTasksBar";
import { InventoryTree } from "./InventoryTree";
import { IconChevronDown } from "./icons";
import { subscribeEvents } from "../lib/ws";
import { useSelectedHost } from "../lib/hostStore";

// The VM/Compute domain: routes where the object Inventory Tree is shown as a
// second pane next to the main content (vSphere's left inventory → right detail).
const VM_DOMAIN = [/^\/vms(\/|$)/, /^\/vm-clusters(\/|$)/, /^\/vm\/connections(\/|$)/];
const TREE_COLLAPSE_KEY = "castor.invtree.hidden";

export function AppShell() {
  const queryClient = useQueryClient();
  const hostId = useSelectedHost();
  const location = useLocation();
  const showTree = VM_DOMAIN.some((re) => re.test(location.pathname));
  const [treeHidden, setTreeHidden] = useState<boolean>(
    () => localStorage.getItem(TREE_COLLAPSE_KEY) === "1",
  );
  const toggleTree = () =>
    setTreeHidden((v) => {
      const next = !v;
      localStorage.setItem(TREE_COLLAPSE_KEY, next ? "1" : "0");
      return next;
    });

  // Fleet-wide events subscription → invalidate workload/resource caches.
  useEffect(() => {
    const sub = subscribeEvents(hostId, {
      onData: (payload) => {
        // Keep the Recent Tasks bar reactive to in-flight actions.
        queryClient.invalidateQueries({ queryKey: ["audit", "recent"] });
        // Targeted invalidation by event kind keeps refetches cheap.
        switch (payload.kind) {
          case "container":
            queryClient.invalidateQueries({ queryKey: ["workloads", hostId] });
            queryClient.invalidateQueries({ queryKey: ["host", hostId] });
            break;
          case "network":
            queryClient.invalidateQueries({ queryKey: ["networks", hostId] });
            break;
          case "volume":
            queryClient.invalidateQueries({ queryKey: ["volumes", hostId] });
            break;
          default:
            break;
        }
      },
    });
    return () => sub.close();
  }, [hostId, queryClient]);

  return (
    <div className="app-shell">
      <Sidebar />
      <div className="app-main">
        <TopBar />
        <div className={`app-workspace${showTree && !treeHidden ? " with-tree" : ""}`}>
          {showTree ? (
            treeHidden ? (
              <button
                className="invtree-reopen"
                onClick={toggleTree}
                title="Show inventory"
                aria-label="Show inventory"
              >
                <IconChevronDown size={16} style={{ transform: "rotate(-90deg)" }} />
              </button>
            ) : (
              <div className="app-tree">
                <InventoryTree />
                <button
                  className="invtree-collapse"
                  onClick={toggleTree}
                  title="Hide inventory"
                  aria-label="Hide inventory"
                >
                  <IconChevronDown size={16} style={{ transform: "rotate(90deg)" }} />
                </button>
              </div>
            )
          ) : null}
          <main className="app-content">
            <div className="content-inner">
              <Outlet />
            </div>
          </main>
        </div>
        <RecentTasksBar />
      </div>
    </div>
  );
}
