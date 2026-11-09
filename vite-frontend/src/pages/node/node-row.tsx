import { memo } from "react";
import { GripVertical } from "lucide-react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Progress } from "@/shadcn-bridge/heroui/progress";
import { LatencyCell, probePoints } from "@/components/latency-cell";
import { ListRow } from "@/components/page-kit";
import { getConnectionStatusMeta } from "@/pages/node/display";
import {
  formatUptime,
  getNodeExpiryMeta,
  SortableItem,
} from "@/pages/node/helpers";
import { NodeLiveStats } from "@/pages/node/node-live-stats";
import {
  formatNodeRenewalTime,
  getNodeRenewalCycleLabel,
} from "@/pages/node/renewal";
import type { Node } from "@/pages/node/types";

export interface NodeUpgradeProgressEntry {
  stage: string;
  percent: number;
  message: string;
}

/**
 * Every callback a row needs, bundled into one object. The node page keeps
 * this object referentially stable across renders (it delegates to the
 * latest closures through a ref), so memoized rows are not invalidated by
 * fresh inline closures on every page render.
 */
export interface NodeRowActions {
  toggleSelect: (id: number) => void;
  dismissExpiryReminder: (id: number) => void;
  updateInfoPopoverPlacement: (
    id: number,
    triggerElement: HTMLElement | null,
  ) => void;
  openInstallSelector: (node: Node) => void;
  openUpgradeModal: (mode: "batch" | "single", nodeId?: number) => void;
  rollbackNode: (node: Node) => void;
  openTune: (node: Node) => void;
  editNode: (node: Node) => void;
  deleteNode: (node: Node) => void;
  probeNode: (id: number) => void;
  editLatencyTargets: (node: Node) => void;
  openLatencySettings: () => void;
  showRoute: (node: Node) => void;
  showTrend: (node: Node) => void;
  openSheet: (id: number) => void;
  openMonitor?: (nodeId: number) => void;
}

interface NodeRowCommonProps {
  node: Node;
  actions: NodeRowActions;
  selected: boolean;
  selectMode: boolean;
  upgradeProgressEntry?: NodeUpgradeProgressEntry;
  /** This month's NIC traffic as separate numbers: the raw record entry is
   * replaced wholesale on every poll, numbers keep row props stable. */
  monthIn?: number;
  monthOut?: number;
  monthLabel: string | null;
  latencyPoolEmpty: boolean | null;
  probing: boolean;
}

export interface NodeCardRowProps extends NodeRowCommonProps {
  infoPlacement: "left" | "bottom";
  showMonitor: boolean;
}

export interface NodeListRowProps extends NodeRowCommonProps {
  isDisplayFiltered: boolean;
}

type RowContentProps<P> = P & { listeners: any };

const monthTuple = (
  monthIn?: number,
  monthOut?: number,
): [number, number] | undefined =>
  monthIn === undefined || monthOut === undefined
    ? undefined
    : [monthIn, monthOut];

/**
 * Latency slot shared by both row variants (renderLatency from the page,
 * moved here with the rows). Explicitly empty target selection
 * (latencyTargets.points is an empty array) means "do not probe": render
 * the settings pill (A1-015); an empty global target pool shows the
 * go-to-settings hint; no result yet renders nothing (DA-4).
 */
function renderLatencySlot({
  node,
  variant,
  latencyPoolEmpty,
  probing,
  actions,
}: {
  node: Node;
  variant: "card" | "list";
  latencyPoolEmpty: boolean | null;
  probing: boolean;
  actions: NodeRowActions;
}) {
  if (node.connectionStatus !== "online") {
    return null;
  }

  if (node.latencyTargets?.points.length === 0) {
    return (
      <button
        className="rounded-full bg-tray px-2 py-0.5 text-xs font-semibold text-foreground hover:text-default-700"
        title="这台节点没选测试点，点此设置"
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          actions.editLatencyTargets(node);
        }}
      >
        不测延迟
      </button>
    );
  }

  if (latencyPoolEmpty === true) {
    return (
      <button
        className="rounded-full bg-tray px-2 py-0.5 text-xs font-semibold text-foreground hover:text-default-700"
        title="还没有配置延迟测试点，点此去设置添加"
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          actions.openLatencySettings();
        }}
      >
        未配置测试点，去设置添加
      </button>
    );
  }

  if (probePoints(node.latency).length === 0) {
    return null;
  }

  return (
    <LatencyCell
      note={node.latencyTargets ? "自定义" : undefined}
      probe={node.latency}
      probing={probing}
      variant={variant}
      onEditTargets={() => actions.editLatencyTargets(node)}
      onProbe={() => actions.probeNode(node.id)}
      onShowRoute={() => actions.showRoute(node)}
      onShowTrend={() => actions.showTrend(node)}
    />
  );
}

