// ui/src/components/ResourceGauge.tsx
//
// Lot 1 — a labeled Used / Free / Capacity bar for the VM Summary tab. Built from
// existing tokens only (no new palette): fill is --accent by default and shifts to
// --warning at ≥75% and --danger at ≥90%, matching the plan's threshold colors.

import type { ReactNode } from "react";

interface Props {
  label: string;
  icon?: ReactNode;
  /** 0..100 */
  percent: number;
  /** Pre-formatted strings shown under the bar. */
  usedLabel?: string;
  capacityLabel?: string;
  /** Override the fill color (e.g. CPU always --accent). */
  baseColor?: string;
}

function fillColor(pct: number, base: string): string {
  if (pct >= 90) return "var(--danger)";
  if (pct >= 75) return "var(--warning)";
  return base;
}

export function ResourceGauge({
  label,
  icon,
  percent,
  usedLabel,
  capacityLabel,
  baseColor = "var(--accent)",
}: Props) {
  const pct = Math.max(0, Math.min(100, Number.isFinite(percent) ? percent : 0));
  return (
    <div className="gauge">
      <div className="gauge-top">
        <span className="gauge-label">
          {icon}
          {label}
        </span>
        <span className="gauge-pct">{pct.toFixed(0)}%</span>
      </div>
      <div className="gauge-track" role="meter" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100} aria-label={label}>
        <div className="gauge-fill" style={{ width: `${pct}%`, background: fillColor(pct, baseColor) }} />
      </div>
      {usedLabel || capacityLabel ? (
        <div className="gauge-foot">
          <span>{usedLabel}</span>
          <span>{capacityLabel}</span>
        </div>
      ) : null}
    </div>
  );
}
