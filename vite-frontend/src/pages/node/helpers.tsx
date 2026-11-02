import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import {
  getNodeRenewalSnapshot,
  type NodeRenewalCycle,
} from "@/pages/node/renewal";
import { Node, NodeExpiryState } from "@/pages/node/types";
import { formatTraffic } from "@/utils/traffic";

/** Expiry, realtime-merge and sortable-row helpers for the node page (no React state). */
export const NODE_FALLBACK_REFRESH_INTERVAL_MS = 15000;

export const EXPIRING_SOON_DAYS = 7;

export const getNodeReminderEnabled = (node: Node): boolean => {
  return !!node.expiryTime && node.expiryTime > 0 && !!node.renewalCycle;
};

export const getNodeExpiryMeta = (
  timestamp?: number | null,
  cycle?: NodeRenewalCycle | null,
) => {
  const renewal = getNodeRenewalSnapshot(timestamp, cycle, EXPIRING_SOON_DAYS);

  if (renewal.state === "unset") {
    return {
      state: "permanent" as NodeExpiryState,
      label: "未设置续费周期",
      tone: "default" as const,
      accentClassName: "",
      bannerClassName: "",
      isHighlighted: false,
      sortWeight: 3,
      nextDueTime: undefined,
    };
  }

  if (renewal.state === "expired") {
    return {
      state: "expired" as NodeExpiryState,
      label: "已过期",
      tone: "danger" as const,
      accentClassName: "border-red-300/80 bg-red-500/10 shadow-red-100",
      bannerClassName: "bg-red-500/10 text-red-700",
      isHighlighted: true,
      sortWeight: 0,
      nextDueTime: renewal.nextDueTime,
    };
  }

  if (renewal.state === "dueSoon") {
    return {
      state: "expiringSoon" as NodeExpiryState,
      label: renewal.label,
      tone: "warning" as const,
      accentClassName: "border-amber-300/80 bg-amber-500/10 shadow-amber-100",
      bannerClassName: "bg-amber-500/10 text-amber-700",
      isHighlighted: true,
      sortWeight: 1,
      nextDueTime: renewal.nextDueTime,
    };
  }

  return {
    state: "healthy" as NodeExpiryState,
    label: renewal.label,
    tone: "success" as const,
    accentClassName: "",
    bannerClassName: "",
    isHighlighted: false,
    sortWeight: 2,
    nextDueTime: renewal.nextDueTime,
  };
};

export const formatSpeed = (bytesPerSecond: number): string => {
  if (bytesPerSecond === 0) return "0 B/s";

  const k = 1024;
  const sizes = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
  const i = Math.floor(Math.log(bytesPerSecond) / Math.log(k));

  return (
    parseFloat((bytesPerSecond / Math.pow(k, i)).toFixed(2)) + " " + sizes[i]
  );
};

export const formatUptime = (seconds: number): string => {
  if (seconds === 0) return "-";

  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);

  if (days > 0) {
    return `${days}天${hours}小时`;
  } else if (hours > 0) {
    return `${hours}小时${minutes}分钟`;
  } else {
    return `${minutes}分钟`;
  }
};

export const formatFlow = (bytes: number): string => {
  return formatTraffic(bytes);
};

export const formatChainType = (chainType: number, hopInx: number) => {
  if (chainType === 1) {
    return "入口节点";
  }
  if (chainType === 2) {
    return `中继跳点 #${hopInx}`;
  }
  if (chainType === 3) {
    return "出口节点";
  }

  return "未知链路";
};

const isPlainDataObject = (
  value: unknown,
): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/**
 * Deep equality over JSON-shaped values (API payloads and the plain
 * derived objects hung off a Node). Used to keep node object references
 * stable across polls when nothing actually changed, so memoized list
 * rows can skip re-rendering.
 */
const sameDataValue = (a: unknown, b: unknown): boolean => {
  if (Object.is(a, b)) {
    return true;
  }

  if (Array.isArray(a) && Array.isArray(b)) {
    return (
      a.length === b.length &&
      a.every((item, index) => sameDataValue(item, b[index]))
    );
  }

  if (isPlainDataObject(a) && isPlainDataObject(b)) {
    const aKeys = Object.keys(a);
    const bKeys = Object.keys(b);

    return (
      aKeys.length === bKeys.length &&
      aKeys.every(
        (key) =>
          Object.prototype.hasOwnProperty.call(b, key) &&
          sameDataValue(a[key], b[key]),
      )
    );
  }

  return false;
};

export const mergeNodeRealtimeState = (
  incomingNode: Node,
  existingNode?: Node,
): Node => {
  const merged: Node = {
    ...incomingNode,
    systemInfo: existingNode?.systemInfo ?? incomingNode.systemInfo ?? null,
    copyLoading: existingNode?.copyLoading ?? incomingNode.copyLoading ?? false,
    upgradeLoading:
      existingNode?.upgradeLoading ?? incomingNode.upgradeLoading ?? false,
    rollbackLoading:
      existingNode?.rollbackLoading ?? incomingNode.rollbackLoading ?? false,
  };

  // Poll responses rebuild every node object from JSON; when the merged
  // result carries exactly the data the existing object already has, keep
  // the existing reference so unchanged rows do not re-render.
  if (existingNode && sameDataValue(merged, existingNode)) {
    return existingNode;
  }

  return merged;
};

export const SortableItem = ({
  id,
  children,
}: {
  id: number;
  children: (listeners: any, attributes?: any) => any;
}) => {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id });

  const style: React.CSSProperties = {
    transform: transform
      ? CSS.Transform.toString({
          ...transform,
          x: Math.round(transform.x),
          y: Math.round(transform.y),
        })
      : undefined,
    transition: isDragging ? undefined : transition || undefined,
    opacity: isDragging ? 0.5 : 1,
    willChange: isDragging ? "transform" : undefined,
  };

  return (
    <div ref={setNodeRef} className="overflow-visible h-full" style={style}>
      {children(listeners, attributes)}
    </div>
  );
};
