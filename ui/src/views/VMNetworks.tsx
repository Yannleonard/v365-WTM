// ui/src/views/VMNetworks.tsx
//
// Virtual networks per hypervisor provider: a DataTable of the provider's
// networks (name, type, vlan) with a "Create network" modal and per-row delete.
// Create/delete are greyed-out-before-click on the provider's "network_write"
// capability + the vm.network.write permission (gateVMNetworkWrite). Mirrors the
// container Networks.tsx layout, but the resource lives under a VM provider
// (selected from a dropdown) rather than a container host.

import { useEffect, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { useVMNetworks, useVMCapabilityLookup } from "../lib/hooks";
import { gateVMNetworkWrite } from "../lib/rbac";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { LoadingFill } from "../components/Spinner";
import { EmptyState } from "../components/EmptyState";
import { ActionButton } from "../components/ActionButton";
import { CapabilityGate } from "../components/CapabilityGate";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField, SelectField } from "../components/Field";
import { IconNetworks, IconPlus, IconTrash, IconRefresh, IconSearch } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { shortId } from "../lib/format";
import type { VMNetwork, VMNetworkType } from "../lib/types";

const EMPTY: VMNetwork[] = [];

const TYPE_OPTIONS: { value: VMNetworkType; label: string }[] = [
  { value: "bridge", label: "Bridge" },
  { value: "nat", label: "NAT" },
  { value: "vlan", label: "VLAN" },
  { value: "isolated", label: "Isolated" },
];

