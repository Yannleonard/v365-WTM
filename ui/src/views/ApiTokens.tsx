// ui/src/views/ApiTokens.tsx
//
// Self-service scoped API tokens (personal access tokens). Any authenticated user
// manages their OWN tokens: create one with a name + a permission subset (chosen
// from the permissions they actually hold) + an optional expiry; the RAW token is
// shown EXACTLY ONCE on creation (with copy + a "store it now" warning) and is
// never retrievable again. List + revoke the rest. Reuses the existing
// PageHeader/DataTable/Modal/ConfirmDestructiveDialog/Field/ActionButton design.
//
// Tokens authenticate via "Authorization: Bearer <raw>" and carry the SCOPED
// subset of the owner's grants (a token can never exceed its owner; the backend
// re-checks). CSRF is not required on the bearer path.

import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../lib/api";
import { useAuth } from "../lib/auth";
import { PageHeader } from "../components/PageHeader";
import { DataTable, type Column } from "../components/DataTable";
import { LoadingFill } from "../components/Spinner";
import { ActionButton } from "../components/ActionButton";
import { Modal } from "../components/Modal";
import { ConfirmDestructiveDialog } from "../components/ConfirmDestructiveDialog";
import { TextField } from "../components/Field";
import { IconLock, IconPlus, IconTrash, IconRefresh, IconCopy, IconCheck } from "../components/icons";
import { toast, toastError } from "../lib/toast";
import { timeAgo } from "../lib/format";
import type { ApiTokenRecord, ApiTokenCreateResponse } from "../lib/types";

const EMPTY: ApiTokenRecord[] = [];

