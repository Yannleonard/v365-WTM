// ui/src/views/VMStorage.tsx
//
// Storage / ISO library per hypervisor provider. Top: storage pools (capacity /
// free) for the selected provider, selectable. Bottom: the selected pool's
// volumes (DataTable: name, ISO badge, capacity, alloc) with "Create volume",
// "Upload ISO" (raw-body upload with a progress bar) and per-row delete. Writes
// are greyed-out-before-click on the provider's "storage_write" capability + the
// vm.storage.write permission (gateVMStorageWrite). Mirrors the container
// Volumes.tsx layout, scoped to a VM provider + a chosen pool.

import { useEffect, useMemo, useRef, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useVMStorage, useVMVolumes, useVMCapabilityLookup } from "../lib/hooks";
import { gateVMStorageWrite } from "../lib/rbac";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { ActionButton } from "../components/ActionButton";
import { CapabilityGate } from "../components/CapabilityGate";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField, SelectField } from "../components/Field";
import { IconVolumes, IconPlus, IconTrash, IconRefresh, IconDownload, IconSearch } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { formatBytes, shortId } from "../lib/format";
import type { VMStorage as VMStoragePool, Volume } from "../lib/types";

const EMPTY_POOLS: VMStoragePool[] = [];
const EMPTY_VOLS: Volume[] = [];

const FORMAT_OPTIONS = ["qcow2", "raw", "vmdk", "vhdx", "vdi"] as const;

