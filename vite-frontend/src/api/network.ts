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
}

interface RequestOptions {
  timeout?: number;
  /** 静默请求：403 时不弹全局「权限不足」提示（后台探测类请求用）。 */
  silent?: boolean;
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
) {
  if (silent) return;
  if (response?.code === 403 || httpStatus === 403) {
    const now = Date.now();

    if (now - lastForbiddenToastAt < 3000) return;
    lastForbiddenToastAt = now;
    toast.error("权限不足");
  }
}

// 检查响应是否为token失效
function isTokenExpired(response: ApiResponse<unknown>) {
  return (
    response &&
    response.code === 401 &&
    (response.msg === "未登录或token已过期" ||
      response.msg === "无效的token或token已过期" ||
      response.msg === "无法获取用户权限信息")
  );
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
            handleForbidden(null, 403, options.silent);
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
          headers: {
            Authorization: getToken(),
            "Content-Type": "application/json",
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
            handleForbidden(null, 403, options.silent);
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
