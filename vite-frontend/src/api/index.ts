import type {
  BatchOperationResult,
  ForwardDiagnosisApiData,
  NftablesTestResult,
  ForwardApiItem,
  NodeReleaseApiItem,
  NodeApiItem,
  NodeConnLogData,
  NodeConnLogQuery,
  SpeedLimitApiItem,
  TunnelBatchDeletePreviewApiData,
  TunnelBatchDeleteWithForwardsApiData,
  TunnelDeletePreviewApiData,
  TunnelDeleteWithForwardsApiData,
  TunnelDiagnosisApiData,
  UserApiItem,
  UserListQuery,
  UserPackageInfoApiData,
  UserTunnelPermissionApiItem,
  TunnelApiItem,
  UserTunnelApiItem,
  UserMutationPayload,
  NodeMutationPayload,
  TunnelMutationPayload,
  UserQuotaResetPayload,
  UserTunnelAssignPayload,
  UserTunnelListQuery,
  UserTunnelRemovePayload,
  ForwardMutationPayload,
  SpeedLimitMutationPayload,
  UpdatePasswordPayload,
  BackupImportPayload,
  NodeMetricApiItem,
  TunnelMetricApiItem,
  ServiceMonitorApiItem,
  ServiceMonitorResultApiItem,
  ServiceMonitorLimitsApiData,
  ServiceMonitorMutationPayload,
  MonitorNodeApiItem,
  MonitorTunnelApiItem,
  MonitorPermissionApiItem,
  MonitorAccessApiData,
  TunnelQualityApiItem,
  TunnelQualityHistoryApiItem,
  StorageSummaryApiData,
  SystemUpgradeCheckApiData,
  SystemUpgradeRunApiData,
  SystemUpgradeStatusApiData,
  SystemUpgradeVersionApiData,
  KernelCapabilitiesApiData,
  KernelApiItem,
  KernelStateApiData,
  InboundApiItem,
  InboundProbe,
  InboundMutationPayload,
  SplitRule,
  InboundShareEntry,
  InboundRelayApiItem,
  SubInfoApiData,
  NodeRoute,
  LatencyTrend,
  RelayLatency,
} from "./types";

import axios from "axios";

import Network from "./network";

import { clearSession } from "@/utils/session";

export type ReleaseChannel = "stable" | "dev";

/**
 * R8-T 族（超时预算三层错位）的前端预算助手。后端界与逐格推导见裁定
 * x-verdicts-r8.md §2 与 r8-lane-t.md 矩阵；此处数值与批 T1 的服务端
 * per-endpoint 放宽、批 T-GATE 的超时登记表对账，改动任一处须同步其余。
 */
/** 批量端点在后端串行逐项执行：每项按 30s 计，下限 120s，上限 30 分钟。 */
const batchCallTimeout = (count: number): number =>
  Math.min(1_800_000, Math.max(120_000, Math.max(0, count) * 30_000));

/**
 * 批量节点升级：前端超时按旧波次口径放大取余量——每节点界 300s
 * （upgradeTimeout），整批界按 ceil(n/5)×300s 估；服务端封顶 50 台
 * （maxBatchUpgradeNodes），写截止 1800s。空版本时整批先付一次版本发现前奏（两段链 ≤120s，
 * 只在派发前解析一次），故前端按台数取「界 + 前奏 120s + 60s 余量」，
 * 下限 360s，上限与服务端写截止齐平 1800s（B05-002 前端面，R2-K2
 * 补计前奏；登记表 T031）。
 */
const batchCallTimeoutUpgrade = (count: number): number => {
  const n = count > 0 ? count : 0;
  const waves = Math.ceil(n / 5);

  return Math.min(1_800_000, Math.max(360_000, waves * 300_000 + 180_000));
};

/**
 * 批量入站删除/启停：后端逐项处理后对受影响内核各下发一次（跨项去
 * 重），每次下发带条件界——内核未装版本时升至 kernelApplyTimeout
 * 300s。不同内核数 ≤ 项数，前端按项数取「n×300s + 60s 余量」，下限
 * 360s，上限与服务端写截止齐平 1800s（B11-001 家族收口，R2-K2；
 * 登记表 T059/T060）。
 */
const batchCallTimeoutKernelApply = (count: number): number => {
  const n = count > 0 ? count : 0;

  return Math.min(1_800_000, Math.max(360_000, n * 300_000 + 60_000));
};

/**
 * 延迟探测（R8-T-08）：作业数 = 节点数 × 每节点测试点（服务端上限 30），
 * 16 路并发、单作业界 22.6s（latencyCommandTimeout）；空 ids = 全部节点
 * （服务端上限 50）。公式界随舰队规模增长，600s 为前端封顶，超出部分
 * 仍可能假超时——只抬阈值，不宣称根除（裁定 §2.8 口径）。
 */
const latencyProbeTimeout = (nodeCount: number): number => {
  const nodes = nodeCount > 0 ? nodeCount : 50;
  const waves = Math.ceil((nodes * 30) / 16);

  return Math.min(600_000, Math.max(90_000, waves * 22_600 + 20_000));
};

/**
 * 中转探测（R8-T-08）：第一段为入口节点延迟探测（按 120s 计），
 * 第二段逐跳 TcpPing，16 路并发、单跳界 22.6s。空 ids = 全部线路，
 * 按 50 条计；同 latencyProbeTimeout 的封顶与残余口径。
 */
const relayProbeTimeout = (relayCount: number): number => {
  const relays = relayCount > 0 ? relayCount : 50;
  const waves = Math.ceil(relays / 16);

  return Math.min(600_000, Math.max(180_000, 120_000 + waves * 22_600));
};

/**
 * 与 diagnosis-stream.ts 的 resolveApiPath 同款规范化拼接：baseURL 恒以
 * `/` 结尾，裸拼会产生 `//`；无斜杠归一的拓扑下 POST 被 Go ServeMux
 * 301 降级为 GET 而失败（R8-K-02）。
 */
const resolveApiPath = (path: string): string => {
  const baseURL = axios.defaults.baseURL || "/api/v1/";
  const normalizedBase = baseURL.endsWith("/") ? baseURL : `${baseURL}/`;

  return `${normalizedBase}${path.replace(/^\//, "")}`;
};

// 登陆相关接口
export interface LoginData {
  username: string;
  password: string;
  captchaId: string;
}

export interface LoginResponse {
  token: string;
  role_id: number;
  name: string;
  requirePasswordChange?: boolean;
}

export const login = (data: LoginData) =>
  Network.post<LoginResponse>("/user/login", data);

export interface PasskeyOptions {
  sessionId: string;
  options: { publicKey: Record<string, unknown> };
}

export interface PasskeyItem {
  id: string;
  name: string;
  createdAt: number;
  lastUsedAt: number;
}

export const getPasskeyStatus = () =>
  Network.post<{ enabled: boolean }>("/user/passkey/status");
export const beginPasskeyLogin = () =>
  Network.post<PasskeyOptions>("/user/passkey/login/begin", {});
export const finishPasskeyLogin = (sessionId: string, credential: unknown) =>
  Network.post<LoginResponse>("/user/passkey/login/finish", {
    sessionId,
    credential,
  });
export const beginPasskeyRegistration = (password: string) =>
  Network.post<PasskeyOptions>("/user/passkey/register/begin", { password });
export const finishPasskeyRegistration = (
  sessionId: string,
  credential: unknown,
  name: string,
) =>
  Network.post("/user/passkey/register/finish", {
    sessionId,
    credential,
    name,
  });
export const listPasskeys = () =>
  Network.post<PasskeyItem[]>("/user/passkey/list");
export const deletePasskey = (id: string, password: string) =>
  Network.post("/user/passkey/delete", { id, password });

// 用户CRUD操作 - 全部使用POST请求
export const createUser = (data: UserMutationPayload, idemKey?: string) =>
  Network.post("/user/create", data, { idemKey });
export const getAllUsers = (pageData: UserListQuery = {}) =>
  Network.post<UserApiItem[]>("/user/list", pageData);
export const updateUser = (data: UserMutationPayload) =>
  Network.post("/user/update", data);
