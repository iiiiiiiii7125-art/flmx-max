export interface NodeApiItem {
  id: number;
  name: string;
  status: number;
  forwardMode?: "agent" | "nftables";
  sshConfig?: NodeSshConfigApiItem | null;
  inx?: number;
  // 以下字段后端以 nullable* 下发：无值时为显式 null，不是键缺失（F-062）。
  remark?: string | null;
  expiryTime?: number | null;
  renewalCycle?: "month" | "quarter" | "year" | "" | null;
  ip?: string;
  serverIp?: string;
  serverIpV4?: string | null;
  serverIpV6?: string | null;
  extraIPs?: string | null;
  port?: string;
  tcpListenAddr?: string;
  udpListenAddr?: string;
  version?: string | null;
  http?: number;
  tls?: number;
  socks?: number;
  isRemote?: number;
  remoteUrl?: string | null;
  remoteToken?: string | null;
  remoteConfig?: string | null;
  interfaceName?: string | null;
  expiryReminderDismissed?: number;
  syncError?: string;
  [key: string]: unknown;
}

export interface UserApiItem {
  id: number;
  user: string;
  name?: string;
  roleId?: number;
  status: number;
  createdTime?: number;
  /** 后端 nullableInt64 下发：从未更新过时为 null（F-062）。 */
  updatedTime?: number | null;
  flow: number;
  flowMiB?: number;
  num: number;
  expTime?: number;
  flowResetTime?: number;
  maxConn?: number;
  inFlow?: number;
  outFlow?: number;
  dailyQuotaGB?: number;
  monthlyQuotaGB?: number;
  dailyUsedBytes?: number;
  monthlyUsedBytes?: number;
  disabledByQuota?: number;
  quotaDisabledAt?: number;
  [key: string]: unknown;
}

export interface UserListQuery {
  current?: number;
  size?: number;
  keyword?: string;
  [key: string]: unknown;
}

export interface TunnelApiItem {
  id: number;
  name: string;
  type: number;
  status: number;
  flow?: number;
  trafficRatio?: number;
  /** 后端 nullableString 下发：未设置时为 null（F-062）。 */
  inIp?: string | null;
  ipPreference?: string;
  probeTargetHost?: string;
  probeTargetPort?: number;
  engine?: string;
  inNodeId?: TunnelChainNodePayload[];
  outNodeId?: TunnelChainNodePayload[];
  chainNodes?: TunnelChainNodePayload[][];
  inx?: number;
  [key: string]: unknown;
}

export interface ForwardApiItem {
  id: number;
  name: string;
  status: number;
  tunnelName?: string;
  tunnelTrafficRatio?: number;
  /** 后端 nullable 下发：无入口 IP/端口时为 null（F-062）。 */
  inIp?: string | null;
  inPort?: number | null;
  remoteAddr?: string;
  inFlow?: number;
  outFlow?: number;
  userId?: number;
  tunnelId?: number;
  speedId?: number | null;
  ipMaxConn?: number;
  ipSpeedId?: number | null;
  ipSpeedLimitName?: string;
  maxConn?: number;
  proxyProtocol?: number;
  proxyProtocolReceive?: number;
  proxyProtocolSend?: number;
  inx?: number;
  [key: string]: unknown;
}

export interface UserTunnelApiItem {
  id: number;
  name: string;
  /** /tunnel/user/tunnel 仅在隧道有入口转发时下发端口范围（F-144）。 */
  portRangeMin?: number;
  portRangeMax?: number;
  [key: string]: unknown;
}

export interface UserTunnelPermissionApiItem {
  id: number;
  userId: number;
  tunnelId: number;
  tunnelName: string;
  status: number;
  flow: number;
  flowMiB?: number;
  num: number;
  expTime: number;
  flowResetTime: number;
  speedId?: number | null;
  speedLimitName?: string;
  inFlow: number;
  outFlow: number;
  tunnelFlow?: number;
  [key: string]: unknown;
}

