import type { InboundForm } from "./presets";

/**
 * 一键填入 IEPL 专用（纯 ENC）配置（CHG-01 / S-21）：VLESS / TCP /
 * 安全无 / ML-KEM-768 后量子认证 / xorpub 外观，无 SNI，并联动勾选
 * 空闲超时「不限」（connIdleSeconds 置 -1 哨兵值）。只填表单，不创建、
 * 不改用户已填的节点 / 端口 / 名字（nodeId、port、name 等原值保留）。
 *
 * 与 ENC 开关的自动套用不同：外观是 xorpub（不是 native），且显式
 * 切协议到 vless、清 SNI。
 *
 * 外观差异的依据（F-L3-02）：外观是 VLESS 加密串的「流量外观」段
 * （mlkem768x25519plus.<外观>.…）——native＝公钥以原生形态上线、
 * xorpub＝公钥经 XOR 混淆后上线（抗识别更强）、random＝全随机；
 * 三者服务端与客户端必须一致，不等效、不可互换（面板由同一条存储
 * 配置同源生成服务端解密串与客户端加密串，故任一取值各自自洽）。
 * IEPL 场景定为 xorpub（CHG-01 定案）；ENC 开关作为通用 ENC 入口填
 * native、且不联动空闲超时「不限」（联动只属本预设的整套 IEPL 形态），
 * 用户可在「流量外观」下拉中改选。两入口差异已在开关处理处注记与
 * 开关下方表单注记中明示，避免被误读为同一形态的两套标准。
 *
 * 流控口径（S-21 已定，单一判据）：纯 ENC 形态（ENC 开 + 安全无）
 * 不开启流控——本标签预设如此，ENC 开关切换的自动填充同此（见
 * inbound-form-modal.tsx 开关处理处注记）。Vision 功能本身完整保留，
 * VLESS Reality 等其他形态照常可开。
 *
 * 空闲超时「不限」只在套用时写入 -1，不做反向回滚：用户可在表单里
 * 自行取消勾选（沿表单通例，取消时恢复勾选前的值或留空）。
 */
export function applyPureEncPreset(prev: InboundForm): InboundForm {
  return {
    ...prev,
    protocol: "vless",
    transportType: "tcp",
    securityType: "none",
    // S-21：纯 ENC 形态不加 XTLS Vision 流控。
    flowVision: false,
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
    // CHG-01（S-21）联动：套用预设时同时勾选空闲超时「不限」（-1 标记）。
    connIdleSeconds: "-1",
  };
}
