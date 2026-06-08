// ui/src/views/HypervisorConnections.tsx
//
// Hypervisor connections admin: register a REAL hypervisor (KVM/libvirt, Hyper-V,
// VMware/ESXi, Xen) so UniHV connects to it and exposes its VMs as a live
// provider. List connections, add one (with a live "Test" probe before saving),
// and delete (which deregisters the live provider). Secrets are sealed
// server-side and NEVER rendered — the API only reports whether one is stored
// (hasSecret); the UI shows a "•••• set" pill.
//
// Gated by vm.create (create/delete; credential management) — viewing requires
// vm.read. The backend re-checks every call.

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useHvConnections } from "../lib/hooks";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { LoadingFill } from "../components/Spinner";
import { ActionButton } from "../components/ActionButton";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField, SelectField } from "../components/Field";
import { StatusDot } from "../components/StatusDot";
import { IconVM, IconPlus, IconTrash, IconRefresh } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type { HvConn, HvConnInput, HvConnKind, HvConnStatus } from "../lib/types";

const EMPTY: HvConn[] = [];

const KIND_OPTIONS: { value: HvConnKind; label: string }[] = [
  { value: "kvm", label: "KVM / libvirt" },
  { value: "vmware", label: "VMware / ESXi" },
  { value: "xen", label: "Xen / XenServer" },
  { value: "hyperv", label: "Microsoft Hyper-V" },
];

const KIND_LABEL: Record<string, string> = Object.fromEntries(KIND_OPTIONS.map((k) => [k.value, k.label]));

// Status pill tint: connected = green, error = red, pending = grey.
const STATUS: Record<HvConnStatus, { label: string; color: string }> = {
  connected: { label: "Connected", color: "var(--success)" },
  error: { label: "Error", color: "var(--danger)" },
  pending: { label: "Pending", color: "var(--state-stopped)" },
};

// kvm uses a libvirt URI/socket and needs no credentials; vmware/xen need an
// endpoint URL + username + password; hyperv talks to a computer name and only
// works when UniHV itself runs on Windows.
function needsCreds(kind: HvConnKind): boolean {
  return kind === "vmware" || kind === "xen";
}

function endpointHint(kind: HvConnKind): string {
  switch (kind) {
    case "kvm":
      return "A libvirt URI or socket, e.g. tcp://host:16509 or /var/run/libvirt/libvirt-sock. No credentials needed.";
    case "hyperv":
      return "A computer name to manage remotely — leave blank for the local host. Only works when UniHV runs on Windows.";
    case "vmware":
      return "The vCenter / ESXi URL, e.g. https://vcenter.example.com.";
    case "xen":
      return "The XenServer / XCP-ng pool master URL, e.g. https://xen.example.com.";
  }
}

function endpointPlaceholder(kind: HvConnKind): string {
  switch (kind) {
    case "kvm":
      return "tcp://host:16509";
    case "hyperv":
      return "(local)";
    case "vmware":
      return "https://vcenter.example.com";
    case "xen":
      return "https://xen.example.com";
  }
}

