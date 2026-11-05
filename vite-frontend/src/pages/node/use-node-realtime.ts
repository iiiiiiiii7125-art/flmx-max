import { useCallback, useEffect, useRef, useState } from "react";
import axios from "axios";

import { IS_DEMO, startDemoRealtime } from "@/demo";
import { getToken } from "@/utils/session";

interface NodeRealtimeMessage {
  id?: string | number;
  type?: string;
  data?: unknown;
  message?: string;
}

interface UseNodeRealtimeOptions {
  onMessage: (message: NodeRealtimeMessage) => void;
  enabled?: boolean;
}

const MAX_STANDARD_RECONNECT_ATTEMPTS = 5;
const STANDARD_RECONNECT_DELAY_MS = 3000;
const MAX_STANDARD_RECONNECT_DELAY_MS = 15000;
const FALLBACK_RECONNECT_DELAY_MS = 30000;

// R-WS-TICKET：连接前先以登录态 JWT（Authorization 头）向后端换取
// 短期单次票据，WS 握手只带票据——长效 JWT 不再进 WS 查询参数。
// 票据单次使用，每次（重）连都重新取票。
const fetchRealtimeWsUrl = async (): Promise<string> => {
  const baseUrl =
    axios.defaults.baseURL ||
    (import.meta.env.VITE_API_BASE
      ? `${import.meta.env.VITE_API_BASE}/api/v1/`
      : "/api/v1/");

  const token = getToken();
  const res = await axios.post("system-info/ticket", null, {
    headers: token ? { Authorization: token } : undefined,
    timeout: 5000,
  });
  const envelope = res.data as
    | { data?: { ticket?: unknown }; ticket?: unknown }
    | undefined;
  const ticket = envelope?.data?.ticket ?? envelope?.ticket;

  if (typeof ticket !== "string" || ticket === "") {
    throw new Error("ws ticket missing from issue response");
  }

  return (
    baseUrl.replace(/^http/, "ws").replace(/\/api\/v1\/$/, "") +
    `/system-info?type=0&ticket=${encodeURIComponent(ticket)}`
  );
};

interface RealtimeConnectionState {
  connected: boolean;
  connecting: boolean;
  usingPollingFallback: boolean;
}

interface RealtimeSubscriber {
  onMessage: (message: NodeRealtimeMessage) => void;
  onStateChange: (state: RealtimeConnectionState) => void;
}

// 同一端点的 WebSocket 全局共享一条连接、订阅扇出：
// 监控页父组件与 MonitorView 各自调用 useNodeRealtime 时不再各连一条
// （同一广播此前会被推送/处理两遍，且两条连接状态来源不唯一）。
let sharedSocket: WebSocket | null = null;
// 在线演示没有后端 WebSocket：startDemoRealtime 伪造指标喂给同一套订阅扇出。
let stopDemoRealtime: (() => void) | null = null;
let sharedState: RealtimeConnectionState = {
  connected: false,
  connecting: false,
  usingPollingFallback: false,
};
let sharedReconnectAttempts = 0;
let sharedReconnectTimer: ReturnType<typeof setTimeout> | null = null;
const sharedSubscribers = new Set<RealtimeSubscriber>();

const setSharedState = (patch: Partial<RealtimeConnectionState>) => {
  sharedState = { ...sharedState, ...patch };
  sharedSubscribers.forEach((subscriber) => {
    subscriber.onStateChange(sharedState);
  });
};

const clearSharedReconnectTimer = () => {
  if (sharedReconnectTimer) {
    clearTimeout(sharedReconnectTimer);
    sharedReconnectTimer = null;
  }
};

const scheduleSharedReconnect = () => {
  if (sharedSubscribers.size === 0) {
    return;
  }

  clearSharedReconnectTimer();
  sharedReconnectAttempts += 1;
  const exhaustedStandardRetries =
    sharedReconnectAttempts >= MAX_STANDARD_RECONNECT_ATTEMPTS;

  setSharedState({ usingPollingFallback: exhaustedStandardRetries });

  const reconnectDelay = exhaustedStandardRetries
    ? FALLBACK_RECONNECT_DELAY_MS
    : Math.min(
        STANDARD_RECONNECT_DELAY_MS * sharedReconnectAttempts,
        MAX_STANDARD_RECONNECT_DELAY_MS,
      );

  sharedReconnectTimer = setTimeout(() => {
    sharedReconnectTimer = null;
    connectShared();
  }, reconnectDelay);
};

// 取票是异步的：防重入标志避免多个订阅者/重连定时器同时发起取票+
// 拨号（票据单次使用，并发拨号还会互相烧毁对方的票）。
let sharedConnectInFlight = false;

