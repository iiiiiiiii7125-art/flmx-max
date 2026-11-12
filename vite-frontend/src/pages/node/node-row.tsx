import { memo } from "react";
import { GripVertical } from "lucide-react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Input } from "@/shadcn-bridge/heroui/input";
import { Progress } from "@/shadcn-bridge/heroui/progress";
import { Switch } from "@/shadcn-bridge/heroui/switch";
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
  openSwap: (node: Node) => void;
  editNode: (node: Node) => void;
  deleteNode: (node: Node) => void;
  probeNode: (id: number) => void;
  editLatencyTargets: (node: Node) => void;
  openLatencySettings: () => void;
  showRoute: (node: Node) => void;
  showTrend: (node: Node) => void;
  openSheet: (id: number) => void;
  openMonitor?: (nodeId: number) => void;
  openConnLog: (node: Node) => void;
  toggleConnLog: (node: Node, enabled: boolean) => void;
  toggleWhitelist: (node: Node, enabled: boolean) => void;
  saveWhitelistExtra: (node: Node, value: string) => void;
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
  /** 管理员才可见连接日志入口与开关（后端亦 enforcing；非管理员不渲染）。 */
  showConnLog: boolean;
  /** 该节点连接日志开关正在保存中，保存期间禁用开关防重复提交。 */
  connLogSaving?: boolean;
  /** 管理员才可见入站白名单开关与输入框（后端亦 enforcing；非管理员不渲染）。 */
  showWhitelist: boolean;
  /** 该节点白名单额外放行输入框的当前编辑文本（失焦/回车时保存）。 */
  whitelistDraft: string;
  onWhitelistDraftChange: (node: Node, value: string) => void;
  /** 该节点白名单开关或额外列表正在保存中，保存期间禁用相关控件防重复提交。 */
  whitelistSaving?: boolean;
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

/**
 * Upgrade/rollback landing-verification status line (A4-V-06),
 * shared by the card and list row variants. It renders only for
 * the two open states the API exposes (pending / failed); a landed
 * or absent verification renders nothing. Both views must render
 * it through this one component — F-211-02: the line previously
 * existed only in the card content, so it was invisible in the
 * default list view while a verification was open.
 */
function NodeUpgradeVerificationLine({ node }: { node: Node }) {
  const verification = node.upgradeVerification;

  if (verification?.status === "failed") {
    return (
      <div className="mt-1 text-xs font-semibold text-danger-700">
        {verification.kind === "rollback" ? "回退未落地" : "升级未落地"}
        ：期望 {verification.targetVersion}
        {verification.observedVersion
          ? `，实际 ${verification.observedVersion}`
          : "，尚未回连"}
      </div>
    );
  }
  if (verification?.status === "pending") {
    return (
      <div className="mt-1 text-xs font-semibold text-warning-700">
        {verification.kind === "rollback" ? "回退已下发" : "升级已下发"}
        ：等待节点以 {verification.targetVersion} 回连核验
      </div>
    );
  }
  return null;
}

/** C4-G2 四态呈现：卸载登记状态芯片（五态不合并）。后端
 * attachNodeUninstallStates 透出 uninstallState/uninstallReason，
 * 无登记的行不渲染。failed/unconfirmed 的原因文案常驻呈现。 */
const UNINSTALL_STATE_META: Record<
  string,
  { label: string; color: "warning" | "success" | "danger" }
> = {
  issued: { label: "卸载已下发", color: "warning" },
  acked: { label: "卸载执行中", color: "warning" },
  succeeded: { label: "卸载已完成", color: "success" },
  failed: { label: "卸载失败", color: "danger" },
  unconfirmed: { label: "卸载结果未确认", color: "danger" },
};

