// ui/src/components/VMActions.tsx
//
// Lot 1 — UX clarity. Replaces the cryptic icon-only VM action row with vSphere
// clarity rendered in the CURRENT Castor look:
//   • a NAMED, state-aware action bar (icon + text) of the few most-used actions
//   • an `Actions ▾` dropdown holding the rest, grouped into submenus
//     (Power ▸ / Snapshots ▸ / Storage ▸ / Networking ▸ ) plus Clone / Migrate / Delete.
//
// This is PURE PRESENTATION over the existing useVMActions() handlers and the
// existing rbac gateVM* gates — no new handlers, no new visual language. It reuses
// ActionButton, CapabilityGate, the TopBar `.menu-pop` dropdown styling, and the
// existing icon set (+ the 3 new glyphs IconPower/IconConsole/IconEject).

import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import type { VM, VMCapability } from "../lib/types";
import {
  gateVMAction,
  gateVMConsole,
  gateVMHotPlug,
  gateVMTemplate,
  type GateResult,
} from "../lib/rbac";
import { ActionButton } from "./ActionButton";
import {
  IconPlay,
  IconPower,
  IconStop,
  IconRestart,
  IconPause,
  IconSnapshot,
  IconClone,
  IconMigrate,
  IconTrash,
  IconEdit,
  IconConsole,
  IconDisk,
  IconNic,
  IconDisc,
  IconEject,
  IconChevronDown,
  IconRefresh,
  IconStacks,
  IconDownload,
} from "./icons";

/** Everything the bar/menu drives — all already provided by useVMActions(). */
export interface VMActionHandlers {
  onPower: (vm: VM, op: "start" | "stop" | "reset" | "suspend" | "resume") => void;
  onSnapshot: (vm: VM) => void;
  onManageSnapshots?: (vm: VM) => void;
  onClone: (vm: VM) => void;
  onReconfigure: (vm: VM) => void;
  onMigrate: (vm: VM) => void;
  onAddDisk: (vm: VM) => void;
  onAddNic: (vm: VM) => void;
  onMountIso: (vm: VM) => void;
  onEjectIso: (vm: VM) => void;
  onDelete: (vm: VM) => void;
  onConsole?: (vm: VM) => void;
  onRefresh?: () => void;
  // Templates (Lot 4A). Optional — present on the VM list. onDeploy deploys a fresh
  // VM from a template (reuses Clone); onMarkTemplate toggles the template marking.
  onDeploy?: (vm: VM) => void;
  onMarkTemplate?: (vm: VM, isTemplate: boolean) => void;
  // Backup (Lot 5B). Optional — opens the "Back up now" backend picker.
  onBackup?: (vm: VM) => void;
}

interface Props extends VMActionHandlers {
  vm: VM;
  caps: VMCapability[] | undefined;
  permissions: string[] | undefined;
  busy: boolean;
  /** "bar" = full named bar + menu (detail header); "menu" = compact Actions ▾ only (table rows). */
  layout?: "bar" | "menu";
  size?: "sm" | "md";
}

/** Protected-VM delete block reason (mirrors the previous VMActionButtons logic). */
function deleteGate(vm: VM, base: GateResult, permissions: string[] | undefined): GateResult {
  const protectedBlock = vm.protected && !(permissions ?? []).includes("*");
  if (protectedBlock)
    return { allowed: false, reason: "Protected — only an administrator can override deletion" };
  return base;
}

