// ui/src/components/ConsolePanel.tsx
//
// INTEGRATED interactive graphical console — renders a real, live screen (not a
// connection-info panel) for both Linux (VNC) and Windows (RDP) guests via
// guacamole-common-js over a same-origin websocket.
//
// The backend bridges GET /vm/providers/{pid}/vms/{vmId}/console/ws to guacd,
// which speaks VNC/RDP to the VM. The browser side speaks the Guacamole protocol:
// a Guacamole.WebSocketTunnel feeds a Guacamole.Client, whose Display element we
// mount into a container div; Guacamole.Mouse + Guacamole.Keyboard make it
// interactive (keyboard + mouse → guacd → VM).
//
// This component is mounted ONLY when the Console tab is active (see
// VirtualMachineDetail), so no socket opens in the background. It tears the
// client + listeners down on unmount.

import { useCallback, useEffect, useRef, useState } from "react";
import Guacamole, { type Client as GuacClient, type Keyboard as GuacKeyboard, type MouseState as GuacMouseState } from "guacamole-common-js";
import { ActionButton } from "./ActionButton";
import { IconRefresh, IconAlert } from "./icons";

interface Props {
  pid: string;
  vmId: string;
}

// Guacamole.Client.onstatechange numeric states.
const STATE_IDLE = 0;
const STATE_CONNECTING = 1;
const STATE_WAITING = 2;
const STATE_CONNECTED = 3;
const STATE_DISCONNECTING = 4;
const STATE_DISCONNECTED = 5;

type Phase = "connecting" | "connected" | "disconnected" | "error";

// Build the same-origin websocket URL (mirrors Terminal.tsx's ws:// vs wss://
// selection from location). The session cookie is sent automatically.
// buildWsUrl returns the tunnel base URL WITHOUT a query string. guacamole-common-js
// appends "?<connectString>" itself, so the w/h/dpi params are passed via
// client.connect(buildConnectString(...)) — putting them here too would produce a
// double "?" and a broken socket.
function buildWsUrl(pid: string, vmId: string): string {
  const proto = window.location.protocol === "https:" ? "wss" : "ws";
  return (
    `${proto}://${window.location.host}/api/v1/vm/providers/${encodeURIComponent(pid)}` +
    `/vms/${encodeURIComponent(vmId)}/console/ws`
  );
}

// buildConnectString is the query guacamole appends after "?": "w=..&h=..&dpi=96".
function buildConnectString(w: number, h: number): string {
  return `w=${w}&h=${h}&dpi=96`;
}

