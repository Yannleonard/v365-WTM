// ui/src/views/useVMActions.tsx
//
// Shared VM lifecycle action handling: power (start/stop/reset/suspend/resume),
// snapshot create, clone, reconfigure, intra-hypervisor migrate, and the
// hot-plug device ops (add/detach disk + NIC, mount/eject ISO).
//
// Power ops fire directly (optimistic toast + cache invalidation). Every
// parameterized action now opens a RIGHT-SIDE DRAWER (volet latéral) instead of
// a centered popup — see <Drawer/>. The Reconfigure drawer is a rich "Edit
// Settings" hardware editor (vCPU / memory / disks / NICs / boot / firmware)
// modeled on vCenter. Destructive delete keeps the small ConfirmDestructiveDialog.

import { Fragment, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { toast, toastError } from "../lib/toast";
import { Drawer } from "../components/Drawer";
import { ActionButton } from "../components/ActionButton";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField, SelectField } from "../components/Field";
import { useVMStorage, useVMVolumes, useVMNetworks, useStorageBackends } from "../lib/hooks";
import { formatBytes } from "../lib/format";
import {
  IconEdit,
  IconClone,
  IconSnapshot,
  IconMigrate,
  IconDisc,
  IconDisk,
  IconNic,
  IconCpu,
  IconMemory,
  IconPlus,
  IconTrash,
  IconAlert,
  IconHelp,
  IconCheck,
  IconScale,
  IconStacks,
  IconDownload,
} from "../components/icons";
import type { VM, VMDisk, VMNic, VMPowerOp, VMSnapshot } from "../lib/types";

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
  // deploy=true renders the same clone drawer as "Deploy from template" — the
  // backend Clone always yields a fresh NON-template VM (Lot 4A).
  deploy?: boolean;
}
interface ReconfigureForm {
  vm: VM;
  vcpus: string;
  memValue: string;
  memUnit: "MB" | "GB";
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
interface ResizeDiskForm {
  vm: VM;
  disk: VMDisk;
  capacityGb: string;
}
interface DeleteSnapForm {
  vm: VM;
  snap: VMSnapshot;
}
interface BackupForm {
  vm: VM;
  backendId: string;
}

// Lot 5A: resource-control + per-disk-QoS form shapes (string-backed inputs).
interface ResForm {
  cpuShares: string;
  cpuReservationMhz: string;
  cpuLimitMhz: string;
  memShares: string;
  memReservationMb: string;
  memLimitMb: string;
}
interface QoSForm {
  totalIops: string;
  readIops: string;
  writeIops: string;
  totalMbps: string;
  readMbps: string;
  writeMbps: string;
}

// numOrUndef parses a positive number from a string field, else undefined ("unset").
function numOrUndef(s: string): number | undefined {
  const n = Number(s);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}
// mbToBytes converts a MB/s string to bytes/sec (or undefined when unset).
function mbToBytes(s: string): number | undefined {
  const n = Number(s);
  return Number.isFinite(n) && n > 0 ? Math.round(n * 1024 * 1024) : undefined;
}
// bytesToMb converts a bytes/sec label string to a MB/s input string ("" when unset).
function bytesToMb(s: string | undefined): string {
  const n = Number(s);
  return Number.isFinite(n) && n > 0 ? String(Math.round(n / (1024 * 1024))) : "";
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
  const [templateBusyId, setTemplateBusyId] = useState<string | null>(null);
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

  // disk resize (online grow) + delete single snapshot
  const [resize, setResize] = useState<ResizeDiskForm | null>(null);
  const [resizeBusy, setResizeBusy] = useState(false);
  const [delSnap, setDelSnap] = useState<DeleteSnapForm | null>(null);

  // backup (Lot 5B): snapshot+export to a storage backend
  const [backup, setBackup] = useState<BackupForm | null>(null);
  const [backupBusy, setBackupBusy] = useState(false);

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

  /* ---- triggers (open drawers) ---- */
  const triggerSnapshot = (vm: VM) =>
    setSnap({ vm, name: "", description: "", memory: false, quiesce: false });
  const triggerClone = (vm: VM) =>
    setClone({ vm, name: `${vm.name}-clone`, linked: false, powerOn: false });
  // Deploy a fresh VM FROM a template — reuses the clone path (the backend Clone of
  // a template produces a NON-template runnable VM). Opens the same drawer, labeled
  // "Deploy from template" and defaulting to power-on.
  const triggerDeploy = (vm: VM) =>
    setClone({ vm, name: `${vm.name}-vm`, linked: false, powerOn: true, deploy: true });
  const triggerReconfigure = (vm: VM) => {
    // Present memory in GB when it divides evenly, else MB — nicer default.
    const gbExact = vm.memoryMb % 1024 === 0 && vm.memoryMb >= 1024;
    setRecfg({
      vm,
      vcpus: String(vm.vcpus),
      memValue: gbExact ? String(vm.memoryMb / 1024) : String(vm.memoryMb),
      memUnit: gbExact ? "GB" : "MB",
    });
  };
  const triggerMigrate = (vm: VM) =>
    setMigrate({ vm, targetHost: "", live: true, targetStorage: "" });
  const triggerDelete = (vm: VM) => setDel(vm);
  const triggerAddDisk = (vm: VM) =>
    setAddDisk({ vm, capacityGb: "10", format: "qcow2", storageId: "" });
  const triggerAddNic = (vm: VM) => setAddNic({ vm, networkId: "", model: "virtio" });
  const triggerMountIso = (vm: VM) => setMountIso({ vm, storageId: "", isoPath: "" });
  // Pre-fill the resize input one GB above the current size (grow-only nudge).
  const triggerResizeDisk = (vm: VM, disk: VMDisk) =>
    setResize({ vm, disk, capacityGb: String(Math.max(1, Math.round(disk.capacityGb)) + 1) });
  const triggerDeleteSnapshot = (vm: VM, snap: VMSnapshot) => setDelSnap({ vm, snap });
  // Back up now (Lot 5B): opens the backend picker; the server snapshots, exports
  // the disk(s) to qcow2 and uploads to the chosen storage backend.
  const triggerBackup = (vm: VM) => setBackup({ vm, backendId: "" });

  /* ---- templates (Lot 4A): mark / unmark a VM as a golden image (direct) ---- */
  const isTemplateVM = (vm: VM) => vm.labels?.["unihv.template"] === "true";
  const markTemplate = async (vm: VM, isTemplate: boolean) => {
    setTemplateBusyId(vm.id);
    try {
      await api.vmMarkTemplate(vm.providerId, vm.id, isTemplate);
      toast.success(isTemplate ? "Marked as template" : "Template mark removed", vm.name);
      invalidate(vm);
    } catch (err) {
      toastError(isTemplate ? "Mark as template failed" : "Unmark template failed", err);
    } finally {
      setTemplateBusyId(null);
    }
  };

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
      toast.success(clone.deploy ? "Deploy requested" : "Clone requested", clone.name.trim());
      invalidate(clone.vm);
      setClone(null);
    } catch (err) {
      toastError(clone.deploy ? "Deploy failed" : "Clone failed", err);
    } finally {
      setCloneBusy(false);
    }
  };

  const recfgMemoryMb = (f: ReconfigureForm): number => {
    const v = Number(f.memValue);
    if (!Number.isFinite(v) || v <= 0) return 0;
    return f.memUnit === "GB" ? Math.round(v * 1024) : Math.round(v);
  };

  const confirmReconfigure = async () => {
    if (!recfg) return;
    const vcpus = Number(recfg.vcpus);
    const memoryMb = recfgMemoryMb(recfg);
    setRecfgBusy(true);
    try {
      await api.vmReconfigure(recfg.vm.providerId, recfg.vm.id, {
        vcpus: Number.isFinite(vcpus) && vcpus > 0 ? vcpus : undefined,
        memoryMb: memoryMb > 0 ? memoryMb : undefined,
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
  // diskFromForm/nicFromForm centralize the attach payload so the rich
  // reconfigure editor and the standalone "Add disk/NIC" drawers share code.
  const attachDisk = async (vm: VM, form: { capacityGb: string; format: string; storageId: string }) => {
    const cap = Number(form.capacityGb);
    await api.vmDiskAttach(vm.providerId, vm.id, {
      capacityGb: Number.isFinite(cap) && cap > 0 ? cap : 1,
      format: form.format || undefined,
      storageId: form.storageId.trim() || undefined,
    });
  };
  const attachNic = async (vm: VM, form: { networkId: string; model: string }) => {
    await api.vmNicAttach(vm.providerId, vm.id, {
      networkId: form.networkId.trim(),
      model: form.model || undefined,
    });
  };

  const confirmAddDisk = async () => {
    if (!addDisk) return;
    setAddDiskBusy(true);
    try {
      await attachDisk(addDisk.vm, addDisk);
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
      await attachNic(addNic.vm, addNic);
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

  const confirmResizeDisk = async () => {
    if (!resize) return;
    const cap = Number(resize.capacityGb);
    setResizeBusy(true);
    try {
      await api.vmDiskResize(resize.vm.providerId, resize.vm.id, resize.disk.id, { capacityGb: cap });
      toast.success("Disk resize requested", `${resize.disk.label || resize.disk.id} → ${cap} GB`);
      invalidate(resize.vm);
      setResize(null);
    } catch (err) {
      toastError("Resize disk failed", err);
    } finally {
      setResizeBusy(false);
    }
  };

  // Delete a single snapshot, then refresh both the snapshot tree and the VM.
  const confirmDeleteSnapshot = async () => {
    if (!delSnap) return;
    try {
      await api.vmSnapshotDelete(delSnap.vm.providerId, delSnap.vm.id, delSnap.snap.id);
      toast.success("Snapshot deleted", delSnap.snap.name);
      queryClient.invalidateQueries({ queryKey: ["vm", "snapshots", delSnap.vm.providerId, delSnap.vm.id] });
      invalidate(delSnap.vm);
    } catch (err) {
      toastError("Delete snapshot failed", err);
      throw err;
    }
  };

  const confirmBackup = async () => {
    if (!backup || !backup.backendId) return;
    setBackupBusy(true);
    try {
      await api.vmBackupRun({ providerId: backup.vm.providerId, vmId: backup.vm.id, backendId: backup.backendId });
      toast.success("Backup complete", backup.vm.name);
      queryClient.invalidateQueries({ queryKey: ["vm-backups"] });
      setBackup(null);
    } catch (err) {
      toastError("Backup failed", err);
    } finally {
      setBackupBusy(false);
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
      {/* Reconfigure — the rich "Edit Settings" hardware editor */}
      <ReconfigureDrawer
        form={recfg}
        busy={recfgBusy}
        memoryMb={recfg ? recfgMemoryMb(recfg) : 0}
        onChange={setRecfg}
        onClose={() => setRecfg(null)}
        onApply={confirmReconfigure}
        attachDisk={attachDisk}
        attachNic={attachNic}
        detachDisk={detachDisk}
        detachNic={detachNic}
        detachBusyId={detachBusyId}
        invalidate={invalidate}
      />

      {/* Snapshot */}
      <Drawer
        open={snap !== null}
        title="Create snapshot"
        subtitle={snap ? snap.vm.name : undefined}
        icon={<IconSnapshot size={18} />}
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
          <div className="drawer-section">
            <TextField
              label="Name"
              value={snap.name}
              autoFocus
              placeholder="e.g. before-upgrade"
              onChange={(e) => setSnap({ ...snap, name: e.target.value })}
            />
            <TextField
              label="Description"
              value={snap.description}
              placeholder="Optional"
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
      </Drawer>

      {/* Clone / Deploy-from-template (same path; deploy yields a non-template VM) */}
      <Drawer
        open={clone !== null}
        title={clone?.deploy ? "Deploy from template" : "Clone virtual machine"}
        subtitle={clone ? clone.vm.name : undefined}
        icon={clone?.deploy ? <IconStacks size={18} /> : <IconClone size={18} />}
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
              {clone?.deploy ? "Deploy" : "Clone"}
            </ActionButton>
          </>
        }
      >
        {clone ? (
          <div className="drawer-section">
            {clone.deploy ? (
              <div className="drawer-banner info">
                <IconHelp size={15} />
                <span>
                  Deploy a new, runnable VM from the <strong>{clone.vm.name}</strong> template. The new
                  VM is independent and is <strong>not</strong> itself a template.
                </span>
              </div>
            ) : null}
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
      </Drawer>

      {/* Migrate (intra-hypervisor) */}
      <Drawer
        open={migrate !== null}
        title="Migrate (intra-hypervisor)"
        subtitle={migrate ? migrate.vm.name : undefined}
        icon={<IconMigrate size={18} />}
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
          <div className="drawer-section">
            <div className="drawer-banner info">
              <IconHelp size={15} />
              <span>
                Move this VM to another host on the <strong>same</strong> hypervisor. For
                cross-hypervisor moves use the Migration (V2V) wizard.
              </span>
            </div>
            <TextField
              label="Target host id"
              value={migrate.targetHost}
              autoFocus
              onChange={(e) => setMigrate({ ...migrate, targetHost: e.target.value })}
            />
            <TextField
              label="Target storage id"
              hint="Optional — leave blank to keep current storage"
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
      </Drawer>

      {/* Add disk (hot-plug) */}
      <Drawer
        open={addDisk !== null}
        title="Add disk"
        subtitle={addDisk ? `${addDisk.vm.name} · live, no reboot` : undefined}
        icon={<IconDisk size={18} />}
        busy={addDiskBusy}
        onClose={() => setAddDisk(null)}
        footer={
          <>
            <button className="btn" onClick={() => setAddDisk(null)} disabled={addDiskBusy}>
              Cancel
            </button>
            <ActionButton variant="primary" loading={addDiskBusy} onClick={confirmAddDisk}>
              <IconPlus size={14} />
              Attach disk
            </ActionButton>
          </>
        }
      >
        {addDisk ? (
          <div className="drawer-section">
            <div className="drawer-banner info">
              <IconHelp size={15} />
              <span>Hot-attach a new virtual disk with no reboot. Leave the pool on default to provision in the provider's default datastore.</span>
            </div>
            <div className="field-grid">
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
                <option value="qcow2">qcow2 (thin)</option>
                <option value="raw">raw (thick)</option>
              </SelectField>
            </div>
            <StoragePoolSelect
              pid={addDisk.vm.providerId}
              value={addDisk.storageId}
              onChange={(v) => setAddDisk({ ...addDisk, storageId: v })}
              label="Storage pool"
              allowEmpty
            />
          </div>
        ) : null}
      </Drawer>

      {/* Add network adapter (hot-plug) */}
      <Drawer
        open={addNic !== null}
        title="Add network adapter"
        subtitle={addNic ? `${addNic.vm.name} · live, no reboot` : undefined}
        icon={<IconNic size={18} />}
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
              <IconPlus size={14} />
              Attach adapter
            </ActionButton>
          </>
        }
      >
        {addNic ? (
          <div className="drawer-section">
            <NetworkSelect
              pid={addNic.vm.providerId}
              value={addNic.networkId}
              onChange={(v) => setAddNic({ ...addNic, networkId: v })}
            />
            <SelectField
              label="Adapter model"
              value={addNic.model}
              onChange={(e) => setAddNic({ ...addNic, model: e.target.value })}
            >
              <option value="virtio">virtio (paravirtual, fastest)</option>
              <option value="e1000">e1000</option>
              <option value="rtl8139">rtl8139</option>
            </SelectField>
          </div>
        ) : null}
      </Drawer>

      {/* Mount ISO (hot-plug) */}
      <Drawer
        open={mountIso !== null}
        title="Mount ISO"
        subtitle={mountIso ? `${mountIso.vm.name} · CD/DVD drive` : undefined}
        icon={<IconDisc size={18} />}
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
              <IconDisc size={14} />
              Mount ISO
            </ActionButton>
          </>
        }
      >
        {mountIso ? (
          <div className="drawer-section">
            <div className="drawer-banner info">
              <IconHelp size={15} />
              <span>Insert an ISO into the virtual CD/DVD drive with no reboot. Pick a pool, then an image from its library.</span>
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
      </Drawer>

      {/* Resize disk (online grow) */}
      <Drawer
        open={resize !== null}
        title="Resize disk"
        subtitle={resize ? `${resize.vm.name} · ${resize.disk.label || resize.disk.id}` : undefined}
        icon={<IconScale size={18} />}
        busy={resizeBusy}
        onClose={() => setResize(null)}
        footer={
          <>
            <button className="btn" onClick={() => setResize(null)} disabled={resizeBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={resizeBusy}
              disabled={!resize || Number(resize.capacityGb) <= resize.disk.capacityGb}
              tooltip={
                resize && Number(resize.capacityGb) <= resize.disk.capacityGb
                  ? "New size must be larger than the current size"
                  : undefined
              }
              onClick={confirmResizeDisk}
            >
              <IconScale size={14} />
              Resize disk
            </ActionButton>
          </>
        }
      >
        {resize ? (
          <div className="drawer-section">
            <div className="drawer-banner info">
              <IconHelp size={15} />
              <span>
                Grow this virtual disk online — no reboot. Disks can only be <strong>enlarged</strong>;
                shrinking is not supported. Extend the filesystem inside the guest afterwards.
              </span>
            </div>
            <TextField
              label="New capacity (GB)"
              type="number"
              min={Math.ceil(resize.disk.capacityGb) + 1}
              autoFocus
              value={resize.capacityGb}
              hint={`Current: ${formatBytes(resize.disk.capacityGb * 1024 ** 3, 0)}`}
              onChange={(e) => setResize({ ...resize, capacityGb: e.target.value })}
            />
            <dl className="spec-summary">
              <dt>Capacity</dt>
              <dd>
                {formatBytes(resize.disk.capacityGb * 1024 ** 3, 0)}
                {Number(resize.capacityGb) > resize.disk.capacityGb ? (
                  <span className="delta"> → {formatBytes(Number(resize.capacityGb) * 1024 ** 3, 0)}</span>
                ) : null}
              </dd>
            </dl>
          </div>
        ) : null}
      </Drawer>

      {/* Delete single snapshot */}
      <ConfirmDestructiveDialog
        open={delSnap !== null}
        title="Delete snapshot"
        variant="danger"
        confirmLabel="Delete snapshot"
        description={
          <>
            Permanently delete the snapshot <strong className="mono">{delSnap?.snap.name}</strong>? This
            cannot be undone. Child snapshots (if any) are re-parented onto its parent by the hypervisor.
          </>
        }
        onConfirm={confirmDeleteSnapshot}
        onClose={() => setDelSnap(null)}
      />

      {/* Back up now (Lot 5B): snapshot -> export -> store to a backend */}
      <Drawer
        open={backup !== null}
        title="Back up now"
        subtitle={backup ? `${backup.vm.name} · snapshot + export` : undefined}
        icon={<IconDownload size={18} />}
        busy={backupBusy}
        onClose={() => setBackup(null)}
        footer={
          <>
            <button className="btn" onClick={() => setBackup(null)} disabled={backupBusy}>
              Cancel
            </button>
            <ActionButton
              variant="primary"
              loading={backupBusy}
              disabled={!backup?.backendId}
              onClick={confirmBackup}
            >
              <IconDownload size={14} />
              Back up
            </ActionButton>
          </>
        }
      >
        {backup ? (
          <div className="drawer-section">
            <div className="drawer-banner info">
              <IconHelp size={15} />
              <span>
                Take a consistency snapshot, export the disk(s) to qcow2 and upload the artifact to the
                chosen storage backend. The snapshot is dropped afterwards.
              </span>
            </div>
            <BackendSelect value={backup.backendId} onChange={(v) => setBackup({ ...backup, backendId: v })} />
          </div>
        ) : null}
      </Drawer>

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
    // templates (Lot 4A)
    triggerDeploy,
    markTemplate,
    isTemplateVM,
    templateBusyId,
    // hot-plug
    triggerAddDisk,
    triggerAddNic,
    triggerMountIso,
    detachDisk,
    detachNic,
    ejectIso,
    detachBusyId,
    // disk resize + delete-snapshot (Lot 3)
    triggerResizeDisk,
    triggerDeleteSnapshot,
    // backup (Lot 5B)
    triggerBackup,
    dialogs,
  };
}

// BackendSelect lists registered storage backends usable as backup targets.
function BackendSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const q = useStorageBackends();
  const backends = q.data ?? [];
  return (
    <SelectField label="Storage backend" value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">Select a backend…</option>
      {backends.map((b) => (
        <option key={b.id} value={b.id}>
          {b.name} ({b.type})
        </option>
      ))}
    </SelectField>
  );
}

/* ===================== Rich Reconfigure ("Edit Settings") ===================== */

function ReconfigureDrawer({
  form,
  busy,
  memoryMb,
  onChange,
  onClose,
  onApply,
  attachDisk,
  attachNic,
  detachDisk,
  detachNic,
  detachBusyId,
  invalidate,
}: {
  form: ReconfigureForm | null;
  busy: boolean;
  memoryMb: number;
  onChange: (f: ReconfigureForm) => void;
  onClose: () => void;
  onApply: () => void;
  attachDisk: (vm: VM, f: { capacityGb: string; format: string; storageId: string }) => Promise<void>;
  attachNic: (vm: VM, f: { networkId: string; model: string }) => Promise<void>;
  detachDisk: (vm: VM, diskId: string) => void;
  detachNic: (vm: VM, nicId: string) => void;
  detachBusyId: string | null;
  invalidate: (vm: VM) => void;
}) {
  if (!form) return <Drawer open={false} title="" onClose={onClose}>{null}</Drawer>;
  return (
    <ReconfigureBody
      form={form}
      busy={busy}
      memoryMb={memoryMb}
      onChange={onChange}
      onClose={onClose}
      onApply={onApply}
      attachDisk={attachDisk}
      attachNic={attachNic}
      detachDisk={detachDisk}
      detachNic={detachNic}
      detachBusyId={detachBusyId}
      invalidate={invalidate}
    />
  );
}

function ReconfigureBody({
  form,
  busy,
  memoryMb,
  onChange,
  onClose,
  onApply,
  attachDisk,
  attachNic,
  detachDisk,
  detachNic,
  detachBusyId,
  invalidate,
}: {
  form: ReconfigureForm;
  busy: boolean;
  memoryMb: number;
  onChange: (f: ReconfigureForm) => void;
  onClose: () => void;
  onApply: () => void;
  attachDisk: (vm: VM, f: { capacityGb: string; format: string; storageId: string }) => Promise<void>;
  attachNic: (vm: VM, f: { networkId: string; model: string }) => Promise<void>;
  detachDisk: (vm: VM, diskId: string) => void;
  detachNic: (vm: VM, nicId: string) => void;
  detachBusyId: string | null;
  invalidate: (vm: VM) => void;
}) {
  const vm = form.vm;
  const running = vm.state === "running";
  const disks = vm.disks ?? [];
  const nics = vm.nics ?? [];

  // local "add device" sub-forms (collapsed until the user clicks Add)
  const [newDisk, setNewDisk] = useState<{ capacityGb: string; format: string; storageId: string } | null>(null);
  const [diskBusy, setDiskBusy] = useState(false);
  const [newNic, setNewNic] = useState<{ networkId: string; model: string } | null>(null);
  const [nicBusy, setNicBusy] = useState(false);

  // Lot 5A: CPU/memory resource control. Initialized from the VM's surfaced
  // unihv.res.* labels (the backend reports the applied <cputune>/<memtune> there).
  const lbl = vm.labels ?? {};
  const [res, setRes] = useState<ResForm>({
    cpuShares: lbl["unihv.res.cpu.shares"] ?? "",
    cpuReservationMhz: lbl["unihv.res.cpu.reservationMhz"] ?? "",
    cpuLimitMhz: lbl["unihv.res.cpu.limitMhz"] ?? "",
    memShares: lbl["unihv.res.mem.shares"] ?? "",
    memReservationMb: lbl["unihv.res.mem.reservationMb"] ?? "",
    memLimitMb: lbl["unihv.res.mem.limitMb"] ?? "",
  });
  const [resBusy, setResBusy] = useState(false);
  const applyResources = async () => {
    setResBusy(true);
    try {
      await api.vmSetResources(vm.providerId, vm.id, {
        cpuShares: numOrUndef(res.cpuShares),
        cpuReservationMhz: numOrUndef(res.cpuReservationMhz),
        cpuLimitMhz: numOrUndef(res.cpuLimitMhz),
        memoryShares: numOrUndef(res.memShares),
        memoryReservationMb: numOrUndef(res.memReservationMb),
        memoryLimitMb: numOrUndef(res.memLimitMb),
      });
      toast.success("Resource allocation applied", vm.name);
      invalidate(vm);
    } catch (err) {
      toastError("Resource control failed", err);
    } finally {
      setResBusy(false);
    }
  };

  // Lot 5A: per-disk QoS editor + live storage migration (one open row at a time).
  const [qosFor, setQosFor] = useState<{ diskId: string; v: QoSForm } | null>(null);
  const [qosBusy, setQosBusy] = useState(false);
  const applyQoS = async () => {
    if (!qosFor) return;
    setQosBusy(true);
    try {
      await api.vmDiskQoS(vm.providerId, vm.id, qosFor.diskId, {
        totalIops: numOrUndef(qosFor.v.totalIops),
        readIops: numOrUndef(qosFor.v.readIops),
        writeIops: numOrUndef(qosFor.v.writeIops),
        totalBytesSec: mbToBytes(qosFor.v.totalMbps),
        readBytesSec: mbToBytes(qosFor.v.readMbps),
        writeBytesSec: mbToBytes(qosFor.v.writeMbps),
      });
      toast.success("Disk QoS applied", qosFor.diskId);
      invalidate(vm);
      setQosFor(null);
    } catch (err) {
      toastError("Disk QoS failed", err);
    } finally {
      setQosBusy(false);
    }
  };
  const [migrateFor, setMigrateFor] = useState<{ diskId: string; target: string } | null>(null);
  const [stMigBusy, setStMigBusy] = useState(false);
  const applyStorageMigrate = async () => {
    if (!migrateFor || !migrateFor.target.trim()) return;
    setStMigBusy(true);
    try {
      await api.vmStorageMigrate(vm.providerId, vm.id, migrateFor.diskId, {
        targetStorageId: migrateFor.target.trim(),
      });
      toast.success("Live storage migration started", migrateFor.diskId);
      invalidate(vm);
      setMigrateFor(null);
    } catch (err) {
      toastError("Storage migration failed", err);
    } finally {
      setStMigBusy(false);
    }
  };

  const cpuChanged = Number(form.vcpus) !== vm.vcpus && Number(form.vcpus) > 0;
  const memChanged = memoryMb > 0 && memoryMb !== vm.memoryMb;
  const compute = cpuChanged || memChanged;

  const submitDisk = async () => {
    if (!newDisk) return;
    setDiskBusy(true);
    try {
      await attachDisk(vm, newDisk);
      toast.success("Disk attached", vm.name);
      invalidate(vm);
      setNewDisk(null);
    } catch (err) {
      toastError("Attach disk failed", err);
    } finally {
      setDiskBusy(false);
    }
  };
  const submitNic = async () => {
    if (!newNic || !newNic.networkId.trim()) return;
    setNicBusy(true);
    try {
      await attachNic(vm, newNic);
      toast.success("Network adapter attached", vm.name);
      invalidate(vm);
      setNewNic(null);
    } catch (err) {
      toastError("Attach NIC failed", err);
    } finally {
      setNicBusy(false);
    }
  };

  return (
    <Drawer
      open
      size="lg"
      icon={<IconEdit size={18} />}
      title="Edit settings"
      subtitle={
        <span>
          {vm.name} · {vm.kind}
          {vm.firmware ? ` · ${vm.firmware}` : ""}
        </span>
      }
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>
            Close
          </button>
          <ActionButton
            variant="primary"
            loading={busy}
            disabled={!compute}
            tooltip={compute ? undefined : "Change vCPU or memory to apply"}
            onClick={onApply}
          >
            <IconCheck size={14} />
            Apply CPU / memory
          </ActionButton>
        </>
      }
    >
      {running ? (
        <div className="drawer-banner warn" style={{ marginBottom: "var(--sp-4)" }}>
          <IconAlert size={15} />
          <span>
            This VM is <strong>running</strong>. Many hypervisors require a power-off before changing
            vCPU or memory. Disk and adapter add/remove apply live (hot-plug).
          </span>
        </div>
      ) : null}

      {/* Compute */}
      <div className="drawer-section">
        <div className="drawer-section-head">
          <span className="drawer-section-title">
            <IconCpu size={15} /> Compute
          </span>
        </div>
        <div className="field-grid">
          <TextField
            label="vCPUs"
            type="number"
            min={1}
            value={form.vcpus}
            onChange={(e) => onChange({ ...form, vcpus: e.target.value })}
            hint={`Current: ${vm.vcpus}`}
          />
          <div className="field">
            <label className="field-label">Memory</label>
            <div className="row" style={{ gap: "var(--sp-2)" }}>
              <input
                className="input"
                type="number"
                min={1}
                value={form.memValue}
                style={{ flex: 1 }}
                onChange={(e) => onChange({ ...form, memValue: e.target.value })}
              />
              <select
                className="select"
                value={form.memUnit}
                style={{ width: 80 }}
                onChange={(e) => onChange({ ...form, memUnit: e.target.value as "MB" | "GB" })}
              >
                <option value="MB">MB</option>
                <option value="GB">GB</option>
              </select>
            </div>
            <span className="field-hint">Current: {formatBytes(vm.memoryMb * 1024 * 1024, 0)}</span>
          </div>
        </div>

        {/* current vs new summary */}
        <dl className="spec-summary">
          <dt>vCPUs</dt>
          <dd>
            {vm.vcpus}
            {cpuChanged ? <span className="delta"> → {Number(form.vcpus)}</span> : null}
          </dd>
          <dt>Memory</dt>
          <dd>
            {formatBytes(vm.memoryMb * 1024 * 1024, 0)}
            {memChanged ? <span className="delta"> → {formatBytes(memoryMb * 1024 * 1024, 0)}</span> : null}
          </dd>
        </dl>
      </div>

      {/* Resources — CPU/memory reservation, limit & shares (Lot 5A). Applies
          independently of vCPU/memory via its own button (<cputune>/<memtune>). */}
      <div className="drawer-section">
        <div className="drawer-section-head">
          <span className="drawer-section-title">
            <IconScale size={15} /> Resource allocation
          </span>
          <ActionButton size="sm" variant="ghost" loading={resBusy} onClick={applyResources}>
            <IconCheck size={13} /> Apply resources
          </ActionButton>
        </div>
        <div className="drawer-banner info" style={{ marginBottom: "var(--sp-3)" }}>
          <IconHelp size={15} />
          <span>
            Reservation, limit and shares control how this VM competes for host CPU &amp; memory
            (<span className="mono">cputune</span>/<span className="mono">memtune</span>). Leave a field blank for the
            hypervisor default. Limit is a hard cap; reservation is a guarantee; shares is a relative weight.
          </span>
        </div>
        <div className="field-grid">
          <TextField
            label="CPU shares"
            type="number"
            min={0}
            value={res.cpuShares}
            placeholder="default"
            onChange={(e) => setRes({ ...res, cpuShares: e.target.value })}
          />
          <TextField
            label="CPU reservation (MHz)"
            type="number"
            min={0}
            value={res.cpuReservationMhz}
            placeholder="0 = none"
            onChange={(e) => setRes({ ...res, cpuReservationMhz: e.target.value })}
          />
          <TextField
            label="CPU limit (MHz)"
            type="number"
            min={0}
            value={res.cpuLimitMhz}
            placeholder="0 = unlimited"
            onChange={(e) => setRes({ ...res, cpuLimitMhz: e.target.value })}
          />
          <TextField
            label="Memory shares"
            type="number"
            min={0}
            value={res.memShares}
            placeholder="default"
            onChange={(e) => setRes({ ...res, memShares: e.target.value })}
          />
          <TextField
            label="Memory reservation (MB)"
            type="number"
            min={0}
            value={res.memReservationMb}
            placeholder="0 = none"
            onChange={(e) => setRes({ ...res, memReservationMb: e.target.value })}
          />
          <TextField
            label="Memory limit (MB)"
            type="number"
            min={0}
            value={res.memLimitMb}
            placeholder="0 = unlimited"
            onChange={(e) => setRes({ ...res, memLimitMb: e.target.value })}
          />
        </div>
      </div>

      {/* Disks */}
      <div className="drawer-section">
        <div className="drawer-section-head">
          <span className="drawer-section-title">
            <IconDisk size={15} /> Hard disks ({disks.length})
          </span>
          {newDisk === null ? (
            <ActionButton
              size="sm"
              variant="ghost"
              onClick={() => setNewDisk({ capacityGb: "10", format: "qcow2", storageId: "" })}
            >
              <IconPlus size={13} /> Add disk
            </ActionButton>
          ) : null}
        </div>

        {disks.length === 0 && newDisk === null ? (
          <span className="muted text-sm">No disks attached.</span>
        ) : null}

        {disks.map((d: VMDisk) => {
          const qp = "unihv.qos." + (d.label || "") + ".";
          const qosActive = Object.keys(lbl).some((k) => k.startsWith(qp));
          return (
          <Fragment key={d.id}>
          <div className="device-row">
            <span className="device-icon">
              <IconDisk size={16} />
            </span>
            <div className="device-main">
              <span className="device-title">{d.label || d.id}</span>
              <span className="device-meta">
                <span>{formatBytes(d.capacityGb * 1024 ** 3, 0)}</span>
                {d.format ? <span className="chip">{d.format}</span> : null}
                {d.storageId ? <span className="mono">{d.storageId}</span> : null}
                {qosActive ? <span className="chip">QoS</span> : null}
              </span>
            </div>
            {/* Lot 5A: per-disk QoS (iotune) */}
            <ActionButton
              size="sm"
              variant="ghost"
              iconOnly
              tooltip="Edit disk QoS (IOPS / bandwidth)"
              aria-label="Edit disk QoS"
              onClick={() =>
                setQosFor(
                  qosFor?.diskId === d.id
                    ? null
                    : {
                        diskId: d.id,
                        v: {
                          totalIops: lbl[qp + "totalIops"] ?? "",
                          readIops: lbl[qp + "readIops"] ?? "",
                          writeIops: lbl[qp + "writeIops"] ?? "",
                          totalMbps: bytesToMb(lbl[qp + "totalBps"]),
                          readMbps: bytesToMb(lbl[qp + "readBps"]),
                          writeMbps: bytesToMb(lbl[qp + "writeBps"]),
                        },
                      },
                )
              }
            >
              <IconScale size={14} />
            </ActionButton>
            {/* Lot 5A: live storage migration */}
            <ActionButton
              size="sm"
              variant="ghost"
              iconOnly
              tooltip="Migrate disk to another storage (live)"
              aria-label="Migrate storage"
              onClick={() =>
                setMigrateFor(migrateFor?.diskId === d.id ? null : { diskId: d.id, target: "" })
              }
            >
              <IconMigrate size={14} />
            </ActionButton>
            <ActionButton
              size="sm"
              variant="ghost"
              iconOnly
              tooltip="Detach disk (live)"
              aria-label="Detach disk"
              loading={detachBusyId === d.id}
              onClick={() => detachDisk(vm, d.id)}
              style={{ color: "var(--danger)" }}
            >
              <IconTrash size={14} />
            </ActionButton>
          </div>

          {/* QoS editor (inline, one at a time) */}
          {qosFor?.diskId === d.id ? (
            <div className="device-add">
              <div className="field-grid">
                <TextField
                  label="Total IOPS"
                  type="number"
                  min={0}
                  value={qosFor.v.totalIops}
                  placeholder="0 = unlimited"
                  onChange={(e) => setQosFor({ ...qosFor, v: { ...qosFor.v, totalIops: e.target.value } })}
                />
                <TextField
                  label="Total bandwidth (MB/s)"
                  type="number"
                  min={0}
                  value={qosFor.v.totalMbps}
                  placeholder="0 = unlimited"
                  onChange={(e) => setQosFor({ ...qosFor, v: { ...qosFor.v, totalMbps: e.target.value } })}
                />
                <TextField
                  label="Read IOPS"
                  type="number"
                  min={0}
                  value={qosFor.v.readIops}
                  onChange={(e) => setQosFor({ ...qosFor, v: { ...qosFor.v, readIops: e.target.value } })}
                />
                <TextField
                  label="Write IOPS"
                  type="number"
                  min={0}
                  value={qosFor.v.writeIops}
                  onChange={(e) => setQosFor({ ...qosFor, v: { ...qosFor.v, writeIops: e.target.value } })}
                />
                <TextField
                  label="Read bandwidth (MB/s)"
                  type="number"
                  min={0}
                  value={qosFor.v.readMbps}
                  onChange={(e) => setQosFor({ ...qosFor, v: { ...qosFor.v, readMbps: e.target.value } })}
                />
                <TextField
                  label="Write bandwidth (MB/s)"
                  type="number"
                  min={0}
                  value={qosFor.v.writeMbps}
                  onChange={(e) => setQosFor({ ...qosFor, v: { ...qosFor.v, writeMbps: e.target.value } })}
                />
              </div>
              <span className="field-hint">Total* limits override the read/write pair. Blank or 0 clears a limit. Applies live, no reboot.</span>
              <div className="row" style={{ justifyContent: "flex-end", gap: "var(--sp-2)" }}>
                <button className="btn btn-sm" onClick={() => setQosFor(null)} disabled={qosBusy}>
                  Cancel
                </button>
                <ActionButton size="sm" variant="primary" loading={qosBusy} onClick={applyQoS}>
                  Apply QoS
                </ActionButton>
              </div>
            </div>
          ) : null}

          {/* Live storage migration (inline) */}
          {migrateFor?.diskId === d.id ? (
            <div className="device-add">
              <div className="drawer-banner info" style={{ marginBottom: "var(--sp-2)" }}>
                <IconHelp size={15} />
                <span>
                  Live-migrate this disk to another storage pool with no downtime (DomainBlockCopy + pivot).
                  The VM must be running.
                </span>
              </div>
              <StoragePoolSelect
                pid={vm.providerId}
                value={migrateFor.target}
                onChange={(v) => setMigrateFor({ ...migrateFor, target: v })}
                label="Target storage pool"
                allowEmpty
              />
              <div className="row" style={{ justifyContent: "flex-end", gap: "var(--sp-2)" }}>
                <button className="btn btn-sm" onClick={() => setMigrateFor(null)} disabled={stMigBusy}>
                  Cancel
                </button>
                <ActionButton
                  size="sm"
                  variant="primary"
                  loading={stMigBusy}
                  disabled={!migrateFor.target.trim()}
                  onClick={applyStorageMigrate}
                >
                  Migrate disk
                </ActionButton>
              </div>
            </div>
          ) : null}
          </Fragment>
          );
        })}

        {newDisk ? (
          <div className="device-add">
            <div className="field-grid">
              <TextField
                label="Capacity (GB)"
                type="number"
                min={1}
                autoFocus
                value={newDisk.capacityGb}
                onChange={(e) => setNewDisk({ ...newDisk, capacityGb: e.target.value })}
              />
              <SelectField
                label="Format"
                value={newDisk.format}
                onChange={(e) => setNewDisk({ ...newDisk, format: e.target.value })}
              >
                <option value="qcow2">qcow2 (thin)</option>
                <option value="raw">raw (thick)</option>
              </SelectField>
            </div>
            <StoragePoolSelect
              pid={vm.providerId}
              value={newDisk.storageId}
              onChange={(v) => setNewDisk({ ...newDisk, storageId: v })}
              label="Storage pool"
              allowEmpty
            />
            <div className="row" style={{ justifyContent: "flex-end", gap: "var(--sp-2)" }}>
              <button className="btn btn-sm" onClick={() => setNewDisk(null)} disabled={diskBusy}>
                Cancel
              </button>
              <ActionButton size="sm" variant="primary" loading={diskBusy} onClick={submitDisk}>
                Attach disk
              </ActionButton>
            </div>
          </div>
        ) : null}
      </div>

      {/* Network adapters */}
      <div className="drawer-section">
        <div className="drawer-section-head">
          <span className="drawer-section-title">
            <IconNic size={15} /> Network adapters ({nics.length})
          </span>
          {newNic === null ? (
            <ActionButton
              size="sm"
              variant="ghost"
              onClick={() => setNewNic({ networkId: "", model: "virtio" })}
            >
              <IconPlus size={13} /> Add adapter
            </ActionButton>
          ) : null}
        </div>

        {nics.length === 0 && newNic === null ? (
          <span className="muted text-sm">No network adapters attached.</span>
        ) : null}

        {nics.map((n: VMNic) => (
          <div key={n.id} className="device-row">
            <span className="device-icon">
              <IconNic size={16} />
            </span>
            <div className="device-main">
              <span className="device-title">{n.networkId || n.id}</span>
              <span className="device-meta">
                {n.model ? <span className="chip">{n.model}</span> : null}
                {n.mac ? <span className="mono">{n.mac}</span> : null}
                <span>{n.connected ? "connected" : "disconnected"}</span>
              </span>
            </div>
            <ActionButton
              size="sm"
              variant="ghost"
              iconOnly
              tooltip="Detach adapter (live)"
              aria-label="Detach adapter"
              loading={detachBusyId === n.id}
              onClick={() => detachNic(vm, n.id)}
              style={{ color: "var(--danger)" }}
            >
              <IconTrash size={14} />
            </ActionButton>
          </div>
        ))}

        {newNic ? (
          <div className="device-add">
            <NetworkSelect
              pid={vm.providerId}
              value={newNic.networkId}
              onChange={(v) => setNewNic({ ...newNic, networkId: v })}
            />
            <SelectField
              label="Adapter model"
              value={newNic.model}
              onChange={(e) => setNewNic({ ...newNic, model: e.target.value })}
            >
              <option value="virtio">virtio (paravirtual, fastest)</option>
              <option value="e1000">e1000</option>
              <option value="rtl8139">rtl8139</option>
            </SelectField>
            <div className="row" style={{ justifyContent: "flex-end", gap: "var(--sp-2)" }}>
              <button className="btn btn-sm" onClick={() => setNewNic(null)} disabled={nicBusy}>
                Cancel
              </button>
              <ActionButton
                size="sm"
                variant="primary"
                loading={nicBusy}
                disabled={!newNic.networkId.trim()}
                onClick={submitNic}
              >
                Attach adapter
              </ActionButton>
            </div>
          </div>
        ) : null}
      </div>

      {/* Firmware & boot (display) */}
      <div className="drawer-section">
        <div className="drawer-section-head">
          <span className="drawer-section-title">
            <IconMemory size={15} /> Boot & firmware
          </span>
        </div>
        <dl className="spec-summary">
          <dt>Firmware</dt>
          <dd>{vm.firmware || "—"}</dd>
          <dt>Secure Boot</dt>
          <dd>{vm.firmware === "uefi" ? "Available (UEFI)" : "Not available (BIOS)"}</dd>
          <dt>TPM</dt>
          <dd>{vm.labels?.tpm === "true" ? "TPM 2.0 (vTPM)" : "Reported by hypervisor"}</dd>
          <dt>Guest OS</dt>
          <dd>{vm.guestOs || "—"}</dd>
          <dt>Boot order</dt>
          <dd>{disks.length ? "Disk → Network → CD/DVD" : "Network → CD/DVD"}</dd>
        </dl>
        <div className="drawer-banner info">
          <IconHelp size={15} />
          <span>Firmware, Secure Boot and TPM are set at creation and cannot be changed in place — they require recreating the VM (use the create wizard's Security step). Boot order is reported by the hypervisor.</span>
        </div>
      </div>
    </Drawer>
  );
}

/* ---- inline selectors shared across drawers ---- */

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
