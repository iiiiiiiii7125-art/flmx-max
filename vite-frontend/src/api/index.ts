import type {
  BatchOperationResult,
  ForwardDiagnosisApiData,
  ForwardApiItem,
  GroupPermissionApiItem,
  NodeReleaseApiItem,
  NodeApiItem,
  SpeedLimitApiItem,
  TunnelBatchDeletePreviewApiData,
  TunnelBatchDeleteWithForwardsApiData,
  TunnelDeletePreviewApiData,
  TunnelDeleteWithForwardsApiData,
  TunnelDiagnosisApiData,
  TunnelGroupApiItem,
  UserApiItem,
  UserGroupApiItem,
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
export const getDashboardNodeExpiryList = () =>
  Network.post<NodeApiItem[]>("/node/list", {});
export const updateNode = (data: NodeMutationPayload) =>
  Network.post("/node/update", data);
export const deleteNode = (id: number) =>
  Network.post<{ brokenReferrers?: string[]; brokenReferrerCount?: number }>(
    "/node/delete",
    { id },
  );
export const getNodeInstallCommand = (
  id: number,
  channel: ReleaseChannel = "stable",
) => Network.post<string>("/node/install", { id, channel });
export const updateNodeOrder = (data: {
  nodes: Array<{ id: number; inx: number }>;
}) => Network.post("/node/update-order", data);
export const dismissNodeExpiryReminder = (id: number) =>
  Network.post("/node/dismiss-expiry-reminder", { id });
export const checkNodeStatus = (nodeId?: number) => {
  const params = nodeId ? { nodeId } : {};

  return Network.post("/node/check-status", params);
};

export const upgradeNode = (
  id: number,
  version?: string,
  channel: ReleaseChannel = "stable",
) =>
  Network.post(
    "/node/upgrade",
    { id, version: version || "", channel },
    { timeout: 5 * 60 * 1000 },
  );
export const batchUpgradeNodes = (
  ids: number[],
  version?: string,
  channel: ReleaseChannel = "stable",
) =>
  Network.post(
    "/node/batch-upgrade",
    { ids, version: version || "", channel },
    { timeout: 15 * 60 * 1000 },
  );
export const getNodeReleases = (channel: ReleaseChannel = "stable") =>
  Network.post<NodeReleaseApiItem[]>("/node/releases", { channel });
export const rollbackNode = (id: number) =>
  Network.post("/node/rollback", { id });
export const testNodeNftables = (nodeId: number) =>
  Network.post("/node/nftables/test", { nodeId });
export const reconcileNodeNftables = (nodeId: number) =>
  Network.post("/node/nftables/reconcile", { nodeId });
export const clearNodeNftables = (nodeId: number) =>
  Network.post("/node/nftables/clear", { nodeId });

// 隧道CRUD操作 - 全部使用POST请求
export const createTunnel = (data: TunnelMutationPayload, idemKey?: string) =>
  Network.post("/tunnel/create", data, { idemKey });
export const getTunnelList = () =>
  Network.post<TunnelApiItem[]>("/tunnel/list");
export const getTunnelById = (id: number) =>
  Network.post<TunnelApiItem>("/tunnel/get", { id });
export const updateTunnel = (data: TunnelMutationPayload) =>
  Network.post("/tunnel/update", data, { timeout: 120_000 });
export const deleteTunnel = (id: number) =>
  Network.post("/tunnel/delete", { id });
export const previewTunnelDelete = (id: number) =>
  Network.post<TunnelDeletePreviewApiData>("/tunnel/delete-preview", { id });
export const deleteTunnelWithForwards = (data: {
  id: number;
  action: "replace" | "delete_forwards";
  targetTunnelId?: number;
}) =>
  Network.post<TunnelDeleteWithForwardsApiData | BatchOperationResult>(
    "/tunnel/delete-with-forwards",
    data,
  );
export const previewBatchTunnelDelete = (ids: number[]) =>
  Network.post<TunnelBatchDeletePreviewApiData>(
    "/tunnel/batch-delete-preview",
    {
      ids,
    },
  );
export const batchDeleteTunnelsWithForwards = (data: {
  ids: number[];
  action: "replace" | "delete_forwards";
  targetTunnelId?: number;
}) =>
  Network.post<TunnelBatchDeleteWithForwardsApiData>(
    "/tunnel/batch-delete-with-forwards",
    data,
  );
export const diagnoseTunnel = (tunnelId: number) =>
  Network.post<TunnelDiagnosisApiData>(
    "/tunnel/diagnose",
    { tunnelId },
    { timeout: 120 * 1000 },
  );
export const updateTunnelOrder = (data: {
  tunnels: Array<{ id: number; inx: number }>;
}) => Network.post("/tunnel/update-order", data);

// 用户隧道权限管理操作 - 全部使用POST请求
export const assignUserTunnel = (data: UserTunnelAssignPayload) =>
  Network.post("/tunnel/user/assign", data);
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
export const createForward = (
  data: ForwardMutationPayload,
  idemKey?: string,
) => Network.post("/forward/create", data, { idemKey });
export const getForwardList = () =>
  Network.post<ForwardApiItem[]>("/forward/list");
export const updateForward = (data: ForwardMutationPayload) =>
  Network.post("/forward/update", data);
export const deleteForward = (id: number) =>
  Network.post("/forward/delete", { id });
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
    { timeout: 120 * 1000 },
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

export const getUserGroups = (id: number) =>
  Network.post<number[]>("/user/groups", { id });

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
  Network.post<SystemUpgradeVersionApiData>("/system/version");

export const checkSystemUpgrade = (
  channel: ReleaseChannel = "stable",
  options?: { timeout?: number; silent?: boolean },
) =>
  Network.post<SystemUpgradeCheckApiData>(
    "/system/check-updates",
    {
      channel,
    },
    options,
  );

export const runSystemUpgrade = (
  version?: string,
  channel: ReleaseChannel = "stable",
) =>
  Network.post<SystemUpgradeRunApiData>(
    "/system/upgrade",
    { version: version || "", channel },
    { timeout: 60 * 1000 },
  );

// 面板升级状态查询：升级后后端容器重启期间请求会失败，调用方需静默重试
export const getSystemUpgradeStatus = () =>
  Network.get<SystemUpgradeStatusApiData>("/system/upgrade/status");

export const exportBackupData = () => Network.post("/backup/export");
export const importBackupData = (data: BackupImportPayload) =>
  Network.post("/backup/import", data);
export const restoreBackupData = (data: BackupImportPayload) =>
  Network.post("/backup/restore", data);

// 验证码相关接口
export const checkCaptcha = () => Network.post("/captcha/check");
export const generateCaptcha = () => Network.post(`/captcha/generate`);
export const verifyCaptcha = (data: { captchaId: string; trackData: string }) =>
  Network.post("/captcha/verify", data);

// 批量操作接口
export const batchDeleteForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>("/forward/batch-delete", { ids });
export const batchPauseForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>("/forward/batch-pause", { ids });
export const batchResumeForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>("/forward/batch-resume", { ids });
export const batchDeleteTunnels = (ids: number[]) =>
  Network.post<BatchOperationResult>("/tunnel/batch-delete", { ids });
export const batchDeleteNodes = (ids: number[]) =>
  Network.post<BatchOperationResult>("/node/batch-delete", { ids });
export const batchRedeployForwards = (ids: number[]) =>
  Network.post<BatchOperationResult>("/forward/batch-redeploy", { ids });
export const batchRedeployTunnels = (ids: number[]) =>
  Network.post<BatchOperationResult>("/tunnel/batch-redeploy", { ids });
export const batchChangeTunnel = (data: {
  forwardIds: number[];
  targetTunnelId: number;
}) => Network.post<BatchOperationResult>("/forward/batch-change-tunnel", data);

// 分组与权限分配接口
export const getTunnelGroupList = () =>
  Network.post<TunnelGroupApiItem[]>("/group/tunnel/list");
export const createTunnelGroup = (
  data: { name: string; status?: number },
  idemKey?: string,
) => Network.post("/group/tunnel/create", data, { idemKey });
export const updateTunnelGroup = (data: {
  id: number;
  name: string;
  status?: number;
}) => Network.post("/group/tunnel/update", data);
export const deleteTunnelGroup = (id: number) =>
  Network.post("/group/tunnel/delete", { id });
export const assignTunnelsToGroup = (data: {
  groupId: number;
  tunnelIds: number[];
}) => Network.post("/group/tunnel/assign", data);

export const getUserGroupList = () =>
  Network.post<UserGroupApiItem[]>("/group/user/list");
export const createUserGroup = (
  data: { name: string; status?: number },
  idemKey?: string,
) => Network.post("/group/user/create", data, { idemKey });
export const updateUserGroup = (data: {
  id: number;
  name: string;
  status?: number;
}) => Network.post("/group/user/update", data);
export const deleteUserGroup = (id: number) =>
  Network.post("/group/user/delete", { id });
export const assignUsersToGroup = (data: {
  groupId: number;
  userIds: number[];
}) => Network.post("/group/user/assign", data);

export const getGroupPermissionList = () =>
  Network.post<GroupPermissionApiItem[]>("/group/permission/list");
export const assignGroupPermission = (data: {
  userGroupId: number;
  tunnelGroupId: number;
}) => Network.post("/group/permission/assign", data);
export const removeGroupPermission = (id: number) =>
  Network.post("/group/permission/remove", { id });

// 面板共享 (Federation) 接口
export const getPeerShareList = () =>
  Network.post<Array<Record<string, unknown>>>("/federation/share/list");
export const createPeerShare = (data: {
  name: string;
  nodeId: number;
  maxBandwidth?: number;
  expiryTime?: number;
  portRangeStart?: number;
  portRangeEnd?: number;
  allowedDomains?: string;
  allowedIps?: string;
}, idemKey?: string) =>
  Network.post("/federation/share/create", data, { idemKey });
export const updatePeerShare = (data: {
  id: number;
  name: string;
  maxBandwidth: number;
  expiryTime: number;
  portRangeStart: number;
  portRangeEnd: number;
  allowedDomains: string;
  allowedIps: string;
}) => Network.post("/federation/share/update", data);
export const deletePeerShare = (id: number) =>
  Network.post("/federation/share/delete", { id });
export const resetPeerShareFlow = (id: number) =>
  Network.post("/federation/share/reset-flow", { id });
export const getPeerRemoteUsageList = () =>
  Network.post<Array<Record<string, unknown>>>(
    "/federation/share/remote-usage/list",
  );
export const importRemoteNode = (data: { remoteUrl: string; token: string }) =>
  Network.post("/federation/node/import", data);

export interface BackupTypes {
  users?: boolean;
  nodes?: boolean;
  tunnels?: boolean;
  forwards?: boolean;
  userTunnels?: boolean;
  speedLimits?: boolean;
  tunnelGroups?: boolean;
  userGroups?: boolean;
  permissions?: boolean;
  configs?: boolean;
  inbounds?: boolean;
  // 后端 ExportPartial/Import 早已支持，前端清单曾漏列（F-026）。
  userInbounds?: boolean;
  announcements?: boolean;
  monitorPermissions?: boolean;
  // B4/F-004/F-030：订阅分配与监控定义。
  userSubscriptions?: boolean;
  serviceMonitors?: boolean;
}

export const exportBackup = async (types: string[] = []) => {
  const token = window.localStorage.getItem("token");
  const baseURL = axios.defaults.baseURL || "/api/v1/";

  const response = await axios.post(
    `${baseURL}/backup/export`,
    { types },
    {
      headers: {
        Authorization: token,
        "Content-Type": "application/json",
      },
      responseType: "blob",
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
  Network.post("/backup/import", data);

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

export const getNodeMetricsLatest = (nodeId: number) =>
  Network.get<NodeMetricApiItem>(`/monitor/nodes/${nodeId}/metrics/latest`, {}, { silent: true });

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
        success: item.success !== 0,
        errorMessage: item.errorMessage,
        timestamp: item.timestamp,
        chainDetails: item.chainDetails,
      }),
    ),
  };
};

