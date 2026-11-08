export type UpdateReleaseChannel = "stable" | "dev";

export const UPDATE_CHANNEL_STORAGE_KEY = "update-release-channel";
export const UPDATE_CHANNEL_CHANGED_EVENT = "updateReleaseChannelChanged";

const CHANNEL_STABLE: UpdateReleaseChannel = "stable";
const CHANNEL_DEV: UpdateReleaseChannel = "dev";

const normalizeChannel = (
  value: string | null | undefined,
): UpdateReleaseChannel => {
  return value === CHANNEL_DEV ? CHANNEL_DEV : CHANNEL_STABLE;
};

// This fork only publishes stable releases, so the channel is fixed.
export const getUpdateReleaseChannel = (): UpdateReleaseChannel =>
  CHANNEL_STABLE;

export const setUpdateReleaseChannel = (
  channel: UpdateReleaseChannel,
): void => {
  if (typeof window === "undefined") {
    return;
  }

  localStorage.setItem(UPDATE_CHANNEL_STORAGE_KEY, normalizeChannel(channel));
  window.dispatchEvent(new Event(UPDATE_CHANNEL_CHANGED_EVENT));
};

// 严格版本解析，与后端 parseVersionParts（go-backend/internal/http/
// handler/system_upgrade.go）同口径：去首尾空白、剥一个可选的小写
// "v" 前缀后按 "." 切分，每段（去首尾空白）必须是纯数字非负整数；
// 含字母/连字符后缀（如 25.5.16-rc1）、空段、空串一律不可解析。
// 段长上限 18 位：后端用 Atoi 解析 int64，超长段同样报错。解析结果
// 归一为去前导零的数字串，比较走「先比长度、再比字典序」的整数
// 语义，避免 JS Number 在大整数段上丢精度。
// 与后端的一处有意差异（方向更严）：后端 Atoi 接受 "+3" 这类带符号
// 段，前端按不可解析处理——本函数只供降级提示，宁可不下结论，
// 绝不编造一个后端不会得出的大小关系（R2-P2-10）。
const parseVersionPartsStrict = (version: string): string[] | null => {
  let normalized = version.trim();

  if (normalized.startsWith("v")) {
    normalized = normalized.slice(1);
  }

  if (normalized === "") {
    return null;
  }

  const parts: string[] = [];

  for (const rawSegment of normalized.split(".")) {
    const segment = rawSegment.trim();

    if (!/^\d+$/.test(segment) || segment.length > 18) {
      return null;
    }

    parts.push(segment.replace(/^0+/, "") || "0");
  }

  return parts;
};

const compareDigitStrings = (left: string, right: string): number => {
  if (left.length !== right.length) {
    return left.length < right.length ? -1 : 1;
  }

  if (left === right) {
    return 0;
  }

  return left < right ? -1 : 1;
};

// 与后端 compareVersions 同口径：精确 "dev" 视为最低版本（先于严格
// 解析判定）；其余输入走严格解析，短段序列按 0 补齐比较。任一侧不
// 可解析时后端返回错误、执行面 fail-closed 拒绝升级；前端此处仅是
// 降级警告的提前提示，不可解析即返回 0（不下大小结论），真正的降级
// 拦截由后端复核兜底（R2-P2-10）。
export const compareVersions = (left: string, right: string): number => {
  if (left === "dev" && right === "dev") {
    return 0;
  }

  if (left === "dev") {
    return -1;
  }

  if (right === "dev") {
    return 1;
  }

  const a = parseVersionPartsStrict(left);
  const b = parseVersionPartsStrict(right);

  if (a === null || b === null) {
    return 0;
  }

  const maxLength = Math.max(a.length, b.length);

  for (let i = 0; i < maxLength; i += 1) {
    const cmp = compareDigitStrings(a[i] ?? "0", b[i] ?? "0");

    if (cmp !== 0) {
      return cmp;
    }
  }

  return 0;
};

// F-L5-05：原 getLatestVersionByChannel / hasVersionUpdate（浏览器直连
// GitHub 查版本）在 P3-3 改走后端 checkSystemUpgrade 后已无消费者，
// 整段删除，避免旧路径被误接回任一表面绕过后端代理/限流口径。版本
// 发现统一走后端，前端仅保留渠道与版本比较工具。
