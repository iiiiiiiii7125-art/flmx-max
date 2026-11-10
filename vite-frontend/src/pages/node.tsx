import type {
  BatchOperationFailure,
  BatchOperationResult,
  InboundApiItem,
} from "@/api/types";

import {
  Fragment,
  useState,
  useEffect,
  useMemo,
  useCallback,
  useRef,
} from "react";
import toast from "react-hot-toast";
import {
  DndContext,
  KeyboardSensor,
  MouseSensor,
  TouchSensor,
  type DragEndEvent,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  rectSortingStrategy,
  sortableKeyboardCoordinates,
} from "@dnd-kit/sortable";
import { useNavigate } from "react-router-dom";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Alert } from "@/shadcn-bridge/heroui/alert";
import { Progress } from "@/shadcn-bridge/heroui/progress";
import {
  createNode,
  getConfigs,
  updateConfig,
  getNodeList,
  getNodeMonthTraffic,
  getInboundList,
  updateNode,
  deleteNode,
  getNodeInstallCommand,
  updateNodeOrder,
  batchDeleteNodes,
  batchSetNodeGroup,
  createNodeGroup,
  renameNodeGroup,
  deleteNodeGroup,
  upgradeNode,
  batchUpgradeNodes,
  getNodeReleases,
  rollbackNode,
  dismissNodeExpiryReminder,
  probeNodeLatency,
  type NodeInstallMode,
  type ReleaseChannel,
} from "@/api";
import { LatencyTrendModal } from "@/components/latency-trend-modal";
import { RouteModal } from "@/components/route-modal";
import { LatencyTargetsModal } from "@/pages/node/latency-targets-modal";
import { RestartScheduleModal } from "@/pages/node/restart-schedule-modal";
import {
  GroupCreateModal,
  GroupManageModal,
  type GroupOpResult,
} from "@/pages/node/group-manage-modal";
import {
  NODE_GROUP_ORDER_CONFIG_KEY,
  UNGROUPED_LABEL,
  collectNodeGroupLabels,
  mergeNodeGroupRegistry,
  nodeGroupLabel,
  parseNodeGroupOrder,
  segmentNodesByGroup,
} from "@/pages/node/grouping";
import {
  Dropdown,
  DropdownItem,
  DropdownMenu,
  DropdownMenuSeparator,
  DropdownTrigger,
} from "@/shadcn-bridge/heroui/dropdown";
import { PageLoadingState } from "@/components/page-state";
import { ActionSheet } from "@/components/action-sheet";
import {
  EmptyHint,
  PageHeader,
  PageShell,
  PageToolbar,
  SelectionBar,
} from "@/components/page-kit";
import { CARD_GRID } from "@/components/info-card";
import { ViewModeToggle, useViewMode } from "@/components/view-mode";
import { NodeTuneModal } from "@/pages/node/tune-modal";
import { copyWithToast } from "@/utils/clipboard";
import {
  formatNodeRenewalTime,
  getNodeRenewalCycleLabel,
} from "@/pages/node/renewal";
import { buildNodeSystemInfo } from "@/pages/node/system-info";
import { useNodeOfflineTimers } from "@/pages/node/use-node-offline-timers";
import { useNodeRealtime } from "@/pages/node/use-node-realtime";
import { useVisiblePolling } from "@/hooks/use-visible-polling";
import { latestProbe } from "@/utils/latest-probe";
import { isAdmin } from "@/utils/auth";
import {
  LATENCY_TARGETS_KEY,
  parseLatencyTargets,
} from "@/utils/latency-targets";
import { useLocalStorageState } from "@/hooks/use-local-storage-state";
import { useIdemKey, useRotateIdemKeyOnCreateOpen } from "@/hooks/useIdemKey";
import { JwtUtil } from "@/utils/jwt";
import { loadStoredOrder, saveOrder } from "@/utils/order-storage";
import { Node, NodeForm, NodeFilterMode } from "@/pages/node/types";
import {
  formatFlow,
  formatUptime,
  getNodeReminderEnabled,
  getNodeExpiryMeta,
  mergeNodeRealtimeState,
} from "@/pages/node/helpers";
import {
  NodeCardRow,
  NodeListRow,
  NodeUninstallStateChip,
  type NodeRowActions,
} from "@/pages/node/node-row";
import { NodeFormModal } from "@/pages/node/node-form-modal";
import {
  BatchDeleteNodesModal,
  DeleteNodeModal,
  RollbackNodeModal,
} from "@/pages/node/confirm-modals";
import {
  BATCH_UPGRADE_NODE_LIMIT,
  InstallCommandModal,
  UpgradeNodeModal,
} from "@/pages/node/agent-modals";
import { NodeFilterModal } from "@/pages/node/filter-modal";
import { BatchActionResultModal } from "@/components/batch-action-result-modal";
import { extractBatchFailures } from "@/api/error-message";

const NODE_ORDER_KEY = "node-order";

// F-L5-03 同族：节点排序键同样按登录用户分键（与 forward/tunnel 的
// `:u:<id>` 口径一致），避免同一浏览器多账号登录时排序互相串扰。
const nodeOrderLocalKey = (): string => {
  const userId = JwtUtil.getUserIdFromToken();

  return userId ? `${NODE_ORDER_KEY}:u:${userId}` : NODE_ORDER_KEY;
};

// 读取时分键下尚无存量则回退读一次旧全局键（升级迁移，写入只写分键）。
const nodeOrderLoadKey = (): string => {
  const scopedKey = nodeOrderLocalKey();

  return scopedKey === NODE_ORDER_KEY ||
    localStorage.getItem(scopedKey) !== null
    ? scopedKey
    : NODE_ORDER_KEY;
};

interface BatchResultModalState {
  failures: BatchOperationFailure[];
  open: boolean;
  summary: string;
  title: string;
}

const EMPTY_BATCH_RESULT_MODAL_STATE: BatchResultModalState = {
  failures: [],
  open: false,
  summary: "",
  title: "",
};

/**
 * R2X-045：计算删除给定节点将打断的跨节点引用——出站/分流指向被删
 * 节点入站、且引用方本身不在被删节点上的入站，按引用方 ID 去重、
 * 以「名称（节点名）」消歧，与后端 nodeCrossReferrers 同口径。
 * 删除确认弹窗在确认前点名这份清单；后端删除响应另回传权威清单。
 */
function computeBrokenReferrers(
  inbounds: InboundApiItem[],
  deletedNodeIds: Set<number>,
): string[] {
  const targetIds = new Set(
    inbounds.filter((ib) => deletedNodeIds.has(ib.nodeId)).map((ib) => ib.id),
  );
  const seen = new Set<number>();
  const out: string[] = [];

  for (const ib of inbounds) {
    if (deletedNodeIds.has(ib.nodeId) || seen.has(ib.id)) continue;
    const targets = [
      ib.outboundId,
      // R2-039：与后端 outboundTargets 同口径——只计 SplitProxy
      // （action === "proxy"）规则；direct/block 规则残留的非零
      // outboundId 不是有效引用，不得计入打断清单（此前口径过宽、
      // 只多报）。谓词由 check-split-referrer-filter.mjs 钉死。
      ...(ib.splitRules || [])
        .filter((rule) => rule.action === "proxy")
        .map((rule) => rule.outboundId ?? 0),
    ];

    if (targets.some((target) => target > 0 && targetIds.has(target))) {
      seen.add(ib.id);
      out.push(`${ib.name}（${ib.nodeName}）`);
    }
  }

  return out;
}