export const getServiceMonitorList = () =>
  Network.get<ServiceMonitorApiItem[]>("/monitor/services", {}, { silent: true });

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

export const runServiceMonitor = (id: number) =>
  Network.post<ServiceMonitorResultApiItem>("/monitor/services/run", { id });

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
export const getKernelVersions = (kernelType: string) =>
  Network.post<
    { version: string; prerelease: boolean; publishedAt?: string }[]
  >("/kernel/versions", { kernelType });

export const installKernel = (data: {
  nodeId: number;
  kernelType: string;
  version: string;
}) => Network.post("/kernel/install", data);
export const controlKernel = (
  id: number,
  action: "start" | "stop" | "restart" | "remove",
  force = false,
) => Network.post("/kernel/control", { id, action, force });
export const getKernelLogs = (id: number) =>
  Network.post<KernelStateApiData>("/kernel/logs", { id });
export const getInboundList = () =>
  Network.post<InboundApiItem[]>("/inbound/list");
export const createInbound = (
  data: InboundMutationPayload,
  idemKey?: string,
) => Network.post<{ id: number }>("/inbound/create", data, { idemKey });
export const updateInbound = (data: InboundMutationPayload) =>
  Network.post<{ id: number }>("/inbound/update", data);
export const deleteInbound = (id: number) =>
  Network.post("/inbound/delete", { id });
