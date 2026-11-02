import type { ReactNode } from "react";

export interface ChartTooltipEntry {
  name?: string;
  value?: unknown;
  color?: string;
  payload?: unknown;
}

interface ChartTooltipProps {
  active?: boolean;
  payload?: ChartTooltipEntry[];
  label?: unknown;
  formatLabel?: (label: unknown) => ReactNode;
  formatEntry?: (entry: ChartTooltipEntry) => { name: string; value: string };
}

/**
 * Shared tooltip for every recharts chart. Chart popups use the same
 * .panel-surface recipe as all other floating layers: foreground text
 * throughout, series identity carried by a saturated dot instead of
 * pale series-colored text.
 */
export function ChartTooltip({
  active,
  payload,
  label,
  formatLabel,
  formatEntry,
}: ChartTooltipProps) {
  if (!active || !payload || payload.length === 0) return null;

  const labelText = formatLabel ? formatLabel(label) : (label as ReactNode);

  return (
    <div className="panel-surface rounded-xl px-3 py-2 text-xs font-semibold shadow-lg">
      {labelText !== undefined && labelText !== null && labelText !== "" && (
        <p className="font-semibold text-foreground">{labelText}</p>
      )}
      <div className="mt-0.5 space-y-0.5">
        {payload.map((entry, i) => {
          const formatted = formatEntry
            ? formatEntry(entry)
            : {
                name: String(entry.name ?? ""),
                value: String(entry.value ?? ""),
              };

          return (
            <p
              key={`${formatted.name}-${i}`}
              className="flex items-center gap-1.5 text-foreground"
            >
              {entry.color && (
                <span
                  className="h-2 w-2 shrink-0 rounded-full"
                  style={{ background: entry.color }}
                />
              )}
              <span>{formatted.name}</span>
              <span className="font-semibold">{formatted.value}</span>
            </p>
          );
        })}
      </div>
    </div>
  );
}
