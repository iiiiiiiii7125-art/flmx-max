import type { SubTraffic, SubTrafficRange } from "@/api";

import { useEffect, useState } from "react";
import {
  Area,
  ComposedChart,
  Line,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { ArrowDown, ArrowUp } from "lucide-react";

import { getMySubscriptionTraffic, getSubscriptionTraffic } from "@/api";
import { Segmented } from "@/pages/subscription/controls";

// 与我方既有图表（延迟趋势等）同一调色：下载蓝、上传橙。
const DOWN = "#3b82f6";
const UP = "#f97316";
// Fixed text colors for the up/down numbers: a series color must not be
// the theme's primary (which recolors with the user's theme pick).
const DOWN_TEXT = "text-blue-600 dark:text-blue-400";
const UP_TEXT = "text-orange-600 dark:text-orange-400";

const RANGES: Array<[SubTrafficRange, string]> = [
  ["hour", "小时"],
  ["day", "日"],
  ["month", "月"],
];

const RANGE_HINT: Record<SubTrafficRange, string> = {
  hour: "近 24 小时",
  day: "近 30 天",
  month: "近 12 个月",
};

const pad = (n: number) => String(n).padStart(2, "0");

/** A bucket's axis, list row and tooltip labels. */
function bucketLabel(
  range: SubTrafficRange,
  ms: number,
): [string, string, string] {
  const d = new Date(ms);
  const md = `${d.getMonth() + 1}/${d.getDate()}`;
  // 非整小时时区（如 UTC+5:30）桶起点落在 :30/:45，标签用实际分钟。
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;

  if (range === "hour") return [hm, `${md} ${hm}`, `${md} ${hm}`];
  if (range === "day") return [md, md, `${d.getFullYear()}/${md}`];

  return [
    d.getMonth() === 0 ? `${d.getFullYear()}/1月` : `${d.getMonth() + 1}月`,
    `${d.getFullYear()}/${d.getMonth() + 1}`,
    `${d.getFullYear()} 年 ${d.getMonth() + 1} 月`,
  ];
}

/** Compact sizes that fit a tile: 3.3 GB, 267 MB. */
function bytes(v: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;

  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }

  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

/** Short axis numbers: 1.2G, 340M. */
function axisBytes(v: number): string {
  if (!v) return "0";
  const units = ["B", "K", "M", "G", "T"];
  let i = 0;

  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }

  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)}${units[i]}`;
}

/**
 * 一条订阅按小时 / 日 / 月的流量：合计、上下行曲线与逐桶明细。
 * 曲线是整条订阅的合计（一条订阅可分配给多个用户，凭据共用、拆不开个人量）。
 * mine 走用户端接口（仅限分配给当前用户的订阅）。
 */
export function SubTrafficStats({ id, mine }: { id: number; mine?: boolean }) {
  const [range, setRange] = useState<SubTrafficRange>("hour");
  const [data, setData] = useState<SubTraffic | null>(null);
  const [error, setError] = useState("");
  const [all, setAll] = useState(false);

  useEffect(() => {
    let active = true;

    setError("");
    (mine ? getMySubscriptionTraffic : getSubscriptionTraffic)(id, range)
      .then((res) => {
        if (!active) return;
        if (res.code === 0 && res.data) setData(res.data);
        else setError(res.msg || "加载失败");
      })
      .catch(() => active && setError("加载失败"));

    return () => {
      active = false;
    };
  }, [id, range, mine]);

  const shown = data?.range === range ? data : null;
  const points = (shown?.points ?? []).map((p) => {
    const [label, row, long] = bucketLabel(range, p.time);

    return { ...p, label, row, long };
  });
  const empty = shown !== null && shown.up + shown.down === 0;
  const rows = [...points].reverse();
  const maxTotal = Math.max(1, ...points.map((p) => p.up + p.down));
  // The bucket the used traffic above started counting in, if in range.
  const sinceAt =
    shown && points.length && shown.since >= points[0].time
      ? points.filter((p) => p.time <= shown.since).pop()
      : undefined;
  const sinceDate = shown ? new Date(shown.since).toLocaleDateString() : "";

  return (
    <div className="space-y-3">
      <Segmented
        options={RANGES}
        value={range}
        onChange={(r) => {
          setRange(r);
          setAll(false);
        }}
      />
      <p className="text-xs text-default-500">{RANGE_HINT[range]}的流量</p>
      <div className="grid grid-cols-3 gap-2">
        {(
          [
            ["下载", shown?.down, DOWN, DOWN_TEXT],
            ["上传", shown?.up, UP, UP_TEXT],
            ["合计", shown ? shown.up + shown.down : undefined, "", ""],
          ] as Array<[string, number | undefined, string, string]>
        ).map(([label, value, color, textColor]) => (
          <div key={label} className="rounded-xl bg-default-100 px-3 py-2">
            <div className="flex items-center gap-1.5 text-xs text-default-500">
              {color && (
                <span
                  className="h-2 w-2 rounded-full"
                  style={{ background: color }}
                />
              )}
              {label}
            </div>
            <div
              className={`mt-0.5 truncate font-mono text-sm font-semibold ${textColor}`}
            >
              {value === undefined ? "…" : bytes(value)}
            </div>
          </div>
        ))}
      </div>

      {error ? (
        <p className="py-8 text-center text-sm text-danger">{error}</p>
      ) : !shown ? (
        <p className="py-8 text-center text-sm text-default-400">正在加载…</p>
      ) : (
        <>
          <div className="relative h-52 w-full sm:h-64">
            <ResponsiveContainer height="100%" width="100%">
              <ComposedChart
                data={points}
                margin={{ top: 8, right: 8, left: 0, bottom: 0 }}
              >
                <XAxis
                  axisLine={false}
                  dataKey="label"
                  interval="preserveStartEnd"
                  minTickGap={24}
                  tick={{ fill: "currentColor", opacity: 0.55, fontSize: 11 }}
                  tickLine={false}
                />
                <YAxis
                  axisLine={false}
                  tick={{ fill: "currentColor", opacity: 0.55, fontSize: 11 }}
                  tickFormatter={(v: number) => axisBytes(v)}
                  tickLine={false}
                  width={48}
                />
                <Tooltip
                  content={({
                    active,
                    payload,
                  }: {
                    active?: boolean;
                    payload?: Array<{ payload?: (typeof points)[number] }>;
                  }) => {
                    const p = payload?.[0]?.payload;

                    if (!active || !p) return null;

                    return (
                      <div className="rounded-xl border border-divider bg-content1 px-3 py-2 text-xs shadow-lg">
                        <p className="font-medium">{p.long}</p>
                        <p className={`mt-1 ${DOWN_TEXT}`}>
                          下载 {bytes(p.down)}
                        </p>
                        <p className={UP_TEXT}>上传 {bytes(p.up)}</p>
                      </div>
                    );
                  }}
                  cursor={{ stroke: "currentColor", strokeOpacity: 0.2 }}
                />
                {sinceAt && (
                  <ReferenceLine
                    stroke="currentColor"
                    strokeDasharray="2 3"
                    strokeOpacity={0.35}
                    x={sinceAt.label}
                  />
                )}
                <Area
                  dataKey="down"
                  fill={DOWN}
                  fillOpacity={0.12}
                  isAnimationActive={false}
                  name="下载"
                  stroke={DOWN}
                  strokeWidth={2}
                  type="monotone"
                />
                <Line
                  dataKey="up"
                  dot={false}
                  isAnimationActive={false}
                  name="上传"
                  stroke={UP}
                  strokeDasharray="5 4"
                  strokeWidth={2}
                  type="monotone"
                />
              </ComposedChart>
            </ResponsiveContainer>
            {empty && (
              <p className="absolute inset-0 flex items-center justify-center text-center text-xs text-default-400">
                {RANGE_HINT[range]}还没有流量
              </p>
            )}
          </div>
          <div className="flex items-center gap-4 text-xs text-default-500">
            <span className="flex items-center gap-1.5">
              <span className="w-4 border-t-2" style={{ borderColor: DOWN }} />
              下载
            </span>
            <span className="flex items-center gap-1.5">
              <span
                className="w-4 border-t-2 border-dashed"
                style={{ borderColor: UP }}
              />
              上传
            </span>
            {sinceAt && (
              <span className="flex items-center gap-1.5">
                <span className="h-3 border-l border-dashed border-default-400" />
                本周期起（{sinceDate}）
              </span>
            )}
            <span className="ml-auto">{RANGE_HINT[range]}</span>
          </div>

          <div className="overflow-hidden rounded-xl border border-divider">
            <div className="grid grid-cols-[4.75rem_minmax(0,1fr)_4.5rem] gap-2 bg-default-100/60 px-3 py-1.5 text-xs text-default-500">
              <span>时间</span>
              <span>下载 / 上传</span>
              <span className="text-right">合计</span>
            </div>
            {(all ? rows : rows.slice(0, 7)).map((p) => (
              <div
                key={p.time}
                className="grid grid-cols-[4.75rem_minmax(0,1fr)_4.5rem] items-center gap-2 border-t border-divider px-3 py-2"
              >
                <span className="truncate text-xs">{p.row}</span>
                <div className="min-w-0 space-y-1">
                  <div
                    aria-hidden="true"
                    className="flex h-1.5 overflow-hidden rounded-full bg-default-100"
                  >
                    <div
                      className="h-full shrink-0"
                      style={{
                        background: DOWN,
                        width: `${(p.down / maxTotal) * 100}%`,
                      }}
                    />
                    <div
                      className="h-full shrink-0"
                      style={{
                        background: UP,
                        width: `${(p.up / maxTotal) * 100}%`,
                      }}
                    />
                  </div>
                  <div className="flex flex-wrap gap-x-2 font-mono text-[11px]">
                    <span
                      className={`inline-flex items-center gap-0.5 ${DOWN_TEXT}`}
                    >
                      <ArrowDown aria-hidden="true" size={11} />
                      {bytes(p.down)}
                    </span>
                    <span
                      className={`inline-flex items-center gap-0.5 ${UP_TEXT}`}
                    >
                      <ArrowUp aria-hidden="true" size={11} />
                      {bytes(p.up)}
                    </span>
                  </div>
                </div>
                <span className="text-right font-mono text-xs font-semibold">
                  {bytes(p.up + p.down)}
                </span>
              </div>
            ))}
            {rows.length > 7 && (
              <button
                className="w-full border-t border-divider py-2 text-xs text-primary"
                type="button"
                onClick={() => setAll(!all)}
              >
                {all ? "收起" : `查看全部 ${rows.length} 条`}
              </button>
            )}
          </div>
          <p className="text-xs text-default-400">
            统计口径与订阅「已用」一致：仅统计独立凭据协议（VLESS、VMess、Trojan、Hysteria2、TUIC
            等）的流量；Shadowsocks、Snell、导入节点等共用凭据不计入。曲线为该订阅的合计流量，自本功能上线起记录
          </p>
          <p className="text-xs text-default-400">
            订阅「已用」从本周期开始（{sinceDate}
            ，订阅创建或上次重置流量时）累计，和这里所选的时间范围不同，两边的数不一定相等。
          </p>
        </>
      )}
    </div>
  );
}
