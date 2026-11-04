import {
  Route,
  Routes,
  useLocation,
  useNavigate,
  Navigate,
} from "react-router-dom";
import { lazy, Suspense, useEffect } from "react";
import { AnimatePresence } from "framer-motion";

import IndexPage from "@/pages/index";
import AdminLayout from "@/layouts/admin";
import H5Layout from "@/layouts/h5";
import H5SimpleLayout from "@/layouts/h5-simple";
import { isAdmin, isLoggedIn } from "@/utils/auth";
import { siteConfig, updateSiteConfig } from "@/config/site";
import { useH5Mode } from "@/hooks/useH5Mode";
import { SESSION_UPDATED_EVENT } from "@/utils/session";
import { useThemeContext } from "@/themes/context";
import { PageLoadingState } from "@/components/page-state";

// Pages load on first visit so the login screen does not wait for all of them.
const loadChangePasswordPage = () => import("@/pages/change-password");
const loadDashboardPage = () => import("@/pages/dashboard");
const loadMonitorPage = () => import("@/pages/monitor");
const loadMySubscriptionPage = () => import("@/pages/my-subscription");
const loadForwardPage = () => import("@/pages/forward");
const loadTunnelPage = () => import("@/pages/tunnel");
const loadNodeHubPage = () => import("@/pages/node-hub");
const loadUserHubPage = () => import("@/pages/user-hub");
const loadProfilePage = () => import("@/pages/profile");
const loadInboundPage = () => import("@/pages/inbound");
const loadRelayPage = () => import("@/pages/relay");
const loadSubscriptionPage = () => import("@/pages/subscription");
const loadConfigPage = () => import("@/pages/config");
const loadPanelSharingPage = () => import("@/pages/panel-sharing");

const ChangePasswordPage = lazy(loadChangePasswordPage);
const DashboardPage = lazy(loadDashboardPage);
const MonitorPage = lazy(loadMonitorPage);
const MySubscriptionPage = lazy(loadMySubscriptionPage);
const ForwardPage = lazy(loadForwardPage);
const TunnelPage = lazy(loadTunnelPage);
const NodeHubPage = lazy(loadNodeHubPage);
const UserHubPage = lazy(loadUserHubPage);
const ProfilePage = lazy(loadProfilePage);
const InboundPage = lazy(loadInboundPage);
const RelayPage = lazy(loadRelayPage);
const SubscriptionPage = lazy(loadSubscriptionPage);
const ConfigPage = lazy(loadConfigPage);
const PanelSharingPage = lazy(loadPanelSharingPage);

// After login, warm the remaining route chunks while the browser is idle
// so the first navigation to each page does not wait on a download. The
// loaders are the very same dynamic imports the lazy routes use, so a
// warmed chunk is never fetched twice. Skipped on data-saver connections.
const COMMON_ROUTE_LOADERS = [
  loadDashboardPage,
  loadMonitorPage,
  loadMySubscriptionPage,
  loadForwardPage,
  loadProfilePage,
  loadChangePasswordPage,
];
const ADMIN_ROUTE_LOADERS = [
  loadTunnelPage,
  loadNodeHubPage,
  loadUserHubPage,
  loadInboundPage,
  loadRelayPage,
  loadSubscriptionPage,
  loadConfigPage,
  loadPanelSharingPage,
];

let commonRoutePreloadScheduled = false;
let adminRoutePreloadScheduled = false;

