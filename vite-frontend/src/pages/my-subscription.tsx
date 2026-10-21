import { useEffect, useState } from "react";
import toast from "react-hot-toast";
import { Copy, Check, QrCode, ChevronDown } from "lucide-react";

import { getMyInbounds, getMySubscriptions, getSubInfo } from "@/api";
import type { MySubscriptionItem } from "@/api";
import type { InboundApiItem } from "@/api/types";
import { copyWithToast } from "@/utils/clipboard";
import { formatTraffic } from "@/utils/traffic";
import { QrCodeButton } from "@/pages/inbound/share";
import { SubTrafficStats } from "@/pages/subscription/traffic-stats";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Chip } from "@/shadcn-bridge/heroui/chip";

const SUB_FORMATS = [
  { key: "", label: "通用" },
  { key: "clash", label: "Clash" },
  { key: "singbox", label: "sing-box" },
  { key: "surge", label: "Surge" },
];

interface MyRelay {
  id: number;
  inboundId: number;
  name: string;
}

function dateText(ms: number): string {
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");

  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export default function MySubscriptionPage() {
  // 分配给我的订阅（新模型）：非空时只展示这些独立链接，不再展示个人链接。
  const [assigned, setAssigned] = useState<MySubscriptionItem[]>([]);
  const [data, setData] = useState<{ userId: number; inbounds: InboundApiItem[]; relays?: MyRelay[]; sig?: string } | null>(null);
  const [subPath, setSubPath] = useState("");
  const [loading, setLoading] = useState(true);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  // 展开了流量统计的分配订阅 id（同一时间只展开一条）。
  const [expandedTrafficId, setExpandedTrafficId] = useState<number | null>(
    null,
  );

  useEffect(() => {
    // getSubInfo 查默认订阅：未被分配默认订阅是正常状态，后端会回 403，
    // 静默处理（不弹全局「权限不足」），页面只隐藏默认订阅区即可。
    Promise.all([getMySubscriptions(), getMyInbounds(), getSubInfo(false, true)])
      .then(([assignedRes, myRes, subRes]) => {
        if (assignedRes.code === 0 && assignedRes.data) {
          setAssigned(assignedRes.data.subscriptions ?? []);
        }
        if (myRes.code === 0 && myRes.data) {
          setData(myRes.data as { userId: number; inbounds: InboundApiItem[]; relays?: MyRelay[]; sig?: string });
        }
        if (subRes.code === 0) {
          setSubPath(subRes.data?.path || "");
        }
      })
      .catch(() => {
        toast.error("加载订阅信息失败");
      })
      .finally(() => setLoading(false));
  }, []);

  const subUrl = (format?: string, inboundId?: number, relayId?: number) => {
    if (!subPath || !data) return "";
    let u = `${window.location.origin}${subPath}?user=${data.userId}`;
    if (data.sig) u += `&sig=${data.sig}`;
    if (format) u += `&format=${format}`;
    if (inboundId) u += `&inbound=${inboundId}`;
    if (relayId) u += `&relay=${relayId}`;
    return u;
  };

  // 分配订阅的独立链接：path 自带 token，不带 ?user= 过滤。
  const assignedUrl = (s: MySubscriptionItem, format?: string) =>
    `${window.location.origin}${s.path}${format ? `?format=${format}` : ""}`;

  const handleCopy = async (url: string, key: string, label: string) => {
    const ok = await copyWithToast(url, `${label} 已复制`);
    if (ok) {
      setCopiedKey(key);
      setTimeout(() => setCopiedKey(null), 2000);
    }
  };

  const formatButtons = (
    keyPrefix: string,
    labelPrefix: string,
    urlFor: (format?: string) => string,
    primary: boolean,
    // 已停用/已过期/流量用完时传入原因：复制禁用、二维码点击给失效提示，
    // 不再把注定拉不到节点的死链接递到用户手里
    inactiveReason?: string,
  ) => (
    <div className="flex flex-wrap gap-2">
      {SUB_FORMATS.map((f) => {
        const key = `${keyPrefix}-${f.key || "auto"}`;
        const url = urlFor(f.key || undefined);
        const label = `${labelPrefix} ${f.label}`;
        return (
          <div key={key} className="flex items-center gap-1">
            <Button
              size="sm"
              variant="flat"
              color={copiedKey === key ? "success" : primary ? "primary" : "default"}
              onPress={() => handleCopy(url, key, label)}
              isDisabled={Boolean(inactiveReason)}
              startContent={
                copiedKey === key ? (
                  <Check className="w-3 h-3" />
                ) : (
                  <Copy className="w-3 h-3" />
                )
              }
            >
              {copiedKey === key ? "已复制" : primary ? `复制 ${f.label}` : f.label}
            </Button>
            {inactiveReason ? (
              <button
                type="button"
                title={`订阅${inactiveReason}，链接不可用`}
                className="inline-flex items-center justify-center w-10 h-10 rounded text-default-300 cursor-not-allowed"
                onClick={() =>
                  toast.error(`订阅${inactiveReason}，链接不可用`)
                }
              >
                <QrCode className="w-3.5 h-3.5" />
              </button>
            ) : (
              <QrCodeButton url={url} label={label} />
            )}
          </div>
        );
      })}
    </div>
  );

  if (loading) {
    return (
      <div className="p-6">
        <p className="text-sm text-default-500">加载中...</p>
      </div>
    );
  }

  const hasLegacy =
    data &&
    ((data.inbounds && data.inbounds.length > 0) ||
      (data.relays && data.relays.length > 0));

  if (assigned.length === 0 && !hasLegacy) {
    return (
      <div className="p-6">
        <Card>
          <CardBody className="py-12 text-center">
            <p className="text-default-500">暂无订阅</p>
            <p className="text-xs text-default-400 mt-1">请联系管理员为你分配订阅权限</p>
          </CardBody>
        </Card>
      </div>
    );
  }

  const inbounds = data?.inbounds ?? [];
  const relays = data?.relays ?? [];

  return (
    <div className="p-6 space-y-4 max-w-4xl mx-auto">
      <div>
        <h1 className="text-xl font-bold">我的订阅</h1>
        <p className="text-sm text-default-500 mt-1">
          管理员为你分配的代理订阅，点击复制链接导入客户端使用
        </p>
      </div>

      {/* 分配给我的订阅（独立链接） */}
      {assigned.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-base font-semibold">分配给我的订阅</h2>
          {assigned.map((s) => {
            const used = s.uploadBytes + s.downloadBytes;
            const expired = s.expireTime > 0 && s.expireTime <= Date.now();
            const inactiveReason = !s.enabled
              ? "已停用"
              : s.overQuota
                ? "流量已用完"
                : expired
                  ? "已过期"
                  : undefined;
            return (
              <Card key={`assigned-${s.id}`}>
                <CardBody className="space-y-2">
                  <div className="flex items-center justify-between gap-2">
                    <p className="font-medium">
                      {s.name}
                      {!s.enabled ? (
                        <Chip className="ml-2" size="sm" variant="flat">
                          已停用
                        </Chip>
                      ) : s.overQuota ? (
                        <Chip className="ml-2" color="danger" size="sm" variant="flat">
                          流量用完
                        </Chip>
                      ) : expired ? (
                        <Chip className="ml-2" color="danger" size="sm" variant="flat">
                          已过期
                        </Chip>
                      ) : null}
                    </p>
                    <span className="text-xs text-default-500">{s.nodeCount} 个节点</span>
                  </div>
                  <p className="text-xs text-default-500">
                    {s.trafficLimit > 0
                      ? `已用 ${formatTraffic(used)} / ${formatTraffic(s.trafficLimit)}`
                      : `已用 ${formatTraffic(used)} · 不限流量`}
                    {s.expireTime > 0 ? ` · ${dateText(s.expireTime)} 到期` : ""}
                  </p>
                  <div className="rounded-lg bg-default-50 p-3">
                    <p className="text-xs text-default-500 mb-2 break-all select-all">
                      {assignedUrl(s)}
                    </p>
                    {formatButtons(`assigned-${s.id}`, s.name, (f) => assignedUrl(s, f), true, inactiveReason)}
                  </div>
                  <button
                    aria-expanded={expandedTrafficId === s.id}
                    className="flex w-full items-center justify-between text-sm font-medium text-default-600"
                    type="button"
                    onClick={() =>
                      setExpandedTrafficId(
                        expandedTrafficId === s.id ? null : s.id,
                      )
                    }
                  >
                    该订阅流量统计
                    <ChevronDown
                      className={`h-4 w-4 transition-transform ${expandedTrafficId === s.id ? "rotate-180" : ""}`}
                    />
                  </button>
                  {expandedTrafficId === s.id && (
                    <SubTrafficStats mine id={s.id} />
                  )}
                </CardBody>
              </Card>
            );
          })}
        </div>
      )}

      {/* 旧个人链接（未分配订阅时的回退） */}
      {assigned.length === 0 && hasLegacy && (
        <>
      {/* 全部订阅 */}
      <Card>
        <CardHeader className="pb-2">
          <h2 className="text-base font-semibold">全部订阅（共 {inbounds.length + relays.length} 个节点）</h2>
        </CardHeader>
        <CardBody className="space-y-2">
          <div className="rounded-lg bg-default-50 p-3">
            <p className="text-xs text-default-500 mb-2 break-all select-all">
              {subUrl()}
            </p>
            <div className="flex flex-wrap gap-2">
              {SUB_FORMATS.map((f) => {
                const key = `all-${f.key || "auto"}`;
                const url = subUrl(f.key || undefined);
                const label = `全部订阅 ${f.label}`;
                return (
                  <div key={key} className="flex items-center gap-1">
                    <Button
                      size="sm"
                      variant="flat"
                      color={copiedKey === key ? "success" : "primary"}
                      onPress={() => handleCopy(url, key, label)}
                      isDisabled={!subPath}
                      startContent={
                        copiedKey === key ? (
                          <Check className="w-3 h-3" />
                        ) : (
                          <Copy className="w-3 h-3" />
                        )
                      }
                    >
                      {copiedKey === key ? "已复制" : `复制 ${f.label}`}
                    </Button>
                    <QrCodeButton url={url} label={label} />
                  </div>
                );
              })}
            </div>
          </div>
        </CardBody>
      </Card>

      {/* 按入站单独订阅 */}
      <div className="space-y-3">
        <h2 className="text-base font-semibold">按入站单独订阅</h2>
        {inbounds.map((inbound) => (
          <Card key={inbound.id}>
            <CardBody className="space-y-2">
              <div className="flex items-center justify-between">
                <div>
                  <p className="font-medium">{inbound.name}</p>
                  <p className="text-xs text-default-500">
                    {inbound.protocol} · {inbound.nodeName}
                  </p>
                </div>
              </div>
              <div className="rounded-lg bg-default-50 p-3">
                <p className="text-xs text-default-500 mb-2 break-all select-all">
                  {subUrl(undefined, inbound.id)}
                </p>
                <div className="flex flex-wrap gap-2">
                  {SUB_FORMATS.map((f) => {
                    const key = `${inbound.id}-${f.key || "auto"}`;
                    const url = subUrl(f.key || undefined, inbound.id);
                    const label = `${inbound.name} ${f.label}`;
                    return (
                      <div key={key} className="flex items-center gap-1">
                        <Button
                          size="sm"
                          variant="flat"
                          color={copiedKey === key ? "success" : "default"}
                          onPress={() => handleCopy(url, key, label)}
                          isDisabled={!subPath}
                          startContent={
                            copiedKey === key ? (
                              <Check className="w-3 h-3" />
                            ) : (
                              <Copy className="w-3 h-3" />
                            )
                          }
                        >
                          {copiedKey === key ? "已复制" : f.label}
                        </Button>
                        <QrCodeButton url={url} label={label} />
                      </div>
                    );
                  })}
                </div>
              </div>
            </CardBody>
          </Card>
        ))}
      </div>

      {/* 按中转单独订阅 */}
      {relays.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-base font-semibold">按中转单独订阅</h2>
          {relays.map((relay) => (
            <Card key={`relay-${relay.id}`}>
              <CardBody className="space-y-2">
                <div className="flex items-center justify-between">
                  <div>
                    <p className="font-medium">
                      {relay.name}
                      <span className="ml-2 text-xs font-normal text-default-500">中转</span>
                    </p>
                  </div>
                </div>
                <div className="rounded-lg bg-default-50 p-3">
                  <p className="text-xs text-default-500 mb-2 break-all select-all">
                    {subUrl(undefined, undefined, relay.id)}
                  </p>
                  <div className="flex flex-wrap gap-2">
                    {SUB_FORMATS.map((f) => {
                      const key = `relay-${relay.id}-${f.key || "auto"}`;
                      const url = subUrl(f.key || undefined, undefined, relay.id);
                      const label = `${relay.name} ${f.label}`;
                      return (
                        <div key={key} className="flex items-center gap-1">
                          <Button
                            size="sm"
                            variant="flat"
                            color={copiedKey === key ? "success" : "default"}
                            onPress={() => handleCopy(url, key, label)}
                            isDisabled={!subPath}
                            startContent={
                              copiedKey === key ? (
                                <Check className="w-3 h-3" />
                              ) : (
                                <Copy className="w-3 h-3" />
                              )
                            }
                          >
                            {copiedKey === key ? "已复制" : f.label}
                          </Button>
                          <QrCodeButton url={url} label={label} />
                        </div>
                      );
                    })}
                  </div>
                </div>
              </CardBody>
            </Card>
          ))}
        </div>
      )}
        </>
      )}
    </div>
  );
}
