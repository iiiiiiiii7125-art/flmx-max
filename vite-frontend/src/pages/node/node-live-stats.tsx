import type { NodeSystemInfo } from "@/pages/node/system-info";

import { ArrowDown, ArrowUp } from "lucide-react";

function bytes(v: number): string {
  const units = ["B", "K", "M", "G", "T", "P"];
  let i = 0;

  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }

  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)}${units[i]}`;
}

function tone(v: number): string {
  return v >= 85 ? "bg-danger" : v >= 60 ? "bg-warning" : "bg-success";
}

function Meter({ label, value }: { label: string; value?: number }) {
  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-baseline justify-between gap-1 text-[11px] font-semibold leading-none">
        <span className="text-foreground">{label}</span>
        <span className="font-mono font-semibold">
          {value === undefined ? "—" : `${Math.round(value)}%`}
        </span>
      </div>
      <div className="h-1 overflow-hidden rounded-full bg-default-100">
        {value !== undefined && (
          <div
            className={`h-full rounded-full ${tone(value)}`}
            style={{ width: `${Math.min(100, Math.max(2, value))}%` }}
          />
        )}
      </div>
    </div>
  );
}

/**
 * A node's live resources on its list row or card (DA-1/DA-2): CPU,
 * memory and disk meters, the live up/down rates and this month's
 * traffic. The month figure is the node's whole-NIC counter (system +
 * forwarding + everything), NOT the tunnel billing volume — the label
 * says 网卡 to keep the two apart. "row" sits at the end of a wide
 * row, "footer" under the name on compact rows (ours stays visible at
 * every width: the list has no wide variant), "card" in a node card.
 * Meters are shown only for figures the node has actually reported:
 * an unreported figure hides its meter instead of reading 0%.
 */
export function NodeLiveStats({
  info,
  month,
  variant,
}: {
  info: NodeSystemInfo | null;
  /** This month's [in, out] bytes from the node's NIC counters. */
  month?: [number, number];
  variant: "row" | "footer" | "card";
}) {
  const up = info ? (info.netOutSpeed ?? info.uploadSpeed) : undefined;
  const down = info ? (info.netInSpeed ?? info.downloadSpeed) : undefined;
  const meterEntries = (
    [
      ["CPU", info?.cpuUsage],
      ["内存", info?.memoryUsage],
      ["磁盘", info?.diskUsage],
    ] as const
  ).filter(([, v]) => v !== undefined);
  const meters =
    meterEntries.length > 0 ? (
      <div
        className="grid gap-3"
        style={{
          gridTemplateColumns: `repeat(${meterEntries.length}, minmax(0, 1fr))`,
        }}
      >
        {meterEntries.map(([label, v]) => (
          <Meter key={label} label={label} value={v} />
        ))}
      </div>
    ) : null;
  const rate = (v?: number) => (v === undefined ? "—" : `${bytes(v)}/s`);
  const upIcon = <ArrowUp className="h-3 w-3 shrink-0 text-emerald-700" />;
  const downIcon = <ArrowDown className="h-3 w-3 shrink-0 text-sky-700" />;
  const total = month ? bytes(month[0] + month[1]) : "—";
  const split = month ? `↑${bytes(month[1])} ↓${bytes(month[0])}` : "";

  if (variant !== "row") {
    return (
      <div className={`space-y-2 ${variant === "footer" ? "mt-2" : ""}`}>
        {meters}
        <div className="grid grid-cols-2 divide-x divide-divider rounded-lg py-1.5 text-[11px] font-semibold tabular-nums">
          <div className="min-w-0 px-2.5">
            <div className="text-[10px] font-semibold text-foreground">网速</div>
            <div className="flex flex-wrap items-center gap-x-2 font-medium">
              <span className="flex items-center gap-1 whitespace-nowrap">
                {upIcon}
                {rate(up)}
              </span>
              <span className="flex items-center gap-1 whitespace-nowrap">
                {downIcon}
                {rate(down)}
              </span>
            </div>
          </div>
          <div className="min-w-0 px-2.5" title={split}>
            <div className="text-[10px] font-semibold text-foreground">本月流量（网卡）</div>
            <div className="flex flex-wrap items-baseline gap-x-1.5">
              <span className="font-semibold">{total}</span>
              <span className="whitespace-nowrap text-[10px] font-semibold text-foreground">
                {split}
              </span>
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="hidden items-center gap-5 lg:flex">
      {meters ? <div className="w-60">{meters}</div> : null}
      <div className="w-24 space-y-0.5 text-[11px] font-semibold leading-tight tabular-nums">
        <div className="flex items-center gap-1">
          {upIcon}
          {rate(up)}
        </div>
        <div className="flex items-center gap-1">
          {downIcon}
          {rate(down)}
        </div>
      </div>
      <div className="w-28 space-y-0.5 leading-tight tabular-nums">
        <div className="flex items-baseline gap-1.5 text-[11px] font-semibold">
          <span className="text-[10px] font-semibold text-foreground">本月（网卡）</span>
          <span className="font-semibold">{total}</span>
        </div>
        <div className="text-[10px] font-semibold text-foreground">{split || "—"}</div>
      </div>
    </div>
  );
}
