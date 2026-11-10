import type {
  KernelApiItem,
  KernelCapabilitiesApiData,
  NodeApiItem,
} from "@/api/types";

import { useCallback, useEffect, useRef, useState } from "react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { getKernelCapabilities, getKernelList, getNodeList } from "@/api";
import { PageLoadingState } from "@/components/page-state";
import { type InstallRequest, KernelPanel } from "@/pages/inbound/kernel-panel";

/** The kernels installed on each node, under 节点 → 内核. */
export function KernelsTab({
  installRequest,
  onInstallRequestHandled,
}: {
  installRequest: InstallRequest | null;
  onInstallRequestHandled: () => void;
}) {
  const [loading, setLoading] = useState(true);
  const [caps, setCaps] = useState<KernelCapabilitiesApiData | null>(null);
  const [nodes, setNodes] = useState<NodeApiItem[]>([]);
  const [kernels, setKernels] = useState<KernelApiItem[]>([]);
  // IRL-3 新成员（R2 加载点普查补入）：内核 tab 三路初载任一失败都
  // 不得落到「还没有安装内核」假空——置位后页级横幅与空状态改判。
  const [loadFailed, setLoadFailed] = useState(false);

  // 在途跳过（X-P3-8/K-02，照 monitor-view loadServiceMonitors 既有
  // 写法）：30s 轮询下慢请求未返回时跳过本轮，避免请求叠加与旧响应覆盖。
  const loadInFlightRef = useRef(false);

  const load = useCallback(async () => {
    if (loadInFlightRef.current) {
      return;
    }
    loadInFlightRef.current = true;
    try {
      const [capsRes, nodeRes, kernelRes] = await Promise.all([
        getKernelCapabilities(),
        getNodeList(),
        getKernelList(),
      ]);

      if (capsRes.code === 0) setCaps(capsRes.data);
      if (nodeRes.code === 0) setNodes(nodeRes.data || []);
      if (kernelRes.code === 0) setKernels(kernelRes.data || []);
      // 三路全成才清错；任一失败置错，已有数据时轮询失败只置错不清空。
      setLoadFailed(
        capsRes.code !== 0 || nodeRes.code !== 0 || kernelRes.code !== 0,
      );
    } catch {
      setLoadFailed(true);
    } finally {
      loadInFlightRef.current = false;
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
    const timer = window.setInterval(load, 30000);

    return () => window.clearInterval(timer);
  }, [load]);

  if (loading) return <PageLoadingState message="正在加载..." />;

  return (
    <div className="space-y-4">
      {loadFailed && (
        <div className="flex flex-wrap items-center gap-2 rounded-xl bg-danger-50 px-3 py-2">
          <p className="text-xs font-semibold text-danger-700">
            部分数据加载失败（内核/节点/能力其一未取到），页面显示的可能是空或不完整数据，不是真实状态。
          </p>
          <Button
            color="danger"
            size="sm"
            variant="flat"
            onPress={() => load()}
          >
            重新加载
          </Button>
        </div>
      )}
      <KernelPanel
        caps={caps}
        installRequest={installRequest}
        kernels={kernels}
        loadFailed={loadFailed}
        nodes={nodes}
        onChanged={load}
        onInstallRequestHandled={onInstallRequestHandled}
      />
    </div>
  );
}