export const deleteUser = (id: number) => Network.post("/user/delete", { id });
export const getUserPackageInfo = () =>
  Network.post<UserPackageInfoApiData>("/user/package");

// 节点CRUD操作 - 全部使用POST请求
export const createNode = (data: NodeMutationPayload, idemKey?: string) =>
  Network.post("/node/create", data, { idemKey });
export const getNodeList = () => Network.post<NodeApiItem[]>("/node/list");
export const getNodeConnLog = (data: NodeConnLogQuery) => {
  const params: Record<string, string> = {
    nodeId: String(data.nodeId),
    offset: String(data.offset ?? 0),
    limit: String(data.limit ?? 20),
  };

  if (data.ip?.trim()) params.ip = data.ip.trim();

  return Network.get<NodeConnLogData>("/node/connlog", params);
};
/**
 * 各节点本月（浏览器时区 1 号起）的网卡流量：{ 节点ID: [入, 出] }（字节）。
 * 这是整机网卡计数（含转发与系统自身流量），与隧道计费/订阅配额是两套口径，
 * 不可混称、不可互相核对。
 */
export const getNodeMonthTraffic = () =>
  Network.post<Record<string, [number, number]>>("/node/traffic/month", {
    tzOffset: -new Date().getTimezoneOffset(),
    // IANA 时区名（R4B10-001）：后端据此按 1 号当天的实际偏移取月初
    // 起点，DST 切换月不再偏移；tzOffset 保留作旧后端回落。
    tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
  });
export const updateNode = (data: NodeMutationPayload) =>
  Network.post("/node/update", data);
export const deleteNode = (id: number, uninstall = false) =>
  Network.post<{
    brokenReferrers?: string[];
    brokenReferrerCount?: number;
    /** 删除使隧道失去唯一入口/出口的点名（后端 mutations.go L4-03）。 */
    soleRoleWarnings?: string[];
    /** 连带卸载三态：done 已确认卸载 / pending 离线待重连卸载 / unsupported agent 过旧。 */
    uninstall?: "done" | "pending" | "unsupported";
    uninstallWarnings?: string[];
  }>("/node/delete", { id, uninstall });
/** 节点安装形态：脚本 / Docker / Docker Compose（容器形态需 privileged 配合服务器调优）。 */
export type NodeInstallMode = "script" | "docker" | "docker-compose";

export const getNodeInstallCommand = (
  id: number,
  channel: ReleaseChannel = "stable",
  mode: NodeInstallMode = "script",
  privileged: boolean = false,
) =>
  // C059：/node/install 生成命令前无条件走版本发现两段链（API 60s +
  // 重定向兜底 60s），后端界 120s，与 /system/version（C080）同型，
  // 前端 150s、服务端放宽 180s（原走默认 30s 必被前端先行中止）。
  Network.post<string>(
    "/node/install",
    { id, channel, mode, privileged },
    { timeout: 150_000 },
  );
export const updateNodeOrder = (data: {
  nodes: Array<{ id: number; inx: number }>;
}) => Network.post("/node/update-order", data);
export const dismissNodeExpiryReminder = (id: number) =>
  Network.post("/node/dismiss-expiry-reminder", { id });
export const upgradeNode = (
  id: number,
  version?: string,
  channel: ReleaseChannel = "stable",
) =>
  Network.post(
    "/node/upgrade",
    { id, version: version || "", channel },
    // R8-T-04 + B05-002（R2-K2）：空版本先走版本发现两段链（≤120s
    // 前奏），再等 upgradeTimeout 300s，后端界 420s；前端 480s，
    // 须破「前端 = 后端」恰等。
    { timeout: 480_000 },
  );
export const batchUpgradeNodes = (
  ids: number[],
  version?: string,
  channel: ReleaseChannel = "stable",
) =>
  Network.post(
    "/node/batch-upgrade",
    { ids, version: version || "", channel },
    // B05-002：静态 900s 只覆盖 N≤15，后端界 ceil(N/5)×300s 随台数增长；
    // 改按台数动态计算（batchCallTimeoutUpgrade），上限对齐服务端 1800s。
    { timeout: batchCallTimeoutUpgrade(ids.length) },
  );
export const getNodeReleases = (channel: ReleaseChannel = "stable") =>
  // R8-T-03：冷缓存版本发现后端界 60s（双腿各 30s 客户端），前端须 ≥90s。
  Network.post<NodeReleaseApiItem[]>(
    "/node/releases",
    { channel },
    { timeout: 90_000 },
  );
export const rollbackNode = (id: number) =>
  // R8-T-04：后端界 30s，须破恰等。
  Network.post("/node/rollback", { id }, { timeout: 45_000 });
export const testNodeNftables = (nodeId: number, resetHostKey = false) =>
  // C3-02：测连通走已保存的 SSH 配置；未钉入时本次测连通 TOFU 钉入
  // 指纹并在回执带 hostKeyFingerprint/warning，resetHostKey=true 时
  // 强制重钉。nginx 该端点界 300s（T051–T053），前端同界。
  Network.post<NftablesTestResult>(
    "/node/nftables/test",
    { nodeId, resetHostKey },
    { timeout: 300_000 },
  );
// 隧道CRUD操作 - 全部使用POST请求
export const createTunnel = (data: TunnelMutationPayload, idemKey?: string) =>
  // R8-T-07：与 update 共用同等重腿，前端预算与 update 对齐 120s。
  Network.post("/tunnel/create", data, { idemKey, timeout: 120_000 });
export const getTunnelList = () =>
  Network.post<TunnelApiItem[]>("/tunnel/list");
export const updateTunnel = (data: TunnelMutationPayload) =>
  Network.post("/tunnel/update", data, { timeout: 120_000 });
export const previewTunnelDelete = (id: number) =>
  Network.post<TunnelDeletePreviewApiData>(
    "/tunnel/delete-preview",
    { id },
    { timeout: 120_000 },
  );
export const deleteTunnelWithForwards = (data: {
  id: number;
  action: "replace" | "delete_forwards";
  targetTunnelId?: number;
}) =>
  Network.post<TunnelDeleteWithForwardsApiData | BatchOperationResult>(
    "/tunnel/delete-with-forwards",
    data,
    { timeout: 120_000 },
  );
export const previewBatchTunnelDelete = (ids: number[]) =>
  Network.post<TunnelBatchDeletePreviewApiData>(
    "/tunnel/batch-delete-preview",
    {
      ids,
    },
    { timeout: batchCallTimeout(ids.length) },
  );
export const batchDeleteTunnelsWithForwards = (data: {
  ids: number[];
  action: "replace" | "delete_forwards";
  targetTunnelId?: number;
}) =>
  Network.post<TunnelBatchDeleteWithForwardsApiData>(
    "/tunnel/batch-delete-with-forwards",
    data,
    { timeout: batchCallTimeout(data.ids.length) },
  );
export const diagnoseTunnel = (tunnelId: number) =>
  Network.post<TunnelDiagnosisApiData>(
    "/tunnel/diagnose",
    { tunnelId },
    // R8-T-05：后端诊断 ctx 120s 为设计上限保留，前端抬到 ~150s 破三层恰等。
    { timeout: 150_000 },
  );
export const updateTunnelOrder = (data: {
  tunnels: Array<{ id: number; inx: number }>;
}) => Network.post("/tunnel/update-order", data);

// 用户隧道权限管理操作 - 全部使用POST请求
export const batchAssignUserTunnel = (data: {
  userId: number;
  tunnels: Array<{ tunnelId: number; speedId?: number | null }>;
}) => Network.post("/tunnel/user/batch-assign", data);
export const getUserTunnelList = (queryData: UserTunnelListQuery = {}) =>
  Network.post<UserTunnelPermissionApiItem[]>("/tunnel/user/list", queryData);
export const removeUserTunnel = (params: UserTunnelRemovePayload) =>
  Network.post("/tunnel/user/remove", params);
export const updateUserTunnel = (data: UserTunnelAssignPayload) =>
  Network.post("/tunnel/user/update", data);
export const userTunnel = () =>
  Network.post<UserTunnelApiItem[]>("/tunnel/user/tunnel");