export interface StatisticsFlowApiItem {
  id: number;
  userId: number;
  flow: number;
  totalFlow: number;
  time: string;
  [key: string]: unknown;
}

export interface SpeedLimitApiItem {
  id: number;
  name: string;
  speed: number;
  status: number;
  /** 后端 int64（毫秒）；updatedTime 经 nullableInt64 下发，未更新过为 null（F-140）。 */
  createdTime: number;
  updatedTime: number | null;
  [key: string]: unknown;
}

export interface TunnelGroupApiItem {
  id: number;
  name: string;
  status: number;
  tunnelIds: number[];
  tunnelNames: string[];
  createdTime: number;
  [key: string]: unknown;
}

export interface UserGroupApiItem {
  id: number;
  name: string;
  status: number;
  userIds: number[];
  userNames: string[];
  createdTime: number;
  [key: string]: unknown;
}

export interface GroupPermissionApiItem {
  id: number;
  userGroupId: number;
  /** 左连接无匹配（组已删）时后端下发 null（F-062）。 */
  userGroupName: string | null;
  tunnelGroupId: number;
  tunnelGroupName: string | null;
  createdTime: number;
  [key: string]: unknown;
}

export interface TunnelDiagnosisApiItem {
  success: boolean;
  /** 流式诊断的起始帧标记（control_plane 起始项），页面本地类型早已自备（F-144）。 */
  diagnosing?: boolean;
  description: string;
  nodeName: string;
  nodeId: string;
  targetIp: string;
  targetPort?: number;
  message?: string;
  averageTime?: number;
  packetLoss?: number;
  fromChainType?: number;
  fromInx?: number;
  toChainType?: number;
  toInx?: number;
  [key: string]: unknown;
}

export interface TunnelDiagnosisApiData {
  tunnelName: string;
  tunnelType: string;
  timestamp: number;
  results: TunnelDiagnosisApiItem[];
}

export interface ForwardDiagnosisApiData {
  forwardName: string;
  timestamp: number;
  results: TunnelDiagnosisApiItem[];
}

export interface NodeReleaseApiItem {
  version: string;
  name: string;
  publishedAt: string;
  prerelease: boolean;
  channel: "stable" | "dev";
}

export interface SystemUpgradeReleaseApiItem {
  version: string;
  name: string;
  publishedAt: string;
  prerelease: boolean;
  channel: "stable" | "dev";
}

export interface UserPackageInfoApiData {
  userInfo: {
    id: number;
    name: string;
    user: string;
    status: number;
    flow: number;
    flowMiB: number;
    inFlow: number;
    outFlow: number;
    num: number;
    /** 后端 int64 毫秒（F-141）；后端不下发 maxConn，勿在此声明。 */
    expTime: number;
    flowResetTime: number;
    createdTime: number;
    /** 从未更新过时为 null。 */
    updatedTime: number | null;
    [key: string]: unknown;
  };
  tunnelPermissions: UserTunnelPermissionApiItem[];
  forwards: ForwardApiItem[];
  statisticsFlows: StatisticsFlowApiItem[];
  [key: string]: unknown;
}

export interface BatchOperationResult {
  successCount: number;
  failCount: number;
  failures?: BatchOperationFailure[];
  /** 批量删除中转为 pending-delete 的条数与明细（节点不可达时，后端 mutations.go）（F-144）。 */
  pendingCount?: number;
  pending?: BatchOperationFailure[];
  [key: string]: unknown;
}

export interface BatchOperationFailure {
  id?: number;
  name?: string;
  reason?: string;
  [key: string]: unknown;
}

export interface TunnelDeletePreviewForwardApiItem {
  id: number;
  name: string;
  userId: number;
  userName: string;
  inPort: number;
  [key: string]: unknown;
}

export interface TunnelDeletePreviewApiData {
  tunnelId: number;
  tunnelName: string;
  forwardCount: number;
  sampleForwards: TunnelDeletePreviewForwardApiItem[];
  [key: string]: unknown;
}

