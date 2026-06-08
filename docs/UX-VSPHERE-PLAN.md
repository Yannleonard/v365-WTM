# UniHV → vSphere-parity plan (UX + functional), Castor identity preserved

> Goal (owner): reach **vSphere Client clarity + functionality** while keeping **Castor's graphic
> identity**. Fix the "guess the icon" problem (cryptic icon-only action row), add a clear menu,
> make the app easy to learn, and make everything actually work.
>
> This is the **validated build plan**. Full detail lives in:
> - `docs/plan/01-ux-navigation-icons-actions.md` — navigation tree, icon refonte, action bar + Actions menu, tabs, Recent Tasks/Alarms
> - `docs/plan/02-vsphere-parity-matrix.md` — ~90-capability vSphere parity matrix + 3-wave roadmap
> - `docs/VM-FEATURE-GAPS.md` — prior gap analysis (refined by the matrix)

---

## A. The UX transformation (what makes it feel like vSphere)

vSphere's clarity comes from 4 things, all reproducible in Castor's light theme/brand:

1. **Inventory tree (left):** object-centric hierarchy `Provider → Cluster → Host → VM` (+ a sibling
   `Containers` root for docker/swarm/k8s). Type icons + state dots + alarm/protected badges; selecting a
   node drives the right detail pane. (Today's grouped sidebar stays as the top-level "Menu".)
2. **Named actions, never bare icons:** the cryptic 13-icon row → **Action bar of icon+TEXT buttons**
   (Console, Power ▾, Edit Settings, Snapshot) **+ an `Actions ▾` dropdown** grouped into submenus
   (Power ▸, Snapshots ▸, Storage ▸, Networking ▸, …). Every item gated by the existing RBAC engine.
3. **Stable horizontal tabs** per object: **Summary | Monitor | Configure | Permissions | Snapshots | Console**.
   Summary = vignette console + 3 resource gauges (CPU/Mem/Storage, Used/Free/Capacity) + Hardware/General/
   Related cards (the vSphere Summary).
4. **Recent Tasks / Alarms bar** (bottom, omnipresent) fed by the existing audit log + task tracker + WS events.

**Icon refonte:** keep the in-house SVG set; enforce "one concept = one glyph everywhere"; add 3 missing
glyphs (`IconPower` = shut down, `IconConsole` = graphical console/monitor, `IconEject` = eject ISO);
de-ambiguate Reset vs Revert (Revert lives only in the Snapshots tab, labeled). Color = intent
(teal=on, amber=stop, red=destructive, Castor-blue=primary). All actions labeled → nothing to guess.

---

## B. vSphere functional parity — already DONE (so we don't rebuild it)

Full lifecycle + power, create-from-scratch, snapshot/clone (full+linked), intra-hv migrate, V2V export+convert,
**hot-add disk/NIC/ISO (live)**, **VNC console in-browser (guacd)**, network/volume/ISO write, **vTPM 2.0**,
**Secure Boot**, **cloud-init**, cross-hv **replication + failover**, SAN/NAS + Azure/S3 storage backends,
FinOps, Insights, RBAC + per-role perms, TOTP/2FA, OIDC/LDAP SSO, audit.

## C. vSphere parity — to BUILD, in 3 waves

**Wave 1 — "behaves like vSphere for a real VM" (highest impact, ~2–3 sprints):**
1. **Guest agent (qemu-ga)** — keystone: IP display, clean shutdown ack, app-consistent quiesce.
2. **Snapshot manager**: tree + delete-single + consolidate (today only create/revert).
3. **Online disk resize** (`DomainBlockResize`).
4. **CPU topology + CPU model** (`<cpu>` sockets/cores/threads; perf + live-migrate/EVC compat).
5. **VM templates + clone-from-template + Windows sysprep** (pairs with cloud-init → real provisioning).
6. **Maintenance mode + host evacuation** (enter-maintenance + batch live-migrate).
7. **API tokens + bulk operations** (unlocks Terraform/Ansible; constant admin need).

**Wave 2 — "manages a fleet like vSphere" (~4–6 sprints):** resource reservation/limit/shares + resource
pools; scheduled backups w/ retention to the S3/Azure/SAN backends; alarms/thresholds + notifications;
disk QoS/TRIM/thin-thick; live storage migration; Terraform provider + per-object permissions/folders/tags;
replication failback + events timeline/perf roll-up.

**Wave 3 — enterprise moats (deep, sequence by demand):** GPU/PCI passthrough; HA auto-restart + DRS +
affinity + EVC; distributed switch + distributed firewall + SR-IOV/teaming; CBT/incremental backup + restore;
VM encryption at rest; NUMA; multipath; serial console; USB passthrough; per-tenant quotas.

---

## D. Sequenced delivery (what I execute after you validate)

Each lot is a shippable, tested increment (Go build linux+windows + tests, UI build+tests, live KVM proof,
browser click-test). Order chosen so the app *feels* like vSphere fastest:

- **Lot 1 — UX shell & clarity (no new backend):** Action bar (icon+text) + `Actions ▾` menu; icon refonte
  (+3 glyphs); rich Summary tab (console vignette + CPU/Mem/Storage gauges + Hardware/General/Related cards);
  Recent Tasks/Alarms bottom bar. *This directly fixes the owner's complaint.*
- **Lot 2 — Inventory tree + tabs:** left inventory tree (Provider→Cluster→Host→VM + Containers root);
  formalize Summary/Monitor/Configure/Permissions/Snapshots/Console tabs.
- **Lot 3 — Wave-1 functional parity #1:** guest agent + snapshot manager (tree/delete/consolidate) +
  online disk resize.
- **Lot 4 — Wave-1 #2:** CPU topology/model + VM templates + sysprep + maintenance mode/evacuation +
  API tokens/bulk ops.
- **Lot 5+ — Wave 2**, then Wave 3 by demand.

Conventions held throughout: every new provider op adds a `CapabilityMatrix` bit (UI greys it out
pre-flight, never "click-then-405"); KVM live first (only real backend), then govmomi/WMI/XAPI; reuse
already-built assets (replication scheduler → scheduled snapshots/backups; storage backends → backup
targets; guacd bridge → serial console; migrate export → backup/restore).