export function VMActions(props: Props) {
  const { vm, caps, permissions, busy, layout = "bar", size = "md", onConsole, onRefresh } = props;

  const running = vm.state === "running";
  const suspended = vm.state === "suspended" || vm.state === "paused";

  const gPower = gateVMAction("stop", caps, permissions); // representative power gate
  const gStart = gateVMAction(suspended ? "resume" : "start", caps, permissions);
  const gConsole = gateVMConsole(caps, permissions);
  const gReconfigure = gateVMAction("reconfigure", caps, permissions);
  const gSnapshot = gateVMAction("snapshot", caps, permissions);
  const gDelete = deleteGate(vm, gateVMAction("delete_vm", caps, permissions), permissions);

  /* ---------- compact menu-only layout (table rows) ---------- */
  if (layout === "menu") {
    return (
      <div className="dt-actions" onClick={(e) => e.stopPropagation()}>
        <ActionsMenu {...props} compact />
      </div>
    );
  }

  /* ---------- full named bar (detail header) ---------- */
  return (
    <div className="row" style={{ gap: "var(--sp-2)" }} onClick={(e) => e.stopPropagation()}>
      {/* Console — primary CTA (accent) */}
      {onConsole ? (
        <ActionButton
          size={size}
          variant="primary"
          disabled={!gConsole.allowed}
          tooltip={gConsole.allowed ? "Open graphical console" : gConsole.reason}
          aria-label="Console"
          onClick={() => onConsole(vm)}
        >
          <IconConsole size={16} /> Console
        </ActionButton>
      ) : null}

      {/* Power: running -> Shut Down split; suspended -> Resume; stopped -> Power On */}
      {running ? (
        <SplitButton
          size={size}
          variant="default"
          disabled={!gPower.allowed}
          loading={busy}
          tooltip={gPower.allowed ? "Shut down (power off)" : gPower.reason}
          label="Shut Down"
          icon={<IconPower size={16} />}
          style={gPower.allowed ? { color: "var(--warning)" } : undefined}
          onClick={() => props.onPower(vm, "stop")}
          menu={<PowerSubmenu {...props} />}
        />
      ) : suspended ? (
        <ActionButton
          size={size}
          disabled={!gStart.allowed}
          loading={busy}
          tooltip={gStart.allowed ? "Resume" : gStart.reason}
          aria-label="Resume"
          onClick={() => props.onPower(vm, "resume")}
          style={gStart.allowed ? { color: "var(--success)" } : undefined}
        >
          <IconPlay size={16} /> Resume
        </ActionButton>
      ) : (
        <ActionButton
          size={size}
          disabled={!gStart.allowed}
          loading={busy}
          tooltip={gStart.allowed ? "Power on" : gStart.reason}
          aria-label="Power On"
          onClick={() => props.onPower(vm, "start")}
          style={gStart.allowed ? { color: "var(--success)" } : undefined}
        >
          <IconPlay size={16} /> Power On
        </ActionButton>
      )}

      {/* Edit Settings */}
      <ActionButton
        size={size}
        disabled={!gReconfigure.allowed}
        tooltip={gReconfigure.allowed ? "Edit settings (vCPU / memory / devices)" : gReconfigure.reason}
        aria-label="Edit Settings"
        onClick={() => props.onReconfigure(vm)}
      >
        <IconEdit size={16} /> Edit Settings
      </ActionButton>

      {/* Snapshot (running) / Delete (stopped) — state-relevant fourth button */}
      {running || suspended ? (
        <ActionButton
          size={size}
          disabled={!gSnapshot.allowed}
          tooltip={gSnapshot.allowed ? "Take a snapshot" : gSnapshot.reason}
          aria-label="Snapshot"
          onClick={() => props.onSnapshot(vm)}
        >
          <IconSnapshot size={16} /> Snapshot
        </ActionButton>
      ) : (
        <ActionButton
          size={size}
          variant="ghost"
          disabled={!gDelete.allowed}
          tooltip={gDelete.allowed ? "Delete this VM" : gDelete.reason}
          aria-label="Delete"
          onClick={() => props.onDelete(vm)}
          style={gDelete.allowed ? { color: "var(--danger)" } : undefined}
        >
          <IconTrash size={16} /> Delete
        </ActionButton>
      )}

      {onRefresh ? (
        <ActionButton size={size} variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={onRefresh}>
          <IconRefresh size={16} />
        </ActionButton>
      ) : null}

      {/* Everything else lives here, grouped. */}
      <ActionsMenu {...props} />
    </div>
  );
}

/* ============================ Split button ============================ */

function SplitButton({
  label,
  icon,
  menu,
  onClick,
  disabled,
  loading,
  tooltip,
  variant = "default",
  size = "md",
  style,
}: {
  label: string;
  icon: ReactNode;
  menu: ReactNode;
  onClick: () => void;
  disabled?: boolean;
  loading?: boolean;
  tooltip?: string;
  variant?: "default" | "primary" | "ghost";
  size?: "sm" | "md";
  style?: React.CSSProperties;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);
  return (
    <div className="host-switcher" ref={ref} style={{ display: "inline-block" }}>
      <div className="split-btn">
        <ActionButton
          size={size}
          variant={variant}
          disabled={disabled}
          loading={loading}
          tooltip={tooltip}
          aria-label={label}
          onClick={onClick}
          style={style}
        >
          {icon} {label}
        </ActionButton>
        <ActionButton
          size={size}
          variant={variant}
          className="split-caret"
          aria-label="More power options"
          aria-haspopup="menu"
          tooltip="More power options"
          onClick={() => setOpen((v) => !v)}
        >
          <IconChevronDown size={14} />
        </ActionButton>
      </div>
      {open ? (
        <div className="menu-pop" role="menu" onClick={() => setOpen(false)}>
          {menu}
        </div>
      ) : null}
    </div>
  );
}

