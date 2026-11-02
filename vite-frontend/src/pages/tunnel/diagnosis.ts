export interface DiagnosisEntry {
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

export interface DiagnosisResult {
  tunnelName: string;
  tunnelType: string;
  timestamp: number;
  results: DiagnosisEntry[];
}

export interface DiagnosisFallbackInput {
  tunnelName: string;
  tunnelType: number;
  description: string;
  message: string;
  targetIp?: string;
  targetPort?: number;
}

export const buildDiagnosisFallbackResult = ({
  tunnelName,
  tunnelType,
  description,
  message,
  targetIp = "-",
  targetPort = 443,
}: DiagnosisFallbackInput): DiagnosisResult => {
  return {
    tunnelName,
    tunnelType: tunnelType === 1 ? "端口转发" : "隧道转发",
    timestamp: Date.now(),
    results: [
      {
        success: false,
        description,
        nodeName: "-",
        nodeId: "-",
        targetIp,
        targetPort,
        message,
      },
    ],
  };
};

export const getDiagnosisQualityDisplay = (
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