export function VMNetworks() {
  const queryClient = useQueryClient();
  const { permissions } = useAuth();
  const { providers } = useVMCapabilityLookup();

  const [provider, setProvider] = useState("");
  // Default the provider selection to the first one once providers load.
  useEffect(() => {
    if (!provider && providers.length > 0) setProvider(providers[0]!.id);
  }, [providers, provider]);

  const caps = providers.find((p) => p.id === provider)?.capabilities;
  const writeGate = gateVMNetworkWrite(caps, permissions);

  const query = useVMNetworks(provider, !!provider);
  const networks = query.data ?? EMPTY;

  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<VMNetwork | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["vm", "networks", provider] });

  const filtered = useMemo(() => {
    const s = search.trim().toLowerCase();
    if (!s) return networks;
    return networks.filter((n) => `${n.name} ${n.type ?? ""} ${n.id}`.toLowerCase().includes(s));
  }, [networks, search]);

  const columns: Column<VMNetwork>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (n) => n.name,
      cell: (n) => (
        <div className="col" style={{ gap: 2 }}>
          <span style={{ fontWeight: 600 }}>{n.name}</span>
          <span className="mono text-xs muted">{shortId(n.id)}</span>
        </div>
      ),
    },
    {
      key: "type",
      header: "Type",
      sortValue: (n) => n.type ?? "",
      cell: (n) => (n.type ? <span className="chip">{n.type}</span> : <span className="muted">—</span>),
    },
    {
      key: "vlan",
      header: "VLAN",
      align: "right",
      sortValue: (n) => n.vlanId ?? 0,
      cell: (n) =>
        n.vlanId !== undefined && n.vlanId !== null ? (
          <span className="mono text-xs">{n.vlanId}</span>
        ) : (
          <span className="muted">—</span>
        ),
    },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "60px",
      cell: (n) => (
        <CapabilityGate gate={writeGate}>
          {(allowed, reason) => (
            <ActionButton
              size="sm"
              iconOnly
              variant="ghost"
              disabled={!allowed}
              tooltip={allowed ? "Remove network" : reason}
              aria-label="Remove network"
              onClick={() => setDeleteTarget(n)}
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
        title="VM Networks"
        subtitle="Virtual networks and port-groups on a hypervisor provider."
        actions={
          <div className="row">
            <CapabilityGate gate={writeGate}>
              {(allowed, reason) => (
                <ActionButton
                  variant="primary"
                  disabled={!allowed || !provider}
                  tooltip={!provider ? "Select a provider" : allowed ? undefined : reason}
                  onClick={() => setCreateOpen(true)}
                >
                  <IconPlus size={15} />
                  Create network
                </ActionButton>
              )}
            </CapabilityGate>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => query.refetch()}>
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
          <div className="row" style={{ flex: "1 1 240px", minWidth: 200, alignSelf: "flex-end" }}>
            <span className="muted">
              <IconSearch size={16} />
            </span>
            <input className="input" placeholder="Search networks…" value={search} onChange={(e) => setSearch(e.target.value)} />
          </div>
          <span className="spacer" />
          <span className="text-sm muted" style={{ alignSelf: "flex-end" }}>
            {filtered.length} of {networks.length}
          </span>
        </div>
      </div>

      {!provider ? (
        <div className="card card-pad">
          <EmptyState icon={<IconNetworks size={40} />} title="No hypervisor provider" message="Connect a hypervisor to manage its virtual networks." />
        </div>
      ) : query.isLoading ? (
        <LoadingFill label="Loading networks…" />
      ) : (
        <DataTable
          columns={columns}
          rows={filtered}
          rowKey={(n) => n.id}
          defaultSortKey="name"
          emptyIcon={<IconNetworks size={40} />}
          emptyTitle="No virtual networks"
          emptyMessage="Create a network to attach virtual machines to it."
        />
      )}

      {createOpen ? (
        <CreateNetworkModal
          pid={provider}
          onClose={() => setCreateOpen(false)}
          onDone={invalidate}
        />
      ) : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Remove virtual network"
        variant="danger"
        confirmLabel="Remove"
        description={
          <>
            Remove network <strong className="mono">{deleteTarget?.name}</strong>? Virtual machines attached to it will lose
            connectivity.
          </>
        }
        onConfirm={async () => {
          if (!deleteTarget) return;
          try {
            await api.vmNetworkDelete(provider, deleteTarget.id);
            toast.success("Network removed", deleteTarget.name);
            invalidate();
          } catch (err) {
            toastError("Remove failed", err);
            throw err;
          }
        }}
        onClose={() => setDeleteTarget(null)}
      />
    </div>
  );
}

/* ----------------------------- create form ------------------------------ */

function CreateNetworkModal({ pid, onClose, onDone }: { pid: string; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [type, setType] = useState<VMNetworkType>("bridge");
  const [bridge, setBridge] = useState("");
  const [vlan, setVlan] = useState("");
  const [cidr, setCidr] = useState("");
  const [busy, setBusy] = useState(false);

  const needsBridge = type === "bridge";
  const needsVlan = type === "vlan";
  const needsCidr = type === "nat" || type === "isolated";

  const vlanNum = Number(vlan);
  const valid =
    name.trim().length > 0 &&
    (!needsVlan || (vlan.trim() !== "" && Number.isInteger(vlanNum) && vlanNum >= 0 && vlanNum <= 4094));

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      await api.vmNetworkCreate(pid, {
        name: name.trim(),
        type,
        bridge: needsBridge && bridge.trim() ? bridge.trim() : undefined,
        vlan: needsVlan ? vlanNum : undefined,
        cidr: needsCidr && cidr.trim() ? cidr.trim() : undefined,
      });
      toast.success("Network creation requested", name.trim());
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
      title="Create virtual network"
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
        <div className="row-wrap" style={{ gap: "var(--sp-3)" }}>
          <TextField label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} style={{ minWidth: 200 }} />
          <SelectField label="Type" value={type} onChange={(e) => setType(e.target.value as VMNetworkType)}>
            {TYPE_OPTIONS.map((t) => (
              <option key={t.value} value={t.value}>
                {t.label}
              </option>
            ))}
          </SelectField>
        </div>

        {needsBridge ? (
          <TextField
            label="Bridge (optional)"
            mono
            placeholder="br0"
            value={bridge}
            onChange={(e) => setBridge(e.target.value)}
            hint="Host bridge interface to attach to. Leave blank to let the hypervisor create one."
          />
        ) : null}

        {needsVlan ? (
          <TextField
            label="VLAN ID"
            type="number"
            min={0}
            max={4094}
            value={vlan}
            onChange={(e) => setVlan(e.target.value)}
            error={vlan.trim() !== "" && !valid ? "VLAN must be 0–4094." : undefined}
            hint="802.1Q VLAN tag for this port-group."
          />
        ) : null}

        {needsCidr ? (
          <TextField
            label="CIDR (optional)"
            mono
            placeholder="10.0.0.0/24"
            value={cidr}
            onChange={(e) => setCidr(e.target.value)}
            hint="Subnet for the NAT/isolated network's DHCP range."
          />
        ) : null}
      </div>
    </Modal>
  );
}
