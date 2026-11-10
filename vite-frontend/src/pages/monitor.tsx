import type { MonitorNodeApiItem } from "@/api/types";

import { useEffect, useMemo, useState } from "react";
import toast from "react-hot-toast";
import { Navigate } from "react-router-dom";
import { LayoutGrid, List } from "lucide-react";

import { AnimatedPage } from "@/components/animated-page";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { getMonitorAccess, getMonitorNodes } from "@/api";
import { MonitorView } from "@/pages/node/monitor-view";
import { TunnelMonitorView } from "@/pages/node/tunnel-monitor-view";
import { getAdminFlag } from "@/utils/session";

type MonitorNode = {
  id: number;
  name: string;
  connectionStatus: "online" | "offline";
  version?: string;
};

/**
 * 监控： admins have it under 节点 (each node's 监控) and 中转 (质量);
 * users allowed to see monitoring get this overview (DA-1).
 * AP-6：原 AdminMonitorOverview 管理员总览已删——管理员在 MonitorPage
 * 入口即重定向 /node，该总览自监控权限收窄后不可达（连带其英雄指标
 * 的假 0 与写死装饰柱，AP-3）。
 */
export default function MonitorPage() {
  return getAdminFlag() ? <Navigate replace to="/node" /> : <MonitorOverview />;
}

// 监控权限范围收窄（2026-11-01 用户拍板）后按 /monitor/access 分流：
// 管理员走原总览（节点＋服务监控＋隧道，行为与收窄前一致）；被授予
// 监控权限的普通用户只看自己隧道的监控（TunnelMonitorView，数据由
// 后端按可达隧道过滤），不渲染节点/服务区，也不再调用已收管理员的
// 端点与节点实时流；无权限用户显示授权提示（h5 布局的同名 toast
// 仍由 access 探针驱动，行为不变）。探针失败（异常或非成功响应）
// 同样落拒绝卡、不回落用户视图（R2-F1-03）：权限未证实时不挂载任何
// 监控数据面，不对受限面发请求，后端逐面 403 仍兜底。
function MonitorOverview() {
  const [accessState, setAccessState] = useState<
    "loading" | "admin" | "user" | "denied"
  >("loading");

  useEffect(() => {
    let cancelled = false;

    void (async () => {
      try {
        const res = await getMonitorAccess();

        if (cancelled) return;
        if (res.code === 0 && res.data) {
          setAccessState(
            res.data.admin ? "admin" : res.data.allowed ? "user" : "denied",
          );

          return;
        }
      } catch {
        // 探针失败落拒绝卡（R2-F1-03）：不回落用户视图。
      }
      if (!cancelled) setAccessState("denied");
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  if (accessState === "loading") return null;
  // AP-6：管理员在 MonitorPage 入口已重定向到 /node，本分支与原
  // AdminMonitorOverview 总览同为不可达死代码（一并删除）；探针异常
  // 报 admin 时同样按入口逻辑重定向，保持单一行为面。
  if (accessState === "admin") return <Navigate replace to="/node" />;
  if (accessState === "denied") {
    return (
      <AnimatedPage className="px-4 sm:px-6 lg:px-8 py-6 lg:py-8">
        <Card>
          <CardHeader>
            <h3 className="text-sm font-semibold">监控</h3>
          </CardHeader>
          <CardBody>
            <div className="text-sm text-foreground">
              暂无监控权限，请联系管理员授权
            </div>
          </CardBody>
        </Card>
      </AnimatedPage>
    );
  }

  return <UserTunnelMonitorOverview />;
}

function UserTunnelMonitorOverview() {
  const [viewMode, setViewMode] = useState<"list" | "grid">("list");

  return (
    <AnimatedPage className="px-4 sm:px-6 lg:px-8 py-6 lg:py-8">
      <div className="mb-6 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0 rounded-xl glass-card px-3 py-2">
            <h2 className="text-xl font-semibold truncate">监控面板</h2>
          </div>
          <Button
            isIconOnly
            size="sm"
            variant="flat"
            onPress={() => setViewMode(viewMode === "list" ? "grid" : "list")}
          >
            {viewMode === "list" ? (
              <LayoutGrid className="w-4 h-4" />
            ) : (
              <List className="w-4 h-4" />
            )}
          </Button>
        </div>
      </div>
      <TunnelMonitorView viewMode={viewMode} />
    </AnimatedPage>
  );
}

/**
 * One node's monitoring, opened from the node list (DA-1): live
 * figures, history charts and its service monitors, without the
 * overview. Rendered by the node hub in place of the list.
 */
export function NodeMonitorDetail({
  nodeId,
  onBack,
}: {
  nodeId: number;
  onBack: () => void;
}) {
  const [nodes, setNodes] = useState<MonitorNodeApiItem[]>([]);
  // Same R2X-038 access passthrough as the overview.
  const [canWrite, setCanWrite] = useState(false);
  // R2-C3-01（IRL-3 同族）：节点列表初载失败必须留持久标记＋重试入口，
  // 不得再按「暂无节点」空态渲染（与 node.tsx nodesLoadFailed 同款纪律）。
  const [nodesLoadFailed, setNodesLoadFailed] = useState(false);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    let active = true;

    (async () => {
      try {
        const res = await getMonitorNodes();

        if (!active) return;
        if (res.code === 0 && Array.isArray(res.data)) {
          setNodes(res.data);
          setNodesLoadFailed(false);
        } else {
          setNodesLoadFailed(true);
          toast.error(res.msg || "加载节点失败");
        }
      } catch {
        if (active) {
          setNodesLoadFailed(true);
          toast.error("加载节点失败");
        }
      }
      try {
        const res = await getMonitorAccess();

        if (active && res.code === 0 && res.data) {
          setCanWrite(Boolean(res.data.admin));
        }
      } catch {
        // 查询失败保持只读呈现（后端仍以 403 兜底写入口）。
      }
    })();

    return () => {
      active = false;
    };
  }, [reloadKey]);

  const nodeMap = useMemo(
    () =>
      new Map<number, MonitorNode>(
        nodes
          .filter((n) => Number(n.id) > 0)
          .map((n) => [
            Number(n.id),
            {
              id: Number(n.id),
              name: String(n.name ?? ""),
              connectionStatus: (n.status === 1
                ? "online"
                : "offline") as MonitorNode["connectionStatus"],
              version: n.version,
            },
          ]),
      ),
    [nodes],
  );

  return (
    <>
      {nodesLoadFailed && (
        <div className="mb-4 flex flex-wrap items-center gap-2 rounded-xl bg-danger-50 px-3 py-2">
          <p className="text-xs font-semibold text-danger-700">
            节点列表加载失败，当前显示的节点可能不完整或为空，不是真实状态。
          </p>
          <Button
            color="danger"
            size="sm"
            variant="flat"
            onPress={() => setReloadKey((k) => k + 1)}
          >
            重新加载
          </Button>
        </div>
      )}
      <MonitorView
        canWrite={canWrite}
        focusNodeId={nodeId}
        nodeMap={nodeMap}
        nodesLoadFailed={nodesLoadFailed}
        onBack={onBack}
      />
    </>
  );
}
