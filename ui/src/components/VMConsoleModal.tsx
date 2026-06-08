// ui/src/components/VMConsoleModal.tsx
//
// Graphical console viewer for a VM. On open it GETs the one-shot console
// endpoint (GET /vm/providers/{pid}/vms/{vmId}/console) and renders:
//
//   - kind "vnc"/"spice": a connection-details panel — kind, host:port, the
//     one-shot password (revealable) — each with a copy button, plus guidance to
//     connect with a VNC/SPICE client. (A websocket VNC bridge isn't wired in
//     this build, so we do NOT add @novnc/novnc; the details panel is the honest,
//     build-green path. If a bridge `path` is present we surface it too.)
//   - kind "rdp": host:port + a "Download .rdp" button that generates a minimal
//     .rdp file client-side for VMConnect / mstsc / any RDP client.
//
// The returned password (when present) is single-use, so we fetch lazily on open
// and never cache it in a query.

import { useEffect, useState } from "react";
import { api } from "../lib/api";
import { Modal } from "./Modal";
import { ActionButton } from "./ActionButton";
import { Spinner } from "./Spinner";
import { IconCopy, IconCheck, IconDownload, IconAlert } from "./icons";
import { toast, toastError } from "../lib/toast";
import type { ConsoleEndpoint } from "../lib/types";

interface Props {
  pid: string;
  vmId: string;
  vmName: string;
  onClose: () => void;
}

export function VMConsoleModal({ pid, vmId, vmName, onClose }: Props) {
  const [endpoint, setEndpoint] = useState<ConsoleEndpoint | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    api
      .vmConsole(pid, vmId)
      .then((ep) => {
        if (!cancelled) setEndpoint(ep);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [pid, vmId]);

  return (
    <Modal
      open
      wide
      title={
        <span className="row" style={{ gap: "var(--sp-2)" }}>
          Console — <span className="mono">{vmName}</span>
        </span>
      }
      onClose={onClose}
      footer={
        <button className="btn" onClick={onClose}>
          Close
        </button>
      }
    >
      {loading ? (
        <div className="row" style={{ gap: "var(--sp-2)", padding: "var(--sp-4)" }}>
          <Spinner />
          <span className="text-sm muted">Requesting console endpoint…</span>
        </div>
      ) : error ? (
        <div className="banner danger" role="alert" style={{ display: "flex", gap: "var(--sp-2)", alignItems: "flex-start" }}>
          <IconAlert size={16} />
          <span>{error}</span>
        </div>
      ) : endpoint ? (
        <ConsoleDetails endpoint={endpoint} vmName={vmName} />
      ) : null}
    </Modal>
  );
}

/* --------------------------- details + actions --------------------------- */

function ConsoleDetails({ endpoint, vmName }: { endpoint: ConsoleEndpoint; vmName: string }) {
  const isRdp = endpoint.kind === "rdp";
  const hostPort = `${endpoint.host}:${endpoint.port}`;

  return (
    <div className="col" style={{ gap: "var(--sp-4)" }}>
      <div className="text-sm secondary">
        {isRdp ? (
          <>Connect with an RDP client (mstsc / VMConnect for Hyper-V). Download a ready-made .rdp file below.</>
        ) : (
          <>
            Connect with a {endpoint.kind.toUpperCase()} client (e.g. TigerVNC, Remmina, virt-viewer). The password below is
            one-shot — it is valid for this session only.
          </>
        )}
      </div>

      <div className="card-pad col" style={{ gap: "var(--sp-2)", border: "1px solid var(--border)", borderRadius: "var(--radius-sm, 8px)" }}>
        <CopyRow label="Protocol" value={endpoint.kind} mono />
        <CopyRow label="Host" value={endpoint.host} mono />
        <CopyRow label="Port" value={String(endpoint.port)} mono />
        <CopyRow label="Host:Port" value={hostPort} mono />
        {endpoint.tlsPort ? <CopyRow label="TLS port" value={String(endpoint.tlsPort)} mono /> : null}
        {endpoint.path ? <CopyRow label="Path" value={endpoint.path} mono /> : null}
        {endpoint.password ? <PasswordRow password={endpoint.password} /> : null}
      </div>

      {isRdp ? (
        <div className="row">
          <ActionButton variant="primary" onClick={() => downloadRdp(endpoint, vmName)}>
            <IconDownload size={15} />
            Download .rdp
          </ActionButton>
        </div>
      ) : (
        <div className="banner info" role="status" style={{ display: "flex", gap: "var(--sp-2)", alignItems: "flex-start" }}>
          <span>
            A browser-embedded VNC/SPICE viewer requires a websocket bridge, which is not enabled in this build. Use a native
            client with the details above. For Hyper-V guests use VMConnect; for RDP use mstsc.
          </span>
        </div>
      )}
    </div>
  );
}

function CopyRow({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
      <span className="field-label" style={{ margin: 0, minWidth: 96 }}>
        {label}
      </span>
      <span className={mono ? "mono text-sm" : "text-sm"} style={{ flex: 1 }}>
        {value}
      </span>
      <CopyButton text={value} label={label} />
    </div>
  );
}

function PasswordRow({ password }: { password: string }) {
  const [show, setShow] = useState(false);
  return (
    <div className="row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
      <span className="field-label" style={{ margin: 0, minWidth: 96 }}>
        Password
      </span>
      <span className="mono text-sm" style={{ flex: 1 }}>
        {show ? password : "•".repeat(Math.min(password.length, 12))}
      </span>
      <button className="btn btn-ghost btn-sm" onClick={() => setShow((s) => !s)}>
        {show ? "Hide" : "Reveal"}
      </button>
      <CopyButton text={password} label="Password" />
    </div>
  );
}

function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      toast.success("Copied", label);
      setTimeout(() => setCopied(false), 1500);
    } catch (err) {
      toastError("Copy failed", err);
    }
  };
  return (
    <ActionButton size="sm" iconOnly variant="ghost" tooltip={`Copy ${label.toLowerCase()}`} aria-label={`Copy ${label}`} onClick={copy}>
      {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
    </ActionButton>
  );
}

// downloadRdp generates a minimal .rdp file client-side and triggers a download.
// The fields are the standard mstsc keys; "full address" carries host:port.
function downloadRdp(endpoint: ConsoleEndpoint, vmName: string): void {
  const lines = [
    `full address:s:${endpoint.host}:${endpoint.port}`,
    "screen mode id:i:2",
    "use multimon:i:0",
    "authentication level:i:0",
    "prompt for credentials:i:1",
    "negotiate security layer:i:1",
  ];
  const blob = new Blob([lines.join("\r\n") + "\r\n"], { type: "application/x-rdp" });
  const safeName = (vmName || "vm").replace(/[^a-zA-Z0-9._-]+/g, "-");
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `${safeName}.rdp`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
