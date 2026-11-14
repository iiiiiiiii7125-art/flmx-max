import type { ForwardApiItem } from "@/api/types";

import { useCallback, useEffect, useRef, useState } from "react";
import toast from "react-hot-toast";

import { getUserPackageInfo, type AnnouncementData } from "@/api";
import { getAdminFlag } from "@/utils/session";
import { useAnnouncement } from "@/pages/dashboard/use-announcement";

export interface DashboardUserInfo {
  flow: number;
  flowMiB?: number;
  inFlow: number;
  outFlow: number;
  num: number;
  /** 后端 int64 毫秒；旧声明 string | number 曾掩盖 types.ts 的错误（F-141）。 */
  expTime?: number;
  flowResetTime?: number;
}

export interface DashboardUserTunnel {
  id: number;
  tunnelId: number;
  tunnelName: string;
  flow: number;
  flowMiB?: number;
  inFlow: number;
  outFlow: number;
  num: number;
  expTime?: number;
  flowResetTime?: number;
  tunnelFlow?: number;
}

export interface DashboardForward {
  id: number;
  name: string;
  tunnelId: number;
  tunnelName: string;
  inIp: string;
  inPort: number;
  remoteAddr: string;
  inFlow: number;
  outFlow: number;
}

export interface DashboardStatisticsFlow {
  id: number;
  userId: number;
  flow: number;
  totalFlow: number;
  time: string;
}

// AP-10：原 DashboardNodeExpiryItem／normalizeNodeExpiryReminders／
// loadNodeExpiryData 已删——后端 dashboardNodeExpiryList 恒 404，
// 该路径恒失败后置空、其仅供的到期提醒卡结构性永不渲染，同为死代码。

const DASHBOARD_POLL_INTERVAL_MS = 5000;
const EXPIRATION_NOTIFICATION_STORAGE_KEY =
  "dashboard:last-expiration-notification";

const buildExpirationNotificationKey = (
  userInfo: DashboardUserInfo,
  tunnels: DashboardUserTunnel[],
) => {
  const userExpTime = userInfo.expTime ?? "permanent";
  const tunnelExpirationKey = [...tunnels]
    .map((tunnel) => `${tunnel.tunnelId}:${tunnel.expTime ?? "permanent"}`)
    .sort()
    .join("|");

  return `user:${userExpTime};tunnels:${tunnelExpirationKey}`;
};

interface DashboardDataState {
  loading: boolean;
  userInfo: DashboardUserInfo;
  userTunnels: DashboardUserTunnel[];
  forwardList: DashboardForward[];
  statisticsFlows: DashboardStatisticsFlow[];
  isAdmin: boolean;
  announcement: AnnouncementData | null;
  isAnnouncementModalOpen: boolean;
  setIsAnnouncementModalOpen: (isOpen: boolean) => void;
  dismissAnnouncementModal: () => void;
}

const checkExpirationNotifications = (
  userInfo: DashboardUserInfo,
  tunnels: DashboardUserTunnel[],
) => {
  const notificationKey = buildExpirationNotificationKey(userInfo, tunnels);
  const lastNotified = localStorage.getItem(
    EXPIRATION_NOTIFICATION_STORAGE_KEY,
  );

  if (lastNotified === notificationKey) {
    return;
  }

  let hasNotification = false;

  if (userInfo.expTime) {
    const expDate = new Date(userInfo.expTime);
    const now = new Date();

    if (!isNaN(expDate.getTime())) {
      // 已过期必须先于「未过期」判断：旧写法外层 expDate > now 把
      // diffDays<=0 的已过期分支变成永不可达的死代码
      if (expDate <= now) {
        hasNotification = true;
        toast("账户已过期，请立即续费", {
          icon: "⚠️",
          duration: 8000,
          style: { background: "#991b1b", color: "#fff" },
        });
      } else {
        const diffTime = expDate.getTime() - now.getTime();
        const diffDays = Math.ceil(diffTime / (1000 * 60 * 60 * 24));

        if (diffDays <= 7 && diffDays > 0) {
          hasNotification = true;
          if (diffDays === 1) {
            toast("账户将于明天过期，请及时续费", {
              icon: "⚠️",
              duration: 6000,
              style: { background: "#f59e0b", color: "#451a03" },
            });
          } else {
            toast(`账户将于${diffDays}天后过期，请及时续费`, {
              icon: "⚠️",
              duration: 6000,
              style: { background: "#f59e0b", color: "#451a03" },
            });
          }
        }
      }
    }
  }

  tunnels.forEach((tunnel) => {
    if (!tunnel.expTime) {
      return;
    }

    const expDate = new Date(tunnel.expTime);
    const now = new Date();

    if (!isNaN(expDate.getTime())) {
      // 同账户段：已过期先行判断，避免被 expDate > now 外层守卫吞掉
      if (expDate <= now) {
        hasNotification = true;
        toast(`隧道"${tunnel.tunnelName}"已过期`, {
          icon: "⚠️",
          duration: 6000,
          style: { background: "#991b1b", color: "#fff" },
        });
      } else {
        const diffTime = expDate.getTime() - now.getTime();
        const diffDays = Math.ceil(diffTime / (1000 * 60 * 60 * 24));

        if (diffDays <= 7 && diffDays > 0) {
          hasNotification = true;
          if (diffDays === 1) {
            toast(`隧道"${tunnel.tunnelName}"将于明天过期`, {
              icon: "⚠️",
              duration: 5000,
              style: { background: "#f59e0b", color: "#451a03" },
            });
          } else {
            toast(`隧道"${tunnel.tunnelName}"将于${diffDays}天后过期`, {
              icon: "⚠️",
              duration: 5000,
              style: { background: "#f59e0b", color: "#451a03" },
            });
          }
        }
      }
    }
  });

  if (hasNotification) {
    localStorage.setItem(EXPIRATION_NOTIFICATION_STORAGE_KEY, notificationKey);
  }
};

