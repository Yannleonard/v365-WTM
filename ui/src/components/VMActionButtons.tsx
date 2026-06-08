// ui/src/components/VMActionButtons.tsx
//
// Row/header lifecycle buttons for one VM, each greyed-out-before-click via
// CapabilityGate (hypervisor capability + RBAC permission). Power affordances are
// state-aware: Start when stopped, Stop/Suspend/Reset when running, Resume when
// suspended. Snapshot / clone / delete follow when the provider supports them.

import type { VM, VMCapability } from "../lib/types";
import { gateVMAction } from "../lib/rbac";
import { CapabilityGate } from "./CapabilityGate";
import { ActionButton } from "./ActionButton";
import {
  IconPlay,
  IconStop,
  IconRestart,
  IconPause,
  IconSnapshot,
  IconClone,
  IconTrash,
} from "./icons";

interface Props {
  vm: VM;
  caps: VMCapability[] | undefined;
  permissions: string[] | undefined;
  busy: boolean;
  size?: "sm" | "md";
  onPower: (vm: VM, op: "start" | "stop" | "reset" | "suspend" | "resume") => void;
  onSnapshot: (vm: VM) => void;
  onClone: (vm: VM) => void;
  onDelete: (vm: VM) => void;
}

export function VMActionButtons({
  vm,
  caps,
  permissions,
  busy,
  size = "sm",
  onPower,
  onSnapshot,
  onClone,
  onDelete,
}: Props) {
  const running = vm.state === "running";
  const suspended = vm.state === "suspended" || vm.state === "paused";
  const stopped = vm.state === "stopped" || vm.state === "unknown";

  return (
    <div className="dt-actions" onClick={(e) => e.stopPropagation()}>
      {(stopped || suspended) ? (
        <CapabilityGate gate={gateVMAction(suspended ? "resume" : "start", caps, permissions)}>
          {(allowed, reason) => (
            <ActionButton
              size={size}
              iconOnly
              variant="ghost"
              disabled={!allowed}
              loading={busy}
              tooltip={allowed ? (suspended ? "Resume" : "Start") : reason}
              aria-label={suspended ? "Resume" : "Start"}
              onClick={() => onPower(vm, suspended ? "resume" : "start")}
              style={allowed ? { color: "var(--success)" } : undefined}
            >
              <IconPlay size={15} />
            </ActionButton>
          )}
        </CapabilityGate>
      ) : null}

      {running ? (
        <>
          <CapabilityGate gate={gateVMAction("suspend", caps, permissions)}>
            {(allowed, reason) => (
              <ActionButton
                size={size}
                iconOnly
                variant="ghost"
                disabled={!allowed}
                loading={busy}
                tooltip={allowed ? "Suspend" : reason}
                aria-label="Suspend"
                onClick={() => onPower(vm, "suspend")}
              >
                <IconPause size={15} />
              </ActionButton>
            )}
          </CapabilityGate>
          <CapabilityGate gate={gateVMAction("reset", caps, permissions)}>
            {(allowed, reason) => (
              <ActionButton
                size={size}
                iconOnly
                variant="ghost"
                disabled={!allowed}
                loading={busy}
                tooltip={allowed ? "Reset" : reason}
                aria-label="Reset"
                onClick={() => onPower(vm, "reset")}
              >
                <IconRestart size={15} />
              </ActionButton>
            )}
          </CapabilityGate>
          <CapabilityGate gate={gateVMAction("stop", caps, permissions)}>
            {(allowed, reason) => (
              <ActionButton
                size={size}
                iconOnly
                variant="ghost"
                disabled={!allowed}
                loading={busy}
                tooltip={allowed ? "Stop" : reason}
                aria-label="Stop"
                onClick={() => onPower(vm, "stop")}
                style={allowed ? { color: "var(--warning)" } : undefined}
              >
                <IconStop size={15} />
              </ActionButton>
            )}
          </CapabilityGate>
        </>
      ) : null}

      <CapabilityGate gate={gateVMAction("snapshot", caps, permissions)}>
        {(allowed, reason) => (
          <ActionButton
            size={size}
            iconOnly
            variant="ghost"
            disabled={!allowed}
            tooltip={allowed ? "Snapshot" : reason}
            aria-label="Snapshot"
            onClick={() => onSnapshot(vm)}
          >
            <IconSnapshot size={15} />
          </ActionButton>
        )}
      </CapabilityGate>

      <CapabilityGate gate={gateVMAction("clone", caps, permissions)}>
        {(allowed, reason) => (
          <ActionButton
            size={size}
            iconOnly
            variant="ghost"
            disabled={!allowed}
            tooltip={allowed ? "Clone" : reason}
            aria-label="Clone"
            onClick={() => onClone(vm)}
          >
            <IconClone size={15} />
          </ActionButton>
        )}
      </CapabilityGate>

      <CapabilityGate gate={gateVMAction("delete_vm", caps, permissions)}>
        {(allowed, reason) => {
          const protectedBlock = vm.protected && !(permissions ?? []).includes("*");
          const finalReason = protectedBlock
            ? "Protected — only an administrator can override deletion"
            : reason;
          return (
            <ActionButton
              size={size}
              iconOnly
              variant="ghost"
              disabled={!allowed || protectedBlock}
              tooltip={allowed && !protectedBlock ? "Delete" : finalReason}
              aria-label="Delete"
              onClick={() => onDelete(vm)}
              style={allowed && !protectedBlock ? { color: "var(--danger)" } : undefined}
            >
              <IconTrash size={15} />
            </ActionButton>
          );
        }}
      </CapabilityGate>
    </div>
  );
}
