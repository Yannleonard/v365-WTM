// ui/src/views/StorageBackends.tsx
//
// Storage backends admin: register a pluggable STORAGE BACKEND so UniHV can use
// it for VM images, ISO libraries and backups. Two families:
//   SAN/NAS — NFS (host + export), iSCSI (portal + IQN), SMB/CIFS (UNC + user/pass);
//     realized server-side as a libvirt storage pool of that type on a target host.
//   cloud object store — Azure Blob (account + container + key), AWS S3 (bucket +
//     region + access/secret); accessed via minimal stdlib REST clients.
// List backends, add one (with a live "Test" probe before saving), and delete.
// Secrets are sealed server-side and NEVER rendered — the API only reports whether
// one is stored (hasSecret); the UI shows a "•••• set" pill.
//
// Gated by storage.backend.write (create/test/delete; credential management) —
// viewing requires storage.backend.read. The backend re-checks every call.

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useStorageBackends } from "../lib/hooks";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { LoadingFill } from "../components/Spinner";
import { ActionButton } from "../components/ActionButton";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField, SelectField } from "../components/Field";
import { StatusDot } from "../components/StatusDot";
import { IconVolumes, IconPlus, IconTrash, IconRefresh } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type { StorageBackend, StorageBackendInput, StorageBackendType, StorageBackendStatus } from "../lib/types";

const EMPTY: StorageBackend[] = [];

const TYPE_OPTIONS: { value: StorageBackendType; label: string }[] = [
  { value: "nfs", label: "NFS (NAS)" },
  { value: "iscsi", label: "iSCSI (SAN)" },
  { value: "smb", label: "SMB / CIFS (NAS)" },
  { value: "azureblob", label: "Azure Blob Storage" },
  { value: "s3", label: "AWS S3 (or compatible)" },
];

const TYPE_LABEL: Record<string, string> = Object.fromEntries(TYPE_OPTIONS.map((t) => [t.value, t.label]));

const STATUS: Record<StorageBackendStatus, { label: string; color: string }> = {
  connected: { label: "Connected", color: "var(--success)" },
  error: { label: "Error", color: "var(--danger)" },
  pending: { label: "Pending", color: "var(--state-stopped)" },
};

function isCloud(t: StorageBackendType): boolean {
  return t === "azureblob" || t === "s3";
}