export interface TunnelBatchDeletePreviewApiData {
  tunnelCount: number;
  totalForwardCount: number;
  items: TunnelDeletePreviewApiData[];
  [key: string]: unknown;
}

export interface TunnelDeleteWithForwardsApiData {
  forwardCount: number;
  migratedCount: number;
  deletedForwardCount: number;
  portAdjustedCount: number;
  warnings?: string[];
  [key: string]: unknown;
}

export interface TunnelBatchDeleteWithForwardsApiData {
  successCount: number;
  failCount: number;
  failures?: BatchOperationFailure[];
  deletedForwardCount: number;
  migratedCount: number;
  portAdjustedCount: number;
  warnings?: string[];
  [key: string]: unknown;
}

export interface UserMutationPayload {
  id?: number;
  user?: string;
  name?: string;
  // X-P3-7（K-01）：后端 userCreate/userUpdate 只读 pwd（mutations.go），
  // 此前误声明 password 会误导调用方传错字段名、密码静默不生效。
  pwd?: string;
  status?: number;
  flow?: number;
  num?: number;
  /** 只收毫秒时间戳数字：字符串会被后端 asInt64 解析失败后静默回退默认到期（F-144）。 */
  expTime?: number;
  flowResetTime?: number;
  maxConn?: number;
  dailyQuotaGB?: number;
  monthlyQuotaGB?: number;
  tunnelFlow?: number;
}

export interface NodeMutationPayload {
  id?: number | null;
  name?: string;
  status?: number;
  forwardMode?: "agent" | "nftables";
  sshConfig?: NodeSshConfigMutationPayload | null;
  inx?: number;
  remark?: string;
  expiryTime?: number;
  renewalCycle?: "month" | "quarter" | "year" | "";
  serverIp?: string;
  serverIpV4?: string;
  serverIpV6?: string;
  extraIPs?: string;
  port?: string;
  tcpListenAddr?: string;
  udpListenAddr?: string;
  interfaceName?: string;
  http?: number;
  tls?: number;
  socks?: number;
}

export interface NodeSshConfigApiItem {
  host?: string;
  port?: number;
  username?: string;
  authType?: "password" | "private_key";
  password?: string;
  privateKey?: string;
  passphrase?: string;
  sudoMode?: "none" | "sudo" | "sudo_su";
  [key: string]: unknown;
}

export interface NodeSshConfigMutationPayload {
  host?: string;
  port?: number;
  username?: string;
  authType?: "password" | "private_key";
  password?: string;
  privateKey?: string;
  passphrase?: string;
  sudoMode?: "none" | "sudo" | "sudo_su";
}

export interface TunnelChainNodePayload {
  nodeId: number;
  protocol?: string;
  strategy?: string;
  connectIp?: string;
  chainType?: number;
  inx?: number;
}

export interface TunnelMutationPayload {
  id?: number;
  name?: string;
  type?: number;
  status?: number;
  forwardMode?: "agent" | "nftables";
  flow?: number;
  trafficRatio?: number;
  inIp?: string;
  ipPreference?: string;
  probeTargetHost?: string;
  probeTargetPort?: number;
  engine?: string;
  inNodeId?: TunnelChainNodePayload[];
  outNodeId?: TunnelChainNodePayload[];
  chainNodes?: TunnelChainNodePayload[][];
}

export interface UserQuotaResetPayload {
  userId: number;
  scope?: "daily" | "monthly" | "all";
}

export interface UserTunnelAssignPayload {
  userId?: number;
  id?: number;
  tunnelId?: number;
  flow?: number;
  flowMiB?: number;
  num?: number;
  expTime?: number;
  flowResetTime?: number;
  maxConn?: number;
  status?: number;
  speedId?: number | null;
  tunnels?: Array<{ tunnelId: number; speedId?: number | null }>;
}

export interface UserTunnelListQuery {
  userId?: number;
  tunnelId?: number;
  current?: number;
  size?: number;
}