// 转发CRUD操作 - 全部使用POST请求
export const createForward = (data: ForwardMutationPayload, idemKey?: string) =>
  Network.post("/forward/create", data, { idemKey });
export const getForwardList = () =>
  Network.post<ForwardApiItem[]>("/forward/list");
export const updateForward = (data: ForwardMutationPayload) =>
  Network.post("/forward/update", data);
export const deleteForward = (id: number) =>
  // R8-T-07：删除按入口节点扇出（每节点 6s 命令），前端放宽至 120s。
  Network.post("/forward/delete", { id }, { timeout: 120_000 });
export const forceDeleteForward = (id: number) =>
  Network.post("/forward/force-delete", { id });

// 转发服务控制操作 - 通过Java后端接口
export const pauseForwardService = (forwardId: number) =>
  Network.post("/forward/pause", { id: forwardId });
export const resumeForwardService = (forwardId: number) =>
  Network.post("/forward/resume", { id: forwardId });
export const resetForwardFlow = (forwardId: number) =>
  Network.post("/forward/reset-flow", { id: forwardId });

// 转发诊断操作
export const diagnoseForward = (forwardId: number) =>
  Network.post<ForwardDiagnosisApiData>(
    "/forward/diagnose",
    { forwardId },
    // R8-T-05：同 diagnoseTunnel，外层抬到 ~150s。
    { timeout: 150_000 },
  );

// 转发排序操作
export const updateForwardOrder = (data: {
  forwards: Array<{ id: number; inx: number }>;
}) => Network.post("/forward/update-order", data);

// 限速规则CRUD操作 - 全部使用POST请求
export const createSpeedLimit = (
  data: SpeedLimitMutationPayload,
  idemKey?: string,
) => Network.post("/speed-limit/create", data, { idemKey });
export const getSpeedLimitList = () =>
  Network.post<SpeedLimitApiItem[]>("/speed-limit/list");
export const updateSpeedLimit = (data: SpeedLimitMutationPayload) =>
  Network.post("/speed-limit/update", data);
export const deleteSpeedLimit = (id: number) =>
  Network.post("/speed-limit/delete", { id });

// 修改密码接口
export const updatePassword = (data: UpdatePasswordPayload) =>
  Network.post("/user/updatePassword", data);

// 重置流量接口
export const resetUserFlow = (data: { id: number; type: number }) =>
  Network.post("/user/reset", data);
export const resetUserQuota = (data: UserQuotaResetPayload) =>
  Network.post("/user/quota/reset", data);

// 网站配置相关接口
export const getConfigs = () =>
  Network.post<Record<string, string>>("/config/list");
export const getConfigByName = (name: string) =>
  Network.post<{ name: string; value: string }>("/config/get", { name });
export const getPublicConfigByName = (name: string) =>
  Network.post<{ name: string; value: string }>("/public/config/get", { name });
export const updateConfigs = (configMap: Record<string, string>) =>
  Network.post("/config/update", configMap);
export const updateConfig = (name: string, value: string) =>
  Network.post("/config/update-single", { name, value });

export const getStorageSummary = () =>
  Network.get<StorageSummaryApiData>("/system/storage");

export const getSystemUpgradeVersion = () =>
  Network.post<SystemUpgradeVersionApiData>(
    "/system/version",
    undefined,
    // B05-001：版本发现两段链后端界 120s（API 代理+直连两腿 60s +
    // 失败后重定向兜底两腿 60s），前端须 ≥150s。此前走 Network 默认
    // 30s 且被超时登记表误分类为 DB/local（C080），慢代理/限流冷缓存
    // 时配置页误报网络错误。
    { timeout: 150_000 },
  );

export const checkSystemUpgrade = (
  channel: ReleaseChannel = "stable",
  options?: { timeout?: number; silent?: boolean },
) =>
  Network.post<SystemUpgradeCheckApiData>(
    "/system/check-updates",
    {
      channel,
    },
    // B05-003：check-updates 补重定向兜底后与 /system/version 同为
    // 发现两段链，后端界 120s，前端默认 150s（调用方可覆盖向上）。
    { timeout: 150_000, ...options },
  );

export const runSystemUpgrade = (
  version?: string,
  channel: ReleaseChannel = "stable",
) =>
  Network.post<SystemUpgradeRunApiData>(
    "/system/upgrade",
    { version: version || "", channel },
    // R8-T-04 + B05-002（R2-K2）：同步前奏最坏 240s（版本解析两段链
    // 120s——重定向段为代理+直连两腿而非一腿——加资产下载 2×60s），
    // 前端 300s。
    { timeout: 300_000 },
  );

// 面板升级状态查询：升级后后端容器重启期间请求会失败，调用方需静默重试
export const getSystemUpgradeStatus = () =>
  Network.get<SystemUpgradeStatusApiData>("/system/upgrade/status");

// R8-T-02 元素 (a) / R8-T-08：备份导入/恢复/导出的耗时随后备规模与舰队
// 规模增长、后端无界，前端统一按 30 分钟计（与仓内 gh-mirror 长响应先例
// 同量级）。元素 (b) 已按用户裁决④落地（B14-001）：导入分两相位——
// 快照相位（写入前）仍绑请求 ctx，此相位关页/断网整单取消、无副作用；
// 进入写入相位后 ctx 解绑，服务端整单跑完，关页/断网不再回滚、也
// 无法取消。前端放宽只抬高武装阈值；结果需重进页面核对（见导入确认框
// 与 doc/guide/nodes.md 的同口径说明）。
// 验证码相关接口
export const checkCaptcha = () => Network.post("/captcha/check");

// 批量操作接口（R8-T-07：后端串行逐项，前端按项数放宽，见 batchCallTimeout）
export const batchDeleteForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>(
    "/forward/batch-delete",
    { ids },
    { timeout: batchCallTimeout(ids.length) },
  );
export const batchPauseForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>(
    "/forward/batch-pause",
    { ids },
    { timeout: batchCallTimeout(ids.length) },
  );
export const batchResumeForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>(
    "/forward/batch-resume",
    { ids },
    { timeout: batchCallTimeout(ids.length) },
  );
export const batchDeleteNodes = (ids: number[], uninstall = false) =>
  Network.post<BatchOperationResult>(
    "/node/batch-delete",
    { ids, uninstall },
    { timeout: batchCallTimeout(ids.length) },
  );

/** 节点展示分组 v2（后端 node_display_group.go）：结果类型与 Go 侧回执结构同形。 */
export interface NodeGroupCreateResult {
  name: string;
  order: string[];
}
export interface NodeGroupRenameResult {
  oldName: string;
  newName: string;
  updatedCount: number;
  order: string[];
}
export interface NodeGroupDeleteResult {
  name: string;
  updatedCount: number;
  order: string[];
}
/** 批量移入/移出分组：group 为 "" 即移出回未分组；回执逐条计数（M-0051 口径）。 */
export const batchSetNodeGroup = (ids: number[], group: string) =>
  Network.post<BatchOperationResult>(
    "/node/batch-set-group",
    { ids, group },
    { timeout: batchCallTimeout(ids.length) },
  );
export const createNodeGroup = (name: string) =>
  Network.post<NodeGroupCreateResult>("/node/group/create", { name });
export const renameNodeGroup = (oldName: string, newName: string) =>
  Network.post<NodeGroupRenameResult>("/node/group/rename", {
    oldName,
    newName,
  });
export const deleteNodeGroup = (name: string) =>
  Network.post<NodeGroupDeleteResult>("/node/group/delete", { name });
export const batchRedeployForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>(
    "/forward/batch-redeploy",
    { ids },
    { timeout: batchCallTimeout(ids.length) },
  );
export const batchRedeployTunnels = (ids: number[]) =>
  Network.post<BatchOperationResult>(
    "/tunnel/batch-redeploy",
    { ids },
    { timeout: batchCallTimeout(ids.length) },
  );
export const batchChangeTunnel = (data: {
  forwardIds: number[];
  targetTunnelId: number;
}) =>
  Network.post<BatchOperationResult>("/forward/batch-change-tunnel", data, {
    timeout: batchCallTimeout(data.forwardIds.length),
  });

