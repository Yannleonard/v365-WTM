# 01 — UX Architecture: Navigation, Icon System, Actions, Tabs, Tasks/Alarms

**Goal:** bring UniHV to **vSphere Client (H5) clarity** while keeping the **Castor graphic identity**
(logo `/brand/castor-logo.jpg`, palette in `ui/src/styles/tokens.css`: navy `#0A2540`, Docker-blue
`#2496ED`, teal `#13A688`, beaver `#8B5E3C`, light blue-grey surfaces).

This is a **buildable spec**, not a redesign of the brand. We replace the cryptic icon-only action row
with **named icon+text buttons + an `Actions ▾` menu**, add an **inventory tree**, formalize **tabs**,
and add a **bottom Recent Tasks / Alarms bar**.

---

## 0. Current state (what we are fixing)

Studied read-only:

- `ui/src/components/Sidebar.tsx` — flat, grouped **flyout-less** left nav (Overview / Compute / Virtual
  Machines / Storage / Orchestrators / Admin). It is a *menu of pages*, not an *inventory of objects*.
- `ui/src/components/VMActionButtons.tsx` + `VirtualMachineDetail.tsx` header — the offending UI: a row of
  **icon-only** `ActionButton iconOnly` (Play, Pause, Reset, Stop, Snapshot, Clone, Delete, Reconfigure=pencil,
  Migrate, Add-disk, Add-NIC, Mount-ISO=disc, Eject=X, Refresh). Tooltips exist but the user must **hover to
  learn** every glyph. ~13 bare icons in one row = the "guess the icon" problem.
- `ui/src/components/icons.tsx` — a solid hand-rolled SVG set already exists (IconPlay, IconStop, IconPause,
  IconRestart, IconSnapshot, IconClone, IconMigrate, IconDisc/IconCdrom, IconNic, IconDisk, IconCpu, IconMemory,
  IconEdit, IconTerminal, IconVM, IconHosts, IconTrash, IconRefresh…). **We mostly reuse it**; gaps listed in §2.
- `ui/src/lib/rbac.ts` — the gating engine that decides which actions are enabled: `gateVMAction`,
  `gateVMConsole`, `gateVMHotPlug`, `gateVMNetworkWrite`, `gateVMStorageWrite`. Every action below maps to one
  of these `GateResult { allowed, reason }`. **The Actions menu reuses these verbatim** — disabled items keep
  the `reason` as their tooltip/submenu hint.