export interface UserTunnelRemovePayload {
  id?: number;
  userId?: number;
  tunnelId?: number;
}

export interface ForwardMutationPayload {
  id?: number;
  name?: string;
  status?: number;
  forwardMode?: "agent" | "nftables";
  tunnelId?: number | null;
  inIp?: string;
  inPort?: number | null;
  remoteAddr?: string;
  strategy?: string;
  speedId?: number | null;
  ipMaxConn?: number;
  ipSpeedId?: number | null;
  maxConn?: number;
  proxyProtocol?: number;
  proxyProtocolReceive?: number;
  proxyProtocolSend?: number;
}

export interface SpeedLimitMutationPayload {
  id?: number;
  name?: string;
  speed?: number;
  status?: number;
}

export interface UpdatePasswordPayload {
  currentPassword: string;
  newPassword: string;
  /** 后端必填：空值即拒绝「确认密码不能为空」（F-143）。 */
  confirmPassword: string;
  /** 后端必填：空值即拒绝「新用户名不能为空」（F-143）。 */
  newUsername: string;
}

export interface BackupImportPayload {
  types: string[];
  [key: string]: unknown;
}

export interface NodeMetricApiItem {
  id: number;
  nodeId: number;
  timestamp: number;
  cpuUsage: number;
  memoryUsage: number;
  diskUsage: number;
  netInBytes: number;
  netOutBytes: number;
  netInSpeed: number;
  netOutSpeed: number;
  load1: number;
  load5: number;
  load15: number;
  tcpConns: number;
  udpConns: number;
  uptime: number;
}

export interface TunnelMetricApiItem {
  /** 聚合查询不选 id 列，恒为 0；图表以 timestamp 为键，勿依赖此字段（F-144）。 */
  id?: number;
  tunnelId: number;
  nodeId: number;
  timestamp: number;
  bytesIn: number;
  bytesOut: number;
  connections: number;
  errors: number;
  avgLatencyMs: number;
}

export interface ServiceMonitorApiItem {
  id: number;
  name: string;
  // Keep as string for forward-compatibility.
  type: string;
  target: string;
  intervalSec: number;
  timeoutSec: number;
  nodeId: number;
  enabled: number;
  createdTime: number;
  updatedTime: number;
}

export interface ServiceMonitorResultApiItem {
  id: number;
  monitorId: number;
  nodeId: number;
  timestamp: number;
  success: number;
  latencyMs: number;
  statusCode: number;
  errorMessage: string;
}

export interface ServiceMonitorMutationPayload {
  id?: number;
  name: string;
  type: "tcp" | "icmp";
  target: string;
  intervalSec?: number;
  timeoutSec?: number;
  nodeId?: number;
  enabled?: number;
}

export interface ServiceMonitorLimitsApiData {
  checkerScanIntervalSec: number;
  workerLimit: number;
  minIntervalSec: number;
  defaultIntervalSec: number;
  minTimeoutSec: number;
  defaultTimeoutSec: number;
  maxTimeoutSec: number;
}

export interface StorageSummaryApiData {
  dbType: string;
  databaseSizeBytes: number;
  databaseSizeText: string;
}

export interface SystemUpgradeCapabilityApiData {
  capable: boolean;
  reasons: string[];
  deployDir: string;
  backendContainer: string;
}

/** 待执行迁移的预检状态（后端 repo.PendingMigrationStatus）（F-144）。 */
export interface PendingMigrationStatusApiData {
  required: boolean;
  tables?: Array<{
    table: string;
    duplicateGroups: number;
    sample?: string;
  }>;
  guide?: string;
}

export interface SystemUpgradeVersionApiData {
  currentVersion: string;
  latestVersion: string;
  hasUpdate: boolean;
  channel: "stable" | "dev";
  reason?: string;
  capability: SystemUpgradeCapabilityApiData;
  /** 预检发现待执行迁移时非空（F-144）。 */
  pendingMigrations?: PendingMigrationStatusApiData;
  /** 需要人工补建管理员时为 true（F-144）。 */
  adminBootstrapRequired?: boolean;
}