/* ============================ Actions ▾ menu ============================ */

function ActionsMenu(props: Props & { compact?: boolean }) {
  const { vm, caps, permissions, compact } = props;
  const [open, setOpen] = useState(false);
  const [openSub, setOpenSub] = useState<string | null>(null);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) {
        setOpen(false);
        setOpenSub(null);
      }
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);

  const running = vm.state === "running";
  const suspended = vm.state === "suspended" || vm.state === "paused";

  const gStart = gateVMAction("start", caps, permissions);
  const gResume = gateVMAction("resume", caps, permissions);
  const gStop = gateVMAction("stop", caps, permissions);
  const gReset = gateVMAction("reset", caps, permissions);
  const gSuspend = gateVMAction("suspend", caps, permissions);
  const gSnapshot = gateVMAction("snapshot", caps, permissions);
  const gHotPlug = gateVMHotPlug(caps, permissions);
  const gClone = gateVMAction("clone", caps, permissions);
  const gMigrate = gateVMAction("migrate", caps, permissions);
  const gTemplate = gateVMTemplate(caps, permissions);
  const gDelete = deleteGate(vm, gateVMAction("delete_vm", caps, permissions), permissions);
  // Backup (Lot 5B) is gated by vm.backup (export-class; reuses the snapshot/export
  // capabilities). A read-only provider that cannot export is excluded.
  const canBackupPerm = (permissions ?? []).includes("vm.backup") || (permissions ?? []).includes("*");
  const gBackup: GateResult = canBackupPerm
    ? { allowed: true, reason: "" }
    : { allowed: false, reason: "You lack the vm.backup permission" };
  const isTemplate = vm.labels?.["unihv.template"] === "true";

  const close = () => {
    setOpen(false);
    setOpenSub(null);
  };

  const item = (
    label: string,
    icon: ReactNode,
    gate: GateResult,
    onClick: () => void,
    opts?: { danger?: boolean },
  ) => (
    <button
      className={`menu-item${gate.allowed ? "" : " disabled"}${opts?.danger ? " is-danger" : ""}`}
      role="menuitem"
      disabled={!gate.allowed}
      title={gate.allowed ? undefined : gate.reason}
      onClick={() => {
        if (!gate.allowed) return;
        close();
        onClick();
      }}
    >
      {icon}
      {label}
    </button>
  );

  const group = (key: string, label: string, children: ReactNode) => (
    <div className="menu-item-group">
      <button
        className="menu-item has-sub"
        role="menuitem"
        aria-haspopup="menu"
        aria-expanded={openSub === key}
        onMouseEnter={() => setOpenSub(key)}
        onClick={() => setOpenSub((v) => (v === key ? null : key))}
      >
        {label}
        <IconChevronDown size={13} className="menu-sub-caret" style={{ transform: "rotate(-90deg)" }} />
      </button>
      {openSub === key ? (
        <div className="submenu" role="menu" onMouseLeave={() => setOpenSub(null)}>
          {children}
        </div>
      ) : null}
    </div>
  );

  return (
    <div className="host-switcher" ref={ref} style={{ display: "inline-block" }}>
      <ActionButton
        size={compact ? "sm" : "md"}
        variant="ghost"
        aria-haspopup="menu"
        aria-expanded={open}
        tooltip="More actions"
        aria-label="Actions"
        onClick={(e) => {
          e.stopPropagation();
          setOpen((v) => !v);
        }}
      >
        Actions <IconChevronDown size={14} />
      </ActionButton>

      {open ? (
        <div className="menu-pop" role="menu">
          {group(
            "power",
            "Power",
            <>
              <div className="menu-sublabel">Power</div>
              {running || suspended
                ? null
                : item("Power On", <IconPlay size={15} style={{ color: "var(--success)" }} />, gStart, () =>
                    props.onPower(vm, "start"),
                  )}
              {suspended
                ? item("Resume", <IconPlay size={15} style={{ color: "var(--success)" }} />, gResume, () =>
                    props.onPower(vm, "resume"),
                  )
                : null}
              {item("Shut Down", <IconPower size={15} />, gStop, () => props.onPower(vm, "stop"))}
              {item("Power Off", <IconStop size={15} />, gStop, () => props.onPower(vm, "stop"))}
              {item("Reset", <IconRestart size={15} />, gReset, () => props.onPower(vm, "reset"))}
              {item("Suspend", <IconPause size={15} />, gSuspend, () => props.onPower(vm, "suspend"))}
            </>,
          )}

          {group(
            "snapshots",
            "Snapshots",
            <>
              <div className="menu-sublabel">Snapshots</div>
              {item("Take Snapshot…", <IconSnapshot size={15} />, gSnapshot, () => props.onSnapshot(vm))}
              {props.onManageSnapshots
                ? item("Manage Snapshots", <IconSnapshot size={15} />, { allowed: true, reason: "" }, () =>
                    props.onManageSnapshots!(vm),
                  )
                : null}
            </>,
          )}

          {group(
            "storage",
            "Storage",
            <>
              <div className="menu-sublabel">Storage</div>
              {item("Add Disk…", <IconDisk size={15} />, gHotPlug, () => props.onAddDisk(vm))}
              {item("Mount ISO…", <IconDisc size={15} />, gHotPlug, () => props.onMountIso(vm))}
              {item("Eject ISO", <IconEject size={15} />, gHotPlug, () => props.onEjectIso(vm))}
            </>,
          )}

          {group(
            "networking",
            "Networking",
            <>
              <div className="menu-sublabel">Networking</div>
              {item("Add Network Adapter…", <IconNic size={15} />, gHotPlug, () => props.onAddNic(vm))}
            </>,
          )}

          <div className="menu-divider" />
          {/* Templates (Lot 4A): Deploy-from-template appears for template VMs; the
              Mark/Unmark toggle is offered when the provider supports templates. */}
          {props.onDeploy && isTemplate
            ? item("Deploy from template…", <IconStacks size={15} />, gClone, () => props.onDeploy!(vm))
            : null}
          {props.onMarkTemplate
            ? item(
                isTemplate ? "Unmark template" : "Mark as template",
                <IconStacks size={15} />,
                gTemplate,
                () => props.onMarkTemplate!(vm, !isTemplate),
              )
            : null}
          {item("Clone…", <IconClone size={15} />, gClone, () => props.onClone(vm))}
          {item("Migrate…", <IconMigrate size={15} />, gMigrate, () => props.onMigrate(vm))}
          {props.onBackup
            ? item("Back up now…", <IconDownload size={15} />, gBackup, () => props.onBackup!(vm))
            : null}
          <div className="menu-divider" />
          {item("Delete", <IconTrash size={15} />, gDelete, () => props.onDelete(vm), { danger: true })}
        </div>
      ) : null}
    </div>
  );
}

