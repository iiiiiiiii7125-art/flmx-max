import axios, { AxiosResponse } from "axios";
import toast from "react-hot-toast";

import {
  extractApiErrorMessage,
  isUnauthorizedError,
} from "@/api/error-message";
import { getPanelAddresses, isWebViewFunc } from "@/utils/panel";
import { clearSession, getToken } from "@/utils/session";

interface PanelAddress {
  name: string;
  address: string;
  inx: boolean;
}

const setPanelAddressesFunc = (newAddress: PanelAddress[]) => {
  newAddress.forEach((item) => {
    if (item.inx) {
      baseURL = `${item.address}/api/v1/`;
      axios.defaults.baseURL = baseURL;
    }
  });
};

function getWebViewPanelAddress() {
  (window as any).setAddresses = setPanelAddressesFunc;
  getPanelAddresses("setAddresses");
}

let baseURL: string = "";

export const reinitializeBaseURL = () => {
  if (isWebViewFunc()) {
    getWebViewPanelAddress();
  } else {
    baseURL = import.meta.env.VITE_API_BASE
      ? `${import.meta.env.VITE_API_BASE}/api/v1/`
      : "/api/v1/";
    axios.defaults.baseURL = baseURL;
  }
};

reinitializeBaseURL();

interface ApiResponse<T = unknown> {
  code: number;
  msg: string;
  data: T;
  /** Optional explicit operator warning (e.g. R7-J-01 plaintext install address); must be surfaced, never silently dropped. */
  warning?: string;
}

interface RequestOptions {
  timeout?: number;
  /** 静默请求：403 时不弹全局「权限不足」提示（后台探测类请求用）。 */
  silent?: boolean;
  /**
   * 取消信号，透传给 axios（F-115）：调用方可用 AbortController 取消
   * 在途请求，配合页面级 in-flight/序号守卫防止旧响应覆盖新状态。
   */
  signal?: AbortSignal;
  /**
   * R2X-004：创建表单的 Idempotency-Key（由 useIdemKey() 在表单层生成
   * 并复用）。Network 只透传请求头，禁止在此逐请求生成——每次新键
   * 会让后端幂等机制失效。
   */
  idemKey?: string;
}

// 处理token失效的逻辑
function handleTokenExpired() {
  clearSession();

  // 跳转到登录页面
  if (window.location.pathname !== "/") {
    window.location.href = "/";
  }
}

// N-003：403（权限不足）全局处理。业务信封 code=403 与 HTTP 403 均覆盖。
// 同一时间窗内只提示一次：页脚版本探测等后台请求在每个页面都会触发，
// 3 秒节流 + silent 静默选项避免普通用户被「权限不足」刷屏。
let lastForbiddenToastAt = 0;

function handleForbidden(
  response: ApiResponse<unknown> | null,
  httpStatus?: number,
  silent?: boolean,
  serverMsg?: string,
) {
  if (silent) return;
  if (response?.code === 403 || httpStatus === 403) {
    const now = Date.now();

    if (now - lastForbiddenToastAt < 3000) return;
    lastForbiddenToastAt = now;
    // F-118：优先透出后端 msg（如「请先修改默认管理员密码」），
    // 无具体原因时才回退通用文案，避免把受限改密等场景误标为权限问题。
    toast.error(response?.msg || serverMsg || "权限不足");
  }
}

// 检查响应是否为token失效。
// F-117：只以信封 code===401 判定，不再与后端三条中文文案逐字耦合
//（后端改文案或新增 401 文案时，旧实现会漏判、用户卡在死会话里）。
function isTokenExpired(response: ApiResponse<unknown>) {
  return !!response && response.code === 401;
}

const Network = {
  get: function <T = unknown>(
    path: string = "",
    data: unknown = {},
    options: RequestOptions = {},
  ): Promise<ApiResponse<T>> {
    return new Promise(function (resolve) {
      // 如果baseURL是默认值且是WebView环境，说明没有设置面板地址
      if (baseURL === "") {
        resolve({ code: -1, msg: " - 请先设置面板地址", data: null as T });

        return;
      }

      axios
        .get(path, {
          params: data,
          timeout: options.timeout ?? 30000,
          signal: options.signal,
          headers: {
            Authorization: getToken(),
          },
        })
        .then(function (response: AxiosResponse<ApiResponse<T>>) {
          // 检查是否token失效
          if (isTokenExpired(response.data)) {
            handleTokenExpired();
          }

          // N-003：业务信封 403 全局提示
          handleForbidden(response.data, undefined, options.silent);

          resolve(response.data);
        })
        .catch(function (error: unknown) {
          // F-115：调用方主动取消（AbortSignal）不作失败处理，不弹任何提示。
          if (axios.isCancel(error)) {
            resolve({ code: -1, msg: "请求已取消", data: null as T });

            return;
          }

          const errorMessage = extractApiErrorMessage(error);

          // 检查是否是401错误（token失效）
          if (isUnauthorizedError(error)) {
            handleTokenExpired();

            resolve({
              code: 401,
              msg: "未登录或token已过期",
              data: null as T,
            });

            return;
          }

          // N-003：HTTP 403 全局提示
          if (axios.isAxiosError(error) && error.response?.status === 403) {
            const payload = error.response.data as
              | { msg?: string }
              | undefined;

            handleForbidden(null, 403, options.silent, payload?.msg);
          }

          resolve({
            code: -1,
            msg: errorMessage,
            data: null as T,
          });
        });
    });
  },

  post: function <T = unknown>(
    path: string = "",
    data: unknown = {},
    options: RequestOptions = {},
  ): Promise<ApiResponse<T>> {
    return new Promise(function (resolve) {
      // 如果baseURL是默认值且是WebView环境，说明没有设置面板地址
      if (baseURL === "") {
        resolve({ code: -1, msg: " - 请先设置面板地址", data: null as T });

        return;
      }

      axios
        .post(path, data, {
          timeout: options.timeout ?? 30000,
          signal: options.signal,
          headers: {
            Authorization: getToken(),
            "Content-Type": "application/json",
            ...(options.idemKey
              ? { "Idempotency-Key": options.idemKey }
              : {}),
          },
        })
        .then(function (response: AxiosResponse<ApiResponse<T>>) {
          // 检查是否token失效
          if (isTokenExpired(response.data)) {
            handleTokenExpired();
          }

          // N-003：业务信封 403 全局提示
          handleForbidden(response.data, undefined, options.silent);

          resolve(response.data);
        })
        .catch(function (error: unknown) {
          // F-115：调用方主动取消（AbortSignal）不作失败处理，不弹任何提示。
          if (axios.isCancel(error)) {
            resolve({ code: -1, msg: "请求已取消", data: null as T });

            return;
          }

          const errorMessage = extractApiErrorMessage(error);

          // 检查是否是401错误（token失效）
          if (isUnauthorizedError(error)) {
            handleTokenExpired();

            resolve({
              code: 401,
              msg: "未登录或token已过期",
              data: null as T,
            });

            return;
          }

          // N-003：HTTP 403 全局提示
          if (axios.isAxiosError(error) && error.response?.status === 403) {
            const payload = error.response.data as
              | { msg?: string }
              | undefined;

            handleForbidden(null, 403, options.silent, payload?.msg);
          }

          resolve({
            code: -1,
            msg: errorMessage,
            data: null as T,
          });
        });
    });
  },
};

export default Network;
