// ui/src/components/Drawer.tsx
//
// A reusable RIGHT-SIDE slide-in panel (the "volet latéral") used for VM
// actions in place of centered modals. It carries an overlay scrim, slides in
// from the right edge, has a sticky header (title + subtitle + close) and a
// sticky footer (Cancel / primary action), a scrollable body, ESC + scrim-click
// to close, a focus trap, and a smooth enter/exit transition. It matches the
// Castor surface/border tokens (same design system as Modal).

import { useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { IconClose } from "./icons";

interface Props {
  open: boolean;
  title: ReactNode;
  /** optional secondary line under the title (entity name, hints) */
  subtitle?: ReactNode;
  /** optional icon shown left of the title */
  icon?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  /** drawer width preset; defaults to "md" (~520px). "lg" (~620px) for the rich editor. */
  size?: "md" | "lg";
  /** prevent backdrop/Escape close while a mutation is in flight */
  busy?: boolean;
}

const FOCUSABLE =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Accessible right-side drawer using the --overlay scrim and brand surfaces. */
export function Drawer({ open, title, subtitle, icon, onClose, children, footer, size = "md", busy }: Props) {
  // Two-phase mount so we can play an exit transition before unmounting.
  const [mounted, setMounted] = useState(open);
  const [entered, setEntered] = useState(false);
  const panelRef = useRef<HTMLDivElement>(null);
  const prevFocus = useRef<HTMLElement | null>(null);

  useEffect(() => {
    if (open) {
      prevFocus.current = document.activeElement as HTMLElement | null;
      setMounted(true);
      // next frame -> trigger the slide-in transition
      const id = requestAnimationFrame(() => setEntered(true));
      return () => cancelAnimationFrame(id);
    }
    setEntered(false);
    const t = setTimeout(() => setMounted(false), 220);
    return () => clearTimeout(t);
  }, [open]);

  // ESC to close + lock body scroll + restore focus on close.
  useEffect(() => {
    if (!mounted) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !busy) {
        e.stopPropagation();
        onClose();
      }
      if (e.key === "Tab") trapFocus(e);
    };
    document.addEventListener("keydown", onKey, true);
    const prevOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.body.style.overflow = prevOverflow;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mounted, busy, onClose]);

  // Focus the first focusable element when the drawer opens.
  useEffect(() => {
    if (entered && panelRef.current) {
      const first = panelRef.current.querySelector<HTMLElement>(FOCUSABLE);
      (first ?? panelRef.current).focus();
    }
  }, [entered]);

  // Restore focus to the trigger after the drawer is fully closed.
  useEffect(() => {
    if (!mounted) prevFocus.current?.focus?.();
  }, [mounted]);

  const trapFocus = (e: KeyboardEvent) => {
    const root = panelRef.current;
    if (!root) return;
    const nodes = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(
      (n) => n.offsetParent !== null || n === document.activeElement,
    );
    if (nodes.length === 0) {
      e.preventDefault();
      root.focus();
      return;
    }
    const first = nodes[0]!;
    const last = nodes[nodes.length - 1]!;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  if (!mounted) return null;

  return createPortal(
    <div
      className={`drawer-scrim${entered ? " is-open" : ""}`}
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && !busy) onClose();
      }}
    >
      <div
        ref={panelRef}
        className={`drawer drawer-${size}${entered ? " is-open" : ""}`}
        role="dialog"
        aria-modal="true"
        tabIndex={-1}
      >
        <div className="drawer-header">
          <div className="drawer-heading">
            {icon ? <span className="drawer-icon">{icon}</span> : null}
            <div className="col" style={{ gap: 2, minWidth: 0 }}>
              <h2 className="drawer-title">{title}</h2>
              {subtitle ? <div className="drawer-subtitle">{subtitle}</div> : null}
            </div>
          </div>
          <button
            className="btn btn-ghost btn-icon btn-sm"
            onClick={onClose}
            disabled={busy}
            aria-label="Close"
          >
            <IconClose size={16} />
          </button>
        </div>
        <div className="drawer-body">{children}</div>
        {footer ? <div className="drawer-footer">{footer}</div> : null}
      </div>
    </div>,
    document.body,
  );
}
