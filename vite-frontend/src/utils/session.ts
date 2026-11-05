import { purgeSensitiveConfigCache } from "@/config/sensitive-keys";

import {
  getRoleIdFromToken,
  getUserIdFromToken,
  getUsernameFromToken,
  isTokenValid,
} from "./jwt";

// N-002：角色/管理员标志不再单独写入 localStorage（role_id/admin 键可被
// 控制台任意篡改，前端 UI 门控会因此失真）。统一从 JWT 解析：
// token 是唯一事实来源，localStorage 只存 token 本身与显示用 name。
export const SESSION_STORAGE_KEYS = {
  token: "token",
  name: "name",
} as const;

// 旧版本写入的键，仅用于登录/登出时清理残留。
const LEGACY_SESSION_KEYS = ["role_id", "admin"] as const;

export interface SessionData {
  token: string | null;
  roleId: number | null;
  name: string | null;
  isAdmin: boolean;
}

export interface LoginSessionPayload {
  token: string;
  role_id: number;
  name: string;
}

const SESSION_EVENT_NAME = "sessionUpdated";

// E1-05：登出专用事件——config/site.ts 监听它复位配置复验状态
// （configVerifiedAt/configFetchInFlight），避免同标签页换账号后
// 60 秒内吃上一会话的全局配置缓存。事件名由本模块导出，site.ts
// 只反向引用常量（其依赖均为叶子模块，不成环）。
export const SESSION_CLEARED_EVENT = "sessionCleared";

// 与 pages/config/gist-backup.tsx 的 GIST_RESTORE_RECEIPT_KEY 同值：
// Gist 恢复回执暂存 sessionStorage，登出时须一并清除，不留给下一
// 个登录本标签页的账号（gist-backup.tsx 侧有同指注释）。
const GIST_RESTORE_RECEIPT_KEY = "flmx-gist-restore-receipt";

export const getToken = (): string | null => {
  return localStorage.getItem(SESSION_STORAGE_KEYS.token);
};

export const getRoleId = (): number | null => {
  const token = getToken();

  if (!token || !isTokenValid(token)) {
    return null;
  }

  return getRoleIdFromToken(token);
};

export const getUserId = (): number | null => {
  const token = getToken();

  if (!token || !isTokenValid(token)) {
    return null;
  }

  return getUserIdFromToken(token);
};

export const getSessionName = (): string | null => {
  const token = getToken();
  const nameFromToken = token ? getUsernameFromToken(token) : null;

  if (nameFromToken) {
    return nameFromToken;
  }

  return localStorage.getItem(SESSION_STORAGE_KEYS.name);
};

export const getAdminFlag = (): boolean => {
  // N-002：从 token 解析，不再读可篡改的 admin/role_id 键。
  return getRoleId() === 0;
};

export const readSession = (): SessionData => {
  return {
    token: getToken(),
    roleId: getRoleId(),
    name: getSessionName(),
    isAdmin: getAdminFlag(),
  };
};

const purgeLegacySessionKeys = (): void => {
  LEGACY_SESSION_KEYS.forEach((key) => localStorage.removeItem(key));
};

export const writeLoginSession = (payload: LoginSessionPayload): void => {
  localStorage.setItem(SESSION_STORAGE_KEYS.token, payload.token);
  localStorage.setItem(SESSION_STORAGE_KEYS.name, payload.name);
  // N-002：不再写入 role_id/admin；顺手清除旧残留，防止被篡改值继续生效。
  purgeLegacySessionKeys();
  window.dispatchEvent(new Event(SESSION_EVENT_NAME));
};

export const clearSession = (): void => {
  localStorage.removeItem(SESSION_STORAGE_KEYS.token);
  localStorage.removeItem(SESSION_STORAGE_KEYS.name);
  purgeLegacySessionKeys();
  // F-047：登出（含 401 强制登出）时一并清掉敏感配置的本地缓存，
  // 避免 jwt_secret / 通知令牌等在共享浏览器上残留到下次登录前。
  purgeSensitiveConfigCache();
  // E1-05：会话级残留一并清——Gist 恢复回执（sessionStorage）删除，
  // 并广播 SESSION_CLEARED_EVENT 让 site.ts 复位配置复验状态，
  // 换账号后不再命中上一会话的 60 秒缓存窗口。
  try {
    sessionStorage.removeItem(GIST_RESTORE_RECEIPT_KEY);
  } catch {
    // sessionStorage 不可用时静默：回执清除让位于登出可用性。
  }
  window.dispatchEvent(new Event(SESSION_CLEARED_EVENT));
  window.dispatchEvent(new Event(SESSION_EVENT_NAME));
};

export const SESSION_UPDATED_EVENT = SESSION_EVENT_NAME;
