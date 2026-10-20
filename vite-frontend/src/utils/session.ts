import { purgeSensitiveConfigCache } from "@/config/sensitive-keys";

import { getRoleIdFromToken, getUsernameFromToken, isTokenValid } from "./jwt";

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
  window.dispatchEvent(new Event(SESSION_EVENT_NAME));
};

export const SESSION_UPDATED_EVENT = SESSION_EVENT_NAME;
