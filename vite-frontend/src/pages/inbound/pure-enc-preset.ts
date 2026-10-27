import type { InboundForm } from "./presets";

/**
 * 一键填入「纯 ENC」配置：VLESS / TCP / 安全无 / Vision / ML-KEM-768
 * 后量子认证 / xorpub 外观，且无 SNI。只填表单，不创建、不改用户
 * 已填的节点 / 端口 / 名字（nodeId、port、name 等原值保留）。
 *
 * 与 ENC 开关的自动套用不同：外观是 xorpub（不是 native），且显式
 * 切协议到 vless、清 SNI。
 */
export function applyPureEncPreset(prev: InboundForm): InboundForm {
  return {
    ...prev,
    protocol: "vless",
    transportType: "tcp",
    securityType: "none",
    flowVision: true,
    encryptionEnabled: true,
    // 后量子认证锁死 ML-KEM-768；x25519 不是后量子，绝不能选。
    encryptionAuth: "mlkem768",
    encryptionMode: "xorpub",
    // 纯 ENC 不需要 SNI。
    sni: "",
    // Reality 残留字段：toPayload 已按 securityType 设防（仅 reality 发
    // 出）；此处清零为纵深防御，切到纯 ENC 时一并清空，避免脏数据进后端。
    realityDest: "",
    realityPrivateKey: "",
    realityShortIds: "",
    realityMinClientVer: "",
  };
}