export function HypervisorConnections() {
  const queryClient = useQueryClient();
  const { can } = useAuth();
  const connectionsQ = useHvConnections();

  // Creating/deleting connections is credential management — gate on vm.create.
  const canWrite = can("vm.create");

  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<HvConn | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["hvConnections"] });

  const connections = connectionsQ.data ?? EMPTY;

  const columns: Column<HvConn>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (c) => c.name,
      cell: (c) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{c.name}</span>
          {c.username ? <span className="mono text-xs muted">{c.username}</span> : null}
        </div>
      ),
    },
    {
      key: "kind",
      header: "Type",
      sortValue: (c) => c.kind,
      cell: (c) => <span className="chip">{KIND_LABEL[c.kind] ?? c.kind}</span>,
    },
    {
      key: "endpoint",
      header: "Endpoint",
      sortValue: (c) => c.endpoint,
      cell: (c) =>
        c.endpoint ? (
          <span className="mono text-xs muted truncate" style={{ maxWidth: 320, display: "inline-block" }} title={c.endpoint}>
            {c.endpoint}
          </span>
        ) : (
          <span className="muted text-sm">{c.kind === "hyperv" ? "local" : "—"}</span>
        ),
    },
    {
      key: "status",
      header: "Status",
      sortValue: (c) => c.status,
      cell: (c) => {
        const s = STATUS[c.status] ?? STATUS.pending;
        return (
          <span
            className="pill"
            style={{ background: "transparent", color: s.color, borderColor: "transparent" }}
            title={c.lastError || s.label}
          >
            <StatusDot color={s.color} pulse={c.status === "connected"} />
            {s.label}
          </span>
        );
      },
    },
    {
      key: "secret",
      header: "Credential",
      sortValue: (c) => (c.hasSecret ? 1 : 0),
      cell: (c) =>
        c.hasSecret ? (
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
      sortValue: (c) => c.createdAt,
      cell: (c) => <span className="text-xs muted nowrap">{timeAgo(c.createdAt)}</span>,
    },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "70px",
      cell: (c) => (
        <div className="dt-actions">
          <ActionButton
            size="sm"
            variant="ghost"
            iconOnly
            disabled={!canWrite}
            tooltip={canWrite ? "Delete connection" : "Requires vm.create"}
            aria-label="Delete connection"
            onClick={() => setDeleteTarget(c)}
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
        title="Connections"
        subtitle="Register a hypervisor (KVM/libvirt, VMware/ESXi, Xen, Hyper-V) so UniHV connects to it and manages its virtual machines."
        actions={
          <div className="row">
            <ActionButton
              variant="primary"
              disabled={!canWrite}
              tooltip={canWrite ? undefined : "Requires vm.create"}
              onClick={() => setCreateOpen(true)}
            >
              <IconPlus size={15} />
              Add connection
            </ActionButton>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => connectionsQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
          </div>
        }
      />

      {connectionsQ.isLoading ? (
        <LoadingFill label="Loading connections…" />
      ) : (
        <DataTable
          columns={columns}
          rows={connections}
          rowKey={(c) => c.id}
          defaultSortKey="name"
          emptyIcon={<IconVM size={40} />}
          emptyTitle="No hypervisor connections"
          emptyMessage="Add a connection to a KVM, VMware/ESXi, Xen, or Hyper-V hypervisor to manage its virtual machines from here."
        />
      )}

      {createOpen ? <ConnectionModal onClose={() => setCreateOpen(false)} onDone={invalidate} /> : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Delete connection"
        variant="danger"
        confirmLabel="Delete"
        description={
          <>
            Delete connection <strong>{deleteTarget?.name}</strong> and its stored credential? UniHV will disconnect from this hypervisor and its
            virtual machines will no longer be manageable until it is re-added.
          </>
        }
        onConfirm={async () => {
          if (!deleteTarget) return;
          try {
            await api.vmConnectionDelete(deleteTarget.id);
            toast.success("Connection deleted", deleteTarget.name);
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

function ConnectionModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<HvConnKind>("kvm");
  const [endpoint, setEndpoint] = useState("");
  const [username, setUsername] = useState("");
  const [secret, setSecret] = useState("");
  const [insecureTls, setInsecureTls] = useState(false);
  const [enabled, setEnabled] = useState(true);

  const [busy, setBusy] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testResult, setTestResult] = useState<TestState>(null);

  const creds = needsCreds(kind);
  // hyperv targets a computer name and may be blank (local); every other kind
  // requires an endpoint. vmware/xen additionally require a username + password.
  const valid =
    name.trim().length > 0 &&
    (kind === "hyperv" || endpoint.trim().length > 0) &&
    (!creds || (username.trim().length > 0 && secret.length > 0));

  // Changing kind clears creds/test that no longer apply, so a stale result
  // isn't shown against a different target.
  const onKindChange = (k: HvConnKind) => {
    setKind(k);
    setTestResult(null);
    if (!needsCreds(k)) {
      setUsername("");
      setSecret("");
    }
  };

  const buildBody = (withEnabled: boolean): HvConnInput => ({
    name: name.trim(),
    kind,
    endpoint: endpoint.trim(),
    username: creds ? username.trim() : "",
    secret: creds ? secret : "",
    insecureTls,
    ...(withEnabled ? { enabled } : {}),
  });

  const runTest = async () => {
    if (!valid) return;
    setTesting(true);
    setTestResult(null);
    try {
      await api.vmConnectionTest(buildBody(false));
      setTestResult({ ok: true });
    } catch (err) {
      // A failed test comes back as a 422 with error.message; surface it inline
      // rather than as a toast so the operator can fix the form in place.
      setTestResult({ ok: false, message: err instanceof Error ? err.message : String(err) });
    } finally {
      setTesting(false);
    }
  };

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const created = await api.vmConnectionCreate(buildBody(true));
      toast.success("Connection added", created.name);
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
      title="Add hypervisor connection"
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
          <TextField label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} hint="A label for this hypervisor." />
          <SelectField label="Type" value={kind} onChange={(e) => onKindChange(e.target.value as HvConnKind)}>
            {KIND_OPTIONS.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </SelectField>
        </div>

        <TextField
          label={kind === "hyperv" ? "Computer name (optional)" : "Endpoint"}
          value={endpoint}
          mono
          onChange={(e) => {
            setEndpoint(e.target.value);
            setTestResult(null);
          }}
          placeholder={endpointPlaceholder(kind)}
          hint={endpointHint(kind)}
        />

        {creds ? (
          <>
            <TextField
              label="Username"
              value={username}
              onChange={(e) => {
                setUsername(e.target.value);
                setTestResult(null);
              }}
              autoComplete="off"
              placeholder={kind === "vmware" ? "administrator@vsphere.local" : "root"}
            />
            <TextField
              label="Password"
              type="password"
              value={secret}
              onChange={(e) => {
                setSecret(e.target.value);
                setTestResult(null);
              }}
              autoComplete="new-password"
              hint="Stored encrypted; never displayed again."
            />
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={insecureTls}
                onChange={(e) => {
                  setInsecureTls(e.target.checked);
                  setTestResult(null);
                }}
              />
              <span>Skip TLS certificate verification (self-signed — lab only)</span>
            </label>
          </>
        ) : null}

        <label className="checkbox-row">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled (connect and register this hypervisor on save)</span>
        </label>

        {testResult ? (
          testResult.ok ? (
            <div
              className="banner"
              role="status"
              style={{
                fontSize: "0.85em",
                background: "var(--success-bg)",
                borderColor: "var(--success)",
                color: "var(--text-primary)",
              }}
            >
              Connection test succeeded.
            </div>
          ) : (
            <div className="banner danger" role="alert" style={{ fontSize: "0.85em" }}>
              {testResult.message}
            </div>
          )
        ) : null}
      </div>
    </Modal>
  );
}
