import { useEffect, useMemo, useState } from "react";
import toast from "react-hot-toast";
import {
  getInboundList,
  getInboundRelays,
  getUserInbounds,
  setUserInbounds,
  type UserInboundAssignment,
} from "@/api";
import type { InboundApiItem, InboundRelayApiItem, UserApiItem } from "@/api/types";
import { getAllUsers } from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";

type Key = string; // `${inboundId}:${relayId}`

const keyOf = (inboundId: number, relayId: number) => `${inboundId}:${relayId}`;

/**
 * 订阅分配：管理员按用户分配入站（含直连）与中转节点的订阅权限。
 * 与订阅 CRUD 页的链式代理配置相互独立：链式代理决定订阅内容如何生成，
 * 这里决定某个用户能看到哪些入站/中转的订阅链接。
 */
export default function SubscriptionAssignSection() {
  const [users, setUsers] = useState<UserApiItem[]>([]);
  const [userId, setUserId] = useState<number | null>(null);
  const [inbounds, setInbounds] = useState<InboundApiItem[]>([]);
  const [relaysByInbound, setRelaysByInbound] = useState<Record<number, InboundRelayApiItem[]>>({});
  const [selected, setSelected] = useState<Set<Key>>(new Set());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  // 加载用户与入站列表
  useEffect(() => {
    setLoading(true);
    Promise.all([getAllUsers(), getInboundList()])
      .then(([uRes, inRes]) => {
        if (uRes.code === 0) setUsers((uRes.data ?? []).filter((u: UserApiItem) => u.id));
        if (inRes.code === 0) {
          const list = (inRes.data ?? []).filter((i: InboundApiItem) => i.enabled);
          setInbounds(list);
          // 并行加载每个入站的中转
          Promise.all(list.map((i) => getInboundRelays(i.id).then((r) => ({ id: i.id, relays: r.code === 0 ? r.data ?? [] : [] })))).then(
            (results) => {
              const map: Record<number, InboundRelayApiItem[]> = {};
              for (const { id, relays } of results) map[id] = relays;
              setRelaysByInbound(map);
            },
          );
        }
      })
      .finally(() => setLoading(false));
  }, []);

  // 加载选中用户的已分配权限
  useEffect(() => {
    if (!userId) {
      setSelected(new Set());
      return;
    }
    getUserInbounds(userId).then((res) => {
      if (res.code === 0) {
        setSelected(new Set(((res.data ?? []) as UserInboundAssignment[]).map((a) => keyOf(a.inboundId, a.relayId))));
      }
    });
  }, [userId]);

  // 入站/中转列表加载完成后，剔除指向已删除入站/中转的孤儿选中项（避免计数虚高）
  useEffect(() => {
    setSelected((prev) => {
      const next = new Set<Key>();
      let changed = false;
      for (const k of prev) {
        const [inboundId, relayId] = String(k).split(":").map(Number);
        if (!inbounds.some((i) => i.id === inboundId)) { changed = true; continue; }
        if (relayId > 0 && !(relaysByInbound[inboundId] ?? []).some((r) => r.id === relayId)) { changed = true; continue; }
        next.add(k);
      }
      return changed ? next : prev;
    });
  }, [inbounds, relaysByInbound]);

  const toggle = (inboundId: number, relayId: number) => {
    const k = keyOf(inboundId, relayId);
    const next = new Set(selected);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    setSelected(next);
  };

  const handleSave = async () => {
    if (!userId) {
      toast.error("请先选择用户");
      return;
    }
    setSaving(true);
    try {
      const assignments: UserInboundAssignment[] = Array.from(selected).map((k) => {
        const [inboundId, relayId] = k.split(":").map(Number);
        return { inboundId, relayId };
      });
      const res = await setUserInbounds(userId, assignments);
      if (res.code === 0) toast.success("订阅权限已保存");
      else toast.error(res.msg || "保存失败");
    } catch {
      toast.error("保存失败");
    } finally {
      setSaving(false);
    }
  };

  const selectedUser = useMemo(() => users.find((u) => u.id === userId), [users, userId]);

  if (loading) return <p className="text-sm text-default-500">加载中...</p>;

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="flex flex-col gap-1">
          <h3 className="text-sm font-semibold">订阅分配</h3>
          <p className="text-xs text-default-500">
            按用户分配可订阅的入站与中转节点。勾选入站表示直连订阅，勾选中转表示该中转线路的订阅。用户在其订阅页面中看到已分配的链接。
          </p>
        </CardHeader>
        <CardBody className="space-y-4">
          <Select
            label="选择用户"
            placeholder="请选择要分配的用户"
            selectedKeys={userId ? [String(userId)] : []}
            onSelectionChange={(keys) => {
              const k = Array.from(keys)[0];
              setUserId(k ? Number(k) : null);
            }}
            className="max-w-xs"
          >
            {users.map((u) => (
              <SelectItem key={String(u.id)}>{u.user || u.name || `用户 ${u.id}`}</SelectItem>
            ))}
          </Select>

          {!userId ? (
            <p className="text-sm text-default-400">请先选择用户，再勾选下方入站与中转节点</p>
          ) : (
            <>
              <div className="grid gap-2 max-h-96 overflow-y-auto pr-1">
                {inbounds.map((inbound) => {
                  const relays = relaysByInbound[inbound.id] ?? [];
                  const directKey = keyOf(inbound.id, 0);
                  const isDirectSelected = selected.has(directKey);
                  return (
                    <div key={inbound.id} className="rounded-lg border border-divider overflow-hidden">
                      <label
                        className={`flex items-center gap-3 px-4 py-3 cursor-pointer transition-all ${
                          isDirectSelected ? "bg-primary-50 dark:bg-primary-900/20" : "bg-white dark:bg-default-50 hover:bg-default-100"
                        }`}
                      >
                        <input
                          type="checkbox"
                          checked={isDirectSelected}
                          onChange={() => toggle(inbound.id, 0)}
                          aria-label={`${inbound.name} 直连`}
                          className="w-4 h-4 rounded"
                        />
                        <div className="min-w-0 flex-1">
                          <p className="text-sm font-medium truncate">
                            {inbound.name}
                            <span className="ml-2 text-xs font-normal text-default-500">直连</span>
                          </p>
                          <p className="text-xs text-default-500">
                            {inbound.protocol} · {inbound.nodeName}
                          </p>
                        </div>
                      </label>
                      {relays.length > 0 && (
                        <div className="border-t border-divider bg-default-50/50 dark:bg-default-100/10">
                          {relays.map((relay) => {
                            const rk = keyOf(inbound.id, relay.id);
                            const isRelaySelected = selected.has(rk);
                            return (
                              <label
                                key={relay.id}
                                className={`flex items-center gap-3 px-4 py-2.5 pl-10 cursor-pointer transition-all border-t border-divider/50 first:border-t-0 ${
                                  isRelaySelected ? "bg-primary-50/70 dark:bg-primary-900/10" : "hover:bg-default-100/50"
                                }`}
                              >
                                <input
                                  type="checkbox"
                                  checked={isRelaySelected}
                                  onChange={() => toggle(inbound.id, relay.id)}
                                  aria-label={`${relay.name || `中转 ${relay.id}`} 中转`}
                                  className="w-4 h-4 rounded"
                                />
                                <div className="min-w-0 flex-1">
                                  <p className="text-sm truncate">
                                    {relay.name || `中转 ${relay.id}`}
                                    <span className="ml-2 text-xs text-default-500">中转</span>
                                  </p>
                                </div>
                              </label>
                            );
                          })}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
              <div className="flex items-center gap-3">
                <Button color="primary" size="sm" isLoading={saving} onPress={handleSave}>
                  保存订阅分配（已选 {selected.size} 个）
                </Button>
                {selectedUser && (
                  <span className="text-xs text-default-500">
                    正在为 {selectedUser.user || selectedUser.name} 分配
                  </span>
                )}
              </div>
            </>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
