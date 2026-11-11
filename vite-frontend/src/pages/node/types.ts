import { type NodeRenewalCycle } from "@/pages/node/renewal";
import { type NodeSystemInfo } from "@/pages/node/system-info";
import { type InboundProbe } from "@/api/types";

/** Shapes used by the node page. */
export interface Node {
  id: number;
  inx?: number;
  name: string;
  /** 节点到各延迟测试点的延迟。 */
  latency?: InboundProbe;
  /** 节点自选的测试点；null 表示跟随全局默认。 */
  latencyTargets?: { points: string[]; pinned: string[] } | null;
  // 后端 nullable* 下发：无值时为 null（列表经 any 映射透传，须按可空声明，B2 变体同步）。
  remark?: string | null;
  /** 展示分组标签（纯展示）：空/缺失＝未分组，节点页据此分段展示。 */
  displayGroup?: string | null;
  expiryTime?: number | null;
  renewalCycle?: NodeRenewalCycle | null;
  expiryReminderDismissed?: number;
  ip: string;
  serverIp: string;
  serverIpV4?: string | null;
  serverIpV6?: string | null;
  port: string;
  tcpListenAddr?: string;
  udpListenAddr?: string;
  extraIPs?: string | null;
  version?: string | null;
  /** A4-V-06 升级/回退落地核验（后端仅回 pending/failed，landed 不下发）：failed 时版本行下显示未落地标记。 */
  upgradeVerification?: {
    kind: string; // upgrade | rollback
    targetVersion: string;
    status: string; // pending | failed
    observedVersion?: string;
    fromVersion?: string;
    dispatchedAt?: number;
    deadlineAt?: number;
  } | null;
  /** C4-G2 待卸载登记状态（后端 attachNodeUninstallStates 透出）：
   * issued/acked/succeeded/failed/unconfirmed；无登记时无字段。
   * failed/unconfirmed 附 uninstallReason。UI 不许合并成「已删除」。 */
  uninstallState?: string;
  uninstallReason?: string;
  http?: number; // 0 关 1 开
  tls?: number; // 0 关 1 开
  socks?: number; // 0 关 1 开
  /** 定时重启 agent 服务（保险丝，默认关）：restartTime 为节点本机时间 HH:MM，jitter 为随机抖动分钟数。 */
  restartEnabled?: number; // 0 关 1 开
  restartTime?: string;
  restartJitterMinutes?: number;
  /** 重启模式：daily＝每天 restartTime 触发（默认）；interval＝每 restartIntervalMinutes 分钟触发一次。 */
  restartMode?: string;
  restartIntervalMinutes?: number;
  /** 转发面会话无进展拆除阈值（秒，保险丝，默认关）：0＝不拆；>0 时只拆双向零流量满该秒数的会话。 */
  idleTimeoutSeconds?: number;
  /** 入口判死窗口（秒，默认关）：0＝关；>0 时客户端方向零字节满该秒数且数据无人签收才拆。 */
  edgeIdleTimeoutSeconds?: number;
  /** 中途兜底窗口（秒，默认关）：0＝关；>0 时源头沉默满该秒数且服务器仅滴流才拆。 */
  backstopIdleTimeoutSeconds?: number;
  /** 兜底滴流速率上限（字节/秒）：0＝agent 默认（256）。 */
  backstopTrickleBps?: number;
  status: number;
  forwardMode?: "agent" | "nftables";
  sshConfig?: {
    host?: string;
    port?: number;
    username?: string;
    authType?: "password" | "private_key";
    password?: string;
    privateKey?: string;
    passphrase?: string;
    sudoMode?: "none" | "sudo" | "sudo_su";
    /** C3-02：已钉入的 SSH 主机密钥指纹（SHA256:…）；缺失＝未钉入。 */
    hostKeyFingerprint?: string;
  } | null;
  interfaceName?: string | null;
  connectionStatus: "online" | "offline";
  systemInfo?: NodeSystemInfo | null;
  copyLoading?: boolean;
  upgradeLoading?: boolean;
  rollbackLoading?: boolean;
}

export interface NodeForm {
  id: number | null;
  name: string;
  remark: string;
  displayGroup: string;
  expiryTime: number;
  renewalCycle: NodeRenewalCycle;
  serverHost: string;
  serverIpV4: string;
  serverIpV6: string;
  port: string;
  tcpListenAddr: string;
  udpListenAddr: string;
  interfaceName: string;
  extraIPs: string;
  forwardMode: "agent" | "nftables";
  sshHost: string;
  sshPort: string;
  sshUsername: string;
  sshAuthType: "password" | "private_key";
  sshPassword: string;
  sshPrivateKey: string;
  sshPassphrase: string;
  sshSudoMode: "none" | "sudo" | "sudo_su";
  http: number; // 0 关 1 开
  tls: number; // 0 关 1 开
  socks: number; // 0 关 1 开
  idleTimeoutSeconds: number; // 会话无进展拆除阈值（秒），0＝关闭（默认）
  edgeIdleTimeoutSeconds: number; // 入口判死窗口（秒），0＝关闭（默认），推荐 ≥900
  backstopIdleTimeoutSeconds: number; // 中途兜底窗口（秒），0＝关闭（默认），推荐 ≥3600
  backstopTrickleBps: number; // 兜底滴流速率上限（字节/秒），0＝agent 默认（256）
}

export type NodeTab = "local";

export type NodeExpiryState =
  | "permanent"
  | "healthy"
  | "expiringSoon"
  | "expired";

export type NodeFilterMode = "all" | "expiringSoon" | "expired" | "withExpiry";