export function ConsolePanel({ pid, vmId }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const displayHostRef = useRef<HTMLDivElement>(null);
  const clientRef = useRef<GuacClient | null>(null);
  const keyboardRef = useRef<GuacKeyboard | null>(null);

  const [phase, setPhase] = useState<Phase>("connecting");
  const [errorMsg, setErrorMsg] = useState<string>("");
  const [fullscreen, setFullscreen] = useState(false);
  // Bumping this re-runs the connect effect (Reconnect button).
  const [attempt, setAttempt] = useState(0);

  const reconnect = useCallback(() => {
    setErrorMsg("");
    setPhase("connecting");
    setAttempt((n) => n + 1);
  }, []);

  useEffect(() => {
    const host = displayHostRef.current;
    const container = containerRef.current;
    if (!host || !container) return;

    setPhase("connecting");
    setErrorMsg("");

    const rect = container.getBoundingClientRect();
    const w = Math.max(640, Math.round(rect.width) || 1024);
    const h = Math.max(480, Math.round(rect.height) || 768);

    const tunnel = new Guacamole.WebSocketTunnel(buildWsUrl(pid, vmId));
    const client = new Guacamole.Client(tunnel);
    clientRef.current = client;

    const display = client.getDisplay();
    const displayEl = display.getElement();
    host.appendChild(displayEl);

    // ---- scale the remote display to fit the container without overflow ----
    const fit = () => {
      const dw = display.getWidth();
      const dh = display.getHeight();
      if (!dw || !dh) return;
      const cw = host.clientWidth || w;
      const ch = host.clientHeight || h;
      const scale = Math.min(cw / dw, ch / dh, 1);
      display.scale(scale > 0 ? scale : 1);
    };
    display.onresize = () => fit();

    // ---- client lifecycle ----
    client.onstatechange = (state: number) => {
      switch (state) {
        case STATE_IDLE:
        case STATE_CONNECTING:
        case STATE_WAITING:
          setPhase("connecting");
          break;
        case STATE_CONNECTED:
          setPhase("connected");
          fit();
          break;
        case STATE_DISCONNECTING:
        case STATE_DISCONNECTED:
          setPhase((p) => (p === "error" ? p : "disconnected"));
          break;
        default:
          break;
      }
    };

    client.onerror = (status) => {
      setErrorMsg(status?.message || `Console error (code ${status?.code ?? "?"})`);
      setPhase("error");
      try {
        client.disconnect();
      } catch {
        /* already gone */
      }
    };

    // ---- interactivity: mouse over the display, keyboard on the document ----
    const mouse = new Guacamole.Mouse(displayEl);
    const sendMouse = (state: GuacMouseState) => {
      // Translate from CSS pixels to the unscaled remote coordinate space.
      const scale = display.getScale() || 1;
      client.sendMouseState({
        ...state,
        x: state.x / scale,
        y: state.y / scale,
      });
    };
    mouse.onmousedown = sendMouse;
    mouse.onmouseup = sendMouse;
    mouse.onmousemove = sendMouse;

    const keyboard = new Guacamole.Keyboard(document);
    keyboardRef.current = keyboard;
    keyboard.onkeydown = (keysym: number) => {
      client.sendKeyEvent(1, keysym);
    };
    keyboard.onkeyup = (keysym: number) => {
      client.sendKeyEvent(0, keysym);
    };

    const onWindowResize = () => fit();
    const ro = new ResizeObserver(() => fit());
    ro.observe(host);
    window.addEventListener("resize", onWindowResize);

    try {
      // guacamole-common-js builds the tunnel URL as `<tunnelURL>?<connectString>`.
      // Calling connect() with no arg appended the literal "?undefined" to the URL,
      // producing ".../console/ws?w=..&dpi=96?undefined" — the socket closed at once
      // and the UI hung on "Opening interactive console". We now pass the params via
      // the connect string so the URL is exactly ".../console/ws?w=..&h=..&dpi=96".
      client.connect(buildConnectString(w, h));
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to open console socket");
      setPhase("error");
    }

    return () => {
      ro.disconnect();
      window.removeEventListener("resize", onWindowResize);
      mouse.onmousedown = null;
      mouse.onmouseup = null;
      mouse.onmousemove = null;
      keyboard.onkeydown = null;
      keyboard.onkeyup = null;
      try {
        keyboard.reset();
      } catch {
        /* ignore */
      }
      keyboardRef.current = null;
      try {
        client.disconnect();
      } catch {
        /* ignore */
      }
      clientRef.current = null;
      if (displayEl.parentNode === host) host.removeChild(displayEl);
    };
    // pid/vmId define the target; attempt forces a manual reconnect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pid, vmId, attempt]);

  const statusLabel =
    phase === "connecting"
      ? "Connecting…"
      : phase === "connected"
        ? "Connected"
        : phase === "error"
          ? "Error"
          : "Disconnected";

  const statusColor =
    phase === "connected"
      ? "var(--ok, #13A688)"
      : phase === "error"
        ? "var(--danger, #E5484D)"
        : "var(--text-muted, #6E8AA6)";

  return (
    <div className="card" style={{ overflow: "hidden" }}>
      <div className="card-header">
        <span className="card-title row" style={{ gap: "var(--sp-2)", alignItems: "center" }}>
          Console
          <span
            className="text-xs"
            style={{
              display: "inline-flex",
              alignItems: "center",
              gap: 6,
              color: statusColor,
            }}
          >
            <span
              aria-hidden
              style={{
                width: 8,
                height: 8,
                borderRadius: "50%",
                background: statusColor,
                display: "inline-block",
              }}
            />
            {statusLabel}
          </span>
        </span>
        <div className="row" style={{ gap: "var(--sp-2)" }}>
          <ActionButton
            size="sm"
            variant="ghost"
            onClick={() => setFullscreen((f) => !f)}
            tooltip={fullscreen ? "Exit fullscreen" : "Fullscreen"}
          >
            {fullscreen ? "Exit fullscreen" : "Fullscreen"}
          </ActionButton>
          <ActionButton size="sm" variant="ghost" onClick={reconnect} tooltip="Reconnect">
            <IconRefresh size={14} />
            Reconnect
          </ActionButton>
        </div>
      </div>

      <div className="card-body" style={{ padding: 0 }}>
        <div
          ref={containerRef}
          style={
            fullscreen
              ? {
                  position: "fixed",
                  inset: 0,
                  zIndex: 1000,
                  background: "#000",
                }
              : {
                  position: "relative",
                  width: "100%",
                  height: 600,
                  background: "#000",
                }
          }
        >
          {/* The Guacamole display element is appended here. */}
          <div
            ref={displayHostRef}
            tabIndex={0}
            style={{
              position: "absolute",
              inset: 0,
              display: "flex",
              alignItems: "center",
              justifyContent: "center",
              outline: "none",
              cursor: phase === "connected" ? "none" : "default",
              overflow: "hidden",
            }}
          />

          {fullscreen ? (
            <ActionButton
              size="sm"
              variant="ghost"
              onClick={() => setFullscreen(false)}
              style={{ position: "absolute", top: 12, right: 12, zIndex: 1001 }}
            >
              Exit fullscreen
            </ActionButton>
          ) : null}

          {phase !== "connected" ? (
            <div
              style={{
                position: "absolute",
                inset: 0,
                display: "flex",
                flexDirection: "column",
                alignItems: "center",
                justifyContent: "center",
                gap: "var(--sp-3)",
                color: "var(--text, #E6EEF6)",
                background: "rgba(6,19,32,0.72)",
                textAlign: "center",
                padding: "var(--sp-4)",
                zIndex: 2,
              }}
            >
              {phase === "connecting" ? (
                <span className="text-sm muted">Opening interactive console…</span>
              ) : phase === "error" ? (
                <>
                  <span className="row" style={{ gap: "var(--sp-2)", color: "var(--danger, #E5484D)" }}>
                    <IconAlert size={16} />
                    <span className="text-sm">{errorMsg || "Console connection failed."}</span>
                  </span>
                  <ActionButton size="sm" variant="primary" onClick={reconnect}>
                    <IconRefresh size={14} />
                    Reconnect
                  </ActionButton>
                </>
              ) : (
                <>
                  <span className="text-sm muted">Console disconnected.</span>
                  <ActionButton size="sm" variant="primary" onClick={reconnect}>
                    <IconRefresh size={14} />
                    Reconnect
                  </ActionButton>
                </>
              )}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}
