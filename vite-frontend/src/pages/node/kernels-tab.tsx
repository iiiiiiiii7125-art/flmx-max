import type {
  KernelApiItem,
  KernelCapabilitiesApiData,
  NodeApiItem,
} from "@/api/types";

import { useCallback, useEffect, useRef, useState } from "react";

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
    <KernelPanel
      caps={caps}
      installRequest={installRequest}
      kernels={kernels}
      nodes={nodes}
      onChanged={load}
      onInstallRequestHandled={onInstallRequestHandled}
    />
  );
}
