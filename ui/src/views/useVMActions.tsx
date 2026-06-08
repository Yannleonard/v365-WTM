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
import { TextField, SelectField } from "../components/Field";
import { useVMStorage, useVMVolumes, useVMNetworks } from "../lib/hooks";
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
interface AddDiskForm {
  vm: VM;
  capacityGb: string;
  format: string;
  storageId: string;
}
interface AddNicForm {
  vm: VM;
  networkId: string;
  model: string;
}
interface MountIsoForm {
  vm: VM;
  storageId: string;
  isoPath: string;
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

  // hot-plug (live device management)
  const [addDisk, setAddDisk] = useState<AddDiskForm | null>(null);
  const [addDiskBusy, setAddDiskBusy] = useState(false);
  const [addNic, setAddNic] = useState<AddNicForm | null>(null);
  const [addNicBusy, setAddNicBusy] = useState(false);
  const [mountIso, setMountIso] = useState<MountIsoForm | null>(null);
  const [mountIsoBusy, setMountIsoBusy] = useState(false);
  const [detachBusyId, setDetachBusyId] = useState<string | null>(null);

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
  const triggerAddDisk = (vm: VM) =>
    setAddDisk({ vm, capacityGb: "10", format: "qcow2", storageId: "" });
  const triggerAddNic = (vm: VM) => setAddNic({ vm, networkId: "", model: "virtio" });
  const triggerMountIso = (vm: VM) => setMountIso({ vm, storageId: "", isoPath: "" });

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

  /* ---- hot-plug (live, no reboot) ---- */
  const confirmAddDisk = async () => {
    if (!addDisk) return;
    const cap = Number(addDisk.capacityGb);
    setAddDiskBusy(true);
    try {
      await api.vmDiskAttach(addDisk.vm.providerId, addDisk.vm.id, {
        capacityGb: Number.isFinite(cap) && cap > 0 ? cap : 1,
        format: addDisk.format || undefined,
        storageId: addDisk.storageId.trim() || undefined,
      });
      toast.success("Disk attached", addDisk.vm.name);
      invalidate(addDisk.vm);
      setAddDisk(null);
    } catch (err) {
      toastError("Attach disk failed", err);
    } finally {
      setAddDiskBusy(false);
    }
  };

  const confirmAddNic = async () => {
    if (!addNic) return;
    setAddNicBusy(true);
    try {
      await api.vmNicAttach(addNic.vm.providerId, addNic.vm.id, {
        networkId: addNic.networkId.trim(),
        model: addNic.model || undefined,
      });
      toast.success("Network adapter attached", addNic.vm.name);
      invalidate(addNic.vm);
      setAddNic(null);
    } catch (err) {
      toastError("Attach NIC failed", err);
    } finally {
      setAddNicBusy(false);
    }
  };

  const confirmMountIso = async () => {
    if (!mountIso) return;
    setMountIsoBusy(true);
    try {
      await api.vmIsoMount(mountIso.vm.providerId, mountIso.vm.id, mountIso.isoPath.trim());
      toast.success("ISO mounted", mountIso.vm.name);
      invalidate(mountIso.vm);
      setMountIso(null);
    } catch (err) {
      toastError("Mount ISO failed", err);
    } finally {
      setMountIsoBusy(false);
    }
  };

  const detachDisk = async (vm: VM, diskId: string) => {
    setDetachBusyId(diskId);
    try {
      await api.vmDiskDetach(vm.providerId, vm.id, diskId);
      toast.success("Disk detached", vm.name);
      invalidate(vm);
    } catch (err) {
      toastError("Detach disk failed", err);
    } finally {
      setDetachBusyId(null);
    }
  };

  const detachNic = async (vm: VM, nicId: string) => {
    setDetachBusyId(nicId);
    try {
      await api.vmNicDetach(vm.providerId, vm.id, nicId);
      toast.success("Network adapter detached", vm.name);
      invalidate(vm);
    } catch (err) {
      toastError("Detach NIC failed", err);
    } finally {
      setDetachBusyId(null);
    }
  };

