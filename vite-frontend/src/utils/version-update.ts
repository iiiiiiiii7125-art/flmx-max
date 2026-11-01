export type UpdateReleaseChannel = "stable" | "dev";

export const UPDATE_CHANNEL_STORAGE_KEY = "update-release-channel";
export const UPDATE_CHANNEL_CHANGED_EVENT = "updateReleaseChannelChanged";

const CHANNEL_STABLE: UpdateReleaseChannel = "stable";
const CHANNEL_DEV: UpdateReleaseChannel = "dev";

const stableVersionPattern = /^\d+(?:\.\d+)+$/;

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

const normalizeTag = (tag: string): string => {
  return tag.trim().replace(/^v/i, "");
};

type VersionParts = {
  numbers: number[];
  stageRank: number;
  stageNumber: number;
};

const parseVersionParts = (version: string): VersionParts => {
  const normalized = normalizeTag(version).toLowerCase();
  const numberMatches = normalized.match(/\d+/g) || [];
  const numbers = numberMatches.map((item) => Number.parseInt(item, 10));

  let stageRank = 0;

  if (normalized.includes("rc")) {
    stageRank = 3;
  } else if (normalized.includes("beta")) {
    stageRank = 2;
  } else if (normalized.includes("alpha")) {
    stageRank = 1;
  } else if (stableVersionPattern.test(normalized)) {
    stageRank = 4;
  }

  const stageNumberMatch = normalized.match(/(?:alpha|beta|rc)[.-]?(\d+)/);
  const stageNumber = stageNumberMatch
    ? Number.parseInt(stageNumberMatch[1], 10)
    : 0;

  return {
    numbers,
    stageRank,
    stageNumber,
  };
};

export const compareVersions = (left: string, right: string): number => {
  const a = parseVersionParts(left);
  const b = parseVersionParts(right);
  const maxLength = Math.max(a.numbers.length, b.numbers.length);

  for (let i = 0; i < maxLength; i += 1) {
    const aValue = a.numbers[i] || 0;
    const bValue = b.numbers[i] || 0;

    if (aValue !== bValue) {
      return aValue - bValue;
    }
  }

  if (a.stageRank !== b.stageRank) {
    return a.stageRank - b.stageRank;
  }

  if (a.stageNumber !== b.stageNumber) {
    return a.stageNumber - b.stageNumber;
  }

  return 0;
};

// F-L5-05：原 getLatestVersionByChannel / hasVersionUpdate（浏览器直连
// GitHub 查版本）在 P3-3 改走后端 checkSystemUpgrade 后已无消费者，
// 整段删除，避免旧路径被误接回任一表面绕过后端代理/限流口径。版本
// 发现统一走后端，前端仅保留渠道与版本比较工具。
