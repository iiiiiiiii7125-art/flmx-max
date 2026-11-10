import type {
  InboundApiItem,
  InboundConnLogItem,
  NodeApiItem,
} from "@/api/types";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";

import { getInboundConnLog } from "@/api";
import { describeInbound } from "@/pages/inbound/presets";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Input } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";
import {
  Table,
  TableBody,
  TableCell,
  TableColumn,
  TableHeader,
  TableRow,
} from "@/shadcn-bridge/heroui/table";

const PAGE_SIZE = 20;

const formatDateTime = (ts: number): string =>
  new Date(ts).toLocaleString("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });

/** Compact Chinese duration, same style family as formatUptime. */
const formatDurationMs = (ms: number): string => {
  if (!Number.isFinite(ms) || ms < 0) return "—";
  const totalSeconds = Math.floor(ms / 1000);

  if (totalSeconds < 1) return "不到1秒";
  if (totalSeconds < 60) return `${totalSeconds}秒`;
  const minutes = Math.floor(totalSeconds / 60);

  if (minutes < 60) {
    const rest = totalSeconds % 60;

    return rest ? `${minutes}分钟${rest}秒` : `${minutes}分钟`;
  }
  const hours = Math.floor(minutes / 60);

  if (hours < 24) {
    const rest = minutes % 60;

    return rest ? `${hours}小时${rest}分钟` : `${hours}小时`;
  }
  const days = Math.floor(hours / 24);

  return `${days}天${hours % 24}小时`;
};

const isSessionKind = (row: InboundConnLogItem) =>
  row.sessionKind === "udp_session" || row.sessionKind === "kernel_session";

/**
 * Connection log of one inbound (design §5.4): records are collected on
 * the entry machine and attributed to this inbound by topology, so the
 * rows shown here are the entry agent's raw records — never merged
 * across machines. Xray rows are event-only by ruling (2026-11-10 (a)):
 * they carry IP + start time and the duration column stays "—".
 */
export function ConnLogModal({
  item,
  nodes,
  onClose,
}: {
  item: InboundApiItem | null;
  nodes: NodeApiItem[];
  onClose: () => void;
}) {
  const [rows, setRows] = useState<InboundConnLogItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ipInput, setIpInput] = useState("");
  const [current, setCurrent] = useState(1);
  const [hasNext, setHasNext] = useState(false);
  const [dropped, setDropped] = useState<number | undefined>(undefined);
  const [retentionDays, setRetentionDays] = useState<number | undefined>(
    undefined,
  );
  const [now, setNow] = useState(() => Date.now());

  const seqRef = useRef(0);
  const itemIdRef = useRef<number | null>(null);
  const queryRef = useRef({ ip: "", current: 1 });

  const nodeNameById = useMemo(
    () => new Map(nodes.map((n) => [n.id, n.name])),
    [nodes],
  );

  const load = useCallback(
    async (pageOverride?: number, ipOverride?: string) => {
      const inboundId = itemIdRef.current;

      if (inboundId == null) return;
      const seq = (seqRef.current += 1);
      const cur = pageOverride ?? queryRef.current.current;
      const ip = (ipOverride ?? queryRef.current.ip).trim();

      queryRef.current = { ip, current: cur };
      setLoading(true);
      setError(null);
      try {
        // size+1 探针（user.tsx 同口径）：多回的 1 条只判断还有下一页。
        const res = await getInboundConnLog({
          inboundId,
          current: cur,
          size: PAGE_SIZE + 1,
          ...(ip ? { ip } : {}),
        });

        if (seqRef.current !== seq) return;
        if (res.code !== 0) {
          setError(res.msg || "获取连接日志失败");
          setRows([]);
          setHasNext(false);

          return;
        }
        const data = res.data;
        let all: InboundConnLogItem[] = [];

        if (Array.isArray(data)) {
          all = data;
          setDropped(undefined);
          setRetentionDays(undefined);
        } else if (data) {
          all = Array.isArray(data.rows) ? data.rows : [];
          setDropped(data.dropped);
          setRetentionDays(data.retentionDays);
        }
        const next = all.length > PAGE_SIZE;

        setHasNext(next);
        setRows(next ? all.slice(0, PAGE_SIZE) : all);
        setCurrent(cur);
      } catch {
        if (seqRef.current === seq) {
          setError("获取连接日志失败");
          setRows([]);
          setHasNext(false);
        }
      } finally {
        if (seqRef.current === seq) setLoading(false);
      }
    },
    [],
  );

  const itemId = item?.id ?? null;

  // Open / switch inbound: reset the query and load the first page.
  // Keyed on the id only, so a background list refresh handing us a new
  // object for the same inbound does not reset the modal mid-reading.
  useEffect(() => {
    itemIdRef.current = itemId;
    if (itemId == null) {
      seqRef.current += 1;

      return;
    }
    setIpInput("");
    queryRef.current = { ip: "", current: 1 };
    setCurrent(1);
    setRows([]);
    setDropped(undefined);
    setRetentionDays(undefined);
    setError(null);
    void load(1, "");
  }, [itemId, load]);

  // IP filter: debounce, then back to page 1 (user.tsx search pattern).
  useEffect(() => {
    if (itemId == null) return;
    if (ipInput.trim() === queryRef.current.ip) return;
    const timer = setTimeout(() => void load(1, ipInput), 300);

    return () => clearTimeout(timer);
  }, [ipInput, itemId, load]);

  // Live ticking durations for open rows.
  useEffect(() => {
    if (itemId == null) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);

    return () => window.clearInterval(timer);
  }, [itemId]);

  const handlePageChange = (page: number) => {
    if (page < 1 || page === current) return;
    if (page > current && !hasNext) return;
    void load(page);
  };

  const hasEventOnly = rows.some((r) => r.sessionKind === "event_only");
  const hasSession = rows.some(isSessionKind);
  // Direct kernel collection is opt-in (design §4.5); relay-entry
  // records are collected regardless of the switch.
  const directNotOptedIn = item?.connLogEnabled === false;
  // D-05: these morphologies never produce direct records — the backend
  // U7 entry list excludes them (kernel.go:2371), so the switch changes
  // nothing for them. Relay landing covers live relays (relayCount)
  // and dead ones (relayInvalidCount): the backend target set takes
  // all managed relays, not only live ones. Chain targets are the
  // fourth morphology but are not visible from this modal's props
  // (item + nodes only, no inbound list), so they keep the generic
  // empty state until a caller passes the list or a backend flag.
  const noDirectRecords =
    item != null &&
    (item.relayOnly ||
      item.relayCount > 0 ||
      (item.relayInvalidCount ?? 0) > 0 ||
      item.external);
  const noDirectReason = item?.relayOnly
    ? "仅中转入站"
    : item != null &&
        (item.relayCount > 0 || (item.relayInvalidCount ?? 0) > 0)
      ? "中转落地目标"
      : item?.external
        ? "导入入站"
        : null;
  const showNoDirectEmptyState =
    !loading && !error && rows.length === 0 && noDirectRecords;
  const showOptInEmptyState =
    !loading && !error && rows.length === 0 && directNotOptedIn;

  const renderStatus = (row: InboundConnLogItem) => {
    if (row.sessionKind === "event_only") {
      return (
        <Chip className="whitespace-nowrap" size="sm" variant="flat">
          连接事件
        </Chip>
      );
    }
    if (row.status === "open") {
      return (
        <Chip
          className="whitespace-nowrap"
          color="success"
          size="sm"
          variant="flat"
        >
          <span className="flex items-center gap-1.5">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-success" />
            连接中
          </span>
        </Chip>
      );
    }
    if (row.status === "interrupted") {
      return (
        <Chip
          className="whitespace-nowrap"
          color="warning"
          size="sm"
          variant="flat"
        >
          已中断
        </Chip>
      );
    }

    return (
      <Chip className="whitespace-nowrap" size="sm" variant="flat">
        已断开
      </Chip>
    );
  };

  const renderDuration = (row: InboundConnLogItem) => {
    if (row.sessionKind === "event_only") return "—";
    if (row.status === "open") {
      return (
        <span className="font-mono">
          {formatDurationMs(now - row.startTime)}
        </span>
      );
    }
    if (row.status === "interrupted") {
      return (
        <span className="text-xs font-semibold text-warning-700">
          中断（不完整）
        </span>
      );
    }
    const ms =
      row.durationMs ??
      (row.endTime != null ? row.endTime - row.startTime : null);

    return (
      <span className="font-mono">
        {ms != null ? formatDurationMs(ms) : "—"}
      </span>
    );
  };

  return (
    <Modal
      isOpen={item !== null}
      scrollBehavior="inside"
      size="xl"
      onClose={onClose}
    >
      <ModalContent>
        <ModalHeader className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="truncate text-base font-semibold">
              连接日志{item ? ` - ${item.name}` : ""}
            </div>
            {item && (
              <div className="truncate text-xs font-semibold text-foreground">
                {item.external
                  ? item.listen
                  : `${item.nodeName} · ${describeInbound(item)} · 端口 ${item.port}`}
                {" · 记录在入口节点采集，按拓扑归属到本入站"}
              </div>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button
              isLoading={loading}
              size="sm"
              variant="flat"
              onPress={() => void load()}
            >
              <RefreshCw className="mr-1 h-4 w-4" />
              刷新
            </Button>
          </div>
        </ModalHeader>
        <ModalBody>
          <Input
            aria-label="按客户端 IP 过滤"
            className="max-w-xs"
            placeholder="按客户端 IP 过滤"
            size="sm"
            value={ipInput}
            onChange={(e) => setIpInput(e.target.value)}
          />
          {loading && rows.length === 0 ? (
            <div className="flex justify-center py-10">
              <RefreshCw className="h-6 w-6 animate-spin" />
            </div>
          ) : error ? (
            <div className="py-10 text-center">
              <p className="text-sm font-semibold text-danger-700">{error}</p>
              <Button
                className="mt-3"
                size="sm"
                variant="flat"
                onPress={() => void load()}
              >
                重试
              </Button>
            </div>
          ) : rows.length > 0 ? (
            <>
              <Table
                aria-label="连接日志"
                className="min-w-[40rem]"
                classNames={{
                  wrapper:
                    "bg-transparent p-0 shadow-none border-none overflow-x-auto rounded-2xl",
                  th: "bg-transparent text-foreground font-semibold text-sm border-b border-white/20 py-3 uppercase tracking-wider first:rounded-tl-[24px] last:rounded-tr-[24px] whitespace-nowrap",
                  td: "py-3 border-b border-divider/50 group-data-[last=true]:border-b-0 whitespace-nowrap",
                  tr: "hover:bg-white/40 transition-colors",
                }}
              >
                <TableHeader>
                  <TableColumn>状态</TableColumn>
                  <TableColumn>客户端 IP</TableColumn>
                  <TableColumn>连接时间</TableColumn>
                  <TableColumn>断开时间</TableColumn>
                  <TableColumn>时长</TableColumn>
                  <TableColumn>来源入口</TableColumn>
                </TableHeader>
                <TableBody>
                  {rows.map((row) => (
                    <TableRow key={row.id ?? row.connId}>
                      <TableCell>{renderStatus(row)}</TableCell>
                      <TableCell>
                        <div className="font-mono text-sm">
                          {row.clientIp}
                          {row.clientPort ? `:${row.clientPort}` : ""}
                        </div>
                        {row.peerIp && (
                          <div className="text-xs font-semibold text-foreground">
                            上一跳 {row.peerIp}
                          </div>
                        )}
                      </TableCell>
                      <TableCell>
                        <span className="font-mono text-sm">
                          {formatDateTime(row.startTime)}
                        </span>
                      </TableCell>
                      <TableCell>
                        {row.status === "open" || row.endTime == null ? (
                          "—"
                        ) : (
                          <>
                            <span className="font-mono text-sm">
                              {formatDateTime(row.endTime)}
                            </span>
                            {row.status === "interrupted" && (
                              <div className="text-xs font-semibold text-foreground">
                                最后已知时刻
                              </div>
                            )}
                          </>
                        )}
                      </TableCell>
                      <TableCell>
                        <span className="flex items-center gap-1.5 whitespace-nowrap">
                          {renderDuration(row)}
                          {isSessionKind(row) && (
                            <Chip
                              className="whitespace-nowrap"
                              color="secondary"
                              size="sm"
                              variant="flat"
                            >
                              会话
                            </Chip>
                          )}
                        </span>
                      </TableCell>
                      <TableCell>
                        <div className="text-sm font-semibold">
                          {row.entryNodeName ??
                            nodeNameById.get(row.entryNodeId) ??
                            `节点 ${row.entryNodeId}`}
                        </div>
                        <div className="text-xs font-semibold text-foreground">
                          {row.entryKind === "forward"
                            ? "中转入口"
                            : "直连入口"}
                          {row.entryId ? ` · ${row.entryId}` : ""}
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              {hasEventOnly && (
                <p className="text-xs font-semibold text-foreground">
                  注：Xray 仅提供连接事件，无时长，时长列以「—」表示。
                </p>
              )}
              {hasSession && (
                <p className="text-xs font-semibold text-foreground">
                  注：带「会话」徽标的行为会话口径记录（UDP
                  无逐连接生命周期），起止为会话首末活动与超时回收时刻，精度为会话
                  TTL 级。
                </p>
              )}
            </>
          ) : showNoDirectEmptyState ? (
            <div className="py-10 text-center">
              <p className="text-sm font-semibold">该入站无直连连接记录</p>
              <p className="mx-auto mt-2 max-w-md text-xs font-semibold text-foreground">
                该入站为{noDirectReason}，直连连接不会产生连接记录
                {item?.external
                  ? "：导入节点不由面板部署，连接日志不在面板采集。"
                  : "；中转入口的连接记录请在对应的入口入站的连接日志中查看。"}
              </p>
            </div>
          ) : showOptInEmptyState ? (
            <div className="py-10 text-center">
              <p className="text-sm font-semibold">直连连接日志尚未开启</p>
              <div className="mx-auto mt-3 max-w-md space-y-1.5 text-left text-xs font-semibold text-foreground">
                <p>
                  直连到本入站的连接默认不记录。开启方法：关闭本弹窗 →
                  点本入站的「编辑」 →
                  在入站表单中找到「记录连接日志」开关并打开 →
                  保存。开启后新连入的连接会被记录。
                </p>
                {item && item.relayCount > 0 && (
                  <p>
                    本入站的中转入口连接由入口节点自动记录、不受此开关影响，目前暂无记录。
                  </p>
                )}
                {item && item.relayCount === 0 && (
                  <p>本入站暂无中转入口，记录来源只有直连这一条。</p>
                )}
              </div>
            </div>
          ) : (
            <div className="py-10 text-center">
              <p className="text-sm font-semibold">暂无连接记录</p>
              <p className="mx-auto mt-2 max-w-md text-xs font-semibold text-foreground">
                有客户端连入后，连接记录会出现在这里：中转入口的连接由入口节点自动记录；直连连接需先在「编辑入站」中打开「记录连接日志」开关。
              </p>
            </div>
          )}
        </ModalBody>
        <ModalFooter className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-0.5 text-left text-xs font-semibold text-foreground">
            <p>
              {retentionDays != null
                ? `连接记录保留 ${retentionDays} 天，超期或超量自动清理`
                : "连接记录按保留策略定期清理"}
            </p>
            {dropped != null && dropped > 0 && (
              <p className="text-warning-700">
                本节点累计丢弃 {dropped} 条记录（清空连接行后归零）
              </p>
            )}
          </div>
          <div className="flex items-center gap-3">
            {(rows.length > 0 || current > 1) && (
              <>
                <Button
                  isDisabled={current <= 1 || loading}
                  size="sm"
                  variant="flat"
                  onPress={() => handlePageChange(current - 1)}
                >
                  上一页
                </Button>
                <span className="text-sm font-medium text-foreground">
                  第 {current} 页 · 本页 {rows.length} 条
                </span>
                <Button
                  isDisabled={!hasNext || loading}
                  size="sm"
                  variant="flat"
                  onPress={() => handlePageChange(current + 1)}
                >
                  下一页
                </Button>
              </>
            )}
            <Button variant="flat" onPress={onClose}>
              关闭
            </Button>
          </div>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
