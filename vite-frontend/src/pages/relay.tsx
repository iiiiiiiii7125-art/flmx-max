import type { InboundApiItem, InboundRelayApiItem } from "@/api/types";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import toast from "react-hot-toast";
import { LayoutGrid, List } from "lucide-react";

import { getInboundList, getInboundRelays, probeInboundRelays } from "@/api";
import { AnimatedPage } from "@/components/animated-page";
import { RelayLatencyCell } from "@/components/relay-latency";
import { PageLoadingState } from "@/components/page-state";
import { useLocalStorageState } from "@/hooks/use-local-storage-state";
import { useVisiblePolling } from "@/hooks/use-visible-polling";
import { latestProbe } from "@/utils/latest-probe";
import ForwardPage from "@/pages/forward";
import { isIeplForm } from "@/pages/inbound/presets";
import { RelayModal } from "@/pages/inbound/relay-modal";
import { TunnelMonitorView } from "@/pages/node/tunnel-monitor-view";
import TunnelPage from "@/pages/tunnel";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody } from "@/shadcn-bridge/heroui/card";
import { Chip } from "@/shadcn-bridge/heroui/chip";

type RelayTab = "lines" | "tunnels" | "rules" | "quality";

const TAB_KEY = "flmx_relay_tab";

function readTab(): RelayTab {
  try {
    const v = localStorage.getItem(TAB_KEY);

    if (v === "lines" || v === "tunnels" || v === "rules" || v === "quality")
      return v;
  } catch {
    // Remembering the tab is only a convenience.
  }

  return "lines";
}

const TABS: Array<{ key: RelayTab; label: string }> = [
  { key: "lines", label: "线路" },
  { key: "tunnels", label: "隧道" },
  { key: "rules", label: "规则" },
  { key: "quality", label: "质量" },
];