- `ui/src/views/useVMActions.tsx` — already holds every handler (power, snapshot, clone, reconfigure="Edit
  settings" rich drawer, migrate, add/detach disk+NIC, mount/eject ISO, delete) and renders right-side
  **Drawers**. The new Action bar/menu is **pure presentation over this existing hook** — no new handlers needed.
- `ui/src/lib/types.ts` `AuditEntry` + `ui/src/lib/api.ts` `audit()` + WS `subscribeEvents` + host `degraded[]`
  — the data sources for the new bottom bar (§5).

**Design principle for "vSphere clarity in Castor identity":** vSphere's clarity comes from (a) a persistent
**object inventory** on the left, (b) **named** actions (bar + Actions menu), (c) **stable horizontal tabs**,
and (d) an **omnipresent Tasks/Alarms** footer. None of that requires vSphere's chrome — we render all of it in
Castor's light theme, Castor blue/teal accents, Inter type, and the existing card/tab/drawer components.

---

## 1. NAVIGATION MODEL — Inventory Tree

### 1.1 Concept

Keep the current grouped sidebar as the **"Menu" / module switcher** (top-level navigation: Dashboard,
Admin, Orchestrators…), exactly like vSphere keeps its hamburger Menu. **Add a second, object-centric
hierarchy: the Inventory Tree**, shown when the user is inside the Compute/VM domain. Selecting a node in the
tree drives the **right detail pane** (same pattern as vSphere's left inventory → right object view).

UniHV is multi-hypervisor **and** multi-orchestrator, so the tree has **two roots** (a "Hosts and Clusters"
style view and an "Inventory" switcher), mirroring vSphere's inventory view pills.

### 1.2 Object model → tree mapping

UniHV real objects (from `types.ts` / inventory API) map to tree nodes as follows:

| Tree node            | UniHV object (source)                                   | Type icon            | Children                          |
|----------------------|---------------------------------------------------------|----------------------|-----------------------------------|
| **Inventory root**   | the deployment                                          | `BeaverMascot` (sm)  | Hypervisor providers + Orchestrators |
| Hypervisor provider  | `ProviderInfo` (vSphere/Proxmox/libvirt/Hyper-V) via `/vm/providers` | `IconShield` (provider) | Clusters, Hosts |
| Cluster              | `vm.clusterId` (grouping)                               | `IconNetworks`       | Hosts, VMs                         |
| Host (hypervisor)    | `vm.hostId` / `HostSummaryEntry`                        | `IconHosts`          | VMs, Datastores                    |
| **Virtual Machine**  | `VM` / `VMDetail`                                       | `IconVM`             | (leaf; snapshots shown in tab)     |
| Datastore / Storage  | VM storage pool (`useVMStorage`)                        | `IconDisk`           | (leaf)                             |
| VM Network           | VM network (`useVMNetworks`)                            | `IconNetworks`       | (leaf)                             |
| **Containers root**  | Orchestrators (docker/swarm/k8s)                        | `IconWorkloads`      | Engines/Hosts                      |
| Engine / docker host | `HostSummaryEntry` (orchestrator)                       | `IconHosts`          | Workloads, Stacks                  |
| Workload / Service   | `Workload`                                              | `IconWorkloads`      | (leaf)                             |

> Rationale: VMs nest under **Host** (and Host under **Cluster** under **Provider**) — the genuine vSphere
> Datacenter→Cluster→Host→VM shape, adapted: **Provider plays the "Datacenter/vCenter" role** because UniHV
> federates several hypervisors. Containers get a **sibling root** so the two worlds don't get tangled.

### 1.3 Badges, expand/collapse, selection

- **Type icon** (left of label) per the table above, 16px, `currentColor` (inherits row color).
- **State dot** (right of label): reuse `StatusDot` / `VMStateBadge` colors —
  `--state-running` (teal), `--state-stopped` (grey), `--state-paused` (amber).
- **Alert badge** (far right): small count pill, color by worst severity —
  `--danger` for alarms, `--warning` for warnings; sourced from host `degraded[]` and the Alarms feed (§5).
  Reuse `IconAlert` glyph at 12px inside the pill.
- **Protected badge**: `ProtectedTag` / `IconLock` (beaver `--state-protected`) on protected VMs.
- **Expand/collapse**: `IconChevronDown` (rotates -90° collapsed). Lazy-load children on first expand
  (each level already has a hook: `useInventory`, `useVMStorage`, `useVMNetworks`).
- **Selection**: clicking a node sets the active object and routes to its detail view
  (`/vms/:pid/:id` for a VM, a host view for a host, etc.). Selected row: `--accent-soft` fill + left
  3px `--accent` rail (matches existing `.nav-item.active`). Keyboard: ↑/↓ move, →/← expand/collapse,
  Enter select (a11y parity with vSphere tree).
- **Search-in-tree**: a small filter input atop the tree filters node labels (reuse the VM-list search logic).

### 1.4 ASCII wireframe — full shell with inventory tree

```
┌──────────────────────────────────────────────────────────────────────────────────────────────┐
│ [Castor logo]   ☰ Menu    🔍 Global search ………………………………………            [Live●] [host▾] [YL ▾] │  ← TopBar (Castor light)
├───────────────────────────┬──────────────────────────────────────────────────────────────────┤
│ INVENTORY                 │  Object header:                                                     │
│ [🔍 filter]               │   🖥  web-prod-01   [Running]   #a1b2c3 · vsphere · host-04   🔒    │
│ ▾ ⛨ vSphere (vcenter-1)   │  ┌──────────────────────────────────────── Action bar ───────────┐ │
│   ▾ ⬡ cluster-prod        │  │ ▶ Console   ⏻ Shut Down ▾   ✎ Edit Settings   📷 Snapshot   ⟳ │ │
│     ▾ 🖳 host-04          │  │                                            Actions ▾           │ │
│       • 🖥 web-prod-01 ●  │  └────────────────────────────────────────────────────────────────┘ │
│         🖥 web-prod-02 ●  │  ┌── Summary │ Monitor │ Configure │ Permissions │ Snapshots │ ▣ Console ─┐
│         🖥 db-01    ⏸ ⚠1 │  │                                                                  │ │
│       🖳 host-05          │  │  ┌── VM hardware ───────────┐  ┌── Resources ────────────────┐  │ │
│   ▸ ⬡ cluster-dr         │  │  │ Guest OS  Ubuntu 22.04   │  │ CPU   ▓▓▓▓▓░░░  62% 5/8 GHz │  │ │
│ ▸ ⛨ Proxmox (pve-1)      │  │  │ vCPU      8              │  │ Mem   ▓▓▓▓▓▓░░  74% 12/16GB │  │ │
│ ──────────────────────    │  │  │ Memory    16 GB         │  │ Store ▓▓▓░░░░░  41% 82/200GB│  │ │
│ ▾ ☐ Containers           │  │  └──────────────────────────┘  └─────────────────────────────┘  │ │
│   ▾ 🖳 docker-local       │  │  ┌── Disks ─────┐ ┌── Network ─────┐ ┌── IPs / Labels ──────┐   │ │
│     □ nginx     ●         │  │  └──────────────┘ └────────────────┘ └──────────────────────┘   │ │
│     □ redis     ●         │  └──────────────────────────────────────────────────────────────────┘
│   ▸ 🖳 k8s-prod           │                                                                       │
├───────────────────────────┴──────────────────────────────────────────────────────────────────┤
│ ▾ Recent Tasks (3 running)                         │ ⚠ Alarms (1 critical · 2 warning)          │  ← bottom bar
│  ✓ Power On  web-prod-02  yann  2s ago             │  ✕ host-05 disconnected    crit  1m ago    │
│  ↻ Clone     db-01→db-02  yann  running…           │  ⚠ db-01 datastore 91% full warn  4m ago   │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```

Legend: `●` running (teal) · `⏸` paused (amber) · `⚠n` alarm count · `🔒` protected · `▾/▸` expand/collapse.
(Emoji here are placeholders for the SVG icons named in §2.)

---

## 2. ICON SYSTEM REFONTE

### 2.1 Rules (sizing / color / state)

- **Library:** keep the in-house `icons.tsx` SVG set (24×24 viewBox, `currentColor`, stroke 1.75). No new dep.
- **Sizing:** tree type-icons & menu items **16px**; action-bar buttons **16px** glyph + text; inline
  table-row actions **14px**; tab icons **15px** (matches current). Object header type-icon **20px**.
- **Color = state/intent, never decoration:**
  - Default/neutral action → `currentColor` (inherits `--text-secondary`).
  - Positive/power-on → `--success` (teal). Caution/stop/shutdown → `--warning` (amber).
    Destructive (delete/detach) → `--danger` (red). Brand-primary CTA → `--accent` (Castor blue).
  - Disabled (gate denied) → 40% opacity + `not-allowed` cursor + `reason` tooltip (already done by `ActionButton`/`CapabilityGate`).
- **States:** hover = `--bg-surface-3`; active/pressed = `--accent-press`; loading = inline spinner replaces glyph
  (existing `ActionButton loading`).
- **Consistency mandate:** **one concept = one glyph everywhere** (tree, action bar, menu, drawer header, table).
  e.g. Snapshot is always `IconSnapshot` (camera); Migrate is always `IconMigrate`.

### 2.2 Action & object icon map (every VM/host/storage/network action)

| Action / Object | Icon concept | Existing glyph | Status | Color |
|---|---|---|---|---|
| **Power On / Start** | play / power-symbol | `IconPlay` | reuse | teal |
| **Shut Down (guest)** | power-symbol (⏻) | **NEW `IconPower`** | **add** (see §2.3) | amber |
| **Power Off (force stop)** | filled square | `IconStop` | reuse | amber/danger |
| **Suspend / Pause** | two bars | `IconPause` | reuse | neutral |
| **Resume** | play | `IconPlay` | reuse | teal |
| **Reset / Restart** | circular arrow | `IconRestart` | reuse | neutral |
| **Edit Settings (reconfigure)** | pencil (ideal: pencil-in-gear) | `IconEdit` | reuse; *optional* `IconEditSettings` combo | neutral |
| **Snapshot (take)** | camera | `IconSnapshot` | reuse | neutral |
| **Manage Snapshots / Revert** | revert arrow / layers | `IconRestart` (revert), `IconSnapshot` (manage) | reuse | neutral |
| **Clone** | two overlapping squares | `IconClone` | reuse | neutral |
| **Migrate (host/storage)** | arrows-between-two-planes | `IconMigrate` | reuse | neutral |
| **Migrate V2V (cross-hv)** | arrows-between-planes (+badge) | `IconMigrate` | reuse | neutral |
| **Console (graphical)** | monitor screen | `IconVM` is taken → **use `IconTerminal`** for serial, **NEW `IconConsole` (monitor)** for graphical | **add `IconConsole`** | accent |
| **Add disk** | disk platter | `IconDisk` | reuse | neutral |
| **Add NIC / network adapter** | NIC card | `IconNic` | reuse | neutral |
| **Mount ISO** | CD/DVD disc | `IconDisc`/`IconCdrom` | reuse | neutral |
| **Eject ISO** | eject (▲ over bar) | currently `IconClose` (✕) — **ambiguous** | **add `IconEject`** | neutral |
| **Detach disk/NIC** | trash (in device row) | `IconTrash` | reuse | danger |
| **Delete VM** | trash | `IconTrash` | reuse | danger |
| **Refresh** | circular arrow (open) | `IconRefresh` | reuse | neutral |
| **Create VM** | plus | `IconPlus` | reuse | accent |
| **Object: VM** | monitor w/ cursor | `IconVM` | reuse | — |
| **Object: Host** | server racks | `IconHosts` | reuse | — |
| **Object: Cluster** | nodes graph | `IconNetworks` | reuse | — |
| **Object: Provider/Hypervisor** | shield | `IconShield` | reuse | — |
| **Object: Datastore/Storage** | disk / cylinder | `IconDisk` / `IconVolumes` | reuse | — |
| **Object: Network** | topology | `IconNetworks` | reuse | — |
| **CPU** | chip | `IconCpu` | reuse | — |
| **Memory** | RAM module | `IconMemory` | reuse | — |
| **Alarm/Alert** | triangle-! | `IconAlert` | reuse | danger/warn |
| **Protected** | lock | `IconLock` | reuse | beaver |

### 2.3 Ambiguous icons flagged → replacements

| Today | Why ambiguous | Replacement |
|---|---|---|
| **Reset = `IconRestart`** AND **Snapshot-revert = `IconRestart`** | same circular-arrow glyph used for two different actions | Keep `IconRestart` for **Reset (power)**; revert lives **only** inside the Snapshots tab labeled "Revert", so context disambiguates. With the new design **every button is also labeled**, removing the clash. |
| **Eject ISO = `IconClose` (✕)** | ✕ reads as "close/cancel", not "eject" | **Add `IconEject`** (triangle over a line). |
| **Graphical console** had no distinct icon (tab used `IconTerminal`) | terminal ≠ graphical console | **Add `IconConsole`** (monitor screen) for graphical; keep `IconTerminal` for serial/exec. |
| **Add-disk vs Detach-disk** both near `IconDisk`/`IconTrash` icon-only | direction unclear | Resolved by **icon+text** ("Add disk" / "Detach") + colour (detach = danger). |
| **The entire bare row** | 13 unlabeled glyphs, hover-to-learn | Replaced by **labeled action bar + Actions ▾ menu** (§3). |

New glyphs to add to `icons.tsx` (small, follow `base()` convention):

```tsx
// Power symbol: circle gap + vertical bar (Shut Down).
export const IconPower = (p) => (<svg {...base(p)}><path d="M12 3v9"/><path d="M7.5 6.7a7 7 0 1 0 9 0"/></svg>);
// Graphical console: monitor screen with stand.
export const IconConsole = (p) => (<svg {...base(p)}><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>);
// Eject: triangle over a bar.
export const IconEject = (p) => (<svg {...base(p)}><path d="M5 14h14L12 6z" fill="currentColor" stroke="none"/><rect x="5" y="16" width="14" height="2" rx="1" fill="currentColor" stroke="none"/></svg>);
```

---

## 3. ACTION BAR + ACTIONS MENU SPEC

**Pattern (owner's choice):** a top **Action bar** of primary buttons (**icon + text**) for the few most-used,
state-relevant actions, **plus** an `Actions ▾` dropdown holding everything else, grouped into submenus
(`Power ▸`, `Snapshots ▸`, `Storage ▸`, `Networking ▸`, …) like vSphere.

**Gating:** every button/menu item is wrapped in the existing `CapabilityGate` over the matching
`gateVM*` result. Disabled → greyed with the gate `reason` as tooltip (button) or muted hint (menu item).
**Nothing is hidden** when merely lacking permission/capability (so the user learns *what exists*); items only
disappear when they're **state-irrelevant** (e.g. no "Power On" while running).

### 3.1 VM — RUNNING

Primary bar (left→right): **`▶ Console`** · **`⏻ Shut Down`** (split-button ▾: Shut Down Guest / Power Off /
Restart Guest / Reset / Suspend) · **`✎ Edit Settings`** · **`📷 Snapshot`** · `⟳ Refresh` (icon-only OK, universally understood).

`Actions ▾` menu:
```
Power ▸           Shut Down Guest   (gateVMAction stop)
                  Power Off         (stop, force)
                  Restart Guest     (reset)
                  Reset             (reset)
                  Suspend           (suspend)
Snapshots ▸       Take Snapshot…    (snapshot)
                  Manage Snapshots  (→ Snapshots tab)
Storage ▸         Add Disk…         (gateVMHotPlug)
                  Mount ISO…        (gateVMHotPlug)
                  Eject ISO         (gateVMHotPlug)
Networking ▸      Add Network Adapter…  (gateVMHotPlug)
─────────────
Clone…            (clone)
Migrate…          (migrate)
Migrate V2V…      (v2v.read → Migration wizard)
─────────────
Delete            (delete_vm)   ← red, blocked + reason if vm.protected
```

### 3.2 VM — STOPPED

Primary bar: **`▶ Power On`** (teal) · **`✎ Edit Settings`** · **`📷 Snapshot`** · `⟳ Refresh`.
(No Console for most providers when off; show it disabled with reason if gate denies.)

`Actions ▾`: same groups, but **Power ▸** collapses to **Power On**; **Storage/Networking** hot-plug items are
**disabled with reason** "VM must be running for live device changes" (the `gateVMHotPlug` reason), while
cold edits route through **Edit Settings**. Clone / Migrate / Delete unchanged.

### 3.3 VM — SUSPENDED / PAUSED

Primary bar: **`▶ Resume`** · `⏻ Power Off ▾` · `✎ Edit Settings` · `⟳`. Snapshot in Actions menu.

### 3.4 Host

Primary bar: **`⛨ Enter/Exit Maintenance`** · **`⟳ Refresh`** · **`▣ Console/SSH`** (if available).
`Actions ▾`: `New VM…`, `Reconnect`, `Disconnect`, `Datastores ▸`, `Networking ▸`, `View Tasks`.
(Today UniHV hosts are mostly read-only; gate everything via host capabilities, show disabled+reason.)

### 3.5 Datastore / Storage

Primary bar: **`＋ Upload ISO`** (gateVMStorageWrite) · **`＋ New Volume`** · `⟳`.
`Actions ▾`: `Browse Files`, `Delete Volume` (danger), `Rescan`.

### 3.6 Network

Primary bar: **`＋ New Network`** (gateVMNetworkWrite) · `⟳`.
`Actions ▾`: `Edit`, `Delete` (danger).

### 3.7 ASCII mockup — VM detail header (running)

```
 🖥  web-prod-01    ● Running     #a1b2c3 · vsphere · host-04 · cluster-prod    🔒 Protected
┌───────────────────────────────────────────────────────────────────────────────────────────┐
│  [▶ Console]  [⏻ Shut Down ▾]  [✎ Edit Settings]  [📷 Snapshot]              [⟳]  [Actions ▾]│
└───────────────────────────────────────────────────────────────────────────────────────────┘
                                                                              click → ┌────────────────────┐
                                                                                      │ Power            ▸ │
                                                                                      │ Snapshots        ▸ │
                                                                                      │ Storage          ▸ │
                                                                                      │ Networking       ▸ │
                                                                                      │ ─────────────────  │
                                                                                      │ Clone…             │
                                                                                      │ Migrate…           │
                                                                                      │ Migrate V2V…       │
                                                                                      │ ─────────────────  │
                                                                                      │ 🗑 Delete          │  (red)
                                                                                      └────────────────────┘
```

**Build note:** the dropdown reuses the existing `.menu-pop` / `.menu-item` / `.menu-divider` styles from
`TopBar.tsx` (host & user menus) — add nested `.menu-item.has-sub` + a `.submenu` flyout. The split-button is
`ActionButton` + a small `▾` `ActionButton iconOnly`. All handlers already exist in `useVMActions.tsx`; this is
wiring, not new logic.

---

## 4. TAB LAYOUT PER OBJECT

Standardize on vSphere's horizontal tab strip (reuse existing `.tabs`/`.tab`). Current VM tabs are
Overview / Snapshots / Metrics / Console / Inspect — we **rename & regroup** to the canonical set and fold
the rest in.

### 4.1 Virtual Machine

| Tab | Contents | Source today |
|---|---|---|
| **Summary** | Type icon + state, **Resource gauges (CPU / Memory / Storage** Used/Free/Capacity bars), guest OS, vCPU, memory, IPs, host/cluster/provider, created, snapshot count, labels, quick disks/NIC summary. | `VMOverview` `dl` + new gauges |
| **Monitor** | CPU/Memory time-series (`StatsChart`), events for this VM (filtered audit), per-VM tasks. | `MetricsPanel` + audit filter |
| **Configure** | The hardware/"Edit settings" view read-form: vCPU, memory, disks table, NIC table, boot & firmware, options. Edit opens the existing Reconfigure drawer. | `VMOverview` disks/NIC tables + `ReconfigureDrawer` |
| **Permissions** | Effective roles/principals on this object (RBAC). | new (rbac API) |
| **Snapshots** | Snapshot tree + Take/Revert/Delete. | `SnapshotsPanel` |
| **Console** | Graphical console (only when `gateVMConsole.allowed`). | `ConsolePanel` |
| *(Inspect)* | Raw hypervisor JSON — keep as a **secondary/"More"** tab for power users. | `InspectTab` |

> Mapping: today's **Overview → split into Summary + Configure**; **Metrics → Monitor**; Snapshots/Console
> unchanged; add **Permissions**; demote **Inspect**.

### 4.2 Host

`Summary` (status, capacity gauges CPU/RAM/storage, providers/capability chips, VM count) · `Monitor`
(host metrics + events) · `Configure` (networking, datastores, services) · `Permissions` · `VMs` (child list).

### 4.3 Datastore / Storage

`Summary` (capacity gauge Used/Free, type, backing) · `Monitor` · `Files/Volumes` (browser + Upload ISO) ·
`Permissions`.

### 4.4 Network

`Summary` (type, subnet, connected VMs count) · `Ports/VMs` (attached adapters) · `Configure` · `Permissions`.

**Resource gauges (shared component, used in every Summary):** a labeled bar `▓▓▓▓░░░ 62%` with
`Used / Free / Capacity` underneath. Colors: fill `--accent` (CPU), `--success` (memory ok) shifting to
`--warning` ≥75% and `--danger` ≥90%. Reuse `Sparkline`/`StatCard` patterns; right-aligned in a 2-col Summary grid.

---

## 5. RECENT TASKS / ALARMS BAR

A persistent footer in `AppShell.tsx`, collapsible (default expanded ~120px; collapses to a 28px summary strip).
Two panes split 60/40: **Recent Tasks** (left) | **Alarms** (right). Always visible regardless of route, like vSphere.

### 5.1 Recent Tasks (left)

- **Data source:** the **audit log** (`api.audit()` → `AuditEntry[]`, already in `types.ts`) for completed
  actions, **+** the live **events WS** (`subscribeEvents`, already wired in `AppShell.tsx`) for in-flight ones.
  No new backend needed.
- **Per row:** status glyph (`✓` success=teal / `✕` error=danger `IconAlert` / `↻` running=spinner `IconRefresh`),
  **task name** = humanized `action` (e.g. `vm.power.start` → "Power On"), **target** = `targetName` (with
  source→dest for clone/migrate), **actor** = `actorName`, **time** = `timeAgo(ts)`. Click → jump to the object
  and open its Monitor tab.
- **Filter chips:** All / Running / Failed. Mirrors vSphere's Recent Tasks pane.
- Mapping table (humanize `action`): `vm.power.*`→Power On/Off/Reset/Suspend; `vm.snapshot.create`→Take Snapshot;
  `vm.clone`→Clone; `vm.migrate`→Migrate; `vm.reconfigure`→Reconfigure; `vm.delete`→Delete; `vm.disk.attach`→Add Disk; etc.

### 5.2 Alarms (right)

- **Data source:** host/provider **`degraded[]`** + `host.status !== "connected"` (already surfaced as the
  TopBar "Degraded" pill) + audit entries with `result: "error"|"denied"` + threshold derivations from resource
  gauges (datastore ≥90%, etc.). Aggregated client-side initially; promote to a backend `/alarms` endpoint later.
- **Per row:** severity icon (`IconAlert` red=critical / amber=warning), **object** (with link), **message**,
  **time**. Header pill: `⚠ Alarms (n critical · m warning)` — same count drives the **tree alert badges** (§1.3)
  and the existing TopBar degraded pill (single source of truth).
- **Acknowledge** action per alarm (local dismiss until next occurrence) — optional, phase 2.

### 5.3 ASCII mockup

```
┌─ ▾ Recent Tasks ───[ All | Running | Failed ]─────────┬─ ⚠ Alarms (1 crit · 2 warn) ──────────────┐
│ ✓ Power On       web-prod-02     yann     2s ago      │ ✕ host-05 disconnected         crit  1m ago│
│ ↻ Clone          db-01 → db-02   yann     running…    │ ⚠ db-01 datastore 91% full     warn  4m ago│
│ ✓ Take Snapshot  web-prod-01     yann     30s ago     │ ⚠ proxmox pve-1 cert expiring  warn  1h ago│
└───────────────────────────────────────────────────────┴────────────────────────────────────────────┘
```

**Build note:** new component `RecentTasksBar` mounted in `AppShell` after `<main>`; consumes
`useAudit()` (wrap `api.audit`) + the existing WS subscription already present in `AppShell`. Styling reuses
card/table/menu tokens; collapse state persists in `localStorage` (like `hostStore`).

---

## 6. Build order (small, shippable PRs)

1. **icons.tsx** — add `IconPower`, `IconConsole`, `IconEject` (§2.3). *(trivial, no behavior change)*
2. **ActionsMenu + split-button** over existing `useVMActions` handlers; replace the bare row in
   `VMActionButtons.tsx` / `VirtualMachineDetail` header with the **labeled bar + Actions ▾** (§3). *(biggest UX win)*
3. **Tabs rename/regroup** in `VirtualMachineDetail` → Summary/Monitor/Configure/Permissions/Snapshots/Console (§4)
   + the shared **Resource gauge** component.
4. **Inventory tree** component + a two-pane layout for the VM domain (§1).
5. **RecentTasksBar** in `AppShell` (§5).

Each step keeps Castor's logo, palette, and existing components — clarity comes from **labels + structure**,
not from changing the brand.