/* The split-button's dropdown content reuses the same gated power items. */
function PowerSubmenu(props: Props) {
  const { vm, caps, permissions } = props;
  const suspended = vm.state === "suspended" || vm.state === "paused";
  const gStop = gateVMAction("stop", caps, permissions);
  const gReset = gateVMAction("reset", caps, permissions);
  const gSuspend = gateVMAction("suspend", caps, permissions);
  const gResume = gateVMAction("resume", caps, permissions);

  const it = (label: string, icon: ReactNode, gate: GateResult, onClick: () => void) => (
    <button
      className={`menu-item${gate.allowed ? "" : " disabled"}`}
      role="menuitem"
      disabled={!gate.allowed}
      title={gate.allowed ? undefined : gate.reason}
      onClick={() => gate.allowed && onClick()}
    >
      {icon}
      {label}
    </button>
  );

  return (
    <>
      <div className="menu-sublabel">Power</div>
      {it("Shut Down Guest", <IconPower size={15} />, gStop, () => props.onPower(vm, "stop"))}
      {it("Power Off", <IconStop size={15} />, gStop, () => props.onPower(vm, "stop"))}
      {it("Restart Guest", <IconRestart size={15} />, gReset, () => props.onPower(vm, "reset"))}
      {it("Reset", <IconRestart size={15} />, gReset, () => props.onPower(vm, "reset"))}
      {it("Suspend", <IconPause size={15} />, gSuspend, () => props.onPower(vm, "suspend"))}
      {suspended ? it("Resume", <IconPlay size={15} />, gResume, () => props.onPower(vm, "resume")) : null}
    </>
  );
}