export const batchDeleteInbounds = (ids: number[]) =>
  Network.post<BatchOperationResult>("/inbound/batch-delete", { ids });
export const batchToggleInbounds = (ids: number[], enabled: boolean) =>
  Network.post<BatchOperationResult>("/inbound/batch-toggle", {
    ids,
    enabled,
  });
export interface SubPipeline {
  lines: "" | "relay" | "direct";
  regions: string[];
  protocols: string[];
  include: string;
  exclude: string;
  rename: string;
  addFlag: boolean;
  prefix: string;
  dedupe: boolean;
  sort: "" | "name";
  regionGroups: boolean;
  /** Client-side chain: names matching chainLanding dial through the relay group. */
  chainLanding: string;
  chainRelay: string;
  /** 路由规则、DNS 与所选服务组（Mihomo / sing-box / Surge），移植自 kure29/flyx。 */
  rules: boolean;
  ruleGroups: string[];
  /** 链式代理所选节点，按 pipeline 处理前的原名。 */
  chainLandingNames: string[];
  chainRelayNames: string[];
  /** 整个订阅源进链式：源内节点全作落地 / 全作中转（87-d）。 */
  chainLandingSources: number[];
  chainRelaySources: number[];
  /** 自建节点全部作落地。 */
  chainLandingOwn: boolean;
  /** 被源/自建落地带入又单独设为「不用」的节点名（二期 UI 设置，一期只透传）。 */
  chainNoneNames: string[];
  /** 中转节点不进普通节点名单（二期 UI 设置，一期只透传）。 */
  chainHideRelays: boolean;
  /** 按源的名称筛选表达式，键为订阅源 ID（二期 UI 设置，一期只透传）。 */
  chainSourceFilters?: Record<string, string>;
  /** 链式源里没角色的节点不放进订阅（二期 UI 设置，一期只透传）。 */
  chainDropUnused: boolean;
  /**
   * 中转范围：订阅包含的端点 ID（0=直连，>0=中转 ID）。
   * 空数组 = 全部端点（不做范围过滤）。
   */
  relayIds: number[];
  /** JavaScript `function operator(nodes)`，在其他步骤后运行。 */
  script: string;
}
/** 订阅规则组（后端下发）。 */
export interface SubRuleGroup {
  key: string;
  name: string;
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
  overQuota: boolean;
  /** 默认订阅：普通用户的个人链接（?user=&sig=）挂在它上面。 */
  isDefault: boolean;
  /** 中转范围（与 options.relayIds 同步下发）：空 = 全部端点。 */
  relayIds: number[];
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
  region: string;
  protocol: string;
  line: "direct" | "relay";
  chain?: "landing" | "relay";
  /** 导入节点的订阅源 ID；0/缺省 = 非订阅源节点。 */
  sourceId?: number;
  /** 自建入站的端点（非导入节点）。 */
  own?: boolean;
}
export const getSubscriptions = () =>
  Network.post<{
    items: SubscriptionItem[];
    regions: string[];
    ruleGroups: SubRuleGroup[];
  }>("/subscription/list");
