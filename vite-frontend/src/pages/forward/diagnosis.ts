export interface ForwardDiagnosisEntry {
  success?: boolean;
  diagnosing?: boolean;
  /**
   * UDP 未验证标记（R6-L4-01）：后端对 UDP 探测项恒以此形态回传
   * （success=false、延迟/丢包为 null），呈现走中性「未验证」形态，
   * 不计入成功/失败。
   */
  unverified?: boolean;
  protocol?: string;
  description: string;
  nodeName: string;
  nodeId: string;
  targetIp: string;
  targetPort?: number;
  message?: string;
  averageTime?: number | null;
  packetLoss?: number | null;
  fromChainType?: number;
  fromInx?: number;
  toChainType?: number;
  toInx?: number;
}

export interface ForwardDiagnosisResult {
  forwardName: string;
  timestamp: number;
  results: ForwardDiagnosisEntry[];
}

export interface ForwardDiagnosisFallbackInput {
  forwardName: string;
  remoteAddr: string;
  description: string;
  message: string;
}

const pickPrimaryTargetIp = (remoteAddr: string): string => {
  return remoteAddr.split(",")[0] || "-";
};

export const buildForwardDiagnosisFallbackResult = ({
  forwardName,
  remoteAddr,
  description,
  message,
}: ForwardDiagnosisFallbackInput): ForwardDiagnosisResult => {
  return {
    forwardName,
    timestamp: Date.now(),
    results: [
      {
        success: false,
        description,
        nodeName: "-",
        nodeId: "-",
        targetIp: pickPrimaryTargetIp(remoteAddr),
        message,
      },
    ],
  };
};

export const getForwardDiagnosisQualityDisplay = (
  averageTime?: number | null,
  packetLoss?: number | null,
): {
  text: string;
  color: "success" | "primary" | "warning" | "danger";
} | null => {
  // 未测值（undefined/null）不评级——null 若漏进数值比较会被强转
  // 为 0 而误判「优秀」（R6-L4-01：UDP 未验证项恒为 null）。
  if (averageTime == null || packetLoss == null) {
    return null;
  }

  if (averageTime < 30 && packetLoss === 0) {
    return { text: "🚀 优秀", color: "success" };
  }

  if (averageTime < 50 && packetLoss === 0) {
    return { text: "✨ 很好", color: "success" };
  }

  if (averageTime < 100 && packetLoss < 1) {
    return { text: "👍 良好", color: "primary" };
  }

  if (averageTime < 150 && packetLoss < 2) {
    return { text: "😐 一般", color: "warning" };
  }

  if (averageTime < 200 && packetLoss < 5) {
    return { text: "😟 较差", color: "warning" };
  }

  return { text: "😵 很差", color: "danger" };
};