const normalizeForwards = (items: ForwardApiItem[]) => {
  return (items || []).map((item) => ({
    ...item,
    name: item.name || "",
    tunnelId: item.tunnelId ?? 0,
    tunnelName: item.tunnelName || "",
    inIp: item.inIp || "",
    inPort: item.inPort || 0,
    remoteAddr: item.remoteAddr || "",
    inFlow: item.inFlow || 0,
    outFlow: item.outFlow || 0,
  }));
};

const normalizeTunnelPermissions = (items: DashboardUserTunnel[]) => {
  return (items || []).map((item) => ({
    ...item,
    inFlow: item.inFlow ?? 0,
    outFlow: item.outFlow ?? 0,
  }));
};

export const useDashboardData = (): DashboardDataState => {
  const [loading, setLoading] = useState(true);
  const [userInfo, setUserInfo] = useState<DashboardUserInfo>(
    {} as DashboardUserInfo,
  );
  const [userTunnels, setUserTunnels] = useState<DashboardUserTunnel[]>([]);
  const [forwardList, setForwardList] = useState<DashboardForward[]>([]);
  const [statisticsFlows, setStatisticsFlows] = useState<
    DashboardStatisticsFlow[]
  >([]);
  const [isAdmin, setIsAdmin] = useState(false);
  // 公告横幅/弹窗：与管理员总览共用同一份逻辑（use-announcement.ts），
  // U1-R2B3-01 抽出前这里曾内联同一段代码。
  const announcementState = useAnnouncement();
  const isMountedRef = useRef(true);
  const packageRequestInFlightRef = useRef(false);

  const applyPackageData = useCallback(
    (data: {
      userInfo?: DashboardUserInfo;
      tunnelPermissions?: DashboardUserTunnel[];
      forwards?: ForwardApiItem[];
      statisticsFlows?: DashboardStatisticsFlow[];
    }) => {
      const normalizedTunnelPermissions = normalizeTunnelPermissions(
        data.tunnelPermissions || [],
      );
      const normalizedForwards = normalizeForwards(data.forwards || []);

      if (!isMountedRef.current) {
        return;
      }

      setUserInfo(data.userInfo || ({} as DashboardUserInfo));
      setUserTunnels(normalizedTunnelPermissions);
      setForwardList(normalizedForwards);
      setStatisticsFlows(data.statisticsFlows || []);

      checkExpirationNotifications(
        data.userInfo || ({} as DashboardUserInfo),
        normalizedTunnelPermissions,
      );
    },
    [],
  );

  const loadPackageData = useCallback(
    async ({ silent = false, notifyOnError = false } = {}) => {
      if (packageRequestInFlightRef.current) {
        return;
      }

      packageRequestInFlightRef.current = true;

      if (!silent && isMountedRef.current) {
        setLoading(true);
      }

      try {
        const res = await getUserPackageInfo();

        if (res.code === 0) {
          applyPackageData(res.data || {});
        } else if (notifyOnError) {
          toast.error(res.msg || "获取套餐信息失败");
        }
      } catch {
        if (notifyOnError) {
          toast.error("获取套餐信息失败");
        }
      } finally {
        packageRequestInFlightRef.current = false;

        if (!silent && isMountedRef.current) {
          setLoading(false);
        }
      }
    },
    [applyPackageData],
  );

  useEffect(() => {
    isMountedRef.current = true;
    const adminFlag = getAdminFlag();

    setIsAdmin(adminFlag);

    void loadPackageData({ notifyOnError: true });

    return () => {
      isMountedRef.current = false;
    };
  }, [loadPackageData]);

  useEffect(() => {
    if (typeof document === "undefined") {
      return;
    }

    const handleVisibilityChange = () => {
      if (document.visibilityState === "visible") {
        void loadPackageData({ silent: true });
      }
    };

    const interval = window.setInterval(() => {
      if (document.visibilityState !== "visible") {
        return;
      }

      void loadPackageData({ silent: true });
    }, DASHBOARD_POLL_INTERVAL_MS);

    document.addEventListener("visibilitychange", handleVisibilityChange);

    return () => {
      window.clearInterval(interval);
      document.removeEventListener("visibilitychange", handleVisibilityChange);
    };
  }, [loadPackageData]);

  return {
    loading,
    userInfo,
    userTunnels,
    forwardList,
    statisticsFlows,
    isAdmin,
    ...announcementState,
  };
};
