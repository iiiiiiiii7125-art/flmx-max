import { useCallback, useState } from "react";

/**
 * R2X-004（BC-13 口径回正）：创建表单的 Idempotency-Key 会话键。
 *
 * 语义（与后端 handler/idempotency.go 的状态机对偶）：
 * - 表单挂载时生成一次；
 * - 同一表单内连点 / 超时重试 / 校验失败改参重试全部复用同一键，
 *   后端回放首次成功结果或等待在途请求，不会重复创建；
 * - 创建成功后由表单调用 rotateIdemKey() 轮换，下一笔正当创建用新键；
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