export interface BackupTypes {
  users?: boolean;
  nodes?: boolean;
  tunnels?: boolean;
  forwards?: boolean;
  userTunnels?: boolean;
  speedLimits?: boolean;
  configs?: boolean;
  inbounds?: boolean;
  // 后端 ExportPartial/Import 早已支持，前端清单曾漏列（F-026）。
  userInbounds?: boolean;
  announcements?: boolean;
  monitorPermissions?: boolean;
  // B4/F-004/F-030：订阅分配与监控定义。
  userSubscriptions?: boolean;
  serviceMonitors?: boolean;
  // R2-E3-F02：Passkey 公钥凭据。
  passkeys?: boolean;
}

export const exportBackup = async (types: string[] = []) => {
  const token = window.localStorage.getItem("token");

  const response = await axios.post(
    resolveApiPath("/backup/export"),
    { types },
    {
      headers: {
        Authorization: token,
        "Content-Type": "application/json",
      },
      responseType: "blob",
      // R8-T-08：与备份导入/导出长操作统一口径（此前裸 axios 无超时）。
      timeout: 1_800_000,
    },
  );

  // 后端错误走统一信封且以 HTTP 200 返回（WriteJSON 不设状态码），裸 blob
  // 下载会把 {"code":-2,...} 当备份文件存下来还报「导出成功」（D5-01）。
  // 成功响应带 Content-Disposition: attachment，错误信封没有——以此为准
  // 校验：无 attachment 时按信封解析，code!==0 直接抛错，绝不触发下载。
  const disposition = String(response.headers?.["content-disposition"] || "");

  if (!disposition.includes("attachment")) {
    const text = await (response.data as Blob).text();
    let envelope: { code?: unknown; msg?: unknown } | null = null;

    try {
      envelope = JSON.parse(text);
    } catch {
      envelope = null;
    }

    if (envelope && typeof envelope.code === "number") {
      if (envelope.code === 401) {
        // 会话失效：与 Network 层一致，清会话回登录页
        clearSession();
        if (window.location.pathname !== "/") {
          window.location.href = "/";
        }
        throw new Error("登录已过期，请重新登录");
      }
      if (envelope.code !== 0) {
        throw new Error(
          typeof envelope.msg === "string" && envelope.msg
            ? envelope.msg
            : "导出失败",
        );
      }
    }

    throw new Error("导出失败：服务器未返回备份文件");
  }

  const url = window.URL.createObjectURL(new Blob([response.data]));
  const link = document.createElement("a");

  link.href = url;
  const timestamp = new Date().toISOString().slice(0, 19).replace(/[:-]/g, "");

  link.setAttribute("download", `backup_${timestamp}.json`);
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  window.URL.revokeObjectURL(url);
};

export const importBackup = (data: BackupImportPayload) =>
  // R8-T-02 元素 (a)：与备份导出同为长操作口径。
  Network.post("/backup/import", data, { timeout: 1_800_000 });

// SEC-01 BC-01(b)：导入前自动备份快照留存服务端 24 小时（A10-10 承诺的
// 服务端版）。回执自 SEC-01 起不再含快照内容，快照只经以下两端点取用，
// 绝不打进日志/console。
export interface ImportAutoBackupStatus {
  exists: boolean;
  expiresAt: number;
}

export const getImportAutoBackupStatus = () =>
  Network.get<ImportAutoBackupStatus>("/backup/import-auto-backup/status");

// 把可能为 Blob 的错误/信封体解析为后端信封（解析失败返回 null）。
const readBlobEnvelope = async (
  data: unknown,
): Promise<{ code?: unknown; msg?: unknown } | null> => {
  try {
    if (data && typeof data === "object" && !(data instanceof Blob)) {
      return data as { code?: unknown; msg?: unknown };
    }

    const text =
      typeof data === "string"
        ? data
        : data instanceof Blob
          ? await data.text()
          : "";

    if (!text) return null;

    return JSON.parse(text) as { code?: unknown; msg?: unknown };
  } catch {
    return null;
  }
};

export const downloadImportAutoBackup = async () => {
  const token = window.localStorage.getItem("token");

  let response;

  try {
    response = await axios.get(resolveApiPath("/backup/import-auto-backup"), {
      headers: {
        Authorization: token,
      },
      responseType: "blob",
      // R8-T-08：备份文件下载同导出规模口径（此前裸 axios 无超时）。
      timeout: 1_800_000,
    });
  } catch (error) {
    // 无快照时后端回 HTTP 404 + JSON 信封（非 200 信封），axios 直接抛错：
    // 从错误体里解析后端 msg 透出，绝不把信封当文件存下来。
    if (axios.isAxiosError(error) && error.response) {
      if (error.response.status === 401) {
        // 会话失效：与 Network 层一致，清会话回登录页
        clearSession();
        if (window.location.pathname !== "/") {
          window.location.href = "/";
        }
        throw new Error("登录已过期，请重新登录");
      }

      const envelope = await readBlobEnvelope(error.response.data);

      throw new Error(
        typeof envelope?.msg === "string" && envelope.msg
          ? envelope.msg
          : "暂无可下载的导入前自动备份",
      );
    }

    throw error instanceof Error ? error : new Error("下载失败");
  }

  // 后端错误走统一信封且以 HTTP 200 返回时，裸 blob 下载会把信封当备份
  // 文件存下来（D5-01）。成功响应带 Content-Disposition: attachment，
  // 错误信封没有——以此为准校验：无 attachment 时按信封解析，code!==0
  // 直接抛错，绝不触发下载。
  const disposition = String(response.headers?.["content-disposition"] || "");

  if (!disposition.includes("attachment")) {
    const envelope = await readBlobEnvelope(response.data);

    if (envelope && typeof envelope.code === "number") {
      if (envelope.code === 401) {
        // 会话失效：与 Network 层一致，清会话回登录页
        clearSession();
        if (window.location.pathname !== "/") {
          window.location.href = "/";
        }
        throw new Error("登录已过期，请重新登录");
      }
      if (envelope.code !== 0) {
        throw new Error(
          typeof envelope.msg === "string" && envelope.msg
            ? envelope.msg
            : "下载失败",
        );
      }
    }

    throw new Error("下载失败：服务器未返回备份文件");
  }

  const url = window.URL.createObjectURL(new Blob([response.data]));
  const link = document.createElement("a");

  link.href = url;
  const timestamp = new Date().toISOString().slice(0, 19).replace(/[:-]/g, "");

  link.setAttribute("download", `auto_backup_${timestamp}.json`);
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  window.URL.revokeObjectURL(url);
};

export interface AnnouncementData {
  content: string;
  enabled: number;
  update_time?: number;
}

export const getAnnouncement = () =>
  Network.get<AnnouncementData>("/announcement/get");
export const updateAnnouncement = ({
  content,
  enabled,
}: Pick<AnnouncementData, "content" | "enabled">) =>
  Network.post("/announcement/update", { content, enabled });

// 监控读接口统一 silent（A9-02）：无监控权限的 403 是权限模型的正常分支，
// 各监控页都有本地 accessDenied/错误态呈现；若不静默，Network 全局提示会
// 与本地空态叠加，且随轮询逐周期重复弹。写操作（创建/删除/立即执行等）
// 不静默，仍走全局提示。
export const getNodeMetrics = (
  nodeId: number,
  start?: number,
  end?: number,
) => {
  const params: Record<string, string> = {};

  if (start) params.start = String(start);
  if (end) params.end = String(end);

  return Network.get<NodeMetricApiItem[]>(
    `/monitor/nodes/${nodeId}/metrics`,
    params,
    { timeout: 60_000, silent: true },
  );
};

export const getTunnelMetrics = (
  tunnelId: number,
  start?: number,
  end?: number,
) => {
  const params: Record<string, string> = {};

  if (start) params.start = String(start);
  if (end) params.end = String(end);

  return Network.get<TunnelMetricApiItem[]>(
    `/monitor/tunnels/${tunnelId}/metrics`,
    params,
    { silent: true },
  );
};