const NodeCardContent = memo(function NodeCardContent({
  node,
  actions,
  selected,
  selectMode,
  upgradeProgressEntry,
  monthIn,
  monthOut,
  monthLabel,
  infoPlacement,
  latencyPoolEmpty,
  probing,
  showMonitor,
  listeners,
}: RowContentProps<NodeCardRowProps>) {
  const expiryMeta = getNodeExpiryMeta(
    node.expiryTime,
    node.renewalCycle,
  );
  const connectionStatusMeta = getConnectionStatusMeta(
    node.connectionStatus,
  );
  const hasRemark = Boolean(node.remark?.trim());
  const hasExpiryInfo = Boolean(
    node.expiryTime &&
      node.expiryTime > 0 &&
      node.renewalCycle &&
      !node.expiryReminderDismissed,
  );
  const hasInfoTrigger = hasRemark || hasExpiryInfo;
  const infoCount = Number(hasExpiryInfo) + Number(hasRemark);
  const month = monthTuple(monthIn, monthOut);
  const renderRowLatency = () =>
    renderLatencySlot({ node, variant: "card", latencyPoolEmpty, probing, actions });

  return (
    <Card
      key={node.id}
      className={`group relative overflow-visible h-full flex flex-col ${node.expiryReminderDismissed ? "" : expiryMeta.accentClassName}`}
      data-node-card="true"
    >
      <CardHeader className="pb-3 md:pb-3">
        <div className="flex justify-between items-start w-full gap-3">
          <div className="flex items-start gap-2 flex-1 min-w-0">
            <div
              className="cursor-grab active:cursor-grabbing p-2 -ml-2 -mt-1 text-foreground hover:text-foreground transition-colors touch-manipulation opacity-100 sm:opacity-0 sm:group-hover:opacity-100 flex-shrink-0"
              {...listeners}
              style={{ touchAction: "none" }}
              title="拖拽排序"
            >
              <svg
                aria-hidden="true"
                className="w-4 h-4"
                fill="currentColor"
                viewBox="0 0 20 20"
              >
                <path d="M7 2a2 2 0 1 1 .001 4.001A2 2 0 0 1 7 2zm0 6a2 2 0 1 1 .001 4.001A2 2 0 0 1 7 8zm0 6a2 2 0 1 1 .001 4.001A2 2 0 0 1 7 14zm6-8a2 2 0 1 1-.001-4.001A2 2 0 0 1 13 6zm0 2a2 2 0 1 1 .001 4.001A2 2 0 0 1 13 8zm0 6a2 2 0 1 1 .001 4.001A2 2 0 0 1 13 14z" />
              </svg>
            </div>
            {selectMode && (
              <Checkbox
                className="mt-0.5"
                isSelected={selected}
                onValueChange={() => actions.toggleSelect(node.id)}
              />
            )}
            <div className="min-w-0 flex-1 pt-0.5">
              <div className="flex items-center gap-2 min-w-0">
                <h3 className="font-semibold text-foreground truncate text-lg leading-5">
                  {node.name}
                </h3>
                <div
                  className={`flex items-center gap-1.5 px-2 py-0.5 rounded-full text-xs font-semibold flex-shrink-0
                  ${connectionStatusMeta.color === "success" ? "bg-emerald-100 text-emerald-700" : "bg-rose-100 text-rose-700"}
                `}
                  title={connectionStatusMeta.text}
                >
                  <span
                    className={`h-1.5 w-1.5 rounded-full ${connectionStatusMeta.color === "success" ? "bg-emerald-500" : "bg-rose-500"}`}
                  />
                  {connectionStatusMeta.text}
                </div>
              </div>
            </div>
          </div>
          <div className="ml-2 flex-shrink-0 self-start">
            {hasInfoTrigger && (
              <div className="group/info relative">
                <button
                  aria-label={`查看节点信息，共 ${infoCount} 项`}
                  className="relative flex h-7 w-7 items-center justify-center rounded-full border border-divider/80 glass-inset text-foreground shadow-sm transition hover:border-default-300 hover:text-foreground focus-visible:border-default-300 focus-visible:text-foreground focus-visible:outline-none"
                  type="button"
                  onFocus={(event) =>
                    actions.updateInfoPopoverPlacement(
                      node.id,
                      event.currentTarget,
                    )
                  }
                  onMouseEnter={(event) =>
                    actions.updateInfoPopoverPlacement(
                      node.id,
                      event.currentTarget,
                    )
                  }
                >
                  <svg
                    aria-hidden="true"
                    className="h-3.5 w-3.5"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                  >
                    <path
                      d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={1.8}
                    />
                  </svg>
                  {hasRemark && (
                    <span className="absolute -right-1 -top-1 flex h-2.5 w-2.5 rounded-full border border-background bg-default-300 shadow-sm" />
                  )}
                </button>
                <div
                  className={`pointer-events-none invisible absolute z-[60] w-72 max-w-[min(18rem,calc(100vw-4rem))] rounded-xl glass-surface p-3 opacity-0 shadow-lg transition-all duration-150 group-hover/info:visible group-hover/info:pointer-events-auto group-hover/info:opacity-100 group-focus-within/info:visible group-focus-within/info:pointer-events-auto group-focus-within/info:opacity-100 ${
                    infoPlacement === "bottom"
                      ? "right-0 top-[calc(100%+0.75rem)] translate-y-1 group-hover/info:translate-y-0 group-focus-within/info:translate-y-0"
                      : "right-[calc(100%+0.75rem)] top-1/2 -translate-y-1/2 translate-x-1 group-hover/info:translate-x-0 group-focus-within/info:translate-x-0"
                  }`}
                >
                  <div className="space-y-3">
                    {hasExpiryInfo && (
                      <div className="space-y-2">
                        <div className="flex items-center justify-between">
                          <div className="text-[11px] font-semibold text-foreground">
                            到期提醒
                          </div>
                          <button
                            className="text-[10px] font-semibold text-foreground hover:text-foreground transition-colors"
                            type="button"
                            onClick={(e) => {
                              e.stopPropagation();
                              actions.dismissExpiryReminder(
                                node.id,
                              );
                            }}
                          >
                            关闭提醒
                          </button>
                        </div>
                        <div className="flex flex-wrap gap-1.5">
                          <Chip
                            className="text-[10px] font-semibold h-5 px-1 flex-shrink-0"
                            color={expiryMeta.tone}
                            size="sm"
                            title={`${formatNodeRenewalTime(expiryMeta.nextDueTime)} (${getNodeRenewalCycleLabel(node.renewalCycle)})`}
                            variant="flat"
                          >
                            {expiryMeta.label}
                          </Chip>
                        </div>
                        <div className="rounded-lg border border-divider/80 glass-inset px-3 py-2 text-xs font-semibold leading-5 text-default-700">
                          {formatNodeRenewalTime(
                            expiryMeta.nextDueTime,
                          )}
                        </div>
                      </div>
                    )}

                    {hasRemark && (
                      <div className="space-y-2">
                        <div className="text-[11px] font-semibold text-foreground">
                          备注
                        </div>
                        <div
                          className="max-h-32 overflow-y-auto rounded-lg border border-divider/80 glass-inset px-3 py-2 text-xs font-semibold leading-5 text-default-700 break-all [scrollbar-width:thin]"
                          title={node.remark?.trim()}
                        >
                          {node.remark?.trim()}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            )}
          </div>
        </div>
      </CardHeader>

      <CardBody className="pt-0 pb-3 md:pt-0 md:pb-3 flex-1 flex flex-col">
        {/* 基础信息 */}
        <div className="space-y-2 mb-4">
          {node.expiryTime &&
            node.expiryTime > 0 &&
            node.renewalCycle && <div className="hidden" />}
          <div className="flex justify-between items-center text-sm min-w-0">
            <span className="text-foreground flex-shrink-0">
              IP
            </span>
            <div className="text-right text-xs font-semibold min-w-0 flex-1 ml-2 min-h-[2.125rem]">
              {node.serverIpV4?.trim() ||
              node.serverIpV6?.trim() ? (
                <div className="space-y-0.5">
                  {node.serverIpV4?.trim() && (
                    <span
                      className="font-mono truncate block"
                      title={node.serverIpV4.trim()}
                    >
                      {node.serverIpV4.trim()}
                    </span>
                  )}
                  {node.serverIpV6?.trim() && (
                    <span
                      className="font-mono truncate block"
                      title={node.serverIpV6.trim()}
                    >
                      {node.serverIpV6.trim()}
                    </span>
                  )}
                </div>
              ) : (
                <span
                  className="font-mono truncate block"
                  title={node.serverIp.trim()}
                >
                  {node.serverIp.trim()}
                </span>
              )}
            </div>
          </div>
          <>
              <div className="flex justify-between text-sm">
                <span className="text-foreground">
                  版本
                </span>
                <span className="text-xs font-semibold">
                  {node.version || "未知"}
                </span>
              </div>
              {node.upgradeVerification?.status === "failed" && (
                <div className="mt-1 text-xs font-semibold text-danger-700">
                  {node.upgradeVerification.kind === "rollback"
                    ? "回退未落地"
                    : "升级未落地"}
                  ：期望 {node.upgradeVerification.targetVersion}
                  {node.upgradeVerification.observedVersion
                    ? `，实际 ${node.upgradeVerification.observedVersion}`
                    : "，尚未回连"}
                </div>
              )}
              {node.upgradeVerification?.status === "pending" && (
                <div className="mt-1 text-xs font-semibold text-warning-700">
                  {node.upgradeVerification.kind === "rollback"
                    ? "回退已下发"
                    : "升级已下发"}
                  ：等待节点以 {node.upgradeVerification.targetVersion}{" "}
                  回连核验
                </div>
              )}
              {upgradeProgressEntry &&
                upgradeProgressEntry.percent < 100 && (
                  <div className="mt-1">
                    <Progress
                      showValueLabel
                      aria-label="升级进度"
                      color="warning"
                      label={
                        upgradeProgressEntry.message
                      }
                      size="sm"
                      value={
                        upgradeProgressEntry.percent
                      }
                    />
                  </div>
                )}
              <div className="flex justify-between text-sm">
                <span className="text-foreground">
                  开机时间
                </span>
                <span className="text-xs font-semibold">
                  {node.connectionStatus === "online" &&
                  node.systemInfo
                    ? formatUptime(node.systemInfo.uptime)
                    : "-"}
                </span>
              </div>
              {monthLabel && (
                <div className="flex justify-between text-sm">
                  <span className="text-foreground">
                    本月流量（网卡）
                  </span>
                  <span className="text-xs font-semibold">
                    {monthLabel}
                  </span>
                </div>
              )}
          </>
        </div>

        {node.latency ||
        latencyPoolEmpty ||
        node.latencyTargets?.points.length === 0 ? (
          <div className="mb-4 empty:hidden">
            {renderRowLatency()}
          </div>
        ) : null}

        <div className="mb-4 space-y-2">
            <NodeLiveStats
              info={node.systemInfo ?? null}
              month={month}
              variant="card"
            />
            {showMonitor && actions.openMonitor && (
              <Button
                className="min-h-8 w-full"
                size="sm"
                variant="flat"
                onPress={() => actions.openMonitor?.(node.id)}
              >
                监控
              </Button>
            )}
          </div>

        <div className="mt-auto space-y-3">
          {/* 操作按钮 */}
          <div className="space-y-1.5">
            <div className="grid grid-cols-3 gap-1.5">
                <Button
                  className="min-h-8"
                  color="success"
                  isLoading={node.copyLoading}
                  size="sm"
                  variant="flat"
                  onPress={() => actions.openInstallSelector(node)}
                >
                  安装
                </Button>
                <Button
                  className="min-h-8"
                  color="warning"
                  isDisabled={
                    node.connectionStatus !== "online"
                  }
                  isLoading={node.upgradeLoading}
                  size="sm"
                  variant="flat"
                  onPress={() =>
                    actions.openUpgradeModal("single", node.id)
                  }
                >
                  升级
                </Button>
                <Button
                  className="min-h-8"
                  color="secondary"
                  isDisabled={
                    node.connectionStatus !== "online"
                  }
                  isLoading={node.rollbackLoading}
                  size="sm"
                  variant="flat"
                  onPress={() => actions.rollbackNode(node)}
                >
                  回退
                </Button>
              </div>
            <div className="grid gap-1.5 grid-cols-3">
              {/* R2X-041：调优仅 agent 模式有入口（nodes.md:161），
                  nftables 节点无 agent 通道、点了必失败，故不渲染 */}
              {node.forwardMode !== "nftables" && (
                  <Button
                    className="min-h-8"
                    color="success"
                    isDisabled={
                      node.connectionStatus !== "online"
                    }
                    size="sm"
                    variant="flat"
                    onPress={() => actions.openTune(node)}
                  >
                    调优
                  </Button>
                )}
              <Button
                className="min-h-8"
                color="primary"
                size="sm"
                variant="flat"
                onPress={() => actions.editNode(node)}
              >
                编辑
              </Button>
              <Button
                className="min-h-8"
                color="danger"
                size="sm"
                variant="flat"
                onPress={() => actions.deleteNode(node)}
              >
                删除
              </Button>
            </div>
          </div>
        </div>
      </CardBody>
    </Card>
  );
});

export const NodeCardRow = memo(function NodeCardRow(
  props: NodeCardRowProps,
) {
  return (
    <SortableItem id={props.node.id}>
      {(listeners) => <NodeCardContent {...props} listeners={listeners} />}
    </SortableItem>
  );
});

const NodeListContent = memo(function NodeListContent({
  node,
  actions,
  selected,
  selectMode,
  upgradeProgressEntry,
  monthIn,
  monthOut,
  monthLabel,
  latencyPoolEmpty,
  probing,
  isDisplayFiltered,
  listeners,
}: RowContentProps<NodeListRowProps>) {
  const online = node.connectionStatus === "online";
  const expiryMeta = getNodeExpiryMeta(
    node.expiryTime,
    node.renewalCycle,
  );
  const showExpiry = Boolean(
    node.expiryTime &&
      node.expiryTime > 0 &&
      node.renewalCycle &&
      !node.expiryReminderDismissed &&
      expiryMeta.isHighlighted,
  );
  const progress = upgradeProgressEntry;
  const upgrading = progress && progress.percent < 100;
  const address =
    node.serverIpV4?.trim() ||
    node.serverIpV6?.trim() ||
    node.serverIp.trim();
  const meta = [
    address,
    upgrading
      ? `升级中 ${progress.percent}%`
      : node.version || "未连接过",
    online && node.systemInfo
      ? `已运行 ${formatUptime(node.systemInfo.uptime)}`
      : "",
    monthLabel ? `本月网卡 ${monthLabel}` : "",
  ];
  const month = monthTuple(monthIn, monthOut);
  const renderRowLatency = () =>
    renderLatencySlot({ node, variant: "list", latencyPoolEmpty, probing, actions });

  return (
    <ListRow
      badges={
        <>
          {showExpiry && (
            <Chip
              className="shrink-0"
              color={expiryMeta.tone}
              size="sm"
              variant="flat"
            >
              {expiryMeta.label}
            </Chip>
          )}
        </>
      }
      footer={
        <NodeLiveStats
          info={node.systemInfo ?? null}
          month={month}
          variant="footer"
        />
      }
      meta={meta.filter(Boolean).join(" · ")}
      right={
        <>
          {renderRowLatency()}
          {!isDisplayFiltered && (
            <span
              className="cursor-grab touch-none p-1 text-foreground hover:text-foreground active:cursor-grabbing"
              title="拖动排序"
              {...listeners}
            >
              <GripVertical className="h-4 w-4" />
            </span>
          )}
        </>
      }
      selectMode={selectMode}
      selected={selected}
      status={upgrading ? "warn" : online ? "ok" : "bad"}
      statusTitle={
        getConnectionStatusMeta(node.connectionStatus).text
      }
      title={node.name}
      onPress={() => actions.openSheet(node.id)}
      onSelect={() => actions.toggleSelect(node.id)}
    />
  );
});

export const NodeListRow = memo(function NodeListRow(
  props: NodeListRowProps,
) {
  return (
    <SortableItem id={props.node.id}>
      {(listeners) => <NodeListContent {...props} listeners={listeners} />}
    </SortableItem>
  );
});