export interface SystemUpgradeCheckApiData extends SystemUpgradeVersionApiData {
  releases: SystemUpgradeReleaseApiItem[];
}

export interface SystemUpgradeRunApiData {
  version: string;
  channel: "stable" | "dev";
  composeAsset: string;
  helperContainer: string;
  backendImageId: string;
  message: string;
}

// 面板升级状态（后端 GET /api/v1/system/upgrade/status；进程重启后不丢失）。
export interface SystemUpgradeStatusApiData {
  inProgress: boolean;
  startedAt?: string;
  version?: string;
  /** 后端落盘的升级终态：success / rolled_back / aborted / helper_start_failed */
  result?: string;
  finishedAt?: string;
}

export interface MonitorNodeApiItem {
  id: number;
  inx: number;
  name: string;
  status: number;
  version?: string;
  updatedTime: number;
}

export interface MonitorTunnelApiItem {
  id: number;
  inx: number;
  name: string;
  status: number;
  updatedTime: number;
}

export interface MonitorPermissionApiItem {
  id: number;
  userId: number;
  createdTime: number;
}

export interface MonitorAccessApiData {
  allowed: boolean;
  // R2X-038：是否管理员，前端据此对只读用户隐藏写入口。
  admin: boolean;
  reason?: string;
}

export interface TunnelQualityHopApiItem {
  fromNodeId: number;
  fromNodeName: string;
  toNodeId: number;
  toNodeName: string;
  latency: number;
  loss: number;
  targetIp?: string;
  targetPort?: number;
}

export interface TunnelQualityCandidateHopApiItem
  extends TunnelQualityHopApiItem {
  fromRole: "entry" | "middle" | "exit";
  toRole: "middle" | "exit" | "target";
  hopIndex: number;
  selected: boolean;
  errorMessage?: string;
}

export interface TunnelQualityChainDetailsApiItem {
  primaryPath?: TunnelQualityHopApiItem[];
  candidateHops?: TunnelQualityCandidateHopApiItem[];
}

/**
 * 隧道质量（归一化形态）：快照端点的 success 本就是 bool；历史端点
 * success 是 int(0/1)、且无 probeTarget*，由 api 层
 * getMonitorTunnelQualityHistory 归一化成本形态后再交出（F-063）。
 */
export interface TunnelQualityApiItem {
  tunnelId: number;
  entryToExitLatency: number;
  exitToBingLatency: number;
  entryToExitLoss: number;
  exitToBingLoss: number;
  probeTargetHost?: string;
  probeTargetPort?: number;
  success: boolean;
  errorMessage?: string;
  timestamp: number;
  chainDetails?: string;
}

/** 隧道质量历史端点的线缆形态（model.TunnelQuality 原样返回）：success 为 int 0/1、有 id、无 probeTarget*。 */
export interface TunnelQualityHistoryApiItem {
  id: number;
  tunnelId: number;
  entryToExitLatency: number;
  exitToBingLatency: number;
  entryToExitLoss: number;
  exitToBingLoss: number;
  success: number;
  errorMessage?: string;
  timestamp: number;
  chainDetails?: string;
}

// ---- 代理内核 / 入站 ----

export interface KernelTransportCap {
  type: string;
  securities: string[];
}

export interface KernelProtocolCap {
  protocol: string;
  transports: KernelTransportCap[];
}

export interface KernelInfo {
  type: string;
  name: string;
  implemented: boolean;
  /** Runs realm-engine tunnel forwards; installed by the panel itself. */
  relay?: boolean;
  protocols: KernelProtocolCap[] | null;
  versions?: string[];
  /** 后端无 omitempty：未配置时下发 null 而非缺失（F-144）。 */
  certModes?: string[] | null;
}

/** A preset 分流 category (geosite list, China IPs, BT). */
export interface SplitCategory {
  key: string;
  label: string;
  group: string;
}