const scheduleRoutePreload = () => {
  const connection = (
    navigator as Navigator & { connection?: { saveData?: boolean } }
  ).connection;

  if (connection?.saveData) {
    return;
  }

  const schedule = (loaders: Array<() => Promise<unknown>>) => {
    const run = () => {
      loaders.forEach((load) => {
        void load().catch(() => undefined);
      });
    };
    const requestIdle = (
      window as Window & {
        requestIdleCallback?: (
          callback: () => void,
          options?: { timeout: number },
        ) => number;
      }
    ).requestIdleCallback;

    if (requestIdle) {
      window.setTimeout(() => requestIdle(run, { timeout: 4000 }), 2000);
    } else {
      window.setTimeout(run, 2500);
    }
  };

  if (!commonRoutePreloadScheduled) {
    commonRoutePreloadScheduled = true;
    schedule(COMMON_ROUTE_LOADERS);
  }

  if (isAdmin() && !adminRoutePreloadScheduled) {
    adminRoutePreloadScheduled = true;
    schedule(ADMIN_ROUTE_LOADERS);
  }
};

const ProtectedRoute = ({
  children,
  useSimpleLayout = false,
  skipLayout = false,
  adminOnly = false,
}: {
  children: React.ReactNode;
  useSimpleLayout?: boolean;
  skipLayout?: boolean;
  adminOnly?: boolean;
}) => {
  const isH5 = useH5Mode();
  const authenticated = isLoggedIn();

  if (!authenticated) {
    return <Navigate replace to="/" />;
  }

  if (adminOnly && !isAdmin()) {
    return <Navigate replace to="/dashboard" />;
  }

  // 如果跳过布局，直接返回子组件
  const page = (
    <Suspense fallback={<PageLoadingState message="正在加载..." />}>
      {children}
    </Suspense>
  );

  if (skipLayout) {
    return page;
  }

  // 根据模式和页面类型选择布局
  const Layout =
    isH5 && useSimpleLayout ? H5SimpleLayout : isH5 ? H5Layout : AdminLayout;

  return <Layout>{page}</Layout>;
};

// 登录页面路由组件 - 已登录则重定向到dashboard
const LoginRoute = () => {
  const authenticated = isLoggedIn();
  const navigate = useNavigate();

  useEffect(() => {
    if (authenticated) {
      // 使用 React Router 导航，避免无限跳转
      navigate("/dashboard", { replace: true });
    }
  }, [authenticated, navigate]);

  if (authenticated) {
    return <Navigate replace to="/dashboard" />;
  }

  return <IndexPage />;
};