export const saveSubscription = (data: SubscriptionDraft, idemKey?: string) =>
  Network.post<SubscriptionItem>("/subscription/save", data, { idemKey });
export const deleteSubscription = (id: number) =>
  Network.post("/subscription/delete", { id });
export const resetSubscription = (id: number) =>
  Network.post<SubscriptionItem>("/subscription/reset", { id });
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
  errors: string[];
}
export const getRemoteSources = () =>
  Network.post<RemoteSourceItem[]>("/subscription/source/list");
export const saveRemoteSource = (data: {
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
}, idemKey?: string) =>
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
    // F-024：脚本步骤出错时后端保留其余步骤的节点并在此回告错误，
    // 预览区必须展示，避免用户以为节点被脚本吞掉。
    scriptError?: string;
  }>("/subscription/preview", data);

export const testNotify = (data: {
  telegramToken: string;
  telegramChatId: string;
  telegramApiBase: string;
  webhookUrl: string;
}) => Network.post("/notify/test", data, { timeout: 30000 });
export const probeInbounds = (ids: number[] = []) =>
  Network.post<Record<string, InboundProbe>>(
    "/inbound/probe",
    { ids },
    { timeout: 60000 },
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
}) => Network.post("/inbound/split", data);
export const toggleInbound = (id: number, enabled: boolean) =>
  Network.post("/inbound/toggle", { id, enabled });