/** Relay lines grouped by the landing they lead to. */
function LinesView() {
  const [loading, setLoading] = useState(true);
  const [inbounds, setInbounds] = useState<InboundApiItem[]>([]);
  const [relays, setRelays] = useState<InboundRelayApiItem[]>([]);
  // IRL-3：任一列表加载失败都要与「真空」可区分——relRes 失败旧实现
  // 完全静默，total=0 直接渲染「还没有中转线路」装空。
  const [loadError, setLoadError] = useState("");
  // IRL-3 同族残留（接管复核发现）：中转列表从未成功加载过时，relays
  // 恒为 []，每条落地都会渲染「无中转」——把「未知」说成「没有」；
  // 顶部横幅还宣称「显示的是上次成功加载的数据」，初载时根本没有上
  // 次数据。relaysLoaded 区分「从未取到」与「轮询失败留旧数据」两种
  // 情形，前者必须整面报错、后者才可留旧数据加横幅。
  const [relaysLoaded, setRelaysLoaded] = useState(false);
  const [editing, setEditing] = useState<InboundApiItem | null>(null);
  const [probingIds, setProbingIds] = useState<Set<number>>(new Set());
  const [onlyWithRelays, setOnlyWithRelays] = useState(false);
  const [viewMode, setViewMode] = useLocalStorageState<"list" | "grid">(
    "relay-lines-view-mode",
    "list",
  );

  // load 序号守卫（R3-D-01，与 node.tsx loadNodes 同型）：初载、
  // 手动刷新与 15 秒轮询可并发在途，只有最新一轮可落 state，
  // 乱序返回的旧列表快照直接丢弃（延迟字段另有 latestProbe
  // 单调保护，此处守的是列表本体）。
  const loadSeqRef = useRef(0);

  const load = useCallback(async (options?: { silent?: boolean }) => {
    const seq = (loadSeqRef.current += 1);

    try {
      const [inRes, relRes] = await Promise.all([
        getInboundList(),
        getInboundRelays(0),
      ]);

      if (loadSeqRef.current !== seq) return;
      if (inRes.code === 0) setInbounds(inRes.data ?? []);
      if (relRes.code === 0) {
        setRelaysLoaded(true);
        setRelays((prev) => {
          const previous = new Map(prev.map((r) => [r.id, r]));

          return (relRes.data ?? []).map((r) => ({
            ...r,
            latency: latestProbe(r.latency, previous.get(r.id)?.latency),
          }));
        });
      }
      // IRL-3：两路任一失败都置错（旧实现只看 inRes、relRes 失败静默
      // 装空）；两路全成才清错。已有数据时轮询失败只置错不清空，页面
      // 顶部出提示行，数据本体不假装消失。
      if (inRes.code === 0 && relRes.code === 0) {
        setLoadError("");
      } else {
        const failed =
          inRes.code !== 0 ? inRes : relRes;

        setLoadError(failed.msg || "加载失败");
        if (!options?.silent) toast.error(failed.msg || "加载失败");
      }
    } catch {
      if (loadSeqRef.current !== seq) return;
      setLoadError("网络错误，请重试");
      if (!options?.silent) toast.error("网络错误，请重试");
    } finally {
      if (loadSeqRef.current === seq) {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // DB-3：中转线路延迟只随列表回包
  // 更新——15 秒可见性轮询让重连重测/定时轮次的结果及时显现。
  useVisiblePolling(useCallback(() => load({ silent: true }), [load]));

  /** 测中转线路的分段延迟（Lane D），结果按线路 id 合并回列表。 */
  const probe = useCallback(
    async (ids: number[]) => {
      const busy = ids.length > 0 ? ids : relays.map((r) => r.id);

      setProbingIds((prev) => new Set([...prev, ...busy]));
      try {
        const res = await probeInboundRelays(ids);

        if (res.code !== 0) {
          toast.error(res.msg || "检测失败");

          return;
        }
        const data = res.data ?? {};

        setRelays((prev) =>
          prev.map((r) =>
            data[String(r.id)]
              ? { ...r, latency: latestProbe(data[String(r.id)], r.latency) }
              : r,
          ),
        );
      } catch {
        toast.error("检测失败");
      } finally {
        setProbingIds((prev) => {
          const next = new Set(prev);

          busy.forEach((id) => next.delete(id));

          return next;
        });
      }
    },
    [relays],
  );

  const latencyOf = (l: InboundRelayApiItem) =>
    l.forwardExists ? (
      <RelayLatencyCell
        latency={l.latency}
        probing={probingIds.has(l.id)}
        onProbe={() => probe([l.id])}
      />
    ) : null;

  const byInbound = useMemo(() => {
    const m = new Map<number, InboundRelayApiItem[]>();

    for (const r of relays) {
      m.set(r.inboundId, [...(m.get(r.inboundId) ?? []), r]);
    }

    return m;
  }, [relays]);

  if (loading) return <PageLoadingState message="正在加载..." />;

  const shown = inbounds.filter(
    (i) => !onlyWithRelays || (byInbound.get(i.id)?.length ?? 0) > 0,
  );
  const total = relays.length;

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs font-semibold text-foreground">
          {inbounds.length} 个落地 ·{" "}
          {loadError && !relaysLoaded
            ? "中转线路未知（列表尚未成功加载）"
            : `${total} 条中转线路`}
          。点落地可添加或删除中转
        </p>
        <div className="flex shrink-0 gap-2">
          <Button
            size="sm"
            variant={onlyWithRelays ? "flat" : "light"}
            onPress={() => setOnlyWithRelays((v) => !v)}
          >
            {onlyWithRelays ? "显示全部" : "只看有中转的"}
          </Button>
          {total > 0 && (
            <Button
              isLoading={probingIds.size > 0}
              size="sm"
              variant="flat"
              onPress={() => probe([])}
            >
              测延迟
            </Button>
          )}
          <Button
            isIconOnly
            aria-label={viewMode === "list" ? "卡片视图" : "列表视图"}
            size="sm"
            variant="flat"
            onPress={() => setViewMode(viewMode === "list" ? "grid" : "list")}
          >
            {viewMode === "list" ? (
              <LayoutGrid className="h-4 w-4" />
            ) : (
              <List className="h-4 w-4" />
            )}
          </Button>
        </div>
      </div>

      {loadError && shown.length > 0 && relaysLoaded && (
        <p className="rounded-xl bg-warning-50 px-3 py-2 text-xs font-semibold text-warning-700">
          部分数据刷新失败（{loadError}），当前显示的是上次成功加载的数据。
        </p>
      )}

      {shown.length === 0 || (loadError && !relaysLoaded) ? (
        <Card className="shadow-sm">
          <CardBody className="py-12 text-center text-sm text-foreground">
            {loadError ? (
              <div className="flex flex-col items-center justify-center gap-3">
                <p className="font-semibold text-danger-700">
                  中转线路加载失败：{loadError}
                </p>
                <p>不能据此判断是否已有中转线路，请重试。</p>
                <Button
                  color="danger"
                  size="sm"
                  variant="flat"
                  onPress={() => load()}
                >
                  重新加载
                </Button>
              </div>
            ) : inbounds.length === 0 ? (
              "还没有落地。先在“落地”里搭建或导入节点，再回来对接中转。"
            ) : (
              "还没有中转线路"
            )}
          </CardBody>
        </Card>
      ) : viewMode === "grid" ? (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {shown.map((i) => {
            const lines = byInbound.get(i.id) ?? [];

            return (
              <div
                key={i.id}
                className="glass-card flex flex-col gap-3 rounded-2xl p-4"
              >
                <div>
                  <div className="flex items-center gap-1.5">
                    <div className="truncate font-semibold">{i.name}</div>
                    {isIeplForm(i) && (
                      <Chip color="danger" size="sm" variant="flat">
                        IEPL专用
                      </Chip>
                    )}
                  </div>
                  <div className="mt-0.5 truncate text-xs font-semibold text-foreground">
                    {i.external ? "导入的节点" : i.nodeName}
                    {" · "}
                    {lines.length > 0 ? `${lines.length} 条中转` : "无中转"}
                    {i.relayOnly ? " · 仅中转" : ""}
                  </div>
                </div>
                {lines.length > 0 && (
                  <div className="space-y-2">
                    {lines.map((l) => {
                      const ok = l.forwardExists && l.forwardStatus === 1;

                      return (
                        <div
                          key={l.id}
                          className="rounded-xl glass-inset px-3 py-2"
                        >
                          <div className="flex items-center gap-2 text-sm">
                            <span
                              className={`h-1.5 w-1.5 shrink-0 rounded-full ${ok ? "bg-success" : "bg-warning"}`}
                            />
                            <span className="min-w-0 flex-1 truncate">
                              {l.name || l.tunnelName}
                            </span>
                            <Chip size="sm" variant="flat">
                              {l.mode === "tunnel" ? "隧道" : "端口转发"}
                            </Chip>
                          </div>
                          {l.tunnelName && (
                            <div className="mt-1 truncate text-xs font-semibold text-foreground">
                              隧道：{l.tunnelName}
                            </div>
                          )}
                          {l.entries.map((en) => (
                            <div
                              key={`${en.Host}:${en.Port}`}
                              className="truncate font-mono text-xs font-semibold text-foreground"
                            >
                              入口：{l.shareHost || en.Host}:{en.Port}
                            </div>
                          ))}
                          {!ok && (
                            <div className="text-xs font-semibold text-warning-700">
                              {l.forwardExists ? "转发已暂停" : "转发已被删除"}
                            </div>
                          )}
                          {l.forwardExists && (
                            <div className="mt-1.5 flex justify-end">
                              {latencyOf(l)}
                            </div>
                          )}
                        </div>
                      );
                    })}
                  </div>
                )}
                <Button
                  className="mt-auto"
                  color="secondary"
                  size="sm"
                  variant="flat"
                  onPress={() => setEditing(i)}
                >
                  {lines.length > 0 ? "管理中转" : "添加中转"}
                </Button>
              </div>
            );
          })}
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-2 2xl:grid-cols-2">
          {shown.map((i) => {
            const lines = byInbound.get(i.id) ?? [];

            return (
              <div
                key={i.id}
                className="glass-card cursor-pointer rounded-xl px-3 py-2.5 transition-colors hover:bg-white/40"
                role="button"
                tabIndex={0}
                onClick={() => setEditing(i)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") {
                    e.preventDefault();
                    setEditing(i);
                  }
                }}
              >
                <div className="flex items-center gap-2">
                  <span className="min-w-0 flex-1 truncate font-medium">
                    {i.name}
                  </span>
                  {isIeplForm(i) && (
                    <Chip color="danger" size="sm" variant="flat">
                      IEPL专用
                    </Chip>
                  )}
                  {i.relayOnly && (
                    <Chip size="sm" variant="flat">
                      仅中转
                    </Chip>
                  )}
                  <span className="shrink-0 text-xs font-semibold text-foreground">
                    {lines.length > 0 ? `${lines.length} 条` : "无中转"}
                  </span>
                </div>
                {lines.length > 0 && (
                  <div className="mt-1.5 space-y-1">
                    {lines.map((l) => {
                      const ok = l.forwardExists && l.forwardStatus === 1;
                      const entry = l.entries[0];

                      return (
                        <div
                          key={l.id}
                          className="flex items-center gap-2 text-xs font-semibold text-foreground"
                        >
                          <span
                            className={`h-1.5 w-1.5 shrink-0 rounded-full ${ok ? "bg-success" : "bg-warning"}`}
                          />
                          <span className="min-w-0 flex-1 truncate">
                            {l.name || l.tunnelName}
                            {" · "}
                            {l.mode === "tunnel" ? "隧道" : "端口转发"}
                            {l.tunnelName ? ` · ${l.tunnelName}` : ""}
                          </span>
                          {entry && (
                            <span className="shrink-0 font-mono">
                              {l.shareHost || entry.Host}:{entry.Port}
                            </span>
                          )}
                          {latencyOf(l)}
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}

      <RelayModal
        item={editing}
        onChanged={load}
        onClose={() => setEditing(null)}
      />
    </div>
  );
}

/** Tunnel quality: latency / loss / throughput timelines (DA-3). The
 * same view also stays under 监控 → 隧道 for granted users. */
function QualityView() {
  return <TunnelMonitorView viewMode="grid" />;
}

/** 中转: relay lines per landing, plus the tunnel and rule pages. */
export default function RelayPage() {
  const [tab, setTabState] = useState<RelayTab>(readTab);
  const setTab = (t: RelayTab) => {
    setTabState(t);
    try {
      localStorage.setItem(TAB_KEY, t);
    } catch {
      // ignore
    }
  };

  const tabBar = (
    <div className="glass-surface grid grid-cols-4 gap-1 rounded-xl p-1 sm:max-w-md">
      {TABS.map((t) => (
        <button
          key={t.key}
          className={`rounded-lg px-3 py-1.5 text-sm transition-colors ${
            tab === t.key ? "bg-tray font-semibold shadow-sm" : "font-semibold text-foreground"
          }`}
          type="button"
          onClick={() => setTab(t.key)}
        >
          {t.label}
        </button>
      ))}
    </div>
  );

  if (tab === "lines") {
    return (
      <AnimatedPage className="space-y-4 px-3 py-6 lg:px-6">
        {tabBar}
        <LinesView />
      </AnimatedPage>
    );
  }

  if (tab === "quality") {
    return (
      <AnimatedPage className="space-y-4 px-3 py-6 lg:px-6">
        {tabBar}
        <QualityView />
      </AnimatedPage>
    );
  }

  // The tunnel and rule pages bring their own page padding.
  return (
    <div>
      <div className="px-3 pt-6 lg:px-6">{tabBar}</div>
      {tab === "tunnels" ? <TunnelPage /> : <ForwardPage />}
    </div>
  );
}