function App() {
  const location = useLocation();
  const navigate = useNavigate();
  const { effectiveMode } = useThemeContext();

  // 全局登录状态监听，当检测到未登录且不在首页时，跳转到首页
  useEffect(() => {
    const handleSessionUpdate = () => {
      if (!isLoggedIn() && location.pathname !== "/") {
        navigate("/", { replace: true });
      }
    };

    window.addEventListener(SESSION_UPDATED_EVENT, handleSessionUpdate);

    return () => {
      window.removeEventListener(SESSION_UPDATED_EVENT, handleSessionUpdate);
    };
  }, [location.pathname, navigate]);

  // 登录后空闲预加载其余路由 chunk（见 scheduleRoutePreload）：挂载时已
  // 登录（刷新/直达）与会话更新为已登录（刚登录成功）两种入口都覆盖。
  useEffect(() => {
    if (isLoggedIn()) {
      scheduleRoutePreload();
    }

    const handlePreloadSessionUpdate = () => {
      if (isLoggedIn()) {
        scheduleRoutePreload();
      }
    };

    window.addEventListener(
      SESSION_UPDATED_EVENT,
      handlePreloadSessionUpdate,
    );

    return () => {
      window.removeEventListener(
        SESSION_UPDATED_EVENT,
        handlePreloadSessionUpdate,
      );
    };
  }, []);

  // 处理自定义背景图片
  useEffect(() => {
    const updateBg = () => {
      // 登录页使用独立壁纸，留空则跟随主界面
      const isLoginPage = location.pathname === "/";
      let customBg: string;
      if (isLoginPage && siteConfig.login_bg_image) {
        customBg = siteConfig.login_bg_image;
      } else {
        customBg = siteConfig.app_bg_image_light || siteConfig.app_bg_image;
      }

      const root = document.documentElement;
      const isImage =
        customBg.startsWith("http") ||
        customBg.startsWith("data:") ||
        customBg.startsWith("/") ||
        customBg.startsWith("blob:");
      // "" and "theme" are the default plain background; "wallpaper" is
      // the built-in picture; anything else is an image URL or a colour.
      const mode =
        !customBg || customBg === "theme"
          ? "plain"
          : customBg === "wallpaper"
            ? "wallpaper"
            : "custom";

      root.classList.toggle("has-wallpaper-bg", mode === "wallpaper");
      root.classList.toggle("has-custom-bg", mode === "custom");
      root.classList.remove("has-theme-bg");
      if (mode === "custom") {
        root.style.setProperty(
          "--custom-bg-image",
          isImage ? `url(${customBg})` : "none",
        );
        root.style.setProperty(
          "--custom-bg-color",
          isImage ? "transparent" : customBg,
        );
      } else {
        root.style.removeProperty("--custom-bg-image");
        root.style.removeProperty("--custom-bg-color");
      }
    };

    updateBg();
    window.addEventListener("site-config-updated", updateBg);

    return () => {
      window.removeEventListener("site-config-updated", updateBg);
    };
  }, [effectiveMode, location.pathname]);

  // 立即设置页面标题（使用已从缓存读取的配置）
  useEffect(() => {
    document.title = siteConfig.name;

    void updateSiteConfig();

    const handleConfigUpdate = () => {
      void updateSiteConfig();
    };

    window.addEventListener("configUpdated", handleConfigUpdate);

    return () => {
      window.removeEventListener("configUpdated", handleConfigUpdate);
    };
  }, []);

  return (
    <AnimatePresence mode="wait">
      <Routes key={location.pathname} location={location}>
        <Route element={<LoginRoute />} path="/" />
        <Route
          element={
            <ProtectedRoute skipLayout={true}>
              <ChangePasswordPage />
            </ProtectedRoute>
          }
          path="/change-password"
        />
        <Route
          element={
            <ProtectedRoute>
              <DashboardPage />
            </ProtectedRoute>
          }
          path="/dashboard"
        />
        <Route
          element={
            <ProtectedRoute>
              <MonitorPage />
            </ProtectedRoute>
          }
          path="/monitor"
        />
        <Route
          element={
            <ProtectedRoute>
              <MySubscriptionPage />
            </ProtectedRoute>
          }
          path="/my-subscription"
        />
        <Route
          element={
            <ProtectedRoute>
              <ForwardPage />
            </ProtectedRoute>
          }
          path="/forward"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true}>
              <TunnelPage />
            </ProtectedRoute>
          }
          path="/tunnel"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true}>
              <NodeHubPage />
            </ProtectedRoute>
          }
          path="/node"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true} useSimpleLayout={true}>
              <UserHubPage />
            </ProtectedRoute>
          }
          path="/user"
        />
        <Route
          element={
            <ProtectedRoute>
              <ProfilePage />
            </ProtectedRoute>
          }
          path="/profile"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true} useSimpleLayout={true}>
              <UserHubPage />
            </ProtectedRoute>
          }
          path="/limit"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true}>
              <InboundPage />
            </ProtectedRoute>
          }
          path="/inbound"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true}>
              <RelayPage />
            </ProtectedRoute>
          }
          path="/relay"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true}>
              <SubscriptionPage />
            </ProtectedRoute>
          }
          path="/subscription"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true}>
              <ConfigPage />
            </ProtectedRoute>
          }
          path="/config"
        />
        <Route
          element={
            <ProtectedRoute adminOnly={true} useSimpleLayout={true}>
              <PanelSharingPage />
            </ProtectedRoute>
          }
          path="/panel-sharing"
        />
        {/* 兜底：未知路径回仪表盘，避免落进受保护布局里渲染空白内容区 */}
        <Route element={<Navigate replace to="/dashboard" />} path="*" />
      </Routes>
    </AnimatePresence>
  );
}

export default App;