export function ApiTokens() {
  const queryClient = useQueryClient();
  const tokensQ = useQuery({
    queryKey: ["apiTokens"],
    queryFn: () => api.apiTokens(),
  });

  const [createOpen, setCreateOpen] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<ApiTokenRecord | null>(null);
  // The raw token is held in memory ONLY until the user dismisses the reveal modal.
  const [revealed, setRevealed] = useState<ApiTokenCreateResponse | null>(null);

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ["apiTokens"] });

  const tokens = tokensQ.data ?? EMPTY;

  const columns: Column<ApiTokenRecord>[] = [
    {
      key: "name",
      header: "Name",
      sortValue: (t) => t.name,
      cell: (t) => <span style={{ fontWeight: 600 }}>{t.name}</span>,
    },
    {
      key: "scopes",
      header: "Scopes",
      cell: (t) =>
        t.permissions.length === 0 ? (
          <span className="muted text-sm">—</span>
        ) : (
          <div className="row" style={{ flexWrap: "wrap", gap: 4 }}>
            {t.permissions.slice(0, 4).map((p) => (
              <span key={p} className="chip mono text-xs">
                {p}
              </span>
            ))}
            {t.permissions.length > 4 ? <span className="text-xs muted">+{t.permissions.length - 4}</span> : null}
          </div>
        ),
    },
    {
      key: "expires",
      header: "Expires",
      sortValue: (t) => t.expiresAt ?? Number.MAX_SAFE_INTEGER,
      cell: (t) =>
        t.expiresAt ? (
          <span className="text-xs nowrap">{timeAgo(t.expiresAt)}</span>
        ) : (
          <span className="text-xs muted">never</span>
        ),
    },
    {
      key: "lastUsed",
      header: "Last used",
      sortValue: (t) => t.lastUsedAt ?? 0,
      cell: (t) =>
        t.lastUsedAt ? <span className="text-xs muted nowrap">{timeAgo(t.lastUsedAt)}</span> : <span className="text-xs muted">never</span>,
    },
    {
      key: "created",
      header: "Created",
      sortValue: (t) => t.createdAt,
      cell: (t) => <span className="text-xs muted nowrap">{timeAgo(t.createdAt)}</span>,
    },
    {
      key: "actions",
      header: "",
      align: "right",
      width: "60px",
      cell: (t) => (
        <div className="dt-actions">
          <ActionButton
            size="sm"
            variant="ghost"
            iconOnly
            tooltip="Revoke token"
            aria-label="Revoke token"
            onClick={() => setDeleteTarget(t)}
            style={{ color: "var(--danger)" }}
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
        title="API Tokens"
        subtitle="Personal access tokens for the REST API. Each token carries a scoped subset of your own permissions and is shown only once."
        actions={
          <div className="row">
            <ActionButton variant="primary" onClick={() => setCreateOpen(true)}>
              <IconPlus size={15} />
              New token
            </ActionButton>
            <ActionButton variant="ghost" iconOnly tooltip="Refresh" aria-label="Refresh" onClick={() => tokensQ.refetch()}>
              <IconRefresh size={16} />
            </ActionButton>
          </div>
        }
      />

      {tokensQ.isLoading ? (
        <LoadingFill label="Loading tokens…" />
      ) : (
        <DataTable
          columns={columns}
          rows={tokens}
          rowKey={(t) => t.id}
          defaultSortKey="created"
          emptyIcon={<IconLock size={40} />}
          emptyTitle="No API tokens"
          emptyMessage="Create a token to call the API without a browser session."
        />
      )}

      {createOpen ? (
        <CreateTokenModal
          onClose={() => setCreateOpen(false)}
          onCreated={(res) => {
            setCreateOpen(false);
            setRevealed(res);
            invalidate();
          }}
        />
      ) : null}

      {revealed ? <RevealTokenModal token={revealed} onClose={() => setRevealed(null)} /> : null}

      <ConfirmDestructiveDialog
        open={!!deleteTarget}
        title="Revoke token"
        variant="danger"
        confirmLabel="Revoke"
        description={
          <>
            Revoke token <strong>{deleteTarget?.name}</strong>? Any client using it will immediately lose access. This cannot be undone.
          </>
        }
        onConfirm={async () => {
          if (!deleteTarget) return;
          try {
            await api.apiTokenDelete(deleteTarget.id);
            toast.success("Token revoked", deleteTarget.name);
            invalidate();
          } catch (err) {
            toastError("Revoke failed", err);
            throw err;
          }
        }}
        onClose={() => setDeleteTarget(null)}
      />
    </div>
  );
}

function CreateTokenModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (res: ApiTokenCreateResponse) => void;
}) {
  const { permissions } = useAuth();
  const [name, setName] = useState("");
  const [expiresInDays, setExpiresInDays] = useState("");
  const [scopes, setScopes] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);

  // A token can grant AT MOST what the owner holds. A "*" owner gets the full
  // catalog to choose from; otherwise they choose among their own grants.
  const isSuper = permissions?.includes("*");
  const catalogQ = useQuery({
    queryKey: ["permissions-catalog"],
    queryFn: () => api.permissions(),
    enabled: !!isSuper,
  });
  const available = useMemo(() => {
    const list = isSuper ? (catalogQ.data ?? []) : (permissions ?? []);
    // Hide the bare "*" from the picker (a token is meant to be scoped); a user
    // who truly wants a full token can still select every concrete scope.
    return [...new Set(list)].filter((p) => p !== "*").sort();
  }, [isSuper, catalogQ.data, permissions]);

  const toggle = (p: string) =>
    setScopes((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  const valid = name.trim().length > 0 && scopes.size > 0;

  const submit = async () => {
    if (!valid) return;
    setBusy(true);
    try {
      const days = parseInt(expiresInDays, 10);
      const res = await api.apiTokenCreate({
        name: name.trim(),
        scopes: [...scopes],
        expiresInDays: Number.isFinite(days) && days > 0 ? days : undefined,
      });
      toast.success("Token created", name.trim());
      onCreated(res);
    } catch (err) {
      toastError("Create failed", err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="New API token"
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
        <TextField label="Name" autoFocus value={name} onChange={(e) => setName(e.target.value)} hint="A label to recognize this token (e.g. ci-pipeline)." />
        <TextField
          label="Expires in (days)"
          type="number"
          value={expiresInDays}
          onChange={(e) => setExpiresInDays(e.target.value)}
          placeholder="leave blank for no expiry"
          hint="Optional. After this many days the token stops authenticating."
        />
        <div className="col" style={{ gap: "var(--sp-2)" }}>
          <div className="row" style={{ justifyContent: "space-between" }}>
            <span className="text-sm" style={{ fontWeight: 600, color: "var(--text-secondary)" }}>
              Scopes ({scopes.size} selected)
            </span>
            <span className="text-xs muted">A token can only do what you can.</span>
          </div>
          {available.length === 0 ? (
            <span className="text-sm muted">You hold no grantable permissions.</span>
          ) : (
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "repeat(auto-fill, minmax(220px, 1fr))",
                gap: "var(--sp-1)",
                maxHeight: 320,
                overflowY: "auto",
              }}
            >
              {available.map((p) => (
                <label key={p} className="checkbox-row" style={{ padding: "4px 6px", borderRadius: "var(--radius-sm)", cursor: "pointer" }}>
                  <input type="checkbox" checked={scopes.has(p)} onChange={() => toggle(p)} />
                  <span className="mono text-xs">{p}</span>
                </label>
              ))}
            </div>
          )}
        </div>
      </div>
    </Modal>
  );
}

function RevealTokenModal({ token, onClose }: { token: ApiTokenCreateResponse; onClose: () => void }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(token.token);
      setCopied(true);
      toast.success("Copied to clipboard");
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      toastError("Copy failed", err);
    }
  };

  return (
    <Modal
      open
      title="Copy your new token"
      onClose={onClose}
      footer={
        <ActionButton variant="primary" onClick={onClose}>
          Done
        </ActionButton>
      }
    >
      <div className="col" style={{ gap: "var(--sp-3)" }}>
        <div className="banner warning">
          <span>
            This is the only time the token <strong>{token.name}</strong> is shown. Copy it now and store it securely — it cannot be retrieved again.
          </span>
        </div>
        <div className="row" style={{ gap: "var(--sp-2)", alignItems: "stretch" }}>
          <input className="input mono text-sm" readOnly value={token.token} style={{ flex: 1 }} onFocus={(e) => e.currentTarget.select()} />
          <ActionButton variant="ghost" onClick={copy} tooltip="Copy token">
            {copied ? <IconCheck size={15} /> : <IconCopy size={15} />}
            {copied ? "Copied" : "Copy"}
          </ActionButton>
        </div>
        <div className="text-xs muted col" style={{ gap: 4 }}>
          <span>Use it as a bearer token:</span>
          <code className="mono text-xs">curl -H "Authorization: Bearer {token.token}" /api/v1/vm/providers</code>
        </div>
      </div>
    </Modal>
  );
}