export const getMonitorTunnels = () =>
  Network.get<MonitorTunnelApiItem[]>("/monitor/tunnels", {}, { silent: true });

export const getMonitorTunnelQuality = () =>
  Network.get<TunnelQualityApiItem[]>(
    "/monitor/tunnels/quality",
    {},
    { silent: true },
  );

export const getMonitorTunnelQualityHistory = async (
  tunnelId: number,
  start?: number,
  end?: number,
) => {
  const params: Record<string, string> = {};

  if (start) params.start = String(start);
  if (end) params.end = String(end);

  // 历史端点 success 是 int(0/1)（model.TunnelQuality），快照端点是 bool：
  // 在 api 层归一化为 TunnelQualityApiItem 再交出，避免消费方混型（F-063）。
  const response = await Network.get<TunnelQualityHistoryApiItem[]>(
    `/monitor/tunnels/${tunnelId}/quality`,
    params,
    { silent: true },
  );

  return {
    ...response,
    data: (response.data || []).map(
      (item): TunnelQualityApiItem => ({
        tunnelId: item.tunnelId,
        entryToExitLatency: item.entryToExitLatency,
        exitToBingLatency: item.exitToBingLatency,
        entryToExitLoss: item.entryToExitLoss,
        exitToBingLoss: item.exitToBingLoss,
        entryToExitPartialUntested: (item.entryToExitPartial ?? 0) !== 0,
        success: item.success !== 0,
        errorMessage: item.errorMessage,
        timestamp: item.timestamp,
        chainDetails: item.chainDetails,
      }),
    ),
  };
};

export const getServiceMonitorList = () =>
  Network.get<ServiceMonitorApiItem[]>(
    "/monitor/services",
    {},
    { silent: true },
  );

export const getServiceMonitorLimits = () =>
  Network.get<ServiceMonitorLimitsApiData>(
    "/monitor/services/limits",
    {},
    { silent: true },
  );

export const createServiceMonitor = (
  data: ServiceMonitorMutationPayload,
  idemKey?: string,
) =>
  Network.post<ServiceMonitorApiItem>("/monitor/services/create", data, {
    idemKey,
  });

export const updateServiceMonitor = (data: ServiceMonitorMutationPayload) =>
  Network.post<ServiceMonitorApiItem>("/monitor/services/update", data);

export const deleteServiceMonitor = (id: number) =>
  Network.post("/monitor/services/delete", { id });

export const getServiceMonitorResults = (
  monitorId: number,
  options?: { limit?: number; start?: number; end?: number },
) => {
  const params: Record<string, string> = {};

  if (options?.start != null && options?.end != null) {
    params.start = String(options.start);
    params.end = String(options.end);
  } else if (options?.limit != null) {
    params.limit = String(options.limit);
  }

  return Network.get<ServiceMonitorResultApiItem[]>(
    `/monitor/services/${monitorId}/results`,
    params,
    { silent: true },
  );
};

export const getServiceMonitorLatestResults = () =>
  Network.get<ServiceMonitorResultApiItem[]>(
    "/monitor/services/latest-results",
    {},
    { silent: true },
  );

export const runServiceMonitor = (id: number, timeoutSec?: number) =>
  Network.post<ServiceMonitorResultApiItem>(
    "/monitor/services/run",
    { id },
    {
      // R8-T-08：后端界 = 该监控配置的 timeoutSec + 2s（on-node 命令余量），
      // 前端按配置动态取值并留边距；未知配置时按下限 70s（覆盖默认
      // MaxTimeoutSec 60s + 边距）。
      timeout: Math.max(70_000, ((timeoutSec ?? 0) + 15) * 1000),
    },
  );

export const getMonitorNodes = () =>
  Network.get<MonitorNodeApiItem[]>("/monitor/nodes", {}, { silent: true });

export const getMonitorAccess = () =>
  Network.get<MonitorAccessApiData>("/monitor/access");

export const getMonitorPermissionList = () =>
  Network.get<MonitorPermissionApiItem[]>(
    "/monitor/permission/list",
    {},
    { silent: true },
  );

export const assignMonitorPermission = (userId: number) =>
  Network.post("/monitor/permission/assign", { userId });

export const removeMonitorPermission = (userId: number) =>
  Network.post("/monitor/permission/remove", { userId });

// 代理内核 / 入站（节点服务）
export const getKernelCapabilities = () =>
  Network.post<KernelCapabilitiesApiData>("/kernel/capabilities");
export const getKernelList = (nodeId?: number) =>
  Network.post<KernelApiItem[]>("/kernel/list", nodeId ? { nodeId } : {});
// R8-T-03：冷缓存后端界 60s（双腿各 30s 客户端），前端须 ≥90s。
export const getKernelVersions = (kernelType: string) =>
  Network.post<
    { version: string; prerelease: boolean; publishedAt?: string }[]
  >("/kernel/versions", { kernelType }, { timeout: 90_000 });

export const installKernel = (data: {
  nodeId: number;
  kernelType: string;
  version: string;
}) =>
  // R8-T-01：后端界 kernelApplyTimeout 300s，前端须 ≥330s（且服务端
  // /kernel/install 由批 T1 作 per-endpoint 写时限放宽，单层不算修完）。
  Network.post("/kernel/install", data, { timeout: 330_000 });
export const controlKernel = (
  id: number,
  action: "start" | "stop" | "restart" | "remove",
  force = false,
) =>
  // B11-001（R2-K2）：启停在内核未装版本时条件性继承 300s 内核应用
  // 预算（applyKernelWith 条件升级），与 installKernel 同口径 ≥330s；
  // 此前走默认 30s、服务端全局 120s，慢下发必假超时。
  Network.post("/kernel/control", { id, action, force }, { timeout: 330_000 });
export const getKernelLogs = (id: number) =>
  Network.post<KernelStateApiData>("/kernel/logs", { id });
export const getInboundList = () =>
  Network.post<InboundApiItem[]>("/inbound/list");
export const createInbound = (data: InboundMutationPayload, idemKey?: string) =>
  // R8-T-01：保存末尾在内核未装版本时条件性继承 300s 内核应用预算，
  // 前端与 installKernel 同口径 ≥330s。
  Network.post<{ id: number }>("/inbound/create", data, {
    idemKey,
    timeout: 330_000,
  });
export const updateInbound = (data: InboundMutationPayload) =>
  Network.post<{ id: number }>("/inbound/update", data, {
    timeout: 330_000,
  });
export const deleteInbound = (id: number) =>
  // B11-001 家族（R2-K2）：删除末尾下发所在内核，未装版本时条件性
  // 继承 300s 应用预算，前端 ≥330s（与 createInbound 同口径）。
  Network.post("/inbound/delete", { id }, { timeout: 330_000 });
export const batchDeleteInbounds = (ids: number[]) =>
  Network.post<BatchOperationResult>(
    "/inbound/batch-delete",
    { ids },
    // B11-001 家族（R2-K2）：逐项删除后对受影响内核逐个下发
    // （去重），每次带 300s 条件界，按项数放宽。
    { timeout: batchCallTimeoutKernelApply(ids.length) },
  );
export const batchToggleInbounds = (ids: number[], enabled: boolean) =>
  Network.post<BatchOperationResult>(
    "/inbound/batch-toggle",
    {
      ids,
      enabled,
    },
    { timeout: batchCallTimeoutKernelApply(ids.length) },
  );