export function VMStorage() {
  const queryClient = useQueryClient();
  const { permissions } = useAuth();
  const { providers } = useVMCapabilityLookup();

  const [provider, setProvider] = useState("");
  useEffect(() => {
    if (!provider && providers.length > 0) setProvider(providers[0]!.id);
  }, [providers, provider]);

  const caps = providers.find((p) => p.id === provider)?.capabilities;
  const writeGate = gateVMStorageWrite(caps, permissions);

  const poolsQ = useVMStorage(provider, !!provider);
  const pools = poolsQ.data ?? EMPTY_POOLS;

  const [poolId, setPoolId] = useState("");
  // Keep a valid pool selected as the provider/pool list changes.
  useEffect(() => {
    if (pools.length === 0) {
      if (poolId) setPoolId("");
      return;
    }
    if (!pools.some((p) => p.id === poolId)) setPoolId(pools[0]!.id);
  }, [pools, poolId]);

  const volsQ = useVMVolumes(provider, poolId, !!provider && !!poolId);
  const volumes = volsQ.data ?? EMPTY_VOLS;

  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [uploadOpen, setUploadOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<Volume | null>(null);

  const invalidateVols = () => queryClient.invalidateQueries({ queryKey: ["vm", "volumes", provider, poolId] });

  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    if (!s) return volumes;
    return volumes.filter((v) => `${v.name} ${v.format ?? ""} ${v.id}`.toLowerCase().includes(s));
  }, [volumes, search]);

  const columns: Column<Volume>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (v) => v.name,
      cell: (v) => (
        <div className="col" style={{ gap: 2 }}>
          <div className="row" style={{ gap: "var(--sp-2)" }}>
            <span className="mono" style={{ fontWeight: 600 }}>
              {v.name}
            </span>
            {v.isIso ? (
              <span className="pill" style={{ color: "var(--accent)", borderColor: "var(--accent)", background: "transparent" }}>
                ISO
              </span>
            ) : null}
          </div>
          <span className="mono text-xs muted">{shortId(v.id)}</span>
        </div>
      ),
    },
    {
      key: "format",
      header: "Format",
      sortValue: (v) => v.format ?? "",
      cell: (v) => (v.format ? <span className="chip">{v.format}</span> : <span className="muted">—</span>),
    },
    {
      key: "capacity",
      header: "Capacity",
      align: "right",
      sortValue: (v) => v.capacityGb,
      cell: (v) => <span className="mono text-xs nowrap">{formatBytes(v.capacityGb * 1024 ** 3, 0)}</span>,
    },
    {
      key: "alloc",
      header: "Allocated",
      align: "right",
      sortValue: (v) => v.allocGb ?? 0,
      cell: (v) =>
        v.allocGb !== undefined && v.allocGb !== null ? (
          <span className="mono text-xs nowrap">{formatBytes(v.allocGb * 1024 ** 3, 1)}</span>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      key: "path",
      header: "Path",
      sortValue: (v) => v.path ?? "",
      cell: (v) =>
        v.path ? (
          <span className="mono text-xs muted truncate" style={{ maxWidth: 280, display: "inline-block" }} title={v.path}>
            {v.path}
          </span>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "60px",
      cell: (v) => (
        <CapabilityGate gate={writeGate}>
          {(allowed, reason) => (
            <ActionButton
              size="sm"
              iconOnly
              variant="ghost"
              disabled={!allowed}
              tooltip={allowed ? "Delete volume" : reason}
              aria-label="Delete volume"
              onClick={() => setDeleteTarget(v)}
              style={allowed ? { color: "var(--danger)" } : undefined}
            >
              <IconTrash size={15} />
            </ActionButton>
          )}
        </CapabilityGate>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="VM Storage"
        subtitle="Storage pools, virtual disks, and the ISO library on a hypervisor provider."
        actions={
          <div className="row">
            <CapabilityGate gate={writeGate}>
              {(allowed, reason) => (
                <>
                  <ActionButton
                    variant="ghost"
                    disabled={!allowed || !poolId}
                    tooltip={!poolId ? "Select a pool" : allowed ? undefined : reason}
                    onClick={() => setUploadOpen(true)}
                  >
                    <IconDownload size={15} />
                    Upload ISO
                  </ActionButton>
                  <ActionButton
                    variant="primary"
                    disabled={!allowed || !poolId}
                    tooltip={!poolId ? "Select a pool" : allowed ? undefined : reason}
                    onClick={() => setCreateOpen(true)}
                  >
                    <IconPlus size={15} />
                    Create volume
                  </ActionButton>
                </>
              )}
            </CapabilityGate>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => { poolsQ.refetch(); volsQ.refetch(); }}>
              <IconRefresh size={16} />
            </ActionButton>
          </div>
        }
      />

      <div className="card card-pad">
        <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
          <SelectField label="Provider" value={provider} onChange={(e) => setProvider(e.target.value)}>
            {providers.length === 0 ? <option value="">No providers</option> : null}
            {providers.map((p) => (
              <option key={p.id} value={p.id}>
                {p.id} ({p.kind})
              </option>
            ))}
          </SelectField>
        </div>
      </div>

      {/* Storage pools */}
      {!provider ? (
        <div className="card card-pad">
          <EmptyState icon={<IconVolumes size={40} />} title="No hypervisor provider" message="Connect a hypervisor to browse its storage." />
        </div>
      ) : poolsQ.isLoading ? (
        <LoadingFill label="Loading storage pools…" />
      ) : pools.length === 0 ? (
        <div className="card card-pad">
          <EmptyState icon={<IconVolumes size={40} />} title="No storage pools" message="This hypervisor exposes no storage pools." />
        </div>
      ) : (
        <div className="card">
          <div className="card-header">
            <span className="card-title">Storage pools</span>
            <span className="text-xs muted">{pools.length}</span>
          </div>
          <div className="card-body" style={{ padding: 0 }}>
            <table className="dt">
              <thead>
                <tr>
                  <th></th>
                  <th>Name</th>
                  <th>Type</th>
                  <th style={{ textAlign: "right" }}>Capacity</th>
                  <th style={{ textAlign: "right" }}>Free</th>
                  <th>Accessible</th>
                </tr>
              </thead>
              <tbody>
                {pools.map((p) => (
                  <tr key={p.id} style={p.id === poolId ? { background: "var(--bg-inset)" } : undefined}>
                    <td style={{ width: 36 }}>
                      <input
                        type="radio"
                        name="pool"
                        checked={p.id === poolId}
                        onChange={() => setPoolId(p.id)}
                        aria-label={`Select pool ${p.name}`}
                      />
                    </td>
                    <td>
                      <span style={{ fontWeight: 600 }}>{p.name}</span>
                    </td>
                    <td>{p.type ? <span className="chip">{p.type}</span> : <span className="muted">—</span>}</td>
                    <td className="mono text-xs nowrap" style={{ textAlign: "right" }}>
                      {p.capacityBytes ? formatBytes(p.capacityBytes, 0) : "—"}
                    </td>
                    <td className="mono text-xs nowrap" style={{ textAlign: "right" }}>
                      {p.freeBytes !== undefined ? formatBytes(p.freeBytes, 0) : "—"}
                    </td>
                    <td>
                      <span className="text-xs">{p.accessible === false ? "No" : "Yes"}</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* Volumes of the selected pool */}
      {provider && poolId ? (
        <div className="card">
          <div className="card-header">
            <span className="card-title">Volumes</span>
            <div className="row" style={{ gap: "var(--sp-2)" }}>
              <span className="muted">
                <IconSearch size={15} />
              </span>
              <input
                className="input"
                placeholder="Search volumes…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                style={{ maxWidth: 240 }}
              />
              <span className="text-sm muted">
                {filtered.length} of {volumes.length}
              </span>
            </div>
          </div>
          <div className="card-body" style={{ padding: 0 }}>
            {volsQ.isLoading ? (
              <LoadingFill label="Loading volumes…" />
            ) : (
              <DataTable
                columns={columns}
                rows={filtered}
                rowKey={(v) => v.id}
                defaultSortKey="name"
                emptyIcon={<IconVolumes size={36} />}
                emptyTitle="No volumes"
                emptyMessage="Create a virtual disk or upload an ISO into this pool."
              />
            )}
          </div>
        </div>
      ) : null}

      {createOpen ? (
        <CreateVolumeModal pid={provider} storageId={poolId} onClose={() => setCreateOpen(false)} onDone={invalidateVols} />
      ) : null}

      {uploadOpen ? (
        <UploadIsoModal pid={provider} storageId={poolId} onClose={() => setUploadOpen(false)} onDone={invalidateVols} />
      ) : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Delete volume"
        variant="danger"
        confirmLabel="Delete"
        description={
          <>
            Delete volume <strong className="mono">{deleteTarget?.name}</strong>? Its data will be lost permanently.
          </>
        }
        onConfirm={async () => {
          if (!deleteTarget) return;
          try {
            await api.vmVolumeDelete(provider, poolId, deleteTarget.id);
            toast.success("Volume deleted", deleteTarget.name);
            invalidateVols();
          } catch (err) {
            toastError("Delete failed", err);
            throw err;
          }
        }}
        onClose={() => setDeleteTarget(null)}
      />
    </div>
  );
}

/* ----------------------------- create volume ---------------------------- */

function CreateVolumeModal({
  pid,
  storageId,
  onClose,
  onDone,
}: {
  pid: string;
  storageId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [name, setName] = useState("");
  const [capacityGb, setCapacityGb] = useState("10");
  const [format, setFormat] = useState<string>("qcow2");
  const [busy, setBusy] = useState(false);

  const cap = Number(capacityGb);
  const valid = name.trim().length > 0 && Number.isFinite(cap) && cap > 0;

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.vmVolumeCreate(pid, storageId, { name: name.trim(), capacityGb: cap, format });
      toast.success("Volume creation requested", name.trim());
      onDone();
      onClose();
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="Create volume"
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Create
          </ActionButton>
        </>
      }
    >
      <div className="col" style={{ gap: "var(--sp-3)" }}>
        <TextField label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="data-disk" />
        <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
          <TextField
            label="Capacity (GB)"
            type="number"
            min={1}
            value={capacityGb}
            onChange={(e) => setCapacityGb(e.target.value)}
            style={{ maxWidth: 160 }}
          />
          <SelectField label="Format" value={format} onChange={(e) => setFormat(e.target.value)}>
            {FORMAT_OPTIONS.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </SelectField>
        </div>
      </div>
    </Modal>
  );
}

/* ------------------------------ upload ISO ------------------------------ */

function UploadIsoModal({
  pid,
  storageId,
  onClose,
  onDone,
}: {
  pid: string;
  storageId: string;
  onClose: () => void;
  onDone: () => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<number | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const valid = !!file && name.trim().length > 0;

  const onPick = (f: File | null) => {
    setFile(f);
    if (f && !name.trim()) setName(f.name);
  };

  const submit = async () => {
    if (!valid || !file) return;
    setBusy(true);
    setProgress(0);
    try {
      await api.vmIsoUpload(pid, storageId, name.trim(), file, (frac) => setProgress(frac));
      toast.success("ISO uploaded", name.trim());
      onDone();
      onClose();
    } catch (err) {
      toastError("Upload failed", err);
    } finally {
      setBusy(false);
      setProgress(null);
    }
  };

  const pct = progress !== null ? Math.round(progress * 100) : null;

  return (
    <Modal
      open
      title="Upload ISO"
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Upload
          </ActionButton>
        </>
      }
    >
      <div className="col" style={{ gap: "var(--sp-3)" }}>
        <div className="text-sm secondary">Upload an ISO image into this pool's library. It can then be used as a boot medium in the VM wizard.</div>
        <div className="field">
          <span className="field-label">ISO file</span>
          <input
            ref={inputRef}
            type="file"
            accept=".iso,application/x-iso9660-image,application/octet-stream"
            disabled={busy}
            onChange={(e) => onPick(e.target.files?.[0] ?? null)}
          />
          {file ? <span className="field-hint">{file.name} — {formatBytes(file.size, 1)}</span> : null}
        </div>
        <TextField
          label="Name"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={busy}
          placeholder="ubuntu-24.04.iso"
          hint="The name the ISO is stored under in the library."
        />
        {pct !== null ? (
          <div className="col" style={{ gap: "var(--sp-1)" }}>
            <div
              style={{
                height: 8,
                borderRadius: 4,
                background: "var(--bg-inset)",
                border: "1px solid var(--border)",
                overflow: "hidden",
              }}
            >
              <div style={{ height: "100%", width: `${pct}%`, background: "var(--accent)", transition: "width 0.15s" }} />
            </div>
            <span className="text-xs muted">{pct}% uploaded</span>
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
