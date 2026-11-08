import { useCallback, useEffect, useState } from "react";
import toast from "react-hot-toast";
import { Copy, Check, QrCode, ChevronDown, Search } from "lucide-react";

import { getMyInbounds, getMySubscriptions, getSubInfo } from "@/api";
import type { MySubscriptionItem } from "@/api";
import type { InboundApiItem } from "@/api/types";
import { copyWithToast } from "@/utils/clipboard";
import { SubFrontPicker } from "@/pages/subscription/front-picker";
import { Segmented } from "@/pages/subscription/controls";
import {
  GroupHeader,
  groupItems,
  groupKeyOf,
  groupLabelOf,
  groupRank,
  inboundMatchesSearch,
  inboundOriginOf,
  inboundSubtitleOf,
  type InboundOriginFilter,
} from "@/pages/inbound/group";
import { useSubscriptionFrontURLs } from "@/utils/subscription-front";
import { formatTraffic } from "@/utils/traffic";
import { QrCodeButton } from "@/pages/inbound/share";
import { panelOrigin } from "@/utils/panel-origin";
import { SubTrafficStats } from "@/pages/subscription/traffic-stats";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Input } from "@/shadcn-bridge/heroui/input";

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
  // 首屏关键接口（分配订阅 / 个人订阅）加载失败的文案：与「暂无订阅」
  // 空态区分，故障不得伪装成未分配（network 层失败是 resolve 非 0 码
  // 而非 reject，.catch 兜不住，必须按返回 code 判失败）。
  const [loadError, setLoadError] = useState<string | null>(null);
  const [copiedKey, setCopiedKey] = useState<string | null>(null);
  // 订阅前置地址（KU6）：选中的链接组装 base，"" = 面板域名；未配置
  // 前置时选择器不渲染，所有链接与此前逐字一致。
  const fronts = useSubscriptionFrontURLs();
  const [frontBase, setFrontBase] = useState("");
  // 展开了流量统计的分配订阅 id（同一时间只展开一条）。
  const [expandedTrafficId, setExpandedTrafficId] = useState<number | null>(
    null,
  );
  const [inboundSectionOpen, setInboundSectionOpen] = useState(true);
  const [inboundOriginFilter, setInboundOriginFilter] =
    useState<InboundOriginFilter>("all");
  const [inboundSearch, setInboundSearch] = useState("");
  const [inboundGroupOpenOverrides, setInboundGroupOpenOverrides] = useState<
    Record<string, boolean>
  >({});

  const load = useCallback(() => {
    setLoading(true);
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
        // 两个关键接口任一非 0 即加载失败（getSubInfo 的 403 静默豁免
        // 保持不变，不计入失败判定）。
        const failedMsg =
          assignedRes.code !== 0
            ? assignedRes.msg
            : myRes.code !== 0
              ? myRes.msg
              : "";

        if (assignedRes.code !== 0 || myRes.code !== 0) {
          setLoadError(failedMsg || "加载订阅信息失败");
          // 本次仍有可呈现内容（部分成功）时弹提示即可；无内容时由
          // 下方错误态整页呈现，不重复打扰。
          const myData =
            myRes.code === 0
              ? (myRes.data as
                  | { inbounds?: InboundApiItem[]; relays?: MyRelay[] }
                  | undefined)
              : undefined;
          const hasContent =
            (assignedRes.code === 0 &&
              (assignedRes.data?.subscriptions?.length ?? 0) > 0) ||
            (myData?.inbounds?.length ?? 0) > 0 ||
            (myData?.relays?.length ?? 0) > 0;

          if (hasContent) {
            toast.error(failedMsg || "加载订阅信息失败");
          }
        } else {
          setLoadError(null);
        }
      })
      .catch(() => {
        setLoadError("加载订阅信息失败");
        toast.error("加载订阅信息失败");
      })
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const subUrl = (format?: string, inboundId?: number, relayId?: number) => {
    if (!subPath || !data) return "";
    let u = `${frontBase || panelOrigin()}${subPath}?user=${data.userId}`;
    if (data.sig) u += `&sig=${data.sig}`;
    if (format) u += `&format=${format}`;
    if (inboundId) u += `&inbound=${inboundId}`;
    if (relayId) u += `&relay=${relayId}`;
    return u;
  };

  // 分配订阅的独立链接：path 自带 token，不带 ?user= 过滤。
  const assignedUrl = (s: MySubscriptionItem, format?: string) =>
    `${frontBase || panelOrigin()}${s.path}${format ? `?format=${format}` : ""}`;

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
                className="inline-flex items-center justify-center w-10 h-10 rounded text-foreground cursor-not-allowed"
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
        <p className="text-sm text-foreground">加载中...</p>
      </div>
    );
  }

  const hasLegacy =
    data &&
    ((data.inbounds && data.inbounds.length > 0) ||
      (data.relays && data.relays.length > 0));

  // 加载失败且无任何可呈现内容：错误态（带重试），不得落到「暂无订阅」
  // 空态误导用户去找管理员分配。
  if (loadError && assigned.length === 0 && !hasLegacy) {
    return (
      <div className="p-6">
        <Card>
          <CardBody className="py-12 text-center">
            <p className="text-danger-700">订阅信息加载失败</p>
            <p className="text-xs font-semibold text-foreground mt-1">{loadError}</p>
            <div className="mt-4">
              <Button color="primary" variant="flat" onPress={load}>
                重试
              </Button>
            </div>
          </CardBody>
        </Card>
      </div>
    );
  }

  if (assigned.length === 0 && !hasLegacy) {
    return (
      <div className="p-6">
        <Card>
          <CardBody className="py-12 text-center">
            <p className="text-foreground">暂无订阅</p>
            <p className="text-xs font-semibold text-foreground mt-1">请联系管理员为你分配订阅权限</p>
          </CardBody>
        </Card>
      </div>
    );
  }

  const inbounds = data?.inbounds ?? [];
  const relays = data?.relays ?? [];
  const localInboundCount = inbounds.filter((i) => !i.external).length;
  const importedInboundCount = inbounds.length - localInboundCount;
  const filteredInbounds = inbounds.filter(
    (inbound) =>
      (inboundOriginFilter === "all" ||
        inboundOriginOf(inbound) === inboundOriginFilter) &&
      inboundMatchesSearch(inbound, inboundSearch),
  );
  const inboundGroups = groupItems(filteredInbounds, groupKeyOf, (_key, i) =>
    groupLabelOf(i),
  ).sort((a, b) => groupRank(a.key) - groupRank(b.key));
  const showInboundGroups = inboundGroups.length > 1;
  const inboundGroupIsOpen = (key: string) =>
    inboundGroupOpenOverrides[key] ?? groupRank(key) === 0;
  const toggleInboundGroup = (key: string) =>
    setInboundGroupOpenOverrides((prev) => ({
      ...prev,
      [key]: !inboundGroupIsOpen(key),
    }));

  const renderInboundCard = (inbound: InboundApiItem) => (
    <Card key={inbound.id}>
      <CardBody className="space-y-2">
        <div className="flex items-center justify-between">
          <div>
            <p className="flex items-center gap-2 font-medium">
              <span>{inbound.name}</span>
              {inbound.external && (
                <Chip
                  color="warning"
                  size="sm"
                  title={
                    inbound.sourceId
                      ? `订阅源 ${inbound.sourceName || inbound.sourceId}`
                      : "手动导入"
                  }
                  variant="flat"
                >
                  导入
                </Chip>
              )}
            </p>
            <p className="text-xs font-semibold text-foreground">
              {inboundSubtitleOf(inbound)}
            </p>
          </div>
        </div>
        <div className="rounded-lg glass-inset p-3">
          <p className="text-xs font-semibold text-foreground mb-2 break-all select-all">
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
  );

  return (
    <div className="p-6 space-y-4 max-w-4xl mx-auto">
      <div>
        <h1 className="text-xl font-bold">我的订阅</h1>
        <p className="text-sm text-foreground mt-1">
          管理员为你分配的代理订阅，点击复制链接导入客户端使用
        </p>
      </div>

      {fronts.length > 0 && (
        <div className="flex justify-end">
          <SubFrontPicker
            fronts={fronts}
            value={frontBase}
            onChange={setFrontBase}
          />
        </div>
      )}

      {/* 分配给我的订阅（独立链接） */}
      {assigned.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-base font-semibold">分配给我的订阅</h2>
          {assigned.map((s) => {
            const used = s.uploadBytes + s.downloadBytes;
            const expired = s.expireTime > 0 && s.expireTime <= Date.now();
            const inactiveReason = s.pendingDelete
              ? "删除中"
              : !s.enabled
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
                      {s.pendingDelete ? (
                        <Chip className="ml-2" color="warning" size="sm" variant="flat">
                          删除中
                        </Chip>
                      ) : !s.enabled ? (
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
                    <span className="text-xs font-semibold text-foreground">{s.nodeCount} 个节点</span>
                  </div>
                  <p className="text-xs font-semibold text-foreground">
                    {s.trafficLimit > 0
                      ? `${dateText(s.trafficSince)} 起已用 ${formatTraffic(used)} / ${formatTraffic(s.trafficLimit)}`
                      : `${dateText(s.trafficSince)} 起已用 ${formatTraffic(used)} · 不限流量`}
                    {s.expireTime > 0 ? ` · ${dateText(s.expireTime)} 到期` : ""}
                  </p>
                  <div className="rounded-lg glass-inset p-3">
                    <p className="text-xs font-semibold text-foreground mb-2 break-all select-all">
                      {assignedUrl(s)}
                    </p>
                    {formatButtons(`assigned-${s.id}`, s.name, (f) => assignedUrl(s, f), true, inactiveReason)}
                  </div>
                  <button
                    aria-expanded={expandedTrafficId === s.id}
                    className="flex w-full items-center justify-between text-sm font-medium text-foreground"
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
          <div className="rounded-lg glass-inset p-3">
            <p className="text-xs font-semibold text-foreground mb-2 break-all select-all">
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
        <button
          aria-expanded={inboundSectionOpen}
          className="flex items-center gap-1 text-left"
          type="button"
          onClick={() => setInboundSectionOpen((open) => !open)}
        >
          <h2 className="text-base font-semibold">
            按入站单独订阅（共 {inbounds.length}）
          </h2>
          <ChevronDown
            className={`h-4 w-4 shrink-0 text-foreground transition-transform ${inboundSectionOpen ? "" : "-rotate-90"}`}
          />
        </button>
        {inboundSectionOpen && (
          <>
            {(localInboundCount > 0 && importedInboundCount > 0) ||
            inbounds.length > 8 ? (
              <div className="flex flex-col gap-2 lg:flex-row lg:items-center">
                {localInboundCount > 0 && importedInboundCount > 0 && (
                  <div className="lg:w-[22rem] lg:shrink-0">
                    <Segmented<InboundOriginFilter>
                      options={[
                        ["all", `全部 ${inbounds.length}`],
                        ["local", `本地创建 ${localInboundCount}`],
                        ["imported", `导入 ${importedInboundCount}`],
                      ]}
                      value={inboundOriginFilter}
                      onChange={setInboundOriginFilter}
                    />
                  </div>
                )}
                {inbounds.length > 8 && (
                  <Input
                    aria-label="搜索按入站单独订阅"
                    className="lg:flex-1"
                    placeholder="搜索名称、节点、协议、端口、备注或来源"
                    size="sm"
                    startContent={<Search className="h-4 w-4" />}
                    value={inboundSearch}
                    onChange={(e) => setInboundSearch(e.target.value)}
                  />
                )}
              </div>
            ) : null}
            {filteredInbounds.length === 0 ? (
              <Card>
                <CardBody>
                  <p className="text-sm font-semibold text-foreground">
                    没有匹配的入站
                  </p>
                </CardBody>
              </Card>
            ) : showInboundGroups ? (
              inboundGroups.map((group) => {
                const open = inboundGroupIsOpen(group.key);
                const rank = groupRank(group.key);

                return (
                  <div key={group.key} className="space-y-3">
                    <GroupHeader
                      collapsed={!open}
                      count={group.items.length}
                      extra={
                        <Chip
                          color={rank === 0 ? "default" : "warning"}
                          size="sm"
                          variant="flat"
                        >
                          {rank === 0
                            ? "本地创建"
                            : rank === 1
                              ? "手动导入"
                              : "订阅源导入"}
                        </Chip>
                      }
                      title={group.title}
                      unit="个入站"
                      onToggle={() => toggleInboundGroup(group.key)}
                    />
                    {open && group.items.map(renderInboundCard)}
                  </div>
                );
              })
            ) : (
              filteredInbounds.map(renderInboundCard)
            )}
          </>
        )}
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
                      <span className="ml-2 text-xs font-semibold text-foreground">中转</span>
                    </p>
                  </div>
                </div>
                <div className="rounded-lg glass-inset p-3">
                  <p className="text-xs font-semibold text-foreground mb-2 break-all select-all">
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
