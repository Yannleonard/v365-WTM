// ui/src/views/useVMActions.tsx
//
// Shared VM lifecycle action handling: power (start/stop/reset/suspend/resume),
// snapshot create, clone, reconfigure, intra-hypervisor migrate, and delete.
// Power ops fire directly (with optimistic toast + cache invalidation); the
// destructive/parameterized ops open a Modal. Mirrors useWorkloadActions so VM
// views stay declarative — they call trigger* and render `actions.dialogs`.

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { toast, toastError } from "../lib/toast";
import { Modal } from "../components/Modal";
import { ActionButton } from "../components/ActionButton";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField } from "../components/Field";
import type { VM, VMPowerOp } from "../lib/types";

interface SnapForm {
  vm: VM;
  name: string;
  description: string;
  memory: boolean;
  quiesce: boolean;
}
interface CloneForm {
  vm: VM;
  name: string;
  linked: boolean;
  powerOn: boolean;
}
interface ReconfigureForm {
  vm: VM;
  vcpus: string;
  memoryMb: string;
}
interface MigrateForm {
  vm: VM;
  targetHost: string;
  live: boolean;
  targetStorage: string;
}

export function useVMActions() {
  const queryClient = useQueryClient();
  const [busyId, setBusyId] = useState<string | null>(null);

  const [snap, setSnap] = useState<SnapForm | null>(null);
  const [snapBusy, setSnapBusy] = useState(false);
  const [clone, setClone] = useState<CloneForm | null>(null);
  const [cloneBusy, setCloneBusy] = useState(false);
  const [recfg, setRecfg] = useState<ReconfigureForm | null>(null);
  const [recfgBusy, setRecfgBusy] = useState(false);
  const [migrate, setMigrate] = useState<MigrateForm | null>(null);
  const [migrateBusy, setMigrateBusy] = useState(false);
  const [del, setDel] = useState<VM | null>(null);

  const invalidate = (vm: VM) => {
    queryClient.invalidateQueries({ queryKey: ["vms", vm.providerId] });
    queryClient.invalidateQueries({ queryKey: ["vm", vm.providerId, vm.id] });
    queryClient.invalidateQueries({ queryKey: ["inventory"] });
  };

  /* ---- power (direct) ---- */
  const runPower = async (vm: VM, op: VMPowerOp) => {
    setBusyId(vm.id);
    try {
      await api.vmPower(vm.providerId, vm.id, op);
      toast.success(`${op[0]!.toUpperCase()}${op.slice(1)} requested`, vm.name);
      invalidate(vm);
    } catch (err) {
      toastError(`${op} failed`, err);
    } finally {
      setBusyId(null);
    }
  };

  /* ---- triggers (open dialogs) ---- */
  const triggerSnapshot = (vm: VM) =>
    setSnap({ vm, name: "", description: "", memory: false, quiesce: false });
  const triggerClone = (vm: VM) =>
    setClone({ vm, name: `${vm.name}-clone`, linked: false, powerOn: false });
  const triggerReconfigure = (vm: VM) =>
    setRecfg({ vm, vcpus: String(vm.vcpus), memoryMb: String(vm.memoryMb) });
  const triggerMigrate = (vm: VM) =>
    setMigrate({ vm, targetHost: "", live: true, targetStorage: "" });
  const triggerDelete = (vm: VM) => setDel(vm);

  /* ---- confirms ---- */
  const confirmSnapshot = async () => {
    if (!snap) return;
    setSnapBusy(true);
    try {
      await api.vmSnapshotCreate(snap.vm.providerId, snap.vm.id, {
        name: snap.name.trim(),
        description: snap.description.trim() || undefined,
        memory: snap.memory,
        quiesce: snap.quiesce,
      });
      toast.success("Snapshot requested", snap.name.trim());
      queryClient.invalidateQueries({ queryKey: ["vm", "snapshots", snap.vm.providerId, snap.vm.id] });
      invalidate(snap.vm);
      setSnap(null);
    } catch (err) {
      toastError("Snapshot failed", err);
    } finally {
      setSnapBusy(false);
    }
  };

  const confirmClone = async () => {
    if (!clone) return;
    setCloneBusy(true);
    try {
      await api.vmClone(clone.vm.providerId, clone.vm.id, {
        name: clone.name.trim(),
        linked: clone.linked,
        powerOn: clone.powerOn,
      });
      toast.success("Clone requested", clone.name.trim());
      invalidate(clone.vm);
      setClone(null);
    } catch (err) {
      toastError("Clone failed", err);
    } finally {
      setCloneBusy(false);
    }
  };

  const confirmReconfigure = async () => {
    if (!recfg) return;
    const vcpus = Number(recfg.vcpus);
    const memoryMb = Number(recfg.memoryMb);
    setRecfgBusy(true);
    try {
      await api.vmReconfigure(recfg.vm.providerId, recfg.vm.id, {
        vcpus: Number.isFinite(vcpus) && vcpus > 0 ? vcpus : undefined,
        memoryMb: Number.isFinite(memoryMb) && memoryMb > 0 ? memoryMb : undefined,
      });
      toast.success("Reconfigure requested", recfg.vm.name);
      invalidate(recfg.vm);
      setRecfg(null);
    } catch (err) {
      toastError("Reconfigure failed", err);
    } finally {
      setRecfgBusy(false);
    }
  };

  const confirmMigrate = async () => {
    if (!migrate) return;
    setMigrateBusy(true);
    try {
      await api.vmMigrate(migrate.vm.providerId, migrate.vm.id, {
        targetHost: migrate.targetHost.trim(),
        live: migrate.live,
        targetStorage: migrate.targetStorage.trim() || undefined,
      });
      toast.success("Migration requested", migrate.vm.name);
      invalidate(migrate.vm);
      setMigrate(null);
    } catch (err) {
      toastError("Migration failed", err);
    } finally {
      setMigrateBusy(false);
    }
  };

  const confirmDelete = async (opts: { force?: boolean; volumes?: boolean }) => {
    if (!del) return;
    try {
      await api.vmDelete(del.providerId, del.id, { force: opts.force, deleteDisks: opts.volumes });
      toast.success("Delete requested", del.name);
      invalidate(del);
    } catch (err) {
      toastError("Delete failed", err);
      throw err;
    }
  };

  const dialogs = (
    <>
      {/* Snapshot */}
      <Modal
        open={snap !== null}
        title="Create snapshot"
        busy={snapBusy}
        onClose={() => setSnap(null)}
        footer={
          <>
            <button className="btn" onClick={() => setSnap(null)} disabled={snapBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={snapBusy}
              disabled={!snap?.name.trim()}
              onClick={confirmSnapshot}
            >
              Create snapshot
            </ActionButton>
          </>
        }
      >
        {snap ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Snapshot <strong className="mono">{snap.vm.name}</strong>.
            </div>
            <TextField
              label="Name"
              value={snap.name}
              autoFocus
              onChange={(e) => setSnap({ ...snap, name: e.target.value })}
            />
            <TextField
              label="Description"
              value={snap.description}
              onChange={(e) => setSnap({ ...snap, description: e.target.value })}
            />
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={snap.memory}
                onChange={(e) => setSnap({ ...snap, memory: e.target.checked })}
              />
              <span>Include memory state</span>
            </label>
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={snap.quiesce}
                onChange={(e) => setSnap({ ...snap, quiesce: e.target.checked })}
              />
              <span>Quiesce guest filesystem</span>
            </label>
          </div>
        ) : null}
      </Modal>

      {/* Clone */}
      <Modal
        open={clone !== null}
        title="Clone virtual machine"
        busy={cloneBusy}
        onClose={() => setClone(null)}
        footer={
          <>
            <button className="btn" onClick={() => setClone(null)} disabled={cloneBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={cloneBusy}
              disabled={!clone?.name.trim()}
              onClick={confirmClone}
            >
              Clone
            </ActionButton>
          </>
        }
      >
        {clone ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Clone <strong className="mono">{clone.vm.name}</strong>.
            </div>
            <TextField
              label="New name"
              value={clone.name}
              autoFocus
              onChange={(e) => setClone({ ...clone, name: e.target.value })}
            />
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={clone.linked}
                onChange={(e) => setClone({ ...clone, linked: e.target.checked })}
              />
              <span>Linked clone (share base disks)</span>
            </label>
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={clone.powerOn}
                onChange={(e) => setClone({ ...clone, powerOn: e.target.checked })}
              />
              <span>Power on after clone</span>
            </label>
          </div>
        ) : null}
      </Modal>

      {/* Reconfigure */}
      <Modal
        open={recfg !== null}
        title="Reconfigure hardware"
        busy={recfgBusy}
        onClose={() => setRecfg(null)}
        footer={
          <>
            <button className="btn" onClick={() => setRecfg(null)} disabled={recfgBusy}>
              Cancel
            </button>
            <ActionButton variant="primary" loading={recfgBusy} onClick={confirmReconfigure}>
              Apply
            </ActionButton>
          </>
        }
      >
        {recfg ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Reconfigure <strong className="mono">{recfg.vm.name}</strong>. Some hypervisors require a
              powered-off VM for CPU/memory changes.
            </div>
            <TextField
              label="vCPUs"
              type="number"
              min={1}
              value={recfg.vcpus}
              onChange={(e) => setRecfg({ ...recfg, vcpus: e.target.value })}
            />
            <TextField
              label="Memory (MB)"
              type="number"
              min={1}
              value={recfg.memoryMb}
              onChange={(e) => setRecfg({ ...recfg, memoryMb: e.target.value })}
            />
          </div>
        ) : null}
      </Modal>

      {/* Migrate (intra-hypervisor) */}
      <Modal
        open={migrate !== null}
        title="Migrate (intra-hypervisor)"
        busy={migrateBusy}
        onClose={() => setMigrate(null)}
        footer={
          <>
            <button className="btn" onClick={() => setMigrate(null)} disabled={migrateBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={migrateBusy}
              disabled={!migrate?.targetHost.trim()}
              onClick={confirmMigrate}
            >
              Migrate
            </ActionButton>
          </>
        }
      >
        {migrate ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Move <strong className="mono">{migrate.vm.name}</strong> to another host on the same
              hypervisor. For cross-hypervisor moves use the Migration (V2V) wizard.
            </div>
            <TextField
              label="Target host id"
              value={migrate.targetHost}
              autoFocus
              onChange={(e) => setMigrate({ ...migrate, targetHost: e.target.value })}
            />
            <TextField
              label="Target storage id (optional)"
              value={migrate.targetStorage}
              onChange={(e) => setMigrate({ ...migrate, targetStorage: e.target.value })}
            />
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={migrate.live}
                onChange={(e) => setMigrate({ ...migrate, live: e.target.checked })}
              />
              <span>Live migration (no downtime)</span>
            </label>
          </div>
        ) : null}
      </Modal>

      {/* Delete */}
      <ConfirmDestructiveDialog
        open={del !== null}
        title="Delete virtual machine"
        variant="danger"
        confirmLabel="Delete"
        showRemoveOptions
        description={
          <>
            Permanently delete <strong className="mono">{del?.name}</strong>? This cannot be undone.
            "Force removal" powers off a running VM first; "remove volumes" also deletes its disks.
          </>
        }
        onConfirm={confirmDelete}
        onClose={() => setDel(null)}
      />
    </>
  );

  return {
    busyId,
    runPower,
    triggerSnapshot,
    triggerClone,
    triggerReconfigure,
    triggerMigrate,
    triggerDelete,
    dialogs,
  };
}