  const ejectIso = async (vm: VM) => {
    setDetachBusyId(vm.id + "-iso");
    try {
      await api.vmIsoUnmount(vm.providerId, vm.id);
      toast.success("ISO ejected", vm.name);
      invalidate(vm);
    } catch (err) {
      toastError("Eject ISO failed", err);
    } finally {
      setDetachBusyId(null);
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

      {/* Add disk (hot-plug) */}
      <Modal
        open={addDisk !== null}
        title="Add disk (live)"
        busy={addDiskBusy}
        onClose={() => setAddDisk(null)}
        footer={
          <>
            <button className="btn" onClick={() => setAddDisk(null)} disabled={addDiskBusy}>
              Cancel
            </button>
            <ActionButton variant="primary" loading={addDiskBusy} onClick={confirmAddDisk}>
              Attach disk
            </ActionButton>
          </>
        }
      >
        {addDisk ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Hot-attach a new disk to <strong className="mono">{addDisk.vm.name}</strong> with no
              reboot. Leave storage blank to provision in the default pool.
            </div>
            <TextField
              label="Capacity (GB)"
              type="number"
              min={1}
              value={addDisk.capacityGb}
              autoFocus
              onChange={(e) => setAddDisk({ ...addDisk, capacityGb: e.target.value })}
            />
            <SelectField
              label="Format"
              value={addDisk.format}
              onChange={(e) => setAddDisk({ ...addDisk, format: e.target.value })}
            >
              <option value="qcow2">qcow2</option>
              <option value="raw">raw</option>
            </SelectField>
            <StoragePoolSelect
              pid={addDisk.vm.providerId}
              value={addDisk.storageId}
              onChange={(v) => setAddDisk({ ...addDisk, storageId: v })}
              label="Storage pool (optional)"
              allowEmpty
            />
          </div>
        ) : null}
      </Modal>

      {/* Add network adapter (hot-plug) */}
      <Modal
        open={addNic !== null}
        title="Add network adapter (live)"
        busy={addNicBusy}
        onClose={() => setAddNic(null)}
        footer={
          <>
            <button className="btn" onClick={() => setAddNic(null)} disabled={addNicBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={addNicBusy}
              disabled={!addNic?.networkId.trim()}
              onClick={confirmAddNic}
            >
              Attach adapter
            </ActionButton>
          </>
        }
      >
        {addNic ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Hot-attach a virtual NIC to <strong className="mono">{addNic.vm.name}</strong> with no
              reboot.
            </div>
            <NetworkSelect
              pid={addNic.vm.providerId}
              value={addNic.networkId}
              onChange={(v) => setAddNic({ ...addNic, networkId: v })}
            />
            <SelectField
              label="Model"
              value={addNic.model}
              onChange={(e) => setAddNic({ ...addNic, model: e.target.value })}
            >
              <option value="virtio">virtio</option>
              <option value="e1000">e1000</option>
              <option value="rtl8139">rtl8139</option>
            </SelectField>
          </div>
        ) : null}
      </Modal>

      {/* Mount ISO (hot-plug) */}
      <Modal
        open={mountIso !== null}
        title="Mount ISO (live)"
        busy={mountIsoBusy}
        onClose={() => setMountIso(null)}
        footer={
          <>
            <button className="btn" onClick={() => setMountIso(null)} disabled={mountIsoBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={mountIsoBusy}
              disabled={!mountIso?.isoPath.trim()}
              onClick={confirmMountIso}
            >
              Mount ISO
            </ActionButton>
          </>
        }
      >
        {mountIso ? (
          <div className="col" style={{ gap: "var(--sp-3)" }}>
            <div className="text-sm secondary">
              Insert an ISO into <strong className="mono">{mountIso.vm.name}</strong>'s CD-ROM with no
              reboot. Pick a pool, then an ISO from its library.
            </div>
            <StoragePoolSelect
              pid={mountIso.vm.providerId}
              value={mountIso.storageId}
              onChange={(v) => setMountIso({ ...mountIso, storageId: v, isoPath: "" })}
              label="Storage pool"
            />
            <ISOSelect
              pid={mountIso.vm.providerId}
              storageId={mountIso.storageId}
              value={mountIso.isoPath}
              onChange={(v) => setMountIso({ ...mountIso, isoPath: v })}
            />
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
    // hot-plug
    triggerAddDisk,
    triggerAddNic,
    triggerMountIso,
    detachDisk,
    detachNic,
    ejectIso,
    detachBusyId,
    dialogs,
  };
}

/* ---- inline selectors for the hot-plug dialogs ---- */

function StoragePoolSelect({
  pid,
  value,
  onChange,
  label = "Storage pool",
  allowEmpty = false,
}: {
  pid: string;
  value: string;
  onChange: (v: string) => void;
  label?: string;
  allowEmpty?: boolean;
}) {
  const q = useVMStorage(pid);
  const pools = q.data ?? [];
  return (
    <SelectField label={label} value={value} onChange={(e) => onChange(e.target.value)}>
      {allowEmpty ? <option value="">Default pool</option> : <option value="">Select a pool…</option>}
      {pools.map((p) => (
        <option key={p.id} value={p.name}>
          {p.name}
        </option>
      ))}
    </SelectField>
  );
}

function NetworkSelect({
  pid,
  value,
  onChange,
}: {
  pid: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const q = useVMNetworks(pid);
  const nets = q.data ?? [];
  return (
    <SelectField label="Network" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Select a network…</option>
      {nets.map((n) => (
        <option key={n.id} value={n.name}>
          {n.name}
          {n.type ? ` (${n.type})` : ""}
        </option>
      ))}
    </SelectField>
  );
}

function ISOSelect({
  pid,
  storageId,
  value,
  onChange,
}: {
  pid: string;
  storageId: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const q = useVMVolumes(pid, storageId, !!storageId);
  const isos = (q.data ?? []).filter((v) => v.isIso);
  return (
    <SelectField
      label="ISO image"
      value={value}
      disabled={!storageId}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">{storageId ? "Select an ISO…" : "Pick a pool first"}</option>
      {isos.map((v) => (
        <option key={v.id} value={v.path || v.id}>
          {v.name}
        </option>
      ))}
    </SelectField>
  );
}
