import { useEffect, useMemo, useState } from "react";
import toast from "react-hot-toast";
import {
  getAllUsers,
  getSubscriptions,
  getUserSubscriptions,
  setUserSubscriptions,
  type SubscriptionItem,
} from "@/api";
import type { UserApiItem } from "@/api/types";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";
import { formatTraffic } from "@/utils/traffic";

function expiryText(ms: number): string {
  if (!ms) return "永不过期";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");
  const label = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  if (ms <= Date.now()) return `${label} 已过期`;

  return `${label} 到期`;
}

/**
 * 订阅分配：管理员把订阅管理里的订阅分配给用户。
 * 用户将使用被分配订阅的独立链接，流量与到期按订阅单独计算；
 * 删除或停用一条订阅只影响被分配到它的用户。
 */
export default function SubscriptionAssignSection() {
  const [users, setUsers] = useState<UserApiItem[]>([]);
  const [userId, setUserId] = useState<number | null>(null);
  const [subs, setSubs] = useState<SubscriptionItem[]>([]);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);

  // 加载用户与订阅列表
  useEffect(() => {
    setLoading(true);
    Promise.all([getAllUsers(), getSubscriptions()])
      .then(([uRes, sRes]) => {
        if (uRes.code === 0)
          setUsers((uRes.data ?? []).filter((u: UserApiItem) => u.id));
        if (sRes.code === 0 && sRes.data) setSubs(sRes.data.items ?? []);
      })
      .finally(() => setLoading(false));
  }, []);

  // 加载选中用户已被分配的订阅
  useEffect(() => {
    if (!userId) {
      setSelected(new Set());

      return;
    }
    getUserSubscriptions(userId).then((res) => {
      if (res.code === 0) {
        setSelected(new Set(res.data?.subscriptionIds ?? []));
      }
    });
  }, [userId]);

  const toggle = (id: number) => {
    const next = new Set(selected);

    if (next.has(id)) next.delete(id);
    else next.add(id);
    setSelected(next);
  };

  const handleSave = async () => {
    if (!userId) {
      toast.error("请先选择用户");

      return;
    }
    setSaving(true);
    try {
      const res = await setUserSubscriptions(userId, Array.from(selected));

      if (res.code === 0) toast.success("订阅分配已保存");
      else toast.error(res.msg || "保存失败");
    } catch {
      toast.error("保存失败");
    } finally {
      setSaving(false);
    }
  };

  const selectedUser = useMemo(
    () => users.find((u) => u.id === userId),
    [users, userId],
  );

  if (loading) return <p className="text-sm text-default-500">加载中...</p>;

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader className="flex flex-col gap-1">
          <h3 className="text-sm font-semibold">订阅分配</h3>
          <p className="text-xs text-default-500">
            按订阅分配：用户将使用被分配订阅的独立链接，流量与到期按订阅单独计算。
          </p>
        </CardHeader>
        <CardBody className="space-y-4">
          <Select
            className="max-w-xs"
            label="选择用户"
            placeholder="请选择要分配的用户"
            selectedKeys={userId ? [String(userId)] : []}
            onSelectionChange={(keys) => {
              const k = Array.from(keys)[0];

              setUserId(k ? Number(k) : null);
            }}
          >
            {users.map((u) => (
              <SelectItem key={String(u.id)}>
                {u.user || u.name || `用户 ${u.id}`}
              </SelectItem>
            ))}
          </Select>

          {!userId ? (
            <p className="text-sm text-default-400">
              请先选择用户，再勾选要分配给他的订阅
            </p>
          ) : (
            <>
              {subs.length === 0 ? (
                <p className="text-sm text-default-400">
                  还没有订阅，先在「订阅管理」里新建订阅再来分配
                </p>
              ) : (
                <div className="grid max-h-96 gap-2 overflow-y-auto pr-1">
                  {subs.map((s) => {
                    const isSelected = selected.has(s.id);
                    const used = s.uploadBytes + s.downloadBytes;

                    return (
                      <label
                        key={s.id}
                        className={`flex cursor-pointer items-center gap-3 rounded-lg border border-divider px-4 py-3 transition-all ${
                          isSelected
                            ? "bg-primary-50 dark:bg-primary-900/20"
                            : "bg-white hover:bg-default-100 dark:bg-default-50"
                        }`}
                      >
                        <input
                          aria-label={`分配订阅 ${s.name}`}
                          checked={isSelected}
                          className="h-4 w-4 shrink-0 rounded"
                          type="checkbox"
                          onChange={() => toggle(s.id)}
                        />
                        <div className="min-w-0 flex-1">
                          <p className="truncate text-sm font-medium">
                            {s.name}
                            {s.isDefault && (
                              <span className="ml-2 text-xs font-normal text-default-500">
                                默认
                              </span>
                            )}
                            {!s.enabled && (
                              <Chip
                                className="ml-2"
                                size="sm"
                                variant="flat"
                              >
                                已停用
                              </Chip>
                            )}
                          </p>
                          <p className="text-xs text-default-500">
                            {s.nodeCount} 个节点 ·{" "}
                            {s.trafficLimit
                              ? `限额 ${formatTraffic(s.trafficLimit)}（已用 ${formatTraffic(used)}）`
                              : "不限流量"}
                            {" · "}
                            {expiryText(s.expireTime)}
                          </p>
                        </div>
                      </label>
                    );
                  })}
                </div>
              )}
              <div className="flex items-center gap-3">
                <Button
                  color="primary"
                  isLoading={saving}
                  size="sm"
                  onPress={handleSave}
                >
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