export default function NodePage({
  embedded = false,
  addRequest = 0,
  onOpenMonitor,
}: {
  embedded?: boolean;
  /** Bumped by the 节点 page header's 新建节点 when embedded. */
  addRequest?: number;
  /** Opens a node's history charts and service monitors (DA-1). */
  onOpenMonitor?: (nodeId: number) => void;
}) {
  const [nodeList, setNodeList] = useState<Node[]>([]);
  // 各节点本月（浏览器时区）的网卡流量 {id: [入, 出]}：整机网卡计数，
  // 与隧道计费/订阅配额是两套口径，仅作节点维度参考，文案须带「网卡」分名。
  // 加载路径唯一化（P2）：loadNodes 同批拉取，P1 的 15 秒可见性轮询经
  // loadNodes 连带刷新（密于 P4 原独立 60 秒轮询，故不保留其重复 loader）；
  // 消费方：P2 的 monthTrafficText（列表/卡片/详情）与 P4 的 NodeLiveStats
  // month prop 共用这一份 state。
  const [monthTraffic, setMonthTraffic] = useState<
    Record<string, [number, number]>
  >({});
  const [nodeOrder, setNodeOrder] = useState<number[]>([]);
  const [loading, setLoading] = useState(true);
  // IRL-3 同族（普查补入）：节点列表初始加载失败标记——空状态据此
  // 改判「加载失败」而非「还没有节点」。
  const [nodesLoadFailed, setNodesLoadFailed] = useState(false);
  const [localSearchKeyword, setLocalSearchKeyword] = useLocalStorageState(
    "node-search-keyword-local",
    "",
  );
  const [nodeFilterMode, setNodeFilterMode, resetNodeFilterMode] =
    useLocalStorageState<NodeFilterMode>("node-expiry-filter-mode", "all");
  const [isFilterModalOpen, setIsFilterModalOpen] = useState(false);
  const [dialogVisible, setDialogVisible] = useState(false);
  // C3-02：正在编辑节点的已钉入 SSH 主机密钥指纹（表单指纹区展示用；
  // 不进 NodeForm——指纹是服务端托管状态，不得随节点提交体回写）。
  const [editingHostKeyFingerprint, setEditingHostKeyFingerprint] =
    useState("");
  const [dialogTitle, setDialogTitle] = useState("");
  const [isEdit, setIsEdit] = useState(false);
  const [submitLoading, setSubmitLoading] = useState(false);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [rollbackModalOpen, setRollbackModalOpen] = useState(false);
  const [tuneNode, setTuneNode] = useState<Node | null>(null);
  const [nodeToRollback, setNodeToRollback] = useState<Node | null>(null);
  const [nodeToDelete, setNodeToDelete] = useState<Node | null>(null);
  // R2X-045：删节点确认前展示的跨节点引用清单（单删/批删各一份）。
  const [deleteBrokenReferrers, setDeleteBrokenReferrers] = useState<string[]>(
    [],
  );
  const [batchDeleteBrokenReferrers, setBatchDeleteBrokenReferrers] = useState<
    string[]
  >([]);
  const [protocolDisabled, setProtocolDisabled] = useState(false);
  const [protocolDisabledReason, setProtocolDisabledReason] = useState("");
  const [form, setForm] = useState<NodeForm>({
    id: null,
    name: "",
    remark: "",
    displayGroup: "",
    expiryTime: 0,
    renewalCycle: "",
    idleTimeoutSeconds: 0,
    serverHost: "",
    serverIpV4: "",
    serverIpV6: "",
    port: "10000-65535",
    tcpListenAddr: "[::]",
    udpListenAddr: "[::]",
    interfaceName: "",
    extraIPs: "",
    forwardMode: "agent",
    sshHost: "",
    sshPort: "22",
    sshUsername: "",
    sshAuthType: "private_key",
    sshPassword: "",
    sshPrivateKey: "",
    sshPassphrase: "",
    sshSudoMode: "none",
    http: 0,
    tls: 0,
    socks: 0,
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  const isNftablesMode = form.forwardMode === "nftables";
  const protocolControlsDisabled = protocolDisabled || isNftablesMode;
  const protocolControlsReason = isNftablesMode
    ? "nftables 模式不支持 agent 协议开关"
    : protocolDisabledReason || "等待节点上线后再设置";

  const [selectMode, setSelectMode] = useState(false);
  const [viewMode, setViewMode] = useViewMode("node");
  const card = viewMode === "card";
  // 展示分组段序（面板配置 node_display_group_order）：与延迟池同
  // 一次 getConfigs 读取载入，保存走 updateConfig 落服务端。
  const [groupOrder, setGroupOrder] = useState<string[]>([]);
  const [groupManageOpen, setGroupManageOpen] = useState(false);
  const [createGroupOpen, setCreateGroupOpen] = useState(false);
  const [groupMoving, setGroupMoving] = useState(false);
  const [infoPopoverPlacement, setInfoPopoverPlacement] = useState<
    Record<number, "left" | "bottom">
  >({});

  const updateInfoPopoverPlacement = useCallback(
    (nodeId: number, triggerElement: HTMLElement | null) => {
      if (!triggerElement) {
        return;
      }

      const rect = triggerElement.getBoundingClientRect();
      const cardElement = triggerElement.closest("[data-node-card='true']");
      const cardRect =
        cardElement instanceof HTMLElement
          ? cardElement.getBoundingClientRect()
          : null;
      const estimatedPanelWidth = 288;
      const containerPadding = 16;
      const availableLeftSpace = cardRect
        ? rect.left - cardRect.left
        : rect.left;
      const nextPlacement: "left" | "bottom" =
        availableLeftSpace >= estimatedPanelWidth + containerPadding
          ? "left"
          : "bottom";

      setInfoPopoverPlacement((prev) =>
        prev[nodeId] === nextPlacement
          ? prev
          : { ...prev, [nodeId]: nextPlacement },
      );
    },
    [],
  );

  const [sheetNodeId, setSheetNodeId] = useState<number | null>(null);
  const [probingIds, setProbingIds] = useState<Set<number>>(new Set());
  const navigate = useNavigate();
  // 零测试点提示：延迟测试点池是否为空。null = 未知（未加载、非管理
  // 员、读取失败），此时不提示、不误报；true = 池为空，延迟槽位显示
  // 去设置添加的提示；false = 池非空，等待态仍画空等定时探测。
  const [latencyPoolEmpty, setLatencyPoolEmpty] = useState<boolean | null>(
    null,
  );
  const [routeNode, setRouteNode] = useState<Node | null>(null);
  const [trendNode, setTrendNode] = useState<Node | null>(null);
  const [targetNodes, setTargetNodes] = useState<Node[]>([]);
  const [restartNode, setRestartNode] = useState<Node | null>(null);
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [batchDeleteModalOpen, setBatchDeleteModalOpen] = useState(false);
  const [batchResultModal, setBatchResultModal] =
    useState<BatchResultModalState>(EMPTY_BATCH_RESULT_MODAL_STATE);
  const [batchLoading, setBatchLoading] = useState(false);

  // 安装命令相关状态
  const [installCommandModal, setInstallCommandModal] = useState(false);
  const [installCommand, setInstallCommand] = useState("");
  const [installWarning, setInstallWarning] = useState("");
  const [currentNodeName, setCurrentNodeName] = useState("");
  const [installNodeId, setInstallNodeId] = useState<number | null>(null);
  const [installMode, setInstallMode] = useState<NodeInstallMode>("script");
  const [installPrivileged, setInstallPrivileged] = useState(false);
  const [installModeLoading, setInstallModeLoading] = useState(false);

  // 升级相关状态
  const [upgradeModalOpen, setUpgradeModalOpen] = useState(false);
  const [upgradeTarget, setUpgradeTarget] = useState<"single" | "batch">(
    "single",
  );
  const [upgradeTargetNodeId, setUpgradeTargetNodeId] = useState<number | null>(
    null,
  );
  const [releases, setReleases] = useState<
    Array<{
      version: string;
      name: string;
      publishedAt: string;
      prerelease: boolean;
      channel: ReleaseChannel;
    }>
  >([]);
  const [releasesLoading, setReleasesLoading] = useState(false);
  const [releaseChannel, setReleaseChannel] =
    useState<ReleaseChannel>("stable");
  const [selectedVersion, setSelectedVersion] = useState("");
  const [batchUpgradeLoading, setBatchUpgradeLoading] = useState(false);
  const [upgradeProgress, setUpgradeProgress] = useState<
    Record<number, { stage: string; percent: number; message: string }>
  >({});

  const handleNodeOffline = useCallback((nodeId: number) => {
    setNodeList((prev) =>
      prev.map((node) => {
        if (node.id !== nodeId) return node;
        if (node.connectionStatus === "offline" && node.systemInfo === null) {
          return node;
        }

        return { ...node, connectionStatus: "offline", systemInfo: null };
      }),
    );
  }, []);

  const { clearOfflineTimer, scheduleNodeOffline } = useNodeOfflineTimers({
    delayMs: 3000,
    onNodeOffline: handleNodeOffline,
  });

  // loadNodes 序号（X-P3-8/K-02，照 monitor-view loadMetrics 既有写法）：
  // 手动刷新与轮询可在途并发，只有最新一轮可落 state，旧响应不得覆盖。
  const loadNodesSeqRef = useRef(0);

  // 加载节点列表
  const loadNodes = useCallback(async (options?: { silent?: boolean }) => {
    const silent = options?.silent ?? false;
    const seq = (loadNodesSeqRef.current += 1);

    if (!silent) {
      setLoading(true);
    }

    // 本月网卡流量与列表同批刷新；失败静默（展示位留空，不影响列表）。
    // 与主请求共用同一 seq 守卫（R3-D-01）：旁路请求乱序返回时旧
    // 快照不得覆盖新值。
    getNodeMonthTraffic()
      .then((r) => {
        if (loadNodesSeqRef.current !== seq) {
          return;
        }
        if (r.code === 0 && r.data) {
          setMonthTraffic(r.data);
        }
      })
      .catch(() => {});

    try {
      const res = await getNodeList();

      if (loadNodesSeqRef.current !== seq) {
        return;
      }

      if (res.code === 0) {
        // IRL-3 同族（普查补入）：任一次成功加载清失败标记。
        setNodesLoadFailed(false);
        const nodesData: Node[] = (res.data || []).map((node: any) => ({
          ...node,
          inx: node.inx ?? 0,
          connectionStatus: node.status === 1 ? "online" : "offline",
          systemInfo: null,
          copyLoading: false,
        }));

        setNodeList((prev) => {
          const previousById = new Map(prev.map((node) => [node.id, node]));

          return nodesData.map((node) =>
            mergeNodeRealtimeState(node, previousById.get(node.id)),
          );
        });

        // 优先使用数据库中的 inx 字段进行排序，否则回退到本地排序
        const hasDbOrdering = nodesData.some(
          (n) => n.inx !== undefined && n.inx !== 0,
        );

        if (hasDbOrdering) {
          const dbOrder = [...nodesData]
            .sort((a, b) => (a.inx ?? 0) - (b.inx ?? 0))
            .map((n) => n.id);

          setNodeOrder(dbOrder);
        } else {
          setNodeOrder(
            loadStoredOrder(
              nodeOrderLoadKey(),
              nodesData.map((n) => n.id),
            ),
          );
        }
      } else {
        if (!silent) {
          toast.error(res.msg || "加载节点列表失败");
          // IRL-3 同族（普查补入）：初始加载失败不得落到「还没有
          // 节点」的假空状态——置位后空状态改判为加载失败＋重试。
          setNodesLoadFailed(true);
        }
      }
    } catch {
      if (!silent && loadNodesSeqRef.current === seq) {
        toast.error("网络错误，请重试");
        setNodesLoadFailed(true);
      }
    } finally {
      if (!silent && loadNodesSeqRef.current === seq) {
        setLoading(false);
      }
    }
  }, []);

  // 处理WebSocket消息
  const handleWebSocketMessage = (data: any) => {
    const { id, type, data: messageData } = data;
    const nodeId = Number(id);

    if (Number.isNaN(nodeId)) return;

    if (type === "status") {
      if (messageData === 1) {
        clearOfflineTimer(nodeId);
        setNodeList((prev) =>
          prev.map((node) => {
            if (node.id !== nodeId) return node;
            if (node.connectionStatus === "online") return node;

            return { ...node, connectionStatus: "online" };
          }),
        );
      } else {
        // 离线事件做延迟处理，避免短抖动导致频繁闪烁
        scheduleNodeOffline(nodeId);
      }
    } else if (type === "info") {
      clearOfflineTimer(nodeId);
      setNodeList((prev) =>
        prev.map((node) => {
          if (node.id === nodeId) {
            const systemInfo = buildNodeSystemInfo(
              messageData,
              node.systemInfo,
            );

            if (!systemInfo) {
              return node;
            }

            return {
              ...node,
              connectionStatus: "online",
              systemInfo,
            };
          }

          return node;
        }),
      );
    } else if (type === "upgrade_progress") {
      try {
        const progressData =
          typeof messageData === "string"
            ? JSON.parse(messageData)
            : messageData;

        if (progressData?.data) {
          setUpgradeProgress((prev) => ({
            ...prev,
            [nodeId]: {
              stage: progressData.data.stage || "",
              percent: progressData.data.percent || 0,
              message: progressData.message || "",
            },
          }));
        }
      } catch {
        // ignore parse errors
      }
    } else if (type === "metric") {
      clearOfflineTimer(nodeId);
      setNodeList((prev) =>
        prev.map((node) => {
          if (node.id !== nodeId) return node;

          const metric =
            typeof messageData === "string"
              ? JSON.parse(messageData)
              : messageData;

          if (!metric || typeof metric !== "object") return node;

          const incomingUptime = metric.uptime ?? 0;

          return {
            ...node,
            connectionStatus: "online",
            // A-F-03：整字面量同口径——每一项缺失都回落上次已上报值，
            // 不得独缺。旧实现只有 cpu/memory 带回落：diskUsage/load
            // 缺帧即变 undefined（磁盘条闪烁消失）、累计流量计数缺帧
            // 归 0（把已知累计值清零）。计数/速率的 0 只作无上次值时
            // 的初值兜底（类型为必填），不再覆盖已知值。
            systemInfo: {
              cpuUsage:
                metric.cpuUsage ??
                metric.cpu_usage ??
                node.systemInfo?.cpuUsage,
              memoryUsage:
                metric.memoryUsage ??
                metric.memory_usage ??
                node.systemInfo?.memoryUsage,
              uploadTraffic:
                metric.netOutBytes ??
                metric.bytes_transmitted ??
                node.systemInfo?.uploadTraffic ??
                0,
              downloadTraffic:
                metric.netInBytes ??
                metric.bytes_received ??
                node.systemInfo?.downloadTraffic ??
                0,
              uploadSpeed:
                metric.netOutSpeed ??
                metric.net_out_speed ??
                node.systemInfo?.uploadSpeed ??
                0,
              downloadSpeed:
                metric.netInSpeed ??
                metric.net_in_speed ??
                node.systemInfo?.downloadSpeed ??
                0,
              uptime: incomingUptime || node.systemInfo?.uptime || 0,
              diskUsage:
                metric.diskUsage ??
                metric.disk_usage ??
                node.systemInfo?.diskUsage,
              load1: metric.load1 ?? node.systemInfo?.load1,
              load5: metric.load5 ?? node.systemInfo?.load5,
              load15: metric.load15 ?? node.systemInfo?.load15,
              tcpConns:
                metric.tcpConns ??
                metric.tcp_conns ??
                node.systemInfo?.tcpConns,
              udpConns:
                metric.udpConns ??
                metric.udp_conns ??
                node.systemInfo?.udpConns,
              netInSpeed:
                metric.netInSpeed ??
                metric.net_in_speed ??
                node.systemInfo?.netInSpeed,
              netOutSpeed:
                metric.netOutSpeed ??
                metric.net_out_speed ??
                node.systemInfo?.netOutSpeed,
            },
          };
        }),
      );
    }
  };

  const { wsConnected, wsConnecting, usingPollingFallback } = useNodeRealtime({
    onMessage: handleWebSocketMessage,
  });

  useEffect(() => {
    loadNodes();
  }, [loadNodes]);

  // 零测试点提示：只读一次池状态，复用设置页延迟卡同一 getConfigs
  // 接口（/config/list），不新增接口。仅管理员读取（提示的落点设置
  // 页延迟卡仅管理员可见）；读取失败时保持未知，不提示。
  useEffect(() => {
    if (!isAdmin()) return;
    let alive = true;

    getConfigs()
      .then((r) => {
        if (!alive || r.code !== 0 || !r.data) return;
        setLatencyPoolEmpty(
          parseLatencyTargets(r.data[LATENCY_TARGETS_KEY]).length === 0,
        );
        setGroupOrder(parseNodeGroupOrder(r.data[NODE_GROUP_ORDER_CONFIG_KEY]));
      })
      .catch(() => {});

    return () => {
      alive = false;
    };
  }, []);

  // DB-3：指标走 WebSocket 实时推送，
  // 但节点延迟缓存只在 node/list 里——无论 WS 是否连通都按 15 秒
  // 可见性轮询静默刷新（含 WS 断线时的兜底），节点重连重测的结果
  // 最迟 15 秒内出现在卡片上。
  useVisiblePolling(
    useCallback(() => loadNodes({ silent: true }), [loadNodes]),
  );

  // 格式化速度

  // 格式化开机时间

  // 格式化流量

  // 本月网卡流量文案（↓入 ↑出）：无数据（远程/联邦节点不上报本机，
  // 或本月尚无累计）返回 null，展示位留空。
  const monthTrafficText = (nodeId: number): string | null => {
    const t = monthTraffic[String(nodeId)];

    if (!t) {
      return null;
    }

    return `↓${formatFlow(t[0])} ↑${formatFlow(t[1])}`;
  };

  // IPv4/IPv6 格式验证（仅用于判定地址族）
  const ipv4Regex =
    /^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$/;
  const ipv6Regex =
    /^(([0-9a-fA-F]{1,4}:){7,7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|fe80:(:[0-9a-fA-F]{0,4}){0,4}%[0-9a-zA-Z]{1,}|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$/;

  const validateIpv4Literal = (ip: string): boolean =>
    ipv4Regex.test(ip.trim());
  const validateIpv6Literal = (ip: string): boolean =>
    ipv6Regex.test(ip.trim());

  // Hostname/domain validation (no scheme/port)
  const hostnameRegex =
    /^(?=.{1,253}$)(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)(?:\.(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?))*$/;
  const validateHostname = (host: string): boolean => {
    const v = host.trim();

    if (!v) return false;
    if (v === "localhost") return true;

    return hostnameRegex.test(v);
  };

  // 验证端口格式：支持 80,443,100-600
  const validatePort = (
    portStr: string,
  ): { valid: boolean; error?: string } => {
    if (!portStr || !portStr.trim()) {
      return { valid: false, error: "请输入端口" };
    }

    const trimmed = portStr.trim();
    const parts = trimmed
      .split(",")
      .map((p) => p.trim())
      .filter((p) => p);

    if (parts.length === 0) {
      return { valid: false, error: "请输入有效的端口" };
    }

    // 严格整数：parseInt 是部分解析（80abc/10.9/0x50 都会被判合法后
    // 落库再被 agent 展开成错端口集），只接受纯十进制数字段
    const parseStrictPort = (raw: string): number | null => {
      if (!/^\d+$/.test(raw)) {
        return null;
      }
      const value = Number(raw);

      return Number.isSafeInteger(value) ? value : null;
    };

    for (const part of parts) {
      // 检查是否是端口范围 (如 100-600)
      if (part.includes("-")) {
        const range = part.split("-").map((p) => p.trim());

        if (range.length !== 2) {
          return { valid: false, error: `端口范围格式错误: ${part}` };
        }

        const start = parseStrictPort(range[0]);
        const end = parseStrictPort(range[1]);

        if (start === null || end === null) {
          return { valid: false, error: `端口必须是数字: ${part}` };
        }

        if (start < 1 || start > 65535 || end < 1 || end > 65535) {
          return {
            valid: false,
            error: `端口范围必须在 1-65535 之间: ${part}`,
          };
        }

        if (start >= end) {
          return { valid: false, error: `起始端口必须小于结束端口: ${part}` };
        }
      } else {
        // 单个端口
        const port = parseStrictPort(part);

        if (port === null) {
          return { valid: false, error: `端口必须是数字: ${part}` };
        }

        if (port < 1 || port > 65535) {
          return { valid: false, error: `端口必须在 1-65535 之间: ${part}` };
        }
      }
    }

    return { valid: true };
  };

  // 表单验证
  const validateForm = (): boolean => {
    const newErrors: Record<string, string> = {};

    if (!form.name.trim()) {
      newErrors.name = "请输入节点名称";
    } else if (form.name.trim().length < 2) {
      newErrors.name = "节点名称长度至少2位";
    } else if (form.name.trim().length > 50) {
      newErrors.name = "节点名称长度不能超过50位";
    }

    if (
      (form.renewalCycle && !form.expiryTime) ||
      (!form.renewalCycle && form.expiryTime)
    ) {
      newErrors.expiryTime = "请同时设置续费周期和续费基准时间";
    }

    const v4 = form.serverIpV4.trim();
    const v6 = form.serverIpV6.trim();
    const host = form.serverHost.trim();

    if (!v4 && !v6 && !host) {
      const msg = "请至少填写一个 IPv4/IPv6 地址或域名";

      newErrors.serverIpV4 = msg;
      newErrors.serverIpV6 = msg;
      newErrors.serverHost = msg;
    } else {
      if (v4 && !validateIpv4Literal(v4)) {
        newErrors.serverIpV4 = "请输入有效的IPv4地址";
      }
      if (v6 && !validateIpv6Literal(v6)) {
        newErrors.serverIpV6 = "请输入有效的IPv6地址";
      }
      if (host && !validateHostname(host)) {
        newErrors.serverHost = "请输入有效的域名/主机名";
      }
    }

    const portValidation = validatePort(form.port);

    if (!portValidation.valid) {
      newErrors.port = portValidation.error || "端口格式错误";
    }

    if (form.forwardMode === "nftables") {
      if (!form.sshHost.trim()) {
        newErrors.sshHost = "请输入 SSH 主机";
      }
      const sshPort = Number(form.sshPort);

      if (!Number.isInteger(sshPort) || sshPort < 1 || sshPort > 65535) {
        newErrors.sshPort = "SSH 端口必须为 1-65535";
      }
      if (!form.sshUsername.trim()) {
        newErrors.sshUsername = "请输入 SSH 用户名";
      }
      if (form.sshAuthType === "password") {
        if (!isEdit && !form.sshPassword.trim()) {
          newErrors.sshPassword = "请输入 SSH 密码";
        }
      } else if (!isEdit && !form.sshPrivateKey.trim()) {
        newErrors.sshPrivateKey = "请输入 SSH 私钥";
      }
    }

    setErrors(newErrors);

    return Object.keys(newErrors).length === 0;
  };

  // 新增节点
  const handleAdd = () => {
    setDialogTitle("新增节点");
    setIsEdit(false);
    setDialogVisible(true);
    resetForm();
    setProtocolDisabled(true);
    setProtocolDisabledReason("节点未在线，等待节点上线后再设置");
  };

  useEffect(() => {
    if (addRequest > 0) handleAdd();
    // Only a new request opens the dialog.
  }, [addRequest]);

  // 编辑节点
  const handleEdit = (node: Node) => {
    setDialogTitle("编辑节点");
    setIsEdit(true);

    const legacy = (node.serverIp || "").trim();
    const normalizedV4 =
      node.serverIpV4?.trim() || (validateIpv4Literal(legacy) ? legacy : "");
    const normalizedV6 =
      node.serverIpV6?.trim() || (validateIpv6Literal(legacy) ? legacy : "");
    const normalizedHost =
      !normalizedV4 && !normalizedV6 && legacy ? legacy : "";

    setForm({
      id: node.id,
      name: node.name,
      remark: node.remark || "",
      displayGroup: node.displayGroup || "",
      expiryTime: node.expiryTime || 0,
      renewalCycle: node.renewalCycle || "",
      idleTimeoutSeconds: node.idleTimeoutSeconds || 0,
      serverHost: normalizedHost,
      serverIpV4: normalizedV4,
      serverIpV6: normalizedV6,
      port: node.port || "10000-65535",
      tcpListenAddr: node.tcpListenAddr || "[::]",
      udpListenAddr: node.udpListenAddr || "[::]",
      interfaceName: (node as any).interfaceName || "",
      extraIPs: node.extraIPs || "",
      forwardMode: node.forwardMode === "nftables" ? "nftables" : "agent",
      sshHost: node.sshConfig?.host || normalizedHost || legacy,
      sshPort: String(node.sshConfig?.port || 22),
      sshUsername: node.sshConfig?.username || "",
      sshAuthType: node.sshConfig?.authType || "private_key",
      sshPassword: node.sshConfig?.password || "",
      sshPrivateKey: node.sshConfig?.privateKey || "",
      sshPassphrase: node.sshConfig?.passphrase || "",
      sshSudoMode: node.sshConfig?.sudoMode || "none",
      http: typeof node.http === "number" ? node.http : 1,
      tls: typeof node.tls === "number" ? node.tls : 1,
      socks: typeof node.socks === "number" ? node.socks : 1,
    });
    setEditingHostKeyFingerprint(node.sshConfig?.hostKeyFingerprint || "");
    const offline = node.connectionStatus !== "online";

    setProtocolDisabled(offline || node.forwardMode === "nftables");
    setProtocolDisabledReason(
      node.forwardMode === "nftables"
        ? "nftables 模式不支持 agent 协议开关"
        : offline
          ? "节点未在线，等待节点上线后再设置"
          : "",
    );
    setDialogVisible(true);
  };

  // 删除节点（R2X-045：打开确认弹窗时先拉入站清单，确认前点名将被
  // 打断的跨节点引用；清单拉取失败不阻塞删除确认本身）。
  const handleDelete = async (node: Node) => {
    setNodeToDelete(node);
    setDeleteBrokenReferrers([]);
    setDeleteModalOpen(true);
    try {
      const res = await getInboundList();

      if (res.code === 0 && Array.isArray(res.data)) {
        setDeleteBrokenReferrers(
          computeBrokenReferrers(res.data, new Set([node.id])),
        );
      }
    } catch {
      // 清单不可用时保持通用确认文案。
    }
  };

  // 批量删除入口（R2X-045：同单删口径，确认前点名跨节点引用）。
  const openBatchDeleteModal = async () => {
    setBatchDeleteBrokenReferrers([]);
    setBatchDeleteModalOpen(true);
    try {
      const res = await getInboundList();

      if (res.code === 0 && Array.isArray(res.data)) {
        setBatchDeleteBrokenReferrers(
          computeBrokenReferrers(res.data, new Set(selectedIds)),
        );
      }
    } catch {
      // 清单不可用时保持通用确认文案。
    }
  };

  const confirmDelete = async (uninstall: boolean) => {
    if (!nodeToDelete || deleteLoading) return;

    setDeleteLoading(true);
    try {
      const res = await deleteNode(nodeToDelete.id, uninstall);

      if (res.code === 0) {
        toast.success("删除成功");
        // F2-21：未勾「同时卸载」时后端把「agent 残留、须手动卸载」
        // 的说明放在信封 warning 里（data 通道此时无此条），须原样
        // 透出（network.ts 合同：must be surfaced）。
        if (res.warning) {
          toast(res.warning, { icon: "⚠️", duration: 12000 });
        }
        // 连带卸载回执：离线待卸载单独播报；告警（过旧/未确认等）
        // 由后端逐条点名，前端原样呈现不吞。
        const uninstallWarnings = res.data?.uninstallWarnings ?? [];

        if (uninstallWarnings.length > 0) {
          toast(uninstallWarnings.join("；"), {
            icon: "⚠️",
            duration: 6000,
          });
        } else if (res.data?.uninstall === "pending") {
          toast("节点当前离线，已登记：它下次连接面板时将自动卸载 agent", {
            icon: "🕒",
            duration: 5000,
          });
        } else if (res.data?.uninstall === "done") {
          // 在线已确认卸载：与批量三态汇总同口径点名「正在卸载」。
          toast("该节点的 agent 正在卸载", {
            icon: "🗑️",
            duration: 5000,
          });
        }
        const returned = res.data?.brokenReferrers ?? [];

        if (returned.length > 0) {
          toast(
            `删除打断了 ${returned.length} 个跨节点引用：${returned.join("、")}`,
            { icon: "⚠️", duration: 5000 },
          );
        }
        // 唯一入口/出口点名：后端已逐条回传，原样呈现不吞。
        const soleRoleWarnings = res.data?.soleRoleWarnings ?? [];

        if (soleRoleWarnings.length > 0) {
          toast(soleRoleWarnings.join("；"), {
            icon: "⚠️",
            duration: 6000,
          });
        }
        setNodeList((prev) => prev.filter((n) => n.id !== nodeToDelete.id));
        setDeleteModalOpen(false);
        setNodeToDelete(null);
      } else {
        toast.error(res.msg || "删除失败");
      }
    } catch {
      toast.error("网络错误，请重试");
    } finally {
      setDeleteLoading(false);
    }
  };

  const handleDismissExpiryReminder = async (nodeId: number) => {
    try {
      const res = await dismissNodeExpiryReminder(nodeId);

      if (res.code === 0) {
        setNodeList((prev) =>
          prev.map((n) =>
            n.id === nodeId ? { ...n, expiryReminderDismissed: 1 } : n,
          ),
        );
      } else {
        toast.error(res.msg || "操作失败");
      }
    } catch {
      toast.error("网络错误，请重试");
    }
  };

  // 拉取指定安装形态的命令（Only the stable channel is published）。
  const fetchInstallCommand = async (
    nodeId: number,
    mode: NodeInstallMode,
    privileged: boolean,
  ) => {
    setInstallModeLoading(true);
    try {
      const res = await getNodeInstallCommand(
        nodeId,
        "stable",
        mode,
        privileged,
      );

      if (res.code === 0 && res.data) {
        setInstallCommand(res.data);
        setInstallWarning(res.warning ?? "");
        // R7-J-01: a plaintext (ws://) install address must be shown
        // explicitly BEFORE the command is copied/used — never let it
        // be used in silence; the modal renders it as a banner too.
        if (res.warning) {
          toast.error(res.warning, { duration: 8000 });
        }
      } else {
        setInstallCommand("");
        toast.error(res.msg || "获取安装命令失败");
      }
    } catch {
      setInstallCommand("");
      toast.error("获取安装命令失败");
    } finally {
      setInstallModeLoading(false);
    }
  };

  // 打开安装窗：默认脚本形态，方式/调优切换时重新拉取对应命令。
  const openInstallSelector = (node: Node) => {
    setCurrentNodeName(node.name);
    setInstallNodeId(node.id);
    setInstallMode("script");
    setInstallPrivileged(false);
    setInstallCommand("");
    setInstallWarning("");
    setInstallCommandModal(true);
    void fetchInstallCommand(node.id, "script", false);
  };

  const handleInstallModeChange = (mode: NodeInstallMode) => {
    setInstallMode(mode);
    if (installNodeId != null) {
      void fetchInstallCommand(installNodeId, mode, installPrivileged);
    }
  };

  const handleInstallPrivilegedChange = (privileged: boolean) => {
    setInstallPrivileged(privileged);
    if (installNodeId != null) {
      void fetchInstallCommand(installNodeId, installMode, privileged);
    }
  };

  // 手动复制安装命令
  const handleManualCopy = async () => {
    if (await copyWithToast(installCommand, "安装命令已复制到剪贴板")) {
      setInstallCommandModal(false);
    }
  };

  const loadReleasesByChannel = useCallback(async (channel: ReleaseChannel) => {
    setReleasesLoading(true);
    try {
      const res = await getNodeReleases(channel);

      if (res.code === 0 && Array.isArray(res.data)) {
        setReleases(res.data);
      } else {
        toast.error(res.msg || "获取版本列表失败");
      }
    } catch {
      toast.error("获取版本列表失败");
    } finally {
      setReleasesLoading(false);
    }
  }, []);

  // 打开版本选择弹窗
  const openUpgradeModal = async (
    target: "single" | "batch",
    nodeId?: number,
  ) => {
    const defaultChannel: ReleaseChannel = "stable";

    setUpgradeTarget(target);
    setUpgradeTargetNodeId(nodeId || null);
    setReleaseChannel(defaultChannel);
    setSelectedVersion("");
    setUpgradeModalOpen(true);
    await loadReleasesByChannel(defaultChannel);
  };

  // 升级/回退确认的下发瞬间守卫：按操作目标分键（单节点按 nodeId、
  // 批量独立一键、回退按 nodeId），只覆盖「点击→下发」瞬间。
  // runConfirmUpgrade/runConfirmRollback 同步执行到首个 await 之前已
  // 关弹窗、置目标 loading 态并发出请求，守卫随即释放，绝不跨整段
  // 长请求持有。此前用全局单例守卫包住整段请求（单节点最长约
  // 480s、批量最长 1800s），任一升级在途时其余节点/批量的确认点击
  // 全被静默吞掉。请求在途期间的重复确认由目标自身的 loading 态
  // （upgradeLoading/batchUpgradeLoading/rollbackLoading）在入口
  // 检查拦截；不同目标互不影响，可并发下发。
  const upgradeConfirmGuardRef = useRef<Set<string>>(new Set());
  const rollbackConfirmGuardRef = useRef<Set<number>>(new Set());

  // 确认升级（从版本弹窗）
  const handleConfirmUpgrade = async () => {
    const guardKey =
      upgradeTarget === "batch"
        ? "batch"
        : upgradeTargetNodeId != null
          ? `single:${upgradeTargetNodeId}`
          : null;

    if (guardKey === null) return;
    if (upgradeConfirmGuardRef.current.has(guardKey)) return;
    // 同一目标已有升级在途（loading 态呈现中）时忽略重复确认
    if (upgradeTarget === "batch") {
      if (batchUpgradeLoading) return;
    } else if (
      nodeList.find((n) => n.id === upgradeTargetNodeId)?.upgradeLoading
    ) {
      return;
    }

    upgradeConfirmGuardRef.current.add(guardKey);
    try {
      // 不 await：runConfirmUpgrade 的同步段已完成下发，余下的长
      // 请求由其内部按目标维护 loading 态与回执 toast。
      void runConfirmUpgrade();
    } finally {
      upgradeConfirmGuardRef.current.delete(guardKey);
    }
  };

  const runConfirmUpgrade = async () => {
    const version = selectedVersion || undefined;

    if (upgradeTarget === "single" && upgradeTargetNodeId) {
      setUpgradeModalOpen(false);
      // Find the node
      const node = nodeList.find((n) => n.id === upgradeTargetNodeId);

      if (!node) return;
      setNodeList((prev) =>
        prev.map((n) =>
          n.id === upgradeTargetNodeId ? { ...n, upgradeLoading: true } : n,
        ),
      );
      try {
        const res = await upgradeNode(
          upgradeTargetNodeId,
          version,
          releaseChannel,
        );

        if (res.code === 0) {
          // R2X-021：同版本短路时后端不下发不重启，回
          // data={alreadyLatest:true,message:"节点已是最新版本…，无需升级"}，
          // 须直接显示后端 message，不得再陈述未发生的命令与重启。
          const data = (res.data ?? null) as {
            alreadyLatest?: boolean;
            message?: string;
          } | null;

          if (data?.alreadyLatest) {
            toast.success(data.message || "节点已是最新版本，无需升级");
          } else {
            toast.success(`节点升级命令已发送，节点将自动重启`);
          }
        } else {
          toast.error(res.msg || "升级失败");
        }
      } catch {
        toast.error("网络错误，请重试");
      } finally {
        setNodeList((prev) =>
          prev.map((n) =>
            n.id === upgradeTargetNodeId ? { ...n, upgradeLoading: false } : n,
          ),
        );
      }
    } else if (upgradeTarget === "batch") {
      const selectedLocalIds = Array.from(selectedIds).filter((id) =>
        nodeList.some((node) => node.id === id),
      );

      if (selectedLocalIds.length === 0) {
        toast.error("请选择本地节点进行升级");
        setUpgradeModalOpen(false);

        return;
      }

      // R8-013：超后端单批上限时提交前拦截并点名（弹窗保持打开），
      // 不再等整单提交后才被后端拒绝。
      if (selectedLocalIds.length > BATCH_UPGRADE_NODE_LIMIT) {
        toast.error(
          `单次批量升级最多 ${BATCH_UPGRADE_NODE_LIMIT} 台节点，当前已选 ${selectedLocalIds.length} 台本地节点，请减少选择后分批提交`,
        );

        return;
      }

      setBatchUpgradeLoading(true);
      setUpgradeModalOpen(false);
      try {
        const res = await batchUpgradeNodes(
          selectedLocalIds,
          version,
          releaseChannel,
        );

        if (res.code === 0) {
          // R2X-021：后端逐节点回执 data.results[]（{id,success,message}），
          // 须汇总成功/失败计数，失败时列首条原因，不得无条件报全量成功。
          const results = Array.isArray((res.data as any)?.results)
            ? ((res.data as any).results as Array<{
                success?: boolean;
                message?: string;
                alreadyLatest?: boolean;
              }>)
            : [];

          if (results.length === 0) {
            toast.success(
              `批量升级命令已发送到 ${selectedLocalIds.length} 个节点`,
            );
          } else {
            const failed = results.filter((item) => !item.success);
            // R8-015：alreadyLatest 为同版本短路——回执 Success 但命令
            // 未下发，汇总必须与「已下发」分开计数，不得计入已发送。
            const skippedLatest = results.filter(
              (item) => item.success && item.alreadyLatest,
            );
            const dispatched =
              results.length - failed.length - skippedLatest.length;
            const skippedNote =
              skippedLatest.length > 0
                ? `，另有 ${skippedLatest.length} 个已是最新版本（未下发）`
                : "";

            if (failed.length === 0) {
              if (dispatched === 0) {
                toast.success(
                  `所选节点均已是最新版本（${skippedLatest.length} 个），未下发升级命令`,
                );
              } else {
                toast.success(
                  `批量升级命令已发送到 ${dispatched} 个节点${skippedNote}`,
                );
              }
            } else if (dispatched === 0 && skippedLatest.length === 0) {
              toast.error(
                `批量升级全部失败（${failed.length} 个节点）：${failed[0]?.message || "未知错误"}`,
              );
            } else {
              toast(
                `批量升级部分完成：已下发 ${dispatched} 个，失败 ${failed.length} 个${skippedNote}，首条失败：${failed[0]?.message || "未知错误"}`,
                { icon: "⚠️", duration: 5000 },
              );
            }
          }
        } else if (res.code === -1) {
          // B05-002：code=-1 是无回执（超时/断连/中止），不等于服务端
          // 未执行——请求可能已抵达且服务端仍在逐台升级。不得呈现为
          // 确定失败、更不得诱导立即重试（与在途升级叠加），明示刷新
          // 节点列表核对各节点版本。
          toast(
            "未收到批量升级回执，服务端可能仍在执行，请稍后刷新节点列表核对各节点版本，勿立即重复提交",
            { icon: "⚠️", duration: 8000 },
          );
        } else {
          toast.error(res.msg || "批量升级失败");
        }
      } catch {
        toast(
          "未收到批量升级回执，服务端可能仍在执行，请稍后刷新节点列表核对各节点版本，勿立即重复提交",
          { icon: "⚠️", duration: 8000 },
        );
      } finally {
        setBatchUpgradeLoading(false);
      }
    }
  };

  // 回退节点
  const handleRollbackNode = (node: Node) => {
    setNodeToRollback(node);
    setRollbackModalOpen(true);
  };

  const confirmRollback = async () => {
    if (!nodeToRollback) return;
    const node = nodeToRollback;

    if (rollbackConfirmGuardRef.current.has(node.id)) return;
    // 该节点已有回退在途（rollbackLoading 呈现中）时忽略重复确认
    if (nodeList.find((n) => n.id === node.id)?.rollbackLoading) return;

    rollbackConfirmGuardRef.current.add(node.id);
    try {
      // 不 await：runConfirmRollback 的同步段已完成下发。
      void runConfirmRollback(node);
    } finally {
      rollbackConfirmGuardRef.current.delete(node.id);
    }
  };

  const runConfirmRollback = async (node: Node) => {
    setRollbackModalOpen(false);
    setNodeList((prev) =>
      prev.map((n) => (n.id === node.id ? { ...n, rollbackLoading: true } : n)),
    );
    try {
      const res = await rollbackNode(node.id);

      if (res.code === 0) {
        // R7-03（承 R2X-021 同口径）：后端回执 data.message 点名实际
        // 落到的版本，存量备份/旧 agent 未经代际校验时同一字段带
        // 未校验披露，须直接显示后端 message；data 缺失时才回落
        // 本地文案。
        const data = (res.data ?? null) as { message?: string } | null;

        toast.success(
          data?.message || `节点 ${node.name} 回退命令已发送，节点将自动重启`,
        );
      } else {
        toast.error(res.msg || "回退失败");
      }
    } catch {
      toast.error("网络错误，请重试");
    } finally {
      setNodeList((prev) =>
        prev.map((n) =>
          n.id === node.id ? { ...n, rollbackLoading: false } : n,
        ),
      );
      // 仅当待回退选择仍是本节点时才清空，避免抹掉用户在本次回退
      // 在途期间为另一节点打开的选择。
      setNodeToRollback((prev) => (prev && prev.id === node.id ? null : prev));
    }
  };

  // R2X-004：创建表单幂等键（同表单复用、成功轮换）。
  const { idemKey, rotateIdemKey } = useIdemKey();

  // R3X-027：创建弹窗打开（上升沿且新建模式）时轮换幂等键；窗内重试仍复用同键。
  useRotateIdemKeyOnCreateOpen(dialogVisible, !isEdit, rotateIdemKey);

  // 提交表单
  const handleSubmit = async () => {
    if (!validateForm()) return;

    setSubmitLoading(true);

    try {
      const { serverHost, ...rest } = form;
      const sshConfig =
        form.forwardMode === "nftables"
          ? {
              host: form.sshHost.trim(),
              port: Number(form.sshPort || 22),
              username: form.sshUsername.trim(),
              authType: form.sshAuthType,
              password:
                form.sshAuthType === "password"
                  ? form.sshPassword.trim()
                  : undefined,
              privateKey:
                form.sshAuthType === "private_key"
                  ? form.sshPrivateKey
                  : undefined,
              passphrase: form.sshPassphrase,
              sudoMode: form.sshSudoMode,
            }
          : null;
      const data = {
        ...rest,
        remark: form.remark.trim(),
        displayGroup: form.displayGroup.trim(),
        expiryTime: form.expiryTime,
        renewalCycle: form.renewalCycle,
        extraIPs: form.extraIPs,
        forwardMode: form.forwardMode,
        sshConfig,
        serverIp:
          form.serverIpV4?.trim() ||
          form.serverIpV6?.trim() ||
          serverHost?.trim() ||
          "",
      };

      const res = isEdit
        ? await updateNode(data)
        : await createNode(data, idemKey);

      if (res.code === 0 && !isEdit) rotateIdemKey();

      if (res.code === 0) {
        toast.success(isEdit ? "更新成功" : "创建成功");
        // T-001：编辑保存后白名单刷新失败时后端在信封 warning 里点名
        // 受影响入站（network.ts 合同：must be surfaced），须原样透出，
        // 不得只报固定成功文案（创建路径后端不发此 warning，恒为空）。
        if (res.warning) {
          toast(res.warning, { icon: "⚠️", duration: 12000 });
        }
        setDialogVisible(false);

        if (isEdit) {
          setNodeList((prev) =>
            prev.map((n) =>
              n.id === form.id
                ? {
                    ...n,
                    name: form.name,
                    remark: form.remark.trim(),
                    displayGroup: form.displayGroup.trim(),
                    expiryTime: form.expiryTime,
                    renewalCycle: form.renewalCycle,
                    idleTimeoutSeconds: form.idleTimeoutSeconds,
                    serverIp:
                      form.serverIpV4?.trim() ||
                      form.serverIpV6?.trim() ||
                      form.serverHost?.trim() ||
                      "",
                    serverIpV4: form.serverIpV4,
                    serverIpV6: form.serverIpV6,
                    port: form.port,
                    tcpListenAddr: form.tcpListenAddr,
                    udpListenAddr: form.udpListenAddr,
                    interfaceName: form.interfaceName,
                    forwardMode: form.forwardMode,
                    sshConfig,
                    http: form.http,
                    tls: form.tls,
                    socks: form.socks,
                  }
                : n,
            ),
          );
        } else {
          loadNodes();
        }
      } else {
        toast.error(res.msg || (isEdit ? "更新失败" : "创建失败"));
      }
    } catch {
      toast.error("网络错误，请重试");
    } finally {
      setSubmitLoading(false);
    }
  };

  // 重置表单
  const resetForm = () => {
    setEditingHostKeyFingerprint("");
    setForm({
      id: null,
      name: "",
      remark: "",
      displayGroup: "",
      expiryTime: 0,
      renewalCycle: "",
      idleTimeoutSeconds: 0,
      serverHost: "",
      serverIpV4: "",
      serverIpV6: "",
      port: "10000-65535",
      tcpListenAddr: "[::]",
      udpListenAddr: "[::]",
      interfaceName: "",
      extraIPs: "",
      forwardMode: "agent",
      sshHost: "",
      sshPort: "22",
      sshUsername: "",
      sshAuthType: "password",
      sshPassword: "",
      sshPrivateKey: "",
      sshPassphrase: "",
      sshSudoMode: "none",
      http: 0,
      tls: 0,
      socks: 0,
    });
    setErrors({});
  };

  // 处理拖拽结束
  // 立即探测节点到延迟测试点的延迟。
  const probeNode = async (id: number) => {
    setProbingIds((prev) => new Set(prev).add(id));
    try {
      const res = await probeNodeLatency([id]);
      const latency = res.code === 0 ? res.data?.[String(id)] : undefined;

      if (!latency) {
        toast.error(res.msg || "检测失败");

        return;
      }
      setNodeList((prev) =>
        prev.map((n) =>
          n.id === id ? { ...n, latency: latestProbe(latency, n.latency) } : n,
        ),
      );
    } catch {
      toast.error("检测失败");
    } finally {
      setProbingIds((prev) => {
        const next = new Set(prev);

        next.delete(id);

        return next;
      });
    }
  };

  const handleDragEnd = async (event: DragEndEvent) => {
    const { active, over } = event;

    if (!active || !over || active.id === over.id) return;
    if (!nodeOrder || nodeOrder.length === 0) return;

    const activeId = Number(active.id);
    const overId = Number(over.id);

    if (isNaN(activeId) || isNaN(overId)) return;

    const displayNodeIds = displayNodes.map((node) => node.id);
    const oldIndex = displayNodeIds.indexOf(activeId);
    const newIndex = displayNodeIds.indexOf(overId);

    if (oldIndex === -1 || newIndex === -1 || oldIndex === newIndex) return;

    const reorderedDisplayIds = arrayMove(displayNodeIds, oldIndex, newIndex);
    const displayIdSet = new Set(displayNodeIds);
    let reorderedDisplayIndex = 0;

    const newOrder = nodeOrder.map((id) => {
      if (!displayIdSet.has(id)) {
        return id;
      }

      const nextId = reorderedDisplayIds[reorderedDisplayIndex];

      reorderedDisplayIndex += 1;

      return nextId;
    });

    setNodeOrder(newOrder);

    saveOrder(nodeOrderLocalKey(), newOrder);

    // 持久化到数据库。E2-04：后端按 batchOperationResult 逐项回执
    // （code:0 仍可含 failCount>0），且本页在请求前已乐观改序并写
    // localStorage——任一失败都不得呈现为已保存：整单失败回滚状态
    // 与本地副本，部分失败按服务器 inx 重同步（loadNodes 以 DB 序
    // 重建），与 DB 真值对齐。
    const rollbackOrder = () => {
      setNodeOrder(nodeOrder);
      saveOrder(nodeOrderLocalKey(), nodeOrder);
    };

    try {
      const nodesToUpdate = newOrder.map((id, index) => ({ id, inx: index }));
      const response = await updateNodeOrder({ nodes: nodesToUpdate });

      if (response.code === 0) {
        const receipt = response.data as BatchOperationResult | undefined;
        const failCount = receipt?.failCount ?? 0;

        if (failCount > 0) {
          const failures = extractBatchFailures(response.data);
          const firstReason = failures[0]?.reason || failures[0]?.name || "";

          toast.error(
            `保存排序部分失败：成功 ${receipt?.successCount ?? 0} 项，失败 ${failCount} 项${firstReason ? `（${firstReason}）` : ""}，已按服务器顺序刷新`,
          );
          saveOrder(nodeOrderLocalKey(), nodeOrder);
          await loadNodes({ silent: true });

          return;
        }

        setNodeList((prev) =>
          prev.map((node) => {
            const updated = nodesToUpdate.find((n) => n.id === node.id);

            return updated ? { ...node, inx: updated.inx } : node;
          }),
        );
      } else {
        rollbackOrder();
        toast.error("保存排序失败：" + (response.msg || "未知错误"));
      }
    } catch {
      rollbackOrder();
      toast.error("保存排序失败，请重试");
    }
  };

  // 批量操作处理函数
  const toggleSelectMode = () => {
    setSelectMode((prev) => {
      if (prev) {
        setSelectedIds(new Set());
      }

      return !prev;
    });
  };

  const toggleSelect = (id: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);

      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }

      return next;
    });
  };

  const selectAll = () => {
    setSelectedIds(new Set(displayNodes.map((n) => n.id)));
  };

  const deselectAll = () => {
    setSelectedIds(new Set());
  };

  const handleBatchDelete = async (uninstall: boolean) => {
    if (selectedIds.size === 0 || batchLoading) return;
    setBatchLoading(true);
    try {
      const res = await batchDeleteNodes(Array.from(selectedIds), uninstall);

      if (res.code === 0) {
        const result = res.data as
          | (BatchOperationResult & { brokenReferrers?: string[] })
          | null
          | undefined;

        // 用全量类型消费逐条回执：failures 带 name/reason，失败时经
        // 结果弹窗逐条呈现，不得只取 id 把原因丢弃。
        const failures = extractBatchFailures(result);
        const failedIds = new Set(
          failures.map((f) => Number(f.id ?? 0)).filter((id) => id > 0),
        );
        const failCount = result?.failCount ?? failedIds.size;

        if (
          result &&
          typeof result.successCount === "number" &&
          (failCount === 0 || failedIds.size > 0)
        ) {
          // 后端逐条回执：只移除成功项，失败项保留在列表并如实播报（A9-04）
          const successCount = result.successCount;

          setNodeList((prev) =>
            prev.filter((n) => !selectedIds.has(n.id) || failedIds.has(n.id)),
          );
          if (failCount > 0) {
            toast.error(
              `成功删除 ${successCount} 个节点，失败 ${failCount} 个，失败的节点已保留在列表中`,
            );
            if (failures.length > 0) {
              setBatchResultModal({
                failures,
                open: true,
                summary: `成功 ${successCount} 项，失败 ${failCount} 项`,
                title: "批量删除结果",
              });
            }
          } else {
            toast.success(`成功删除 ${successCount} 个节点`);
          }
          // R2X-045：后端回传的权威跨节点引用清单，删除后如实播报。
          const returnedBroken = result?.brokenReferrers ?? [];

          if (returnedBroken.length > 0) {
            toast(
              `删除打断了 ${returnedBroken.length} 个跨节点引用：${returnedBroken.join("、")}`,
              { icon: "⚠️", duration: 5000 },
            );
          }
          // 唯一入口/出口点名：后端已逐条回传，原样呈现不吞。
          const returnedSoleRole = result?.soleRoleWarnings ?? [];

          if (returnedSoleRole.length > 0) {
            toast(returnedSoleRole.join("；"), {
              icon: "⚠️",
              duration: 6000,
            });
          }
        } else {
          // 后端未给逐条回执时不按选中数虚报全成功：以服务器列表为准刷新，
          // 删除失败的节点会重新出现（A9-04 前端半）
          toast.success("删除请求已完成，正在刷新列表确认结果");
          if (failures.length > 0) {
            setBatchResultModal({
              failures,
              open: true,
              summary: `失败 ${failCount} 项`,
              title: "批量删除结果",
            });
          }
          await loadNodes({ silent: true });
        }
        // 连带卸载三态汇总播报（仅请求带 uninstall 时后端才填充）。
        if (uninstall && result) {
          const parts: string[] = [];

          if (result.uninstalledCount) {
            parts.push(`${result.uninstalledCount} 个节点的 agent 正在卸载`);
          }
          if (result.uninstallPendingCount) {
            parts.push(
              `${result.uninstallPendingCount} 个离线节点将在下次连接时自动卸载`,
            );
          }
          if (result.uninstallUnsupportedCount) {
            parts.push(
              `${result.uninstallUnsupportedCount} 个节点的 agent 版本过旧、需手动卸载`,
            );
          }
          if (parts.length > 0) {
            toast(parts.join("；"), { icon: "🗑️", duration: 6000 });
          }
          if (result.uninstallWarnings && result.uninstallWarnings.length > 0) {
            toast(result.uninstallWarnings.join("；"), {
              icon: "⚠️",
              duration: 6000,
            });
          }
        }
        setSelectedIds(new Set());
        setBatchDeleteModalOpen(false);
        setSelectMode(false);
      } else {
        toast.error(res.msg || "删除失败");
      }
    } catch {
      toast.error("网络错误，请重试");
    } finally {
      setBatchLoading(false);
    }
  };

  // 传感器配置
  const sensors = useSensors(
    useSensor(MouseSensor, {
      activationConstraint: {
        distance: 8,
      },
    }),
    useSensor(TouchSensor, {
      activationConstraint: {
        delay: 250,
        tolerance: 8,
      },
    }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  const nodeExpiryStats = useMemo(() => {
    return nodeList.reduce(
      (acc, node) => {
        const meta = getNodeExpiryMeta(node.expiryTime, node.renewalCycle);

        if (meta.state === "expired") acc.expired += 1;
        if (meta.state === "expiringSoon") acc.expiringSoon += 1;
        if (getNodeReminderEnabled(node)) {
          acc.withExpiry += 1;
        }

        return acc;
      },
      { expired: 0, expiringSoon: 0, withExpiry: 0 },
    );
  }, [nodeList]);

  // 根据排序顺序获取节点列表
  const sortedNodes = useMemo((): Node[] => {
    if (!nodeList || nodeList.length === 0) return [];

    const sortedByDb = [...nodeList].sort((a, b) => {
      const aInx = a.inx ?? 0;
      const bInx = b.inx ?? 0;

      return aInx - bInx;
    });

    // 如果数据库中没有排序信息，则使用本地存储的顺序
    if (
      nodeOrder &&
      nodeOrder.length > 0 &&
      sortedByDb.every((n) => n.inx === undefined || n.inx === 0)
    ) {
      const nodeMap = new Map(nodeList.map((n) => [n.id, n] as const));
      const localSorted: Node[] = [];

      nodeOrder.forEach((id) => {
        const node = nodeMap.get(id);

        if (node) localSorted.push(node);
      });

      nodeList.forEach((node) => {
        if (!nodeOrder.includes(node.id)) {
          localSorted.push(node);
        }
      });

      return localSorted;
    }

    return sortedByDb;
  }, [nodeList, nodeOrder]);

  const filterNodesByKeyword = useCallback((nodes: Node[], keyword: string) => {
    const normalizedKeyword = keyword.trim().toLowerCase();

    if (!normalizedKeyword) {
      return nodes;
    }

    return nodes.filter(
      (node) =>
        (node.name && node.name.toLowerCase().includes(normalizedKeyword)) ||
        (node.remark &&
          node.remark.toLowerCase().includes(normalizedKeyword)) ||
        (node.serverIp &&
          node.serverIp.toLowerCase().includes(normalizedKeyword)) ||
        (node.serverIpV4 &&
          node.serverIpV4.toLowerCase().includes(normalizedKeyword)) ||
        (node.serverIpV6 &&
          node.serverIpV6.toLowerCase().includes(normalizedKeyword)),
    );
  }, []);

  const localNodes = sortedNodes;

  const filteredLocalNodes = useMemo(() => {
    const keywordFiltered = filterNodesByKeyword(
      localNodes,
      localSearchKeyword,
    );

    if (nodeFilterMode === "all") {
      return keywordFiltered;
    }

    return keywordFiltered.filter((node) => {
      const expiryMeta = getNodeExpiryMeta(node.expiryTime, node.renewalCycle);

      switch (nodeFilterMode) {
        case "expiringSoon":
          return expiryMeta.state === "expiringSoon";
        case "expired":
          return expiryMeta.state === "expired";
        case "withExpiry":
          return getNodeReminderEnabled(node);
        default:
          return true;
      }
    });
  }, [filterNodesByKeyword, localNodes, localSearchKeyword, nodeFilterMode]);

  const currentSearchKeyword = localSearchKeyword;
  const setCurrentSearchKeyword = setLocalSearchKeyword;

  // Row callbacks bundled for the memoized row components: the actions
  // object itself never changes identity (it delegates through a ref to
  // the latest closures), so rows whose data did not change are skipped
  // on every poll/realtime re-render of this page.
  const rowActionsRef = useRef<NodeRowActions | null>(null);

  rowActionsRef.current = {
    toggleSelect,
    dismissExpiryReminder: handleDismissExpiryReminder,
    updateInfoPopoverPlacement,
    openInstallSelector,
    openUpgradeModal,
    rollbackNode: handleRollbackNode,
    openTune: (node) => setTuneNode(node),
    editNode: handleEdit,
    deleteNode: handleDelete,
    probeNode,
    editLatencyTargets: (node) => setTargetNodes([node]),
    openLatencySettings: () =>
      navigate("/config", { state: { scrollTo: "latency-targets" } }),
    showRoute: (node) => setRouteNode(node),
    showTrend: (node) => setTrendNode(node),
    openSheet: (id) => setSheetNodeId(id),
    openMonitor: onOpenMonitor,
  };

  const rowActions = useMemo<NodeRowActions>(() => {
    const delegate =
      <K extends keyof NodeRowActions>(key: K) =>
      (...args: Parameters<NonNullable<NodeRowActions[K]>>) =>
        (rowActionsRef.current?.[key] as any)?.(...args);

    return {
      toggleSelect: delegate("toggleSelect"),
      dismissExpiryReminder: delegate("dismissExpiryReminder"),
      updateInfoPopoverPlacement: delegate("updateInfoPopoverPlacement"),
      openInstallSelector: delegate("openInstallSelector"),
      openUpgradeModal: delegate("openUpgradeModal"),
      rollbackNode: delegate("rollbackNode"),
      openTune: delegate("openTune"),
      editNode: delegate("editNode"),
      deleteNode: delegate("deleteNode"),
      probeNode: delegate("probeNode"),
      editLatencyTargets: delegate("editLatencyTargets"),
      openLatencySettings: delegate("openLatencySettings"),
      showRoute: delegate("showRoute"),
      showTrend: delegate("showTrend"),
      openSheet: delegate("openSheet"),
      openMonitor: (...args) => rowActionsRef.current?.openMonitor?.(...args),
    };
  }, []);

  const displayNodes = filteredLocalNodes;
  const hasKeywordSearch = currentSearchKeyword.trim().length > 0;
  const hasActiveFilters = nodeFilterMode !== "all";
  const isDisplayFiltered = hasKeywordSearch || hasActiveFilters;

  // 展示分组（纯展示）：标签全集取自全量节点（表单提示与排序弹窗
  // 消费），分段只作用于已过滤的显示列表。
  const allGroupLabels = useMemo(
    () => collectNodeGroupLabels(nodeList),
    [nodeList],
  );
  // 分段口径：显示列表里没有任何带标签节点时回落平铺渲染（与无
  // 分组时完全一致、零段标题）；否则先过滤后分段、空段不显示、
  // 未分组沉底。
  const groupSections = useMemo(() => {
    if (!displayNodes.some((node) => nodeGroupLabel(node) !== "")) {
      return null;
    }

    return segmentNodesByGroup(displayNodes, groupOrder);
  }, [displayNodes, groupOrder]);
  // 分组全集（v2）：登记名单∪在用标签——移入菜单、分组管理弹窗与
  // 编辑表单下拉的共同选项源；空组（0 台）在名单里存在、页面不出段。
  const knownGroups = useMemo(
    () => mergeNodeGroupRegistry(groupOrder, allGroupLabels),
    [groupOrder, allGroupLabels],
  );
  const groupCounts = useMemo(() => {
    const counts: Record<string, number> = {};

    for (const node of nodeList) {
      const label = nodeGroupLabel(node);

      if (label) counts[label] = (counts[label] ?? 0) + 1;
    }

    return counts;
  }, [nodeList]);
  const saveGroupOrder = useCallback(async (order: string[]) => {
    try {
      const res = await updateConfig(
        NODE_GROUP_ORDER_CONFIG_KEY,
        JSON.stringify(order),
      );

      if (res.code !== 0) return false;
      // T-001：信封 warning（延迟优选丢弃点名，network.ts 合同：
      // must be surfaced）须原样透出；静默持久化无成功 toast，
      // 只在后端点名时出声。
      if (res.warning) {
        toast(res.warning, { icon: "⚠️", duration: 12000 });
      }
      setGroupOrder(order);

      return true;
    } catch {
      return false;
    }
  }, []);

  // 分组管理三操作（v2）：名单以服务端回执的 order 为准刷新；改名/
  // 删组级联改了成员标签，静默重拉节点列表对齐段与计数。
  const handleCreateGroup = useCallback(
    async (name: string): Promise<GroupOpResult> => {
      try {
        const res = await createNodeGroup(name);

        if (res.code !== 0) {
          return { ok: false, msg: res.msg || "创建失败" };
        }
        if (res.data?.order) setGroupOrder(res.data.order);

        return { ok: true };
      } catch {
        return { ok: false, msg: "网络错误，请重试" };
      }
    },
    [],
  );
  const handleRenameGroup = useCallback(
    async (oldName: string, newName: string): Promise<GroupOpResult> => {
      try {
        const res = await renameNodeGroup(oldName, newName);

        if (res.code !== 0) {
          return { ok: false, msg: res.msg || "改名失败" };
        }
        if (res.data?.order) setGroupOrder(res.data.order);
        await loadNodes({ silent: true });
        const count = res.data?.updatedCount ?? 0;

        return {
          ok: true,
          msg:
            count > 0
              ? `已改名为「${newName}」，${count} 台节点已同步`
              : `已改名为「${newName}」`,
        };
      } catch {
        return { ok: false, msg: "网络错误，请重试" };
      }
    },
    [loadNodes],
  );
  const handleDeleteGroup = useCallback(
    async (name: string): Promise<GroupOpResult> => {
      try {
        const res = await deleteNodeGroup(name);

        if (res.code !== 0) {
          return { ok: false, msg: res.msg || "删除失败" };
        }
        if (res.data?.order) setGroupOrder(res.data.order);
        await loadNodes({ silent: true });
        const count = res.data?.updatedCount ?? 0;

        return {
          ok: true,
          msg:
            count > 0
              ? `分组「${name}」已删除，${count} 台节点已回未分组`
              : `分组「${name}」已删除`,
        };
      } catch {
        return { ok: false, msg: "网络错误，请重试" };
      }
    },
    [loadNodes],
  );

  // 批量移入/移出分组：消费后端逐条回执（M-0051 口径）——只把成功
  // 项的标签改到本地状态，失败项保留勾选并经结果弹窗逐条呈现原
  // 因，不按选中数虚报全成功（与批量删除同纪律）。
  const handleMoveToGroup = async (group: string) => {
    if (selectedIds.size === 0 || groupMoving) return;
    setGroupMoving(true);
    const ids = Array.from(selectedIds);

    try {
      const res = await batchSetNodeGroup(ids, group);

      if (res.code !== 0) {
        toast.error(res.msg || "移入分组失败");

        return;
      }
      const result = res.data as BatchOperationResult | null | undefined;
      const failures = extractBatchFailures(result);
      const failedIds = new Set(
        failures.map((f) => Number(f.id ?? 0)).filter((id) => id > 0),
      );
      const failCount = result?.failCount ?? failedIds.size;

      if (
        result &&
        typeof result.successCount === "number" &&
        (failCount === 0 || failedIds.size > 0)
      ) {
        const successCount = result.successCount;

        setNodeList((prev) =>
          prev.map((node) =>
            selectedIds.has(node.id) && !failedIds.has(node.id)
              ? { ...node, displayGroup: group }
              : node,
          ),
        );
        if (failCount > 0) {
          toast.error(
            `成功 ${successCount} 个节点，失败 ${failCount} 个，失败的节点已保留勾选`,
          );
          if (failures.length > 0) {
            setBatchResultModal({
              failures,
              open: true,
              summary: `成功 ${successCount} 项，失败 ${failCount} 项`,
              title: "移入分组结果",
            });
          }
          setSelectedIds(failedIds);
        } else {
          toast.success(
            group
              ? `已将 ${successCount} 个节点移入「${group}」`
              : `已将 ${successCount} 个节点移出分组`,
          );
          toggleSelectMode();
        }
      } else {
        // 后端未给逐条回执时不虚报：以服务器列表为准刷新。
        toast.success("操作已完成，正在刷新列表确认结果");
        await loadNodes({ silent: true });
      }
    } catch {
      toast.error("移入分组失败，请重试");
    } finally {
      setGroupMoving(false);
    }
  };

  const sortableNodeIds = useMemo(
    () => displayNodes.map((n) => n.id),
    [displayNodes],
  );

  const renderNodeRow = (node: Node) => {
    const month = monthTraffic[String(node.id)];

    return card ? (
      <NodeCardRow
        key={node.id}
        actions={rowActions}
        infoPlacement={infoPopoverPlacement[node.id] ?? "left"}
        latencyPoolEmpty={latencyPoolEmpty}
        monthIn={month?.[0]}
        monthLabel={monthTrafficText(node.id)}
        monthOut={month?.[1]}
        node={node}
        probing={probingIds.has(node.id)}
        selectMode={selectMode}
        selected={selectedIds.has(node.id)}
        showMonitor={!!onOpenMonitor}
        upgradeProgressEntry={upgradeProgress[node.id]}
      />
    ) : (
      <NodeListRow
        key={node.id}
        actions={rowActions}
        isDisplayFiltered={isDisplayFiltered}
        latencyPoolEmpty={latencyPoolEmpty}
        monthIn={month?.[0]}
        monthLabel={monthTrafficText(node.id)}
        monthOut={month?.[1]}
        node={node}
        probing={probingIds.has(node.id)}
        selectMode={selectMode}
        selected={selectedIds.has(node.id)}
        upgradeProgressEntry={upgradeProgress[node.id]}
      />
    );
  };

  return (
    <PageShell embedded={embedded}>
      {!embedded && (
        <PageHeader
          description="装了 agent 的服务器，用来搭建落地和中转"
          primary={{ key: "add", label: "新建节点", onPress: handleAdd }}
          title="节点"
        />
      )}
      {selectMode ? (
        <SelectionBar
          actions={[
            {
              key: "upgrade",
              label: "升级",
              disabled: batchUpgradeLoading,
              onPress: () => openUpgradeModal("batch"),
            },
            {
              key: "targets",
              label: "测试点",
              disabled: !displayNodes.some((n) => selectedIds.has(n.id)),
              onPress: () =>
                setTargetNodes(
                  displayNodes.filter((n) => selectedIds.has(n.id)),
                ),
            },
            {
              key: "delete",
              label: "删除",
              danger: true,
              onPress: () => void openBatchDeleteModal(),
            },
          ]}
          allSelected={
            displayNodes.length > 0 &&
            displayNodes.every((n) => selectedIds.has(n.id))
          }
          count={selectedIds.size}
          onExit={toggleSelectMode}
          onToggleAll={() =>
            displayNodes.every((n) => selectedIds.has(n.id))
              ? deselectAll()
              : selectAll()
          }
        >
          <Dropdown placement="bottom-end">
            <DropdownTrigger>
              <Button
                isDisabled={selectedIds.size === 0 || groupMoving}
                size="sm"
                variant="flat"
              >
                移入分组
              </Button>
            </DropdownTrigger>
            <DropdownMenu aria-label="移入分组">
              {knownGroups.map((label) => (
                <DropdownItem
                  key={label}
                  onPress={() => void handleMoveToGroup(label)}
                >
                  {label}
                </DropdownItem>
              ))}
              {knownGroups.length > 0 && <DropdownMenuSeparator />}
              <DropdownItem
                key="__ungrouped__"
                onPress={() => void handleMoveToGroup("")}
              >
                未分组（移出）
              </DropdownItem>
            </DropdownMenu>
          </Dropdown>
        </SelectionBar>
      ) : (
        <PageToolbar
          more={[
            {
              key: "batch",
              label: "批量操作",
              disabled: displayNodes.length === 0,
              onPress: toggleSelectMode,
            },
          ]}
          placeholder="搜索名称或 IP"
          search={currentSearchKeyword}
          onSearch={setCurrentSearchKeyword}
        >
          <Button
            className="shrink-0"
            color={nodeFilterMode !== "all" ? "primary" : "default"}
            size="sm"
            variant="flat"
            onPress={() => setIsFilterModalOpen(true)}
          >
            {nodeFilterMode !== "all" ? "已筛选" : "筛选"}
          </Button>
          <Button
            className="shrink-0"
            size="sm"
            variant="flat"
            onPress={() => setCreateGroupOpen(true)}
          >
            新建分组
          </Button>
          {knownGroups.length > 0 && (
            <Button
              className="shrink-0"
              size="sm"
              variant="flat"
              onPress={() => setGroupManageOpen(true)}
            >
              分组管理
            </Button>
          )}
          <ViewModeToggle value={viewMode} onChange={setViewMode} />
        </PageToolbar>
      )}

      {!wsConnected && (
        <Alert
          className="mb-3"
          color="warning"
          description={
            wsConnecting
              ? "监控连接中..."
              : usingPollingFallback
                ? "监控连接已断开，已切换为列表自动刷新兜底模式。"
                : "监控连接已断开，正在重连..."
          }
          variant="flat"
        />
      )}

      {loading ? (
        <PageLoadingState message="正在加载..." />
      ) : nodeList.length === 0 && nodesLoadFailed ? (
        <EmptyHint
          action={{
            key: "retry",
            label: "重新加载",
            onPress: () => loadNodes(),
          }}
          text="节点列表加载失败，不能据此判断是否已有节点；请检查网络后重试。"
        />
      ) : nodeList.length === 0 ? (
        <EmptyHint
          action={{ key: "add", label: "新建节点", onPress: handleAdd }}
          text="还没有节点。新建后在服务器上运行安装命令，节点就会连上面板"
        />
      ) : displayNodes.length === 0 ? (
        <EmptyHint
          text={isDisplayFiltered ? "没有匹配的节点" : "暂无本地节点"}
        />
      ) : (
        <DndContext sensors={sensors} onDragEnd={handleDragEnd}>
          <SortableContext
            items={sortableNodeIds}
            strategy={rectSortingStrategy}
          >
            <div
              className={
                card ? CARD_GRID : "grid grid-cols-1 gap-2 2xl:grid-cols-2"
              }
            >
              {groupSections
                ? groupSections.map((section) => (
                    <Fragment key={section.label || "__ungrouped"}>
                      <div className="col-span-full flex items-baseline gap-2 pt-2 first:pt-0">
                        <h3 className="text-sm font-bold">
                          {section.label || UNGROUPED_LABEL}
                        </h3>
                        <span className="text-xs font-semibold text-foreground">
                          {section.nodes.length} 台
                        </span>
                      </div>
                      {section.nodes.map((node) => renderNodeRow(node))}
                    </Fragment>
                  ))
                : displayNodes.map((node) => renderNodeRow(node))}
            </div>
          </SortableContext>
        </DndContext>
      )}

      {(() => {
        const node = nodeList.find((n) => n.id === sheetNodeId) ?? null;
        const online = node?.connectionStatus === "online";
        const progress = node ? upgradeProgress[node.id] : undefined;
        const expiryMeta = node
          ? getNodeExpiryMeta(node.expiryTime, node.renewalCycle)
          : null;
        const close = () => setSheetNodeId(null);
        const rows: Array<[string, React.ReactNode]> = node
          ? [
              [
                "IP",
                [node.serverIpV4?.trim(), node.serverIpV6?.trim()]
                  .filter(Boolean)
                  .join(" / ") || node.serverIp.trim(),
              ],
              ["版本", node.version || "未知"],
              // C4-G2：卸载登记状态行（五态不合并；failed/unconfirmed
              // 附原因），与列表/卡片芯片同一信源。
              ...(node.uninstallState
                ? ([
                    [
                      "卸载状态",
                      <NodeUninstallStateChip key="uninstall" node={node} />,
                    ],
                  ] as Array<[string, React.ReactNode]>)
                : []),
              ...(online && node.systemInfo
                ? ([["已运行", formatUptime(node.systemInfo.uptime)]] as Array<
                    [string, React.ReactNode]
                  >)
                : []),
              ...(monthTrafficText(node.id)
                ? ([["本月流量（网卡）", monthTrafficText(node.id)]] as Array<
                    [string, React.ReactNode]
                  >)
                : []),
              ...(node.expiryTime && node.renewalCycle && expiryMeta
                ? ([
                    [
                      "续费",
                      `${expiryMeta.label} · ${formatNodeRenewalTime(
                        expiryMeta.nextDueTime,
                      )}（${getNodeRenewalCycleLabel(node.renewalCycle)}）`,
                    ],
                  ] as Array<[string, React.ReactNode]>)
                : []),
            ]
          : [];

        return (
          <ActionSheet
            actions={
              !node
                ? []
                : [
                    {
                      key: "install",
                      label: "安装命令",
                      color: "primary" as const,
                      onPress: () => openInstallSelector(node),
                    },
                    // R2X-041：同卡片入口，nftables 节点不显示调优（必失败入口）
                    ...(node.forwardMode !== "nftables"
                      ? [
                          {
                            key: "tune",
                            label: "调优",
                            disabled: !online,
                            onPress: () => setTuneNode(node),
                          },
                        ]
                      : []),
                    {
                      key: "edit",
                      label: "编辑",
                      onPress: () => handleEdit(node),
                    },
                    ...(onOpenMonitor
                      ? [
                          {
                            key: "monitor",
                            label: "监控",
                            onPress: () => onOpenMonitor(node.id),
                          },
                        ]
                      : []),
                    {
                      key: "targets",
                      label: "测试点",
                      onPress: () => setTargetNodes([node]),
                    },
                    // 定时重启只对 agent 节点开放：nftables 无 agent
                    // 服务。（原远程节点排除随联邦功能移除一并下线。）
                    ...(node.forwardMode !== "nftables"
                      ? [
                          {
                            key: "restart",
                            label: "定时重启",
                            onPress: () => setRestartNode(node),
                          },
                        ]
                      : []),
                    {
                      key: "upgrade",
                      label: "升级",
                      disabled: !online,
                      onPress: () => openUpgradeModal("single", node.id),
                    },
                    {
                      key: "rollback",
                      label: "回退",
                      disabled: !online,
                      onPress: () => handleRollbackNode(node),
                    },
                    ...(node.expiryTime &&
                    node.renewalCycle &&
                    !node.expiryReminderDismissed &&
                    expiryMeta?.isHighlighted
                      ? [
                          {
                            key: "dismiss",
                            label: "关闭到期提醒",
                            onPress: () => handleDismissExpiryReminder(node.id),
                          },
                        ]
                      : []),
                    {
                      key: "delete",
                      label: "删除",
                      color: "danger" as const,
                      onPress: () => handleDelete(node),
                    },
                  ]
            }
            isOpen={Boolean(node)}
            subtitle={node && (online ? "在线" : "离线 · agent 没有连上面板")}
            title={node?.name ?? ""}
            onClose={close}
          >
            {node && (
              <div className="space-y-2 text-sm">
                {rows.map(([k, v]) => (
                  <div key={k} className="flex justify-between gap-3">
                    <span className="shrink-0 text-foreground">{k}</span>
                    <span className="min-w-0 break-all text-right font-mono text-xs font-semibold leading-5">
                      {v}
                    </span>
                  </div>
                ))}
                {progress && progress.percent < 100 && (
                  <Progress
                    showValueLabel
                    aria-label="升级进度"
                    color="warning"
                    label={progress.message}
                    size="sm"
                    value={progress.percent}
                  />
                )}
                {node.remark?.trim() && (
                  <p className="whitespace-pre-wrap break-all text-xs font-semibold text-foreground">
                    {node.remark.trim()}
                  </p>
                )}
              </div>
            )}
          </ActionSheet>
        );
      })()}
      {/* 新增/编辑节点对话框 */}
      <NodeFormModal
        dialogTitle={dialogTitle}
        dialogVisible={dialogVisible}
        errors={errors}
        form={form}
        groupOptions={knownGroups}
        handleSubmit={handleSubmit}
        protocolControlsDisabled={protocolControlsDisabled}
        protocolControlsReason={protocolControlsReason}
        setDialogVisible={setDialogVisible}
        setForm={setForm}
        sshHostKeyFingerprint={editingHostKeyFingerprint}
        submitLoading={submitLoading}
      />

      <NodeTuneModal node={tuneNode} onClose={() => setTuneNode(null)} />

      {/* 回退确认模态框 */}
      <RollbackNodeModal
        confirmRollback={confirmRollback}
        nodeToRollback={nodeToRollback}
        rollbackModalOpen={rollbackModalOpen}
        setRollbackModalOpen={setRollbackModalOpen}
      />

      {/* 删除确认模态框 */}
      <DeleteNodeModal
        brokenReferrers={deleteBrokenReferrers}
        confirmDelete={confirmDelete}
        deleteLoading={deleteLoading}
        deleteModalOpen={deleteModalOpen}
        nodeToDelete={nodeToDelete}
        setDeleteModalOpen={setDeleteModalOpen}
      />

      {/* 安装命令模态框 */}
      <InstallCommandModal
        currentNodeName={currentNodeName}
        handleManualCopy={handleManualCopy}
        installCommand={installCommand}
        installCommandModal={installCommandModal}
        installMode={installMode}
        installModeLoading={installModeLoading}
        installPrivileged={installPrivileged}
        installWarning={installWarning}
        setInstallCommandModal={setInstallCommandModal}
        onInstallModeChange={handleInstallModeChange}
        onInstallPrivilegedChange={handleInstallPrivilegedChange}
      />

      {/* 版本选择升级模态框 */}
      <UpgradeNodeModal
        batchLocalCount={
          Array.from(selectedIds).filter((id) =>
            nodeList.some((n) => n.id === id),
          ).length
        }
        handleConfirmUpgrade={handleConfirmUpgrade}
        releases={releases}
        releasesLoading={releasesLoading}
        selectedIds={selectedIds}
        selectedVersion={selectedVersion}
        setSelectedVersion={setSelectedVersion}
        setUpgradeModalOpen={setUpgradeModalOpen}
        upgradeModalOpen={upgradeModalOpen}
        upgradeTarget={upgradeTarget}
      />

      {/* 批量删除确认模态框 */}
      <BatchDeleteNodesModal
        batchDeleteModalOpen={batchDeleteModalOpen}
        batchLoading={batchLoading}
        brokenReferrers={batchDeleteBrokenReferrers}
        handleBatchDelete={handleBatchDelete}
        selectedIds={selectedIds}
        setBatchDeleteModalOpen={setBatchDeleteModalOpen}
      />

      <BatchActionResultModal
        failures={batchResultModal.failures}
        isOpen={batchResultModal.open}
        summary={batchResultModal.summary}
        title={batchResultModal.title}
        onOpenChange={(open) => {
          if (open) {
            setBatchResultModal((prev) => ({ ...prev, open: true }));

            return;
          }
          setBatchResultModal(EMPTY_BATCH_RESULT_MODAL_STATE);
        }}
      />

      <NodeFilterModal
        isFilterModalOpen={isFilterModalOpen}
        nodeExpiryStats={nodeExpiryStats}
        nodeFilterMode={nodeFilterMode}
        resetNodeFilterMode={resetNodeFilterMode}
        setIsFilterModalOpen={setIsFilterModalOpen}
        setNodeFilterMode={setNodeFilterMode}
      />
      <RouteModal
        nodeId={routeNode?.id ?? null}
        nodeName={routeNode?.name}
        onClose={() => setRouteNode(null)}
      />
      {/* 延迟趋势：数据来自后端 POST /node/latency/history（Lane D），
          入口在 LatencyCell 的 onShowTrend（node-row 的延迟槽位挂载）。 */}
      <LatencyTrendModal
        nodeId={trendNode?.id ?? null}
        nodeName={trendNode?.name}
        onClose={() => setTrendNode(null)}
      />
      <LatencyTargetsModal
        nodes={targetNodes}
        onClose={() => setTargetNodes([])}
        onSaved={() => loadNodes({ silent: true })}
      />
      <RestartScheduleModal
        node={restartNode}
        onClose={() => setRestartNode(null)}
        onSaved={() => loadNodes({ silent: true })}
      />
      <GroupManageModal
        counts={groupCounts}
        groups={knownGroups}
        open={groupManageOpen}
        onClose={() => setGroupManageOpen(false)}
        onCreate={handleCreateGroup}
        onDelete={handleDeleteGroup}
        onRename={handleRenameGroup}
        onSaveOrder={saveGroupOrder}
      />
      <GroupCreateModal
        open={createGroupOpen}
        onClose={() => setCreateGroupOpen(false)}
        onCreate={handleCreateGroup}
      />
    </PageShell>
  );
}