export interface SubPipeline {
  /**
   * 中转范围三档（后端 lines 键）：""
   * = 全部端点（直连 + 所有中转）；direct = 只要直连；relay = 只要中转。
   */
  lines: "" | "direct" | "relay";
  /**
   * 中转范围「自定义」：逐端点白名单（0 = 直连，>0 = 中转线路 ID）。
   * 非空时即精确范围、优先于 lines；缺省或空数组 = 不启用自定义。
   */
  relayIds?: number[];
  /**
   * 订阅模板：按客户端槽位（clash / singbox / surge / stash /
   * shadowrocket）选模板 ID；选了模板的格式渲染进模板骨架。
   * 空 = 不用模板。
   */
  templates?: Record<string, number>;
}
export interface SubscriptionItem {
  id: number;
  name: string;
  kind: "own" | "share";
  token: string;
  path: string;
  sourceAll: boolean;
  inboundIds: number[];
  sourceIds: number[];
  sharedIds: number[];
  options: SubPipeline;
  expireTime: number;
  enabled: boolean;
  active: boolean;
  remark: string;
  nodeCount: number;
  uploadBytes: number;
  downloadBytes: number;
  /** Bytes; 0 = unlimited. */
  trafficLimit: number;
  /** Day of month (1-28) the traffic resets; 0 = never. */
  resetDay: number;
  /** 已用流量起算时刻（毫秒）：创建或上次重置流量之时。 */
  trafficSince: number;
  overQuota: boolean;
  /** 默认订阅：普通用户的个人链接（?user=&sig=）挂在它上面。 */
  isDefault: boolean;
  /** 仅保存响应回填（B10-002）：本次保存校验时被排除的伴生入站 ID
   * （伴生入站不能作订阅源）；列表等其他视图恒缺省。 */
  excludedCompanionIds?: number[];
  /** L4-08：订阅 ID 超出 socket mark 计量编码上限时为 true——该订阅
   * 的流量永不计量、流量限额永不触发，须显式提示管理员。 */
  meteringUnsupported?: boolean;
}
export interface SubscriptionDraft {
  id?: number;
  name: string;
  kind: "own" | "share";
  sourceAll: boolean;
  inboundIds: number[];
  sourceIds: number[];
  options: SubPipeline;
  expireTime: number;
  enabled?: boolean;
  remark: string;
  trafficLimit: number;
  resetDay: number;
}
export interface SubPreviewItem {
  name: string;
  original: string;
  protocol: string;
  line: "direct" | "relay";
  /** 导入节点的订阅源 ID；0/缺省 = 非订阅源节点。 */
  sourceId?: number;
  /** 自建入站的端点（非导入节点）。 */
  own?: boolean;
}
export const getSubscriptions = () =>
  Network.post<{
    items: SubscriptionItem[];
  }>("/subscription/list");
export const saveSubscription = (data: SubscriptionDraft, idemKey?: string) =>
  // B11-001 家族（R2-K2）：保存后同步下发订阅涉及的各内核，未装
  // 版本时每次下发带 300s 条件界，前端 ≥330s（与 createInbound 同
  // 口径的条件界登记）。
  Network.post<SubscriptionItem>("/subscription/save", data, {
    idemKey,
    timeout: 330_000,
  });
export const deleteSubscription = (id: number) =>
  Network.post("/subscription/delete", { id });

/** 订阅模板格式（骨架所属客户端）。 */
export type SubTemplateKind = "clash" | "singbox" | "surge";
/** 订阅里可选模板的客户端槽位；stash / shadowrocket 用 clash 模板。 */
export type SubTemplateSlot =
  | "clash"
  | "singbox"
  | "surge"
  | "stash"
  | "shadowrocket";

export interface TemplateBuilderGroup {
  name: string;
  type: string;
  filter: string;
}
export interface TemplateBuilderRule {
  type: string;
  value: string;
  target: string;
}
/** 可视化构建器的配方（与后端 kernel.TemplateBuilder 对应）。 */
export interface TemplateBuilder {
  serviceGroups?: boolean;
  version: number;
  preset: string;
  groups: TemplateBuilderGroup[];
  rules: TemplateBuilderRule[];
  final: string;
}
export interface SubTemplateItem {
  id: number;
  name: string;
  kind: SubTemplateKind;
  content: string;
  sourceUrl: string;
  lastFetch: number;
  lastError: string;
  updatedTime: number;
  usedBy: number;
  groups: number;
  autoGroups: number;
  rules: number;
  builder?: TemplateBuilder;
}
export interface SubTemplateDraft {
  id?: number;
  name: string;
  kind: SubTemplateKind;
  content?: string;
  sourceUrl?: string;
  builder?: TemplateBuilder;
  subscriptionId?: number;
}
export interface TemplateRuleCategoryItem {
  key: string;
  name: string;
  target: string;
  presets: string[];
}
export interface TemplateCheckGroup {
  name: string;
  type: string;
  proxies: string[];
  auto: boolean;
  nodes: string[];
  filterError?: string;
}
export interface TemplateCheckResult {
  summary: {
    groups: TemplateCheckGroup[];
    rules: number;
    providers: number;
    warnings: string[];
  };
  serviceGroups: Array<{
    key: string;
    name: string;
    target: string;
    members: string[];
  }>;
  autoGroups: number;
  sample: string;
  nodes: number;
  content: string;
}
export const getSubscriptionTemplates = () =>
  Network.post<SubTemplateItem[]>("/subscription/template/list");
export const getTemplateCatalog = () =>
  Network.post<TemplateRuleCategoryItem[]>("/subscription/template/catalog");
export const saveSubscriptionTemplate = (
  data: SubTemplateDraft,
  idemKey?: string,
) =>
  Network.post<SubTemplateItem>("/subscription/template/save", data, {
    idemKey,
  });
export const checkSubscriptionTemplate = (data: SubTemplateDraft) =>
  Network.post<TemplateCheckResult>("/subscription/template/check", data);
export const refreshSubscriptionTemplate = (id: number) =>
  Network.post<SubTemplateItem>("/subscription/template/refresh", { id });
export const deleteSubscriptionTemplate = (id: number) =>
  Network.post("/subscription/template/delete", { id });
export const resetSubscription = (id: number, idemKey?: string) =>
  // C1-06：凭据轮换带幂等键（useTargetIdemKey 按目标生成）——超时
  // 重试/双击回放首轮回执（含首轮新 token），不触发二次轮换。
  Network.post<SubscriptionItem>("/subscription/reset", { id }, { idemKey });
export type RemoteSourceRole = "" | "relay" | "landing";
export interface RemoteSourceItem {
  id: number;
  name: string;
  url: string;
  /** "url" = fetched from the URL; "file" = uploaded, updated by uploading again. */
  kind: "url" | "file";
  /** 用途：新建订阅链式代理的默认带入（"" 普通 / "relay" 中转 / "landing" 落地）。 */
  role: RemoteSourceRole;
  /** 0 = refresh by hand only. */
  intervalHours: number;
  relayOnly: boolean;
  enabled: boolean;
  lastFetch: number;
  lastError: string;
  nodeCount: number;
  /** The provider's Subscription-Userinfo header. */
  userinfo: string;
}
export interface RemoteSyncResult {
  added: number;
  updated: number;
  removed: number;
  kept: number;
  keptUnknown: number;
  keptStale: number;
  /** Entries skipped because the node was deleted by hand and shielded (R10-04). */
  shielded: number;
  /** Machine-readable round status (C1-05): "ok", or "truncated" when the source exceeded the per-round node cap — branch on this, not on the errors prose. */
  status: string;
  /** Nodes dropped by the per-round cap this round (0 unless status is "truncated"). */
  truncated: number;
  errors: string[];
}
export const getRemoteSources = () =>
  Network.post<RemoteSourceItem[]>("/subscription/source/list");
export const saveRemoteSource = (
  data: {
    id?: number;
    name: string;
    url: string;
    kind?: "url" | "file";
    role?: RemoteSourceRole;
    /** Uploaded file contents (file sources). */
    content?: string;
    intervalHours: number;
    relayOnly: boolean;
    enabled?: boolean;
  },
  idemKey?: string,
) =>
  Network.post<
    RemoteSourceItem | { source: RemoteSourceItem; result: RemoteSyncResult }
  >("/subscription/source/save", data, { timeout: 60000, idemKey });
export const refreshRemoteSource = (id: number) =>
  Network.post<{ source: RemoteSourceItem; result: RemoteSyncResult }>(
    "/subscription/source/refresh",
    { id },
    { timeout: 60000 },
  );
export const deleteRemoteSource = (id: number, keepNodes: boolean) =>
  Network.post("/subscription/source/delete", { id, keepNodes });
export const resetSubscriptionTraffic = (id: number) =>
  Network.post<SubscriptionItem>("/subscription/reset-traffic", { id });
