// ui/src/components/VMStateBadge.tsx
//
// VM lifecycle state pill. Mirrors StateBadge (container) but over the VMState
// vocabulary, which adds "suspended" (a hypervisor-only state with no container
// analogue). Reuses the same .pill + StatusDot tokens so it reads identically.

import type { VMState } from "../lib/types";
import { StatusDot } from "./StatusDot";

const LABEL: Record<VMState, string> = {
  running: "Running",
  stopped: "Stopped",
  suspended: "Suspended",
  paused: "Paused",
  unknown: "Unknown",
};

const TINT: Record<VMState, { bg: string; fg: string }> = {
  running: { bg: "var(--success-bg)", fg: "var(--state-running)" },
  stopped: { bg: "rgba(110,138,166,0.16)", fg: "var(--state-stopped)" },
  suspended: { bg: "rgba(142,124,195,0.18)", fg: "var(--state-pending)" },
  paused: { bg: "var(--warning-bg)", fg: "var(--state-paused)" },
  unknown: { bg: "rgba(110,138,166,0.16)", fg: "var(--state-unknown)" },
};

// Map a VMState onto a WorkloadState-shaped color for the dot (StatusDot only
// knows WorkloadState/HostStatus); "suspended" reuses the pending color.
const DOT_COLOR: Record<VMState, string> = {
  running: "var(--state-running)",
  stopped: "var(--state-stopped)",
  suspended: "var(--state-pending)",
  paused: "var(--state-paused)",
  unknown: "var(--state-unknown)",
};

interface Props {
  state: VMState;
  /** show the hypervisor-native raw string as the badge tooltip */
  raw?: string;
}

/** A pill rendering a normalized VMState with the matching token color. */
export function VMStateBadge({ state, raw }: Props) {
  const tint = TINT[state] ?? TINT.unknown;
  return (
    <span
      className="pill"
      style={{ background: tint.bg, color: tint.fg, borderColor: "transparent" }}
      title={raw || LABEL[state]}
    >
      <StatusDot color={DOT_COLOR[state] ?? DOT_COLOR.unknown} pulse={state === "running"} />
      {LABEL[state] ?? state}
    </span>
  );
}