export const resetInboundTraffic = (id: number) =>
  Network.post("/inbound/reset-traffic", { id });
export const regenerateInbound = (id: number) =>
  Network.post("/inbound/regenerate", { id });
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
}) => Network.post("/inbound/external/update", data);
export const getInboundShare = (id: number) =>
  Network.post<InboundShareEntry[]>("/inbound/share", { id });
export const getSubInfo = (reset = false, silent = false) =>
  Network.post<SubInfoApiData>("/inbound/sub/info", { reset }, { silent });
export const getInboundRelays = (inboundId: number) =>
  Network.post<InboundRelayApiItem[]>("/inbound/relay/list", { inboundId });
/** 立即测量中转线路（空 ids = 全部）及其入口节点的延迟。 */
export const probeInboundRelays = (ids: number[] = []) =>
  Network.post<Record<string, RelayLatency>>(
    "/inbound/relay/probe",
    { ids },
    { timeout: 90000 },
  );
export const createInboundRelay = (data: {
  inboundId: number;
  tunnelId: number;
  inPort?: number;
  name?: string;
  shareHost?: string;
}, idemKey?: string) =>
  Network.post<{ id: number }>("/inbound/relay/create", data, { idemKey });
export const deleteInboundRelay = (id: number, force = false) =>
  Network.post("/inbound/relay/delete", { id, force });
export interface UserInboundAssignment {
  inboundId: number;
  relayId: number;
}
export const getUserInbounds = (userId: number) =>
  Network.post<UserInboundAssignment[]>("/user-inbound/list", { userId });
// assignments: [{inboundId, relayId}]; relayId=0 means the inbound itself (direct).
export const setUserInbounds = (userId: number, assignments: UserInboundAssignment[]) =>
  Network.post("/user-inbound/set", { userId, assignments });
// Legacy: plain inbound IDs (direct only). Kept for backward compatibility.
export const setUserInboundsLegacy = (userId: number, inboundIds: number[]) =>
  Network.post("/user-inbound/set", { userId, inboundIds });
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
  /** 过期时间（毫秒）；0 = 永不过期。 */
  expireTime: number;
  overQuota: boolean;
  enabled: boolean;
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
export const getMySubscriptionTraffic = (
  id: number,
  range: SubTrafficRange,
) =>
  Network.post<SubTraffic>(
    "/user/subscription/traffic",
    trafficQuery(id, range),
  );
/** 立即探测节点到延迟测试点的延迟（移植自 kure29/flyx）。 */
export const probeNodeLatency = (ids: number[] = []) =>
  Network.post<Record<string, InboundProbe>>(
    "/node/latency/probe",
    { ids },
    { timeout: 60000 },
  );
/** 设置节点的延迟测试点：自选或跟随默认。 */
export const setNodeLatencyTargets = (data: {
  ids: number[];
  custom: boolean;
  points: string[];
  pinned: string[];
}) => Network.post("/node/latency/targets", data, { timeout: 60000 });
/** 节点到各测试点最近 hours 小时的延迟趋势。 */
export const getNodeLatencyHistory = (id: number, hours: number) =>
  Network.post<LatencyTrend>("/node/latency/history", { id, hours });
/** 节点调优的单个 sysctl 参数。 */
export interface NodeTuneSetting {
  key: string;
  current: string;
  target: string;
  supported: boolean;
  /** 已达到推荐值（AtLeast 只升不降）。 */
  ok: boolean;
}
export interface NodeTuneGroup {
  id: string;
  name: string;
  desc: string;
  applied: boolean;
  settings: NodeTuneSetting[];
}
export interface NodeTuneStatus {
  kernel: string;
  congestion: string;
  available: string;
  hasBackup: boolean;
  groups: NodeTuneGroup[];
  warnings?: string[];
}
/** 节点网络调优（sysctl）：读取状态、应用所选分组、恢复默认（移植自 kure29/flyx）。 */
export const nodeTune = (
  id: number,
  action: "status" | "apply" | "restore",
  groups: string[] = [],
) =>
  Network.post<NodeTuneStatus>(
    "/node/tune",
    { id, action, groups },
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
    { timeout: 300000 },
  );