export const previewSubscription = (data: SubscriptionDraft) =>
  Network.post<{
    items: SubPreviewItem[];
    total: number;
    dropped: number;
    // B10-002：与 save 同口径，预览也回告被排除的伴生入站（非空才出现）。
    excludedCompanionIds?: number[];
  }>("/subscription/preview", data);

export const testNotify = (data: {
  telegramToken: string;
  telegramChatId: string;
  telegramApiBase: string;
  webhookUrl: string;
}) =>
  // R8-T-08：双通道最坏 (10+10)+3+(10+10)=43s，前端须 ≥50s。
  Network.post("/notify/test", data, { timeout: 50_000 });
export const probeInbounds = (ids: number[] = []) =>
  Network.post<Record<string, InboundProbe>>(
    "/inbound/probe",
    { ids },
    // R8-T-08：后端界 = ⌈N/16⌉×4s 拨号（N = 外部入站数，空 ids 为全部、
    // 前端不可知），静态放宽至 300s，可覆盖约 1200 个全失联入站；
    // 超出仍可能假超时，只抬阈值（裁定 §2.8 口径）。
    { timeout: 300_000 },
  );
export const updateInboundSplit = (data: {
  id: number;
  outboundId: number;
  splitRules: SplitRule[];
  outboundIpStrategy?:
    | "auto"
    | "ipv4_only"
    | "ipv6_only"
    | "prefer_ipv4"
    | "prefer_ipv6";
}) =>
  // B11-001 家族（R2-K2）：保存末尾下发所在内核及变更目标，未装
  // 版本时带 300s 条件界，前端 ≥330s。
  Network.post("/inbound/split", data, { timeout: 330_000 });
export const toggleInbound = (id: number, enabled: boolean) =>
  // B11-001 家族（R2-K2）：启停末尾下发所在内核，同上 ≥330s。
  Network.post("/inbound/toggle", { id, enabled }, { timeout: 330_000 });
export const resetInboundTraffic = (id: number) =>
  Network.post("/inbound/reset-traffic", { id });
export const regenerateInbound = (id: number, idemKey?: string) =>
  // B11-001 家族（R2-K2）：重生成末尾下发所在内核及链式引用方，
  // 未装版本时带 300s 条件界，前端 ≥330s。
  // C1-06：凭据轮换带幂等键（useTargetIdemKey 按目标生成），
  // 超时重试/双击不触发二次轮换。
  Network.post("/inbound/regenerate", { id }, { timeout: 330_000, idemKey });
export const importInbounds = (links: string, relayOnly: boolean) =>
  Network.post<{ created: number[]; errors: string[] }>("/inbound/import", {
    links,
    relayOnly,
  });
export const updateExternalInbound = (data: {
  id: number;
  name: string;
  host: string;
  port: number;
  method?: string;
  password?: string;
  /** Replaces the node's parameters with this share link. */
  link?: string;
  remark: string;
  relayOnly: boolean;
}) =>
  // B11-001 家族（R2-K2）：保存后同步下发经本入站链式引用的各
  // 内核，未装版本时带 300s 条件界，前端 ≥330s。
  Network.post("/inbound/external/update", data, { timeout: 330_000 });
export const getInboundShare = (id: number) =>
  Network.post<InboundShareEntry[]>("/inbound/share", { id });
export const getSubInfo = (reset = false, silent = false, idemKey?: string) =>
  // C1-06：reset=true 是凭据轮换，可带幂等键（useTargetIdemKey）；
  // 读分支不带键、行为不变。
  Network.post<SubInfoApiData>(
    "/inbound/sub/info",
    { reset },
    { silent, idemKey },
  );
export const getInboundRelays = (inboundId: number) =>
  Network.post<InboundRelayApiItem[]>("/inbound/relay/list", { inboundId });
/** 立即测量中转线路（空 ids = 全部）及其入口节点的延迟。 */
export const probeInboundRelays = (ids: number[] = []) =>
  Network.post<Record<string, RelayLatency>>(
    "/inbound/relay/probe",
    { ids },
    // R8-T-08：两段扇出按公式放宽（见 relayProbeTimeout）。
    { timeout: relayProbeTimeout(ids.length) },
  );
export const createInboundRelay = (
  data: {
    inboundId: number;
    tunnelId: number;
    inPort?: number;
    name?: string;
    shareHost?: string;
  },
  idemKey?: string,
) => Network.post<{ id: number }>("/inbound/relay/create", data, { idemKey });
export const deleteInboundRelay = (id: number, force = false) =>
  Network.post("/inbound/relay/delete", { id, force });
export const getMyInbounds = () => Network.post("/user-inbound/mine", {});

/** 按订阅分配：某用户被分配的订阅 ID 列表（管理员）。 */
export interface UserSubscriptionListData {
  userId: number;
  subscriptionIds: number[];
}
export const getUserSubscriptions = (userId: number) =>
  Network.post<UserSubscriptionListData>("/user-subscription/list", { userId });
export const setUserSubscriptions = (
  userId: number,
  subscriptionIds: number[],
) => Network.post("/user-subscription/set", { userId, subscriptionIds });

/** 我的订阅：分配给当前用户的订阅（自带独立链接，不走个人链接过滤）。 */
export interface MySubscriptionItem {
  id: number;
  name: string;
  path: string;
  nodeCount: number;
  uploadBytes: number;
  downloadBytes: number;
  /** Bytes; 0 = unlimited. */
  trafficLimit: number;
  /** 已用流量起算时刻（毫秒）：创建或上次重置流量之时。 */
  trafficSince: number;
  /** 过期时间（毫秒）；0 = 永不过期。 */
  expireTime: number;
  overQuota: boolean;
  enabled: boolean;
  /** 删除墓碑滞留中（管理员已删除、节点凭据下发未完成）：链接已 404，页面呈「删除中」。 */
  pendingDelete: boolean;
}
export const getMySubscriptions = () =>
  Network.post<{ subscriptions: MySubscriptionItem[] }>(
    "/user-subscription/mine",
    {},
  );

/** 订阅流量统计的时间粒度。 */
export type SubTrafficRange = "hour" | "day" | "month";
/** 一条订阅的流量统计：近 24 小时 / 30 天 / 12 个月，按浏览器时区对齐分桶。 */
export interface SubTraffic {
  range: SubTrafficRange;
  /** 每个时间桶的起点（unix 毫秒）与该桶的上传/下载字节数。 */
  points: Array<{ time: number; up: number; down: number }>;
  up: number;
  down: number;
  /** 已用流量起算时刻（毫秒）：订阅创建或上次重置流量之时。 */
  since: number;
}
const trafficQuery = (id: number, range: SubTrafficRange) => ({
  id,
  range,
  tzOffset: -new Date().getTimezoneOffset(),
  // IANA 时区名（A6-002/BC-12）：后端据此按各日期实际偏移分桶，
  // 非整小时时区与 DST 切换周不再错位；tzOffset 保留作旧后端回落。
  tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
});

/** 管理端：任一订阅的流量统计。 */
export const getSubscriptionTraffic = (id: number, range: SubTrafficRange) =>
  Network.post<SubTraffic>("/subscription/traffic", trafficQuery(id, range));
/** 用户端：分配给当前用户的一条订阅的流量统计（整条订阅合计）。 */
export const getMySubscriptionTraffic = (id: number, range: SubTrafficRange) =>
  Network.post<SubTraffic>(
    "/user/subscription/traffic",
    trafficQuery(id, range),
  );
/** 立即探测节点到延迟测试点的延迟。 */
export const probeNodeLatency = (ids: number[] = []) =>
  Network.post<Record<string, InboundProbe>>(
    "/node/latency/probe",
    { ids },
    // R8-T-08：扇出作业按公式放宽（见 latencyProbeTimeout）。
    { timeout: latencyProbeTimeout(ids.length) },
  );
/** 设置节点的延迟测试点：自选或跟随默认。 */
export const setNodeLatencyTargets = (data: {
  ids: number[];
  custom: boolean;
  points: string[];
  pinned: string[];
}) => Network.post("/node/latency/targets", data, { timeout: 60000 });
/**
 * 设置节点的定时重启 agent 服务（保险丝，默认关）。
 * pushed=false 表示节点离线或 agent 未确认：设置已存面板，节点重连时自动补发。
 */