/** A 分流 rule as stored on an inbound. */
export interface SplitRule {
  categories?: string[];
  domains?: string[];
  ips?: string[];
  action: "direct" | "block" | "proxy";
  outboundId?: number;
}

export interface KernelCapabilitiesApiData {
  kernels: KernelInfo[];
  ssMethods: string[];
  splitCategories?: SplitCategory[];
  defaults: {
    realitySni: string;
    fingerprint: string;
    ssMethod: string;
    flow: string;
  };
}

export interface KernelApiItem {
  id: number;
  nodeId: number;
  nodeName: string;
  nodeOnline: boolean;
  kernelType: string;
  version: string;
  installedVersion: string;
  status: "pending" | "running" | "stopped" | "error" | string;
  lastError: string;
  synced: boolean;
  enabled: boolean;
  inboundCount: number;
  updatedTime: number;
}

export interface KernelStateApiData {
  type: string;
  running: boolean;
  enabled: boolean;
  pid: number;
  version: string;
  error: string;
  restarts: number;
  startedAt: number;
  logs?: string[];
}

export interface InboundSettings {
  uuid?: string;
  flow?: string;
  encryptionAuth?: string;
  encryptionMode?: string;
  encryptionSecret?: string;
  password?: string;
  method?: string;
  snellVersion?: number;
  /** Snell v6 traffic shaping: unset (default) or "unshaped". */
  snellMode?: string;
  masquerade?: string;
  backendPort?: number;
  portHopping?: string;
  username?: string;
}

export interface InboundTransport {
  type: string;
  path?: string;
  host?: string;
  serviceName?: string;
  mode?: string;
}

export interface InboundSecurity {
  type: string;
  sni?: string;
  alpn?: string[];
  fingerprint?: string;
  certMode?: string;
  /** 仅请求侧：自签 PEM 内容；响应侧被后端遮蔽、从不回显（F-144）。 */
  certPem?: string;
  keyPem?: string;
  certFile?: string;
  keyFile?: string;
  realityDest?: string;
  realityPrivateKey?: string;
  realityPublicKey?: string;
  realityShortIds?: string[];
  realityMinClientVer?: string;
  shadowTlsPassword?: string;
  acmeEmail?: string;
  acmeDns?: string;
  acmeDnsKey?: string;
  acmeDnsSecret?: string;
  /** List output only: DNS credentials are stored but not returned. */
  acmeDnsSet?: boolean;
}

export interface InboundProbe {
  ok: boolean;
  skipped?: boolean;
  latencyMs?: number;
  /** 通过节点 agent 探测时的丢包率。 */
  lossPct?: number;
  /** 探测发起方（如"浙江移动"）；空表示面板服务器。 */
  from?: string;
  error?: string;
  checkedAt: number;
  /** 各测试点的延迟结果（节点延迟功能，移植自 kure29/flyx）。 */
  points?: InboundProbePoint[];
}

export interface InboundProbePoint {
  name: string;
  /** 在设置中固定显示在卡片上。 */
  pinned?: boolean;
  ok: boolean;
  latencyMs?: number;
  lossPct?: number;
  error?: string;
  /** 到该测试点的回程路由，如"电信 CN2 GIA"（第四批填充）。 */
  route?: string;
  /** The IPv6 return route of a dual-stack node, when traced. */
  routeV6?: string;
}