export function NodeUninstallStateChip({ node }: { node: Node }) {
  if (!node.uninstallState) return null;
  const meta = UNINSTALL_STATE_META[node.uninstallState] ?? {
    label: `卸载状态：${node.uninstallState}`,
    color: "warning" as const,
  };
  const reason = node.uninstallReason?.trim();
  const showReason =
    reason &&
    (node.uninstallState === "failed" ||
      node.uninstallState === "unconfirmed");

  return (
    <span className="inline-flex min-w-0 items-center gap-1">
      <Chip
        className="shrink-0 whitespace-nowrap"
        color={meta.color}
        size="sm"
        title={reason || meta.label}
        variant="flat"
      >
        {meta.label}
      </Chip>
      {showReason && (
        <span
          className="min-w-0 truncate text-xs font-semibold text-danger-700"
          title={reason}
        >
          {reason}
        </span>
      )}
    </span>
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
  showConnLog,
  connLogSaving,
  showWhitelist,
  whitelistDraft,
  onWhitelistDraftChange,
  whitelistSaving,
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
                {/* C4-G2：卸载登记状态芯片（五态不合并），与在线状态并列。 */}
                <NodeUninstallStateChip node={node} />
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
              <NodeUpgradeVerificationLine node={node} />
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
          {/* 连接日志开关：按节点 opt-in，默认关。nftables 节点无 agent
              通道、采集无从谈起，故不渲染（与调优/Swap 同口径）。 */}
          {showConnLog && node.forwardMode !== "nftables" && (
            <div className="flex items-center justify-between gap-2 rounded-xl border border-divider/80 px-3 py-2">
              <span className="text-sm font-semibold">连接日志</span>
              <span className="flex items-center gap-2">
                <span className="text-xs font-semibold text-foreground">
                  {node.connLogEnabled === true ? "已开启" : "未开启"}
                </span>
                <Switch
                  aria-label="连接日志开关"
                  isDisabled={connLogSaving}
                  isSelected={node.connLogEnabled === true}
                  size="sm"
                  onValueChange={(v) => actions.toggleConnLog(node, v)}
                />
              </span>
            </div>
          )}
          {/* 入站白名单：节点级覆盖层，默认关。开时本节点全部入站只放行
              中转机、本机与额外列表中的 IP/网段；逐入站白名单（入站表单）
              保留不动，两层叠加生效。nftables 节点无 agent 通道，不渲染。 */}
          {showWhitelist && node.forwardMode !== "nftables" && (
            <div className="space-y-2 rounded-xl border border-divider/80 px-3 py-2">
              <div className="flex items-center justify-between gap-2">
                <span className="text-sm font-semibold">入站白名单</span>
                <span className="flex items-center gap-2">
                  <span className="text-xs font-semibold text-foreground">
                    {node.whitelistEnabled === true ? "已开启" : "未开启"}
                  </span>
                  <Switch
                    aria-label="入站白名单开关"
                    isDisabled={whitelistSaving}
                    isSelected={node.whitelistEnabled === true}
                    size="sm"
                    onValueChange={(v) => actions.toggleWhitelist(node, v)}
                  />
                </span>
              </div>
              {/* nftables 状态：后端上报的展示串，原样展示；字段缺失时不渲染。 */}
              {node.nftablesStatus && (
                <p className="text-xs font-semibold text-foreground">
                  nftables：{node.nftablesStatus}
                </p>
              )}
              {node.whitelistEnabled === true && (
                <>
                  <p className="text-xs font-semibold text-foreground">
                    该节点全部入站只放行中转机、本机与列表中的 IP/网段。
                  </p>
                  <Input
                    aria-label="白名单额外放行"
                    description="中转机出口 IP 与节点地址不同时，在此补充；支持 IP 和网段，逗号分隔"
                    isDisabled={whitelistSaving}
                    label="额外放行"
                    placeholder="203.0.113.9, 10.0.0.0/8"
                    size="sm"
                    value={whitelistDraft}
                    variant="bordered"
                    onBlur={() =>
                      actions.saveWhitelistExtra(node, whitelistDraft)
                    }
                    onChange={(e) =>
                      onWhitelistDraftChange(node, e.target.value)
                    }
                    onKeyDown={(e) => {
                      if (e.key === "Enter")
                        (e.target as HTMLInputElement).blur();
                    }}
                  />
                </>
              )}
            </div>
          )}
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
              {/* 设置 Swap 与调优同为 agent 本机操作：nftables 节点无
                  agent 通道、点了必失败，故同样不渲染；离线时禁用
                  （现状与执行都须 agent 在线）。 */}
              {node.forwardMode !== "nftables" && (
                  <Button
                    className="min-h-8"
                    color="primary"
                    isDisabled={
                      node.connectionStatus !== "online"
                    }
                    size="sm"
                    variant="flat"
                    onPress={() => actions.openSwap(node)}
                  >
                    Swap
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
              {showConnLog && node.forwardMode !== "nftables" && (
                <Button
                  className="min-h-8"
                  size="sm"
                  variant="flat"
                  onPress={() => actions.openConnLog(node)}
                >
                  连接日志
                </Button>
              )}
            </div>
            {/* CARDVIEW-RESTART：卡片视图此前没有任何打开详情 sheet
                的入口，定时重启/测试点/安装命令等只存在于列表行点击
                的 sheet 里，两视图功能不对等。补一个与列表同路的
                sheet 入口，入口集合自动与列表一致。 */}
            <Button
              className="min-h-8 w-full"
              size="sm"
              variant="light"
              onPress={() => actions.openSheet(node.id)}
            >
              更多操作（含定时重启）
            </Button>
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
      : // C3-05：版本缺失的兜底三面统一为「未知」——旧列表面写作
        // 「未连接过」是因果断言（版本未回填的节点未必没连过），
        // 与卡片/详情面同字段两说法。
        node.version || "未知",
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
          {/* C4-G2：卸载登记状态芯片（五态不合并），与到期芯片并列。 */}
          <NodeUninstallStateChip node={node} />
        </>
      }
      footer={
        <>
          <NodeUpgradeVerificationLine node={node} />
          <NodeLiveStats
            info={node.systemInfo ?? null}
            month={month}
            variant="footer"
          />
        </>
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