const connectShared = () => {
  if (sharedSubscribers.size === 0) {
    return;
  }

  if (IS_DEMO) {
    // 短路：不建 WebSocket、状态直接置已连接——usingPollingFallback 恒假，
    // node 页的 REST 轮询兜底也不会启动（演示下它只会反复打录制外的接口）。
    if (!stopDemoRealtime) {
      stopDemoRealtime = startDemoRealtime((message) => {
        sharedSubscribers.forEach((subscriber) => {
          subscriber.onMessage(message as NodeRealtimeMessage);
        });
      });
      setSharedState({
        connected: true,
        connecting: false,
        usingPollingFallback: false,
      });
    }

    return;
  }

  if (
    sharedSocket &&
    (sharedSocket.readyState === WebSocket.OPEN ||
      sharedSocket.readyState === WebSocket.CONNECTING)
  ) {
    return;
  }

  if (sharedConnectInFlight) {
    return;
  }

  if (sharedSocket) {
    sharedSocket.onopen = null;
    sharedSocket.onmessage = null;
    sharedSocket.onerror = null;
    sharedSocket.onclose = null;
    sharedSocket = null;
  }

  sharedConnectInFlight = true;
  setSharedState({ connecting: true });

  void (async () => {
    try {
      const url = await fetchRealtimeWsUrl();

      // 取票期间订阅者可能已全部退订（disconnectShared 已跑）：
      // 放弃建连，避免无人订阅的孤儿连接。
      if (sharedSubscribers.size === 0) {
        return;
      }

      sharedSocket = new WebSocket(url);

      sharedSocket.onopen = () => {
        sharedReconnectAttempts = 0;
        setSharedState({
          connected: true,
          connecting: false,
          usingPollingFallback: false,
        });
      };

      sharedSocket.onmessage = (event) => {
        try {
          const parsed = JSON.parse(event.data);

          if (parsed && typeof parsed === "object") {
            sharedSubscribers.forEach((subscriber) => {
              subscriber.onMessage(parsed as NodeRealtimeMessage);
            });
          }
        } catch {}
      };

      sharedSocket.onerror = () => {};

      sharedSocket.onclose = () => {
        sharedSocket = null;
        setSharedState({ connected: false, connecting: false });
        scheduleSharedReconnect();
      };
    } catch {
      // 取票失败（未登录/票据端点异常）与构造抛错同路径处理：
      // 排重连并在标准重试耗尽后置 usingPollingFallback，
      // 避免实时通道整会话静默死亡、页面停在首屏静态数据
      sharedSocket = null;
      setSharedState({ connected: false, connecting: false });
      scheduleSharedReconnect();
    } finally {
      sharedConnectInFlight = false;
    }
  })();
};

const disconnectShared = () => {
  clearSharedReconnectTimer();
  sharedReconnectAttempts = 0;

  if (stopDemoRealtime) {
    stopDemoRealtime();
    stopDemoRealtime = null;
  }

  if (sharedSocket) {
    sharedSocket.onopen = null;
    sharedSocket.onmessage = null;
    sharedSocket.onerror = null;
    sharedSocket.onclose = null;

    if (
      sharedSocket.readyState === WebSocket.OPEN ||
      sharedSocket.readyState === WebSocket.CONNECTING
    ) {
      sharedSocket.close();
    }

    sharedSocket = null;
  }

  sharedState = {
    connected: false,
    connecting: false,
    usingPollingFallback: false,
  };
};

export const useNodeRealtime = ({
  onMessage,
  enabled = true,
}: UseNodeRealtimeOptions) => {
  const [connectionState, setConnectionState] =
    useState<RealtimeConnectionState>(sharedState);
  const onMessageRef = useRef(onMessage);
  const subscriberRef = useRef<RealtimeSubscriber | null>(null);

  useEffect(() => {
    onMessageRef.current = onMessage;
  }, [onMessage]);

  useEffect(() => {
    if (!enabled) {
      return;
    }

    const subscriber: RealtimeSubscriber = {
      onMessage: (message) => onMessageRef.current(message),
      onStateChange: (state) => setConnectionState(state),
    };

    subscriberRef.current = subscriber;
    sharedSubscribers.add(subscriber);
    setConnectionState(sharedState);
    connectShared();

    return () => {
      sharedSubscribers.delete(subscriber);
      if (subscriberRef.current === subscriber) {
        subscriberRef.current = null;
      }
      if (sharedSubscribers.size === 0) {
        disconnectShared();
      }
    };
  }, [enabled]);

  const reconnectRealtime = useCallback(() => {
    if (!enabled) {
      return;
    }
    connectShared();
  }, [enabled]);

  const disconnectRealtime = useCallback(() => {
    const subscriber = subscriberRef.current;

    if (subscriber) {
      sharedSubscribers.delete(subscriber);
      subscriberRef.current = null;
    }
    if (sharedSubscribers.size === 0) {
      disconnectShared();
    }
    setConnectionState({
      connected: false,
      connecting: false,
      usingPollingFallback: false,
    });
  }, []);

  return {
    wsConnected: enabled ? connectionState.connected : false,
    wsConnecting: enabled ? connectionState.connecting : false,
    usingPollingFallback: enabled ? connectionState.usingPollingFallback : false,
    reconnectRealtime,
    disconnectRealtime,
  };
};
