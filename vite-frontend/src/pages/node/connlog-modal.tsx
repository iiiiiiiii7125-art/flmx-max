import type { NodeConnLogItem } from "@/api/types";
import type { Node } from "@/pages/node/types";

import { useCallback, useEffect, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";

import { getNodeConnLog } from "@/api";
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

const isSessionKind = (row: NodeConnLogItem) =>
  row.sessionKind === "udp_session" || row.sessionKind === "kernel_session";

/**
 * Connection log of one node: every client that connected to this
 * server, with the local port it reached. Collection is per-node
 * opt-in (default off): rows only exist after the node's switch is
 * turned on and a connection has passed through. Rows are the node's
 * own records — never merged across machines. Xray rows are
 * event-only by ruling (2026-11-10 (a)): they carry IP + start time
 * and the duration column stays "—".
 */
export function NodeConnLogModal({
  node,
  onClose,
}: {
  node: Node | null;
  onClose: () => void;
}) {
  const [rows, setRows] = useState<NodeConnLogItem[]>([]);
  const [total, setTotal] = useState(0);
  // Server-reported page metadata (O-1): `enabled` is the fallback
  // for the not-enabled gate, `dropped`/`retentionDays` feed the
  // footer (A-P2-1, same shape as the old per-inbound modal).
  const [serverEnabled, setServerEnabled] = useState<boolean | undefined>(
    undefined,
  );
  const [dropped, setDropped] = useState<number | undefined>(undefined);
  const [retentionDays, setRetentionDays] = useState<number | undefined>(
    undefined,
  );
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [ipInput, setIpInput] = useState("");
  const [current, setCurrent] = useState(1);
  const [now, setNow] = useState(() => Date.now());

  const seqRef = useRef(0);
  const nodeIdRef = useRef<number | null>(null);
  const queryRef = useRef({ ip: "", current: 1 });

  const nodeId = node?.id ?? null;
  // List state is the primary gate; the server-reported `enabled` is
  // the fallback — when the list is stale (another tab flipped the
  // switch), a server `false` still shows the not-enabled state.
  const listEnabled = node?.connLogEnabled === true;
  const enabled = listEnabled && serverEnabled !== false;
  // 展示用“采集中”：列表态开且服务端未否定（与 enabled 同值）。
  const isCollecting = enabled;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const load = useCallback(async (pageOverride?: number, ipOverride?: string) => {
    const id = nodeIdRef.current;

    if (id == null) return;
    const seq = (seqRef.current += 1);
    const cur = pageOverride ?? queryRef.current.current;
    const ip = (ipOverride ?? queryRef.current.ip).trim();

    queryRef.current = { ip, current: cur };
    setLoading(true);
    setError(null);
    try {
      const res = await getNodeConnLog({
        nodeId: id,
        offset: (cur - 1) * PAGE_SIZE,
        limit: PAGE_SIZE,
        ...(ip ? { ip } : {}),
      });

      if (seqRef.current !== seq) return;
      if (res.code !== 0) {
        setError(res.msg || "获取连接日志失败");
        setRows([]);
        setTotal(0);
        setServerEnabled(undefined);
        setDropped(undefined);
        setRetentionDays(undefined);

        return;
      }
      const data = res.data;

      if (Array.isArray(data)) {
        setRows(data);
        setTotal(data.length);
        setServerEnabled(undefined);
        setDropped(undefined);
        setRetentionDays(undefined);
      } else if (data) {
        setServerEnabled(
          typeof data.enabled === "boolean" ? data.enabled : undefined,
        );
        setDropped(
          typeof data.dropped === "number" ? data.dropped : undefined,
        );
        setRetentionDays(
          typeof data.retentionDays === "number"
            ? data.retentionDays
            : undefined,
        );
        if (data.enabled === false) {
          // Server says collection is off: show the not-enabled state
          // and don't display stale history rows.
          setRows([]);
          setTotal(0);
        } else {
          setRows(Array.isArray(data.rows) ? data.rows : []);
          setTotal(
            typeof data.total === "number"
              ? data.total
              : data.rows?.length ?? 0,
          );
        }
      } else {
        setRows([]);
        setTotal(0);
        setServerEnabled(undefined);
        setDropped(undefined);
        setRetentionDays(undefined);
      }
      setCurrent(cur);
    } catch {
      if (seqRef.current === seq) {
        setError("获取连接日志失败");
        setRows([]);
        setTotal(0);
        setServerEnabled(undefined);
        setDropped(undefined);
        setRetentionDays(undefined);
      }
    } finally {
      if (seqRef.current === seq) setLoading(false);
    }
  }, []);

  // Open / switch node: reset the query; load the first page only
  // once the node's switch is on. Keyed on the id and the list
  // switch, so flipping the switch while the modal is open reloads
  // (or clears). The load is gated on the list switch (not the
  // server-reported one) so a stale server `false` can't wedge the
  // modal after the user turns the switch back on; the server value
  // is refreshed by that load. Server values reset only when the
  // node changes — a server `false` must survive a switch flip so
  // the not-enabled state keeps showing.
  useEffect(() => {
    const nodeChanged = nodeIdRef.current !== nodeId;
    nodeIdRef.current = nodeId;
    if (nodeId == null) {
      seqRef.current += 1;

      return;
    }
    setIpInput("");
    queryRef.current = { ip: "", current: 1 };
    setCurrent(1);
    setRows([]);
    setTotal(0);
    setDropped(undefined);
    setRetentionDays(undefined);
    setServerEnabled(undefined);
    setError(null);
    if (nodeChanged) {
      setServerEnabled(undefined);
      setDropped(undefined);
      setRetentionDays(undefined);
    }
    if (listEnabled) void load(1, "");
  }, [nodeId, listEnabled, load]);

  // IP filter: debounce, then back to page 1 (user.tsx search pattern).
  useEffect(() => {
    if (nodeId == null || !enabled) return;
    if (ipInput.trim() === queryRef.current.ip) return;
    const timer = setTimeout(() => void load(1, ipInput), 300);

    return () => clearTimeout(timer);
  }, [ipInput, nodeId, enabled, load]);

  // Live ticking durations for open rows.
  useEffect(() => {
    if (nodeId == null) return;
    const timer = window.setInterval(() => setNow(Date.now()), 1000);

    return () => window.clearInterval(timer);
  }, [nodeId]);

  const handlePageChange = (page: number) => {
    if (page < 1 || page > totalPages || page === current) return;
    void load(page);
  };

  const hasEventOnly = rows.some((r) => r.sessionKind === "event_only");
  const hasSession = rows.some(isSessionKind);

  const renderStatus = (row: NodeConnLogItem) => {
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

  const renderDuration = (row: NodeConnLogItem) => {
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
      isOpen={node !== null}
      scrollBehavior="inside"
      size="xl"
      onClose={onClose}
    >
      <ModalContent>
        <ModalHeader className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="truncate text-base font-semibold">
              连接日志{node ? ` - ${node.name}` : ""}
            </div>
            {node && (
              <div className="truncate text-xs font-semibold text-foreground">
                连过这台服务器的 IP 与所连端口
                {isCollecting ? "" : " · 当前未开启"}
              </div>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button
              isDisabled={!isCollecting}
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
            isDisabled={!isCollecting}
            placeholder="按客户端 IP 过滤"
            size="sm"
            value={ipInput}
            onChange={(e) => setIpInput(e.target.value)}
          />
          {!isCollecting ? (
            <div className="py-10 text-center">
              <p className="text-sm font-semibold">连接日志未开启</p>
              <p className="mx-auto mt-2 max-w-md text-xs font-semibold text-foreground">
                这台节点的连接日志开关是关着的，开关打开后才开始采集；有
                客户端连入后，记录才会出现在这里。现在没有可查的记录，并
                不是数据丢了。
              </p>
              <p className="mx-auto mt-2 max-w-md text-xs font-semibold text-foreground">
                打开方法：节点卡片（或节点详情）里的「连接日志」开关打开
                即可。
              </p>
            </div>
          ) : loading && rows.length === 0 ? (
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
                  <TableColumn>本地端口</TableColumn>
                  <TableColumn>入口/入站名</TableColumn>
                  <TableColumn>网络/会话类型</TableColumn>
                  <TableColumn>连入时刻</TableColumn>
                  <TableColumn>断开时刻</TableColumn>
                  <TableColumn>时长</TableColumn>
                </TableHeader>
                <TableBody>
                  {rows.map((row, index) => (
                    <TableRow
                      key={`${row.clientIp}:${row.clientPort ?? ""}:${row.startTime}:${index}`}
                    >
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
                          {row.localPort ?? "—"}
                        </span>
                      </TableCell>
                      <TableCell>
                        <div className="text-sm font-semibold">
                          {row.entryName ?? "—"}
                        </div>
                      </TableCell>
                      <TableCell>
                        <span className="flex items-center gap-1.5 whitespace-nowrap">
                          <span className="font-mono text-sm">
                            {row.network ?? "—"}
                          </span>
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
                      <TableCell>{renderDuration(row)}</TableCell>
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
          ) : (
            <div className="py-10 text-center">
              <p className="text-sm font-semibold">暂无连接记录</p>
              <p className="mx-auto mt-2 max-w-md text-xs font-semibold text-foreground">
                开关已开启，有客户端连入后，记录会出现在这里。
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
                  第 {current} / {totalPages} 页 · 本页 {rows.length} 条 · 共{" "}
                  {total} 条
                </span>
                <Button
                  isDisabled={current >= totalPages || loading}
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