export function StorageBackends() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const backendsQ = useStorageBackends();

  const canWrite = can("storage.backend.write");

  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<StorageBackend | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["storageBackends"] });

  const backends = backendsQ.data ?? EMPTY;

  const columns: Column<StorageBackend>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (b) => b.name,
      cell: (b) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{b.name}</span>
          {b.username ? <span className="mono text-xs muted">{b.username}</span> : null}
        </div>
      ),
    },
    {
      key: "type",
      header: "Type",
      sortValue: (b) => b.type,
      cell: (b) => <span className="chip">{TYPE_LABEL[b.type] ?? b.type}</span>,
    },
    {
      key: "target",
      header: "Target",
      sortValue: (b) => b.target,
      cell: (b) => {
        const detail = [b.endpoint, b.target].filter(Boolean).join(" : ") || b.target || "—";
        return (
          <span className="mono text-xs muted truncate" style={{ maxWidth: 320, display: "inline-block" }} title={detail}>
            {detail}
            {b.region ? <span className="muted"> ({b.region})</span> : null}
          </span>
        );
      },
    },
    {
      key: "status",
      header: "Status",
      sortValue: (b) => b.status,
      cell: (b) => {
        const s = STATUS[b.status] ?? STATUS.pending;
        return (
          <span
            className="pill"
            style={{ background: "transparent", color: s.color, borderColor: "transparent" }}
            title={b.lastError || s.label}
          >
            <StatusDot color={s.color} pulse={b.status === "connected"} />
            {s.label}
          </span>
        );
      },
    },
    {
      key: "secret",
      header: "Credential",
      sortValue: (b) => (b.hasSecret ? 1 : 0),
      cell: (b) =>
        b.hasSecret ? (
          <span className="pill" style={{ color: "var(--success)", background: "var(--success-bg)", borderColor: "transparent" }}>
            •••• set
          </span>
        ) : (
          <span className="text-xs muted">none</span>
        ),
    },
    {
      key: "created",
      header: "Added",
      sortValue: (b) => b.createdAt,
      cell: (b) => <span className="text-xs muted nowrap">{timeAgo(b.createdAt)}</span>,
    },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "70px",
      cell: (b) => (
        <div className="dt-actions">
          <ActionButton
            size="sm"
            variant="ghost"
            iconOnly
            disabled={!canWrite}
            tooltip={canWrite ? "Delete backend" : "Requires storage.backend.write"}
            aria-label="Delete backend"
            onClick={() => setDeleteTarget(b)}
            style={canWrite ? { color: "var(--danger)" } : undefined}
          >
            <IconTrash size={15} />
          </ActionButton>
        </div>
      ),
    },
  ];

  return (
    <div className="page">
      <PageHeader
        title="Storage Backends"
        subtitle="Register SAN/NAS (NFS, iSCSI, SMB/CIFS) and cloud object storage (Azure Blob, AWS S3) for VM images, ISO libraries and backups."
        actions={
          <div className="row">
            <ActionButton
              variant="primary"
              disabled={!canWrite}
              tooltip={canWrite ? undefined : "Requires storage.backend.write"}
              onClick={() => setCreateOpen(true)}
            >
              <IconPlus size={15} />
              Add backend
            </ActionButton>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => backendsQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
          </div>
        }
      />

      {backendsQ.isLoading ? (
        <LoadingFill label="Loading storage backends…" />
      ) : (
        <DataTable
          columns={columns}
          rows={backends}
          rowKey={(b) => b.id}
          defaultSortKey="name"
          emptyIcon={<IconVolumes size={40} />}
          emptyTitle="No storage backends"
          emptyMessage="Add an NFS/iSCSI/SMB target or an Azure Blob / AWS S3 bucket to store VM images, ISOs and backups."
        />
      )}

      {createOpen ? <BackendModal onClose={() => setCreateOpen(false)} onDone={invalidate} /> : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Delete storage backend"
        variant="danger"
        confirmLabel="Delete"
        description={
          <>
            Delete storage backend <strong>{deleteTarget?.name}</strong> and its stored credential? UniHV will no longer use this target for
            images, ISOs or backups.
          </>
        }
        onConfirm={async () => {
          if (!deleteTarget) return;
          try {
            await api.storageBackendDelete(deleteTarget.id);
            toast.success("Backend deleted", deleteTarget.name);
            invalidate();
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

/* ----------------------------- create form ------------------------------ */

type TestState = { ok: true } | { ok: false; message: string } | null;

function BackendModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState<StorageBackendType>("nfs");
  // SAN/NAS fields.
  const [endpoint, setEndpoint] = useState(""); // NFS/SMB host, iSCSI portal
  const [target, setTarget] = useState(""); // NFS export, iSCSI IQN, SMB UNC, bucket/container
  const [username, setUsername] = useState("");
  const [secret, setSecret] = useState("");
  const [region, setRegion] = useState("");
  const [enabled, setEnabled] = useState(true);

  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<TestState>(null);

  const cloud = isCloud(type);

  // Per-type validity.
  const valid = (() => {
    if (name.trim().length === 0) return false;
    switch (type) {
      case "nfs":
      case "iscsi":
        return endpoint.trim().length > 0 && target.trim().length > 0;
      case "smb":
        // SMB usually needs creds, but anonymous/guest shares exist — require host+share.
        return endpoint.trim().length > 0 && target.trim().length > 0;
      case "azureblob":
        return username.trim().length > 0 && target.trim().length > 0 && secret.length > 0;
      case "s3":
        return target.trim().length > 0 && username.trim().length > 0 && secret.length > 0 && region.trim().length > 0;
    }
  })();

  const onTypeChange = (t: StorageBackendType) => {
    setType(t);
    setTestResult(null);
    setEndpoint("");
    setTarget("");
    setUsername("");
    setSecret("");
    setRegion("");
  };

  const buildBody = (withEnabled: boolean): StorageBackendInput => ({
    name: name.trim(),
    type,
    endpoint: endpoint.trim(),
    target: target.trim(),
    username: username.trim(),
    secret,
    region: region.trim(),
    providerId: "",
    options: "",
    enabled: withEnabled ? enabled : false,
  });

  const runTest = async () => {
    if (!valid) return;
    setTesting(true);
    setTestResult(null);
    try {
      await api.storageBackendTest(buildBody(false));
      setTestResult({ ok: true });
    } catch (err) {
      setTestResult({ ok: false, message: err instanceof Error ? err.message : String(err) });
    } finally {
      setTesting(false);
    }
  };

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const created = await api.storageBackendCreate(buildBody(true));
      toast.success("Backend added", created.name);
      onDone();
      onClose();
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setBusy(false);
    }
  };

  const clearTest = () => setTestResult(null);

  return (
    <Modal
      open
      title="Add storage backend"
      busy={busy}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <ActionButton variant="ghost" loading={testing} disabled={!valid || busy} onClick={runTest}>
            Test
          </ActionButton>
          <ActionButton variant="primary" loading={busy} disabled={!valid} onClick={submit}>
            Save
          </ActionButton>
        </>
      }
    >
      <div className="col" style={{ gap: "var(--sp-3)" }}>
        <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
          <TextField label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} hint="A label for this storage backend." />
          <SelectField label="Type" value={type} onChange={(e) => onTypeChange(e.target.value as StorageBackendType)}>
            {TYPE_OPTIONS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </SelectField>
        </div>

        {/* NFS: host + export */}
        {type === "nfs" ? (
          <>
            <TextField label="NFS server (host)" mono value={endpoint} onChange={(e) => { setEndpoint(e.target.value); clearTest(); }} placeholder="192.168.1.10" />
            <TextField label="Export path" mono value={target} onChange={(e) => { setTarget(e.target.value); clearTest(); }} placeholder="/export/images" hint="Defined server-side as a libvirt netfs pool. No credentials needed." />
          </>
        ) : null}

        {/* iSCSI: portal + IQN */}
        {type === "iscsi" ? (
          <>
            <TextField label="iSCSI portal (host[:port])" mono value={endpoint} onChange={(e) => { setEndpoint(e.target.value); clearTest(); }} placeholder="10.0.0.5:3260" />
            <TextField label="Target IQN" mono value={target} onChange={(e) => { setTarget(e.target.value); clearTest(); }} placeholder="iqn.2004-04.com.example:target0" hint="Defined server-side as a libvirt iscsi pool." />
          </>
        ) : null}

        {/* SMB/CIFS: UNC + user/pass */}
        {type === "smb" ? (
          <>
            <TextField label="SMB server (host)" mono value={endpoint} onChange={(e) => { setEndpoint(e.target.value); clearTest(); }} placeholder="fileserver" />
            <TextField label="Share / UNC" mono value={target} onChange={(e) => { setTarget(e.target.value); clearTest(); }} placeholder="\\fileserver\isos" />
            <TextField label="Username" value={username} onChange={(e) => { setUsername(e.target.value); clearTest(); }} autoComplete="off" placeholder="DOMAIN\\user" />
            <TextField label="Password" type="password" value={secret} onChange={(e) => { setSecret(e.target.value); clearTest(); }} autoComplete="new-password" hint="Stored encrypted; never displayed again." />
          </>
        ) : null}

        {/* Azure Blob: account + container + key */}
        {type === "azureblob" ? (
          <>
            <TextField label="Storage account" mono value={username} onChange={(e) => { setUsername(e.target.value); clearTest(); }} placeholder="mystorageacct" />
            <TextField label="Container" mono value={target} onChange={(e) => { setTarget(e.target.value); clearTest(); }} placeholder="images" />
            <TextField label="Account key" type="password" value={secret} onChange={(e) => { setSecret(e.target.value); clearTest(); }} autoComplete="new-password" hint="Base64 account key; stored encrypted, never displayed again." />
          </>
        ) : null}

        {/* S3: bucket + region + access/secret */}
        {type === "s3" ? (
          <>
            <TextField label="Bucket" mono value={target} onChange={(e) => { setTarget(e.target.value); clearTest(); }} placeholder="my-bucket" />
            <TextField label="Region" mono value={region} onChange={(e) => { setRegion(e.target.value); clearTest(); }} placeholder="us-east-1" />
            <TextField label="Access key ID" mono value={username} onChange={(e) => { setUsername(e.target.value); clearTest(); }} autoComplete="off" placeholder="AKIA…" />
            <TextField label="Secret access key" type="password" value={secret} onChange={(e) => { setSecret(e.target.value); clearTest(); }} autoComplete="new-password" hint="Stored encrypted; never displayed again." />
          </>
        ) : null}

        <label className="checkbox-row">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled (probe connectivity on save)</span>
        </label>

        {testResult ? (
          testResult.ok ? (
            <div
              className="banner"
              role="status"
              style={{ fontSize: "0.85em", background: "var(--success-bg)", borderColor: "var(--success)", color: "var(--text-primary)" }}
            >
              Connection test succeeded.
            </div>
          ) : (
            <div className="banner danger" role="alert" style={{ fontSize: "0.85em" }}>
              {testResult.message}
            </div>
          )
        ) : null}

        {cloud ? (
          <p className="text-xs muted">Used for storing/backing up images, ISOs and VM backups via a minimal REST client.</p>
        ) : null}
      </div>
    </Modal>
  );
}