export interface InboundApiItem {
  id: number;
  kernelId: number;
  kernelType: string;
  kernelStatus: string;
  /** Imported node: not deployed by the panel, only relayed and shared. */
  external: boolean;
  /** Remote subscription the imported node came from. */
  sourceId?: number;
  sourceName?: string;
  /** Imported nodes: protocol summary and flattened parameters. */
  importedSummary?: string;
  importedParams?: [string, string][];
  nodeId: number;
  nodeName: string;
  nodeServerIp: string;
  name: string;
  protocol: string;
  listen: string;
  port: number;
  network: string;
  settings: InboundSettings;
  transport: InboundTransport;
  security: InboundSecurity;
  extra: string;
  shareHost: string;
  remark: string;
  relayOnly: boolean;
  relayCount: number;
  acceptProxyProtocol: boolean;
  relayWhitelist: boolean;
  whitelistExtra: string;
  enabled: boolean;
  uploadBytes: number;
  downloadBytes: number;
  createdTime: number;
  updatedTime: number;
  /** Last reachability check of an imported node, from the panel. */
  probe?: InboundProbe;
  /** Inbound this one's traffic is chained through; 0 leaves directly. */
  outboundId: number;
  outboundName?: string;
  /** The chain target was deleted; traffic is dropped until changed. */
  outboundMissing?: boolean;
  /** 分流 rules, tried in order before the default exit. */
  splitRules: SplitRule[];
  /** Local egress address family, independent of the inbound listener. */
  outboundIpStrategy?:
    | "auto"
    | "ipv4_only"
    | "ipv6_only"
    | "prefer_ipv4"
    | "prefer_ipv6";
}

export interface InboundMutationPayload {
  id?: number;
  nodeId?: number;
  kernelType?: string;
  name: string;
  protocol: string;
  listen: string;
  port: number;
  settings: InboundSettings;
  transport: InboundTransport;
  security: InboundSecurity;
  extra: string;
  shareHost: string;
  remark: string;
  relayOnly: boolean;
  acceptProxyProtocol: boolean;
  relayWhitelist: boolean;
  whitelistExtra: string;
  outboundId: number;
  splitRules: SplitRule[];
  outboundIpStrategy?:
    | "auto"
    | "ipv4_only"
    | "ipv6_only"
    | "prefer_ipv4"
    | "prefer_ipv6";
  enabled?: boolean;
}

export interface InboundShareEntry {
  name: string;
  kind: "direct" | "relay";
  host: string;
  port: number;
  uri: string;
  surge: string;
}

export interface InboundRelayApiItem {
  id: number;
  inboundId: number;
  forwardId: number;
  name: string;
  shareHost: string;
  managed: boolean;
  mode: "port" | "tunnel";
  forwardName: string;
  forwardStatus: number;
  forwardExists: boolean;
  tunnelId: number;
  tunnelName: string;
  remoteAddr: string;
  entries: { Host: string; Port: number }[];
  createdTime: number;
  /** Last measurement of the line, when made. */
  latency?: RelayLatency;
}

/** One leg of a relay line, measured from its first node. */
export interface RelayHop {
  from: string;
  to: string;
  ok: boolean;
  latencyMs?: number;
  lossPct?: number;
  error?: string;
}

/**
 * A relay line's estimated latency to each reference point: the entry
 * node's latency plus every leg to the landing. Without reference points
 * the only point is the path itself.
 */
export interface RelayLatency extends InboundProbe {
  entry?: string;
  entryNodeId?: number;
  hops: RelayHop[];
  pathMs?: number;
}

export interface SubInfoApiData {
  token: string;
  path: string;
}

/** 回程路由追踪的一跳。 */
export interface NodeRouteHop {
  ttl: number;
  ip?: string;
  rttMs?: number;
  /** 所属骨干网：CN2、163、4837、9929、CMI。 */
  net?: string;
}

export interface NodeRoutePoint {
  name: string;
  route?: string;
  reached: boolean;
  /** "IPv6" when traced over IPv6. */
  family?: string;
  hops?: NodeRouteHop[];
  error?: string;
  /** A dual-stack node's IPv6 trace to the same point. */
  ipv6?: NodeRoutePoint;
}

export interface NodeRoute {
  checkedAt: number;
  points: NodeRoutePoint[];
}

export interface LatencyTrendPoint {
  name: string;
  /** Averaged buckets; ms is null when every probe in it failed. */
  samples: { t: number; ms: number | null; lossPct: number }[];
  avgMs: number;
  maxMs: number;
  lossPct: number;
}

export interface LatencyTrend {
  since: number;
  until: number;
  points: LatencyTrendPoint[];
}
