import { loadStoredOrder } from "@/utils/order-storage";

export interface ForwardOrderItem {
  id: number;
  userId?: number;
  inx?: number;
}

export const FORWARD_ORDER_KEY = "forward-order";

// F-L5-03：排序键按登录用户分键（与分组排序/折叠键的 `:u:<id>` 口径
// 一致），避免同一浏览器多账号登录时排序互相串扰。userId 为空（无
// token）时退回旧全局键。
export const buildForwardOrderLocalKey = (userId: number | null): string => {
  return userId ? `${FORWARD_ORDER_KEY}:u:${userId}` : FORWARD_ORDER_KEY;
};

export const getUserScopedForwards = <T extends ForwardOrderItem>(
  forwards: T[],
  currentUserId: number | null,
): T[] => {
  if (currentUserId === null) {
    return forwards;
  }

  return forwards.filter((item) => item.userId === currentUserId);
};

export const buildForwardOrder = <T extends ForwardOrderItem>(
  forwards: T[],
  currentUserId: number | null,
): { order: number[]; fromDatabase: boolean } => {
  const userForwards = getUserScopedForwards(forwards, currentUserId);

  const hasDbOrdering = userForwards.some(
    (item) => item.inx !== undefined && item.inx !== 0,
  );

  if (hasDbOrdering) {
    const dbOrder = [...userForwards]
      .sort((a, b) => (a.inx ?? 0) - (b.inx ?? 0))
      .map((item) => item.id);

    return { order: dbOrder, fromDatabase: true };
  }

  // 分键下尚无存量时回退读一次旧全局键（升级迁移，写入只写分键）。
  const scopedKey = buildForwardOrderLocalKey(currentUserId);
  const storageKey =
    scopedKey === FORWARD_ORDER_KEY || localStorage.getItem(scopedKey) !== null
      ? scopedKey
      : FORWARD_ORDER_KEY;

  return {
    order: loadStoredOrder(
      storageKey,
      userForwards.map((item) => item.id),
    ),
    fromDatabase: false,
  };
};