export const setNodeRestartSchedule = (data: {
  id: number;
  enabled: number;
  time: string;
  jitterMinutes: number;
  /** daily＝每天定时（默认）；interval＝按间隔，配合 intervalMinutes。 */
  mode: string;
  intervalMinutes: number;
}) =>
  Network.post<{ pushed: boolean; pushError?: string }>(
    "/node/restart-schedule",
    data,
    { timeout: 60000 },
  );
/** 节点 Swap 现状（面板经 agent 在节点本机查得）：总量/已用、条目明细、根盘可用与内存总量。 */
export interface NodeSwapInfo {
  totalMB: number;
  usedMB: number;
  entries: { name: string; type: string; sizeMB: number; usedMB: number }[];
  diskFreeMB: number;
  memTotalMB: number;
  /** 节点上已有面板管理的 /swapfile：执行将把它的大小调整为所选值。 */
  managedFilePresent: boolean;
  /**
   * /swapfile 在磁盘上的实际文件大小（MB），不在盘为 0；在盘未启用
   * 也回真实大小——agent 容量校验把这块占量计入可用（重建时释放），
   * 弹窗空间预检凭此字段逐字镜像 agent 谓词（R3-NODE-01）。
   */
  fileSizeMB: number;
}
export const getNodeSwapInfo = (nodeId: number) =>
  Network.post<NodeSwapInfo>("/node/swap-info", { nodeId }, { timeout: 60000 });
/** 一键设置 Swap 的执行回执：执行前后的总量/已用对照。 */
export interface NodeSwapSetResult {
  before: { totalMB: number; usedMB: number };
  after: { totalMB: number; usedMB: number };
  sizeGB: number;
  /** 非致命残留提示（如旧 swap 备份文件删除失败）：执行已成功，逐条以 ⚠️ toast 呈现。 */
  warnings?: string[];
}
export const setNodeSwap = (data: { nodeId: number; sizeGB: number }) =>
  Network.post<NodeSwapSetResult>("/node/swap-set", data, {
    // 与后端预算对齐（timeout-budgets.tsv P006/T067）：后端命令腿 600s、写截止
    // 660s；swapoff 须排空在用页、慢盘 dd 回退更久，FE 短于 660s 会出现「界面
    // 报超时失败、agent 实际执行成功」的假失败回执。
    timeout: 660000,
  });
/** 节点到各测试点最近 hours 小时的延迟趋势。 */
export const getNodeLatencyHistory = (id: number, hours: number) =>
  Network.post<LatencyTrend>("/node/latency/history", { id, hours });
/** 节点调优四面（vpsing tcp.sh 模型）：每面独立应用、独立报告状态。 */
export type NodeTuneFaceId = "ipv4" | "bbr" | "kernel" | "nic";
/** 四面状态：键与 agent 状态回包的 faces 对象同名（线协议，不改名）。 */
export interface NodeTuneFaces {
  ipv4: boolean;
  bbr: boolean;
  kernel: boolean;
  nic: boolean;
}
/** 调优状态（agent TuneStatus / Tune 回包，面板原样透传）：
 * 四面布尔 + 当前拥塞算法 / 默认队列算法概要。消费侧须先经
 * normalizeNodeTuneStatus（api/node-tune-status）归一：2.1.0
 * agent 的回包是扁平形状（ipv4Priority / congestionControl /
 * defaultQdisc），不归一会被误判为旧 agent。 */
export interface NodeTuneStatus {
  faces: NodeTuneFaces;
  /** 当前拥塞控制算法（如 "bbr" / "cubic"）。 */
  congestion: string;
  /** 当前默认队列算法（如 "fq" / "pfifo_fast"）。 */
  qdisc: string;
  /** agent 进程自身的句柄上限实回值（C3-03）：soft/hard 为当前进程
   * 的 RLIMIT_NOFILE，limitsFile 表示 limits 文件是否在位。旧回包
   * 缺失时为 undefined，消费面必须整段省略、不得写死数字冒充。 */
  nofile?: { soft: number; hard: number; limitsFile: boolean };
  warnings?: string[];
}
/** 线路探针结果（agent TuneProbe 回包）：保留两条腿——参考主机
 * TCP 连接 RTT + agent 内 sock_diag 对已建立连接的 RTT 采样。
 * withSpeed/speedtestURL 下载测速腿已退役（其唯一消费者是已弃用的
 * BDP 尺寸推导），回包中的 suggested/bandwidth 字段不再消费。 */
export interface NodeTuneProbeResult {
  /** sock_diag 采样的 RTT 中位数（毫秒），不可用时缺失。 */
  rttMedianMs?: number;
  /** 参与中位数计算的样本数。 */
  rttSamples: number;
  rttAvailable: boolean;
  /** "tcp_info_median" | "unavailable"。 */
  rttSource: string;
  /** sock_diag 采样 RTT 分布（毫秒）：最小 / p95 / 最大，与中位数
   * 同源逐样本计算；不可用时缺失。本腿是连接快照，无丢包/抖动。 */
  rttMinMs?: number;
  rttP95Ms?: number;
  rttMaxMs?: number;
  /** 参考腿：探针序列成功样本的 TCP 连接 RTT 中位数（毫秒），仅作参考值。 */
  panelRttMs?: number;
  panelRttAvailable: boolean;
  /** 参考腿一轮探针的发送 / 收到计数；参考腿未跑（或旧 agent）时缺失。 */
  panelRttSent?: number;
  panelRttReceived?: number;
  /** 参考腿丢包率（0–1，由发送/收到推导）；整轮全丢包时为 1，
   * 参考腿未跑时缺失。 */
  panelRttLossRate?: number;
  /** 参考腿成功样本的 RTT 分布（毫秒）：最小 / 最大；成功样本 ≥ 2
   * 时另有 p95。不可测时缺失。 */
  panelRttMinMs?: number;
  panelRttP95Ms?: number;
  panelRttMaxMs?: number;
  /** 参考腿抖动（毫秒）：相邻成功样本 RTT 差的平均绝对值；
   * 成功样本 < 2 时缺失（不可测）。 */
  panelRttJitterMs?: number;
  warnings?: string[];
}
/** 调优动作：status 读四面状态；apply-* / rollback 经面板映射为
 * agent 的 Tune 命令（payload {action}）；probe 为线路探针
 * （面板映射 TuneProbe）。旧模型的 apply（分组勾选 + 带宽/延迟手填）、
 * auto/deep/cancel 任务与 restore/restoreAuto 已全部退役。 */
export type NodeTuneAction =
  | "status"
  | "apply-ipv4"
  | "apply-bbr"
  | "apply-kernel"
  | "apply-nic"
  | "rollback"
  | "probe";
/** 节点网络调优：四面状态读取/逐面应用/一键回退与线路探针。
 * 全部动作共用同一 40s 前端界（登记 T019：面板 30s < FE 40s；
 * probe 的面板界为 90s，见后端 nodeTuneProbeTimeout）。 */
export const nodeTune = <T = NodeTuneStatus>(
  id: number,
  action: NodeTuneAction,
  referenceHost?: string,
) =>
  Network.post<T>(
    "/node/tune",
    {
      id,
      action,
      ...(referenceHost ? { referenceHost } : {}),
    },
    { timeout: 40 * 1000 },
  );
/** 节点到延迟测试点的回程路由追踪结果。 */
export const getNodeRoute = (id: number) =>
  Network.post<NodeRoute | null>("/node/route", { id });
/** 立即追踪节点回程路由（逐个测试点）。 */
export const probeNodeRoute = (id: number) =>
  Network.post<NodeRoute | null>(
    "/node/route/probe",
    { id },
    // R8-T-08：逐目标串行追踪的现实界 ≈360s（≤30 目标 × agent 每腿 ≈6s
    // 上限 × 双腿），前端抬至 390s；面板侧命令界 20s/腿的最坏 1200s
    // 与服务端 120s 截断不在本批口径内，残余见修复报告。
    { timeout: 390_000 },
  );
