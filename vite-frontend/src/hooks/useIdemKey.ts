import { useCallback, useEffect, useRef, useState } from "react";

/**
 * R2X-004（BC-13 口径回正）：创建表单的 Idempotency-Key 会话键。
 *
 * 语义（与后端 handler/idempotency.go 的状态机对偶）：
 * - 表单挂载时生成一次；
 * - 同一表单内连点 / 超时重试 / 校验失败改参重试全部复用同一键，
 *   后端回放首次成功结果或等待在途请求，不会重复创建；
 * - 创建成功后由表单调用 rotateIdemKey() 轮换，下一笔正当创建用新键；
 * - 创建弹窗每次打开（新建模式）亦经 useRotateIdemKeyOnCreateOpen 轮换
 *   （R3X-027：防「响应丢失后关窗重开改参再提交」被旧键回放吞掉）；
 * - 组件卸载即丢弃（state 随组件销毁），不跨表单、不跨会话复用。
 *
 * 禁止在 Network.post 里逐请求生成（每次新键 = 机制失效），禁止模块级
 * 全局键（第二次正当创建会被回放吞掉）。
 *
 * 面板常经 HTTP 明文访问，crypto.randomUUID() 在非安全上下文不可用，
 * 故以 crypto.getRandomValues 拼 UUIDv4；getRandomValues 本身也缺失时
 * 退化到 Math.random 填充（键只要求同一表单内稳定且全局难碰撞）。
 */
export function newIdemKey(): string {
  const bytes = new Uint8Array(16);

  if (
    typeof crypto !== "undefined" &&
    typeof crypto.getRandomValues === "function"
  ) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) {
      bytes[i] = Math.floor(Math.random() * 256);
    }
  }
  // RFC 4122 version 4 + variant bits.
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;

  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join(
    "",
  );

  return [
    hex.slice(0, 8),
    hex.slice(8, 12),
    hex.slice(12, 16),
    hex.slice(16, 20),
    hex.slice(20),
  ].join("-");
}

export function useIdemKey(): {
  idemKey: string;
  rotateIdemKey: () => void;
} {
  const [idemKey, setIdemKey] = useState<string>(() => newIdemKey());
  const rotateIdemKey = useCallback(() => {
    setIdemKey(newIdemKey());
  }, []);

  return { idemKey, rotateIdemKey };
}

/**
 * R3X-027：开窗轮换的判定谓词（纯函数，供桩验证）。
 *
 * 仅在「弹窗由关到开的上升沿」且「新建模式」时轮换：
 * - 关窗重开必新键——上一窗内若发生「服务端已落库但响应丢失」，旧键已被
 *   后端 completed 回执污染，新表单会话绝不能再复用它（否则改参再提交会被
 *   回放吞掉、静默不创建）；
 * - 窗内保持打开时不轮换——同表单内校验失败/超时后不关窗、同参或改参重试
 *   仍复用同键，后端幂等语义（同键回放/等待在途）不变；
 * - 编辑模式开窗不轮换——更新分支本就不带键。
 */
export function shouldRotateOnCreateOpen(
  wasOpen: boolean,
  isOpen: boolean,
  isCreateMode: boolean,
): boolean {
  return isOpen && !wasOpen && isCreateMode;
}

/**
 * R3X-027：创建弹窗打开（上升沿且新建模式）时轮换幂等键。
 * hook 声明点可留在页面/常驻组件层，只需把弹窗的打开状态与是否新建模式
 * 传进来；轮换只发生在开窗一刻，窗内重试不受影响。
 */
export function useRotateIdemKeyOnCreateOpen(
  isOpen: boolean,
  isCreateMode: boolean,
  rotateIdemKey: () => void,
): void {
  const wasOpenRef = useRef(false);

  useEffect(() => {
    const shouldRotate = shouldRotateOnCreateOpen(
      wasOpenRef.current,
      isOpen,
      isCreateMode,
    );

    wasOpenRef.current = isOpen;
    if (shouldRotate) rotateIdemKey();
  }, [isOpen, isCreateMode, rotateIdemKey]);
}
