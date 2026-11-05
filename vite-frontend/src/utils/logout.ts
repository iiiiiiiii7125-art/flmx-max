import axios from "axios";

import { clearSession, getToken } from "@/utils/session";

/**
 * 安全退出登录函数
 * E1-02：清本地态前先通知服务端登出（fire-and-forget）——服务端推进
 * 该用户的吊销水位，此前签发的全部 JWT（含被复制走的、已建立的
 * 实时通道会话）随即失效。调用失败（离线/服务端不可达）不阻塞本地
 * 退出，本地态照常清除。
 */
export const safeLogout = () => {
  const token = getToken();

  if (token) {
    void axios
      .post("user/logout", null, {
        headers: { Authorization: token },
        timeout: 3000,
      })
      .catch(() => {});
  }
  clearSession();
};
