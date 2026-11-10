import type {
  RemoteSourceItem,
  SubPipeline,
  SubTemplateItem,
  SubPreviewItem,
  SubscriptionDraft,
  SubscriptionItem,
} from "@/api";
import type { InboundApiItem, InboundRelayApiItem } from "@/api/types";

import { useCallback, useEffect, useState } from "react";
import toast from "react-hot-toast";
import { ChevronDown, Copy, QrCode } from "lucide-react";

import SubscriptionAssignSection from "./subscription/assign";

import {
  useIdemKey,
  useRotateIdemKeyOnCreateOpen,
  useTargetIdemKey,
} from "@/hooks/useIdemKey";
import {
  deleteSubscription,
  getInboundList,
  getInboundRelays,
  getSubscriptionTemplates,
  getRemoteSources,
  getSubscriptions,
  previewSubscription,
  resetSubscription,
  resetSubscriptionTraffic,
  saveSubscription,
} from "@/api";
import { AnimatedPage } from "@/components/animated-page";
import { PageLoadingState } from "@/components/page-state";
import { copyText, panelOrigin, QrImage } from "@/pages/inbound/share";
import { SubFrontPicker } from "@/pages/subscription/front-picker";
import { useSubscriptionFrontURLs } from "@/utils/subscription-front";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody } from "@/shadcn-bridge/heroui/card";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Input } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";
import { Switch } from "@/shadcn-bridge/heroui/switch";
import { formatTraffic } from "@/utils/traffic";
import { SUB_CLIENTS } from "@/pages/subscription/clients";
import { Segmented } from "@/pages/subscription/controls";
import { SubscriptionTemplatesModal } from "@/pages/subscription/templates";
import { SubTrafficStats } from "@/pages/subscription/traffic-stats";

const modalBase =
  "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden";

/** Protocols that can carry per-user credentials (mirrors the backend). */
const MULTI_USER_PROTOCOLS = new Set([
  "vless",
  "vmess",
  "trojan",
  "hysteria2",
  "tuic",
  "anytls",
  "naive",
]);

const EMPTY_PIPELINE: SubPipeline = {
  /** 中转范围三档（后端 lines 键）："" = 全部端点 / direct = 只要直连 / relay = 只要中转。 */
  lines: "",
  /** 订阅模板槽位 → 模板 ID；空 = 不用模板。 */
  templates: {},
};

function toDateInput(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  const pad = (n: number) => String(n).padStart(2, "0");

  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** The end of the chosen local day, so a link stays valid through it. */
function fromDateInput(value: string): number {
  if (!value) return 0;
  const [y, m, d] = value.split("-").map(Number);

  return new Date(y, m - 1, d, 23, 59, 59).getTime();
}

function expiryLabel(ms: number): string {
  if (!ms) return "永不过期";
  const days = Math.ceil((ms - Date.now()) / 86400000);

  if (days <= 0) return `${toDateInput(ms)} 已过期`;

  return `${toDateInput(ms)} 到期 · 剩 ${days} 天`;
}

/** Used / limit of a subscription, with a bar when it has a limit. */
function Usage({ s, compact }: { s: SubscriptionItem; compact?: boolean }) {
  const used = s.uploadBytes + s.downloadBytes;
  const pct = s.trafficLimit ? Math.min(100, (used / s.trafficLimit) * 100) : 0;
  const tone =
    pct >= 100 ? "bg-danger" : pct >= 90 ? "bg-warning" : "bg-primary";

  return (
    <div className={compact ? "space-y-1" : "space-y-1.5"}>
      <div className="flex items-baseline justify-between gap-2 text-xs font-semibold text-foreground">
        <span className="font-mono">
          {formatTraffic(used)}
          {s.trafficLimit
            ? ` / ${formatTraffic(s.trafficLimit)}`
            : " · 不限流量"}
        </span>
        {!compact && (
          <span>{s.resetDay ? `每月 ${s.resetDay} 号重置` : "不自动重置"}</span>
        )}
      </div>
      {s.trafficLimit > 0 && (
        <div className="h-1.5 overflow-hidden rounded-full bg-default-100">
          <div
            className={`h-full rounded-full ${tone}`}
            style={{ width: `${Math.max(2, pct)}%` }}
          />
        </div>
      )}
      {!compact && (
        <div className="font-mono text-xs font-semibold text-foreground">
          ↑ {formatTraffic(s.uploadBytes)} · ↓ {formatTraffic(s.downloadBytes)}
        </div>
      )}
    </div>
  );
}

function Section({
  title,
  hint,
  summary,
  collapsible,
  defaultOpen = true,
  children,
}: {
  title: string;
  hint?: string;
  /** Shown next to the title while collapsed. */
  summary?: string;
  collapsible?: boolean;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const shown = !collapsible || open;

  return (
    <section className="space-y-3 border-t border-divider pt-4 first:border-t-0 first:pt-0">
      {collapsible ? (
        <button
          aria-expanded={open}
          className="flex w-full items-start gap-2 text-left"
          type="button"
          onClick={() => setOpen((v) => !v)}
        >
          <div className="min-w-0 flex-1">
            <h3 className="text-sm font-semibold">
              {title}
              {!open && summary && (
                <span className="ml-2 text-xs font-semibold text-primary-700">
                  {summary}
                </span>
              )}
            </h3>
            {open && hint && <p className="text-xs font-semibold text-foreground">{hint}</p>}
          </div>
          <ChevronDown
            className={`mt-0.5 h-4 w-4 shrink-0 text-foreground transition-transform ${open ? "rotate-180" : ""}`}
          />
        </button>
      ) : (
        <div>
          <h3 className="text-sm font-semibold">{title}</h3>
          {hint && <p className="text-xs font-semibold text-foreground">{hint}</p>}
        </div>
      )}
      {shown && children}
    </section>
  );
}

interface Draft extends SubscriptionDraft {
  expire: string;
  /** Traffic limit typed in GB ("" = unlimited). */
  limitGB: string;
}

const GB = 1024 ** 3;

function gbText(bytes: number): string {
  if (!bytes) return "";
  const v = bytes / GB;

  return String(Number.isInteger(v) ? v : Number(v.toFixed(2)));
}

function draftFrom(s?: SubscriptionItem): Draft {
  if (!s) {
    return {
      name: "",
      // New subscriptions get their own credentials, so any of them can
      // expire, be capped or cut off; older own-credential ones keep working.
      kind: "share",
      sourceAll: true,
      inboundIds: [],
      sourceIds: [],
      options: { ...EMPTY_PIPELINE },
      expireTime: 0,
      expire: "",
      enabled: true,
      remark: "",
      trafficLimit: 0,
      limitGB: "",
      resetDay: 0,
    };
  }

  // 中转范围「自定义」：存量非空 relayIds 原样带入（打开即自定义模式，
  // 勾选集与存储逐一相同）；空/缺省则不带，免得把三档订阅误开成自定义。
  const storedRelayIds = Array.from(
    new Set((s.options?.relayIds ?? []).filter((id) => id >= 0)),
  );

  return {
    id: s.id,
    name: s.name,
    kind: s.kind,
    sourceAll: s.sourceAll,
    inboundIds: s.inboundIds,
    sourceIds: s.sourceIds ?? [],
    options: {
      ...EMPTY_PIPELINE,
      ...s.options,
      lines:
        s.options?.lines === "direct" || s.options?.lines === "relay"
          ? s.options.lines
          : "",
      relayIds: storedRelayIds.length ? storedRelayIds : undefined,
    },
    expireTime: s.expireTime,
    expire: toDateInput(s.expireTime),
    enabled: s.enabled,
    remark: s.remark,
    trafficLimit: s.trafficLimit,
    limitGB: gbText(s.trafficLimit),
    resetDay: s.resetDay,
  };
}

/** The API payload: the backend rejects unknown fields like `expire`. */
function toPayload(d: Draft): SubscriptionDraft {
  const { expire, limitGB, ...rest } = d;
  const gb = Number(limitGB);

  return {
    ...rest,
    kind: "share",
    expireTime: fromDateInput(expire),
    trafficLimit: gb > 0 ? Math.round(gb * GB) : 0,
    resetDay: d.resetDay,
  };
}

function Editor({
  draft,
  setDraft,
  inbounds,
  sources,
  saving,
  onSave,
  onClose,
}: {
  draft: Draft;
  setDraft: (d: Draft) => void;
  inbounds: InboundApiItem[];
  sources: RemoteSourceItem[];
  saving: boolean;
  onSave: () => void;
  onClose: () => void;
}) {
  const [preview, setPreview] = useState<{
    items: SubPreviewItem[];
    total: number;
    dropped: number;
    excludedCompanionIds?: number[];
  } | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [templates, setTemplates] = useState<SubTemplateItem[]>([]);

  useEffect(() => {
    getSubscriptionTemplates()
      .then((res) => {
        if (res.code === 0) setTemplates(res.data ?? []);
      })
      .catch(() => {});
  }, []);
  const o = draft.options;
  const setOpts = (patch: Partial<SubPipeline>) => {
    setDraft({ ...draft, options: { ...o, ...patch } });
    setPreview(null);
  };
  const set = (patch: Partial<Draft>) => {
    setDraft({ ...draft, ...patch });
    setPreview(null);
  };

  const selfBuilt = inbounds.filter((i) => !i.external);
  // Nodes of a remote source are picked as the whole source.
  const imported = inbounds.filter((i) => i.external && !i.sourceId);
  const sharedPicked = inbounds.filter(
    (i) =>
      (draft.sourceAll
        ? i.enabled
        : draft.inboundIds.includes(i.id) ||
          draft.sourceIds.includes(i.sourceId ?? 0)) &&
      (i.external || !MULTI_USER_PROTOCOLS.has(i.protocol)),
  ).length;

  // 中转范围「自定义」：每个入站的中转线路（逐端点勾选的数据源）。
  const [relaysByInbound, setRelaysByInbound] = useState<
    Record<number, InboundRelayApiItem[]>
  >({});

  useEffect(() => {
    let active = true;

    Promise.all(
      inbounds.map((i) =>
        getInboundRelays(i.id).then((r) => ({
          id: i.id,
          relays: r.code === 0 ? (r.data ?? []) : [],
        })),
      ),
    ).then((results) => {
      if (!active) return;
      const map: Record<number, InboundRelayApiItem[]> = {};

      for (const { id, relays } of results) map[id] = relays;
      setRelaysByInbound(map);
    });

    return () => {
      active = false;
    };
  }, [inbounds]);

  // 当前订阅覆盖的入站（全部落地 = 全部入站；指定落地 = 已勾选的
  // 入站 + 订阅源派生的入站，与 sharedPicked 同口径）。
  const scopeInbounds = draft.sourceAll
    ? inbounds
    : inbounds.filter(
        (i) =>
          draft.inboundIds.includes(i.id) ||
          draft.sourceIds.includes(i.sourceId ?? 0),
      );

  // 覆盖范围内的全部端点 ID：0 = 直连（任一入站可直连时纳入），
  // >0 = 中转线路 ID。relayIds 是全局端点集（0 对所选落地整体生效）。
  const allEndpointIds = () => {
    const ids = new Set<number>();

    for (const inbound of scopeInbounds) {
      if (!inbound.relayOnly) ids.add(0);
      for (const relay of relaysByInbound[inbound.id] ?? []) ids.add(relay.id);
    }

    return Array.from(ids);
  };

  // 当前档位实际生效的端点：进入「自定义」时的预填集。
  const effectiveEndpointIds = () =>
    allEndpointIds().filter((id) =>
      o.lines === "direct" ? id === 0 : o.lines === "relay" ? id > 0 : true,
    );

  // 自定义模式 = relayIds 键在场（哪怕暂时勾成空集）；三档 = 键缺省。
  const relayCustom = o.relayIds !== undefined;
  const endpointChecked = (id: number) => (o.relayIds ?? []).includes(id);
  const toggleEndpoint = (id: number) => {
    const cur = o.relayIds ?? [];

    setOpts({
      relayIds: cur.includes(id) ? cur.filter((x) => x !== id)
        : [...cur, id],
    });
  };
  const setScope = (v: "" | "direct" | "relay" | "custom") => {
    if (v === "custom") setOpts({ relayIds: effectiveEndpointIds() });
    // 切回三档必须摘掉 relayIds：非空白名单在后端优先于 lines，
    // 留着会让档位形同虚设。
    else setOpts({ lines: v, relayIds: undefined });
  };

  const runPreview = async () => {
    setPreviewing(true);
    try {
      const res = await previewSubscription(toPayload(draft));

      if (res.code !== 0) {
        toast.error(res.msg || "预览失败");

        return;
      }
      setPreview(res.data);
    } finally {
      setPreviewing(false);
    }
  };

  const pickList = (title: string, list: InboundApiItem[]) =>
    list.length > 0 && (
      <div className="space-y-1">
        <p className="text-xs font-semibold text-foreground">{title}</p>
        {list.map((i) => (
          <Checkbox
            key={i.id}
            className="w-full"
            isSelected={draft.inboundIds.includes(i.id)}
            onValueChange={(on) =>
              set({
                inboundIds: on
                  ? [...draft.inboundIds, i.id]
                  : draft.inboundIds.filter((x) => x !== i.id),
              })
            }
          >
            <span className="text-sm">{i.name}</span>
            {!i.enabled && (
              <span className="ml-1 text-xs font-semibold text-foreground">已停用</span>
            )}
            {(i.external || !MULTI_USER_PROTOCOLS.has(i.protocol)) && (
              <span className="ml-1 text-xs font-semibold text-warning-700">共用凭据</span>
            )}
          </Checkbox>
        ))}
      </div>
    );

  return (
    <>
      <ModalHeader>
        <h2 className="text-lg font-bold">
          {draft.id ? "编辑订阅" : "新建订阅"}
        </h2>
      </ModalHeader>
      <ModalBody className="space-y-4">
        <Section title="基本">
          <Input
            label="名称"
            placeholder="例如 我的手机 / 小明"
            value={draft.name}
            variant="bordered"
            onChange={(e) => set({ name: e.target.value })}
          />
          <p className="text-xs font-semibold text-foreground">
            有独立的 UUID /
            密码：停用、到期或流量用完后从节点上删除，已导入的配置也连不上
          </p>
          <div className="space-y-1">
            <label className="text-sm font-medium" htmlFor="sub-expire">
              到期日期
            </label>
            <div className="flex gap-2">
              <input
                className="h-10 min-w-0 flex-1 rounded-lg border border-divider bg-content1 px-2 text-sm"
                id="sub-expire"
                type="date"
                value={draft.expire}
                onChange={(e) => set({ expire: e.target.value })}
              />
              {[30, 90, 365].map((d) => (
                <Button
                  key={d}
                  className="min-w-0 px-2"
                  size="sm"
                  variant="flat"
                  onPress={() =>
                    set({ expire: toDateInput(Date.now() + d * 86400000) })
                  }
                >
                  {d === 365 ? "1年" : `${d}天`}
                </Button>
              ))}
            </div>
            <p className="text-xs font-semibold text-foreground">
              {draft.expire ? "到期当天 23:59 后断开" : "留空表示永不过期"}
            </p>
          </div>
          <div className="space-y-1">
            <label className="text-sm font-medium" htmlFor="sub-limit">
              流量上限
            </label>
            <div className="flex gap-2">
              <div className="relative min-w-0 flex-1">
                <input
                  className="h-10 w-full rounded-lg border border-divider bg-content1 px-2 pr-9 text-sm"
                  id="sub-limit"
                  inputMode="decimal"
                  placeholder="不限"
                  value={draft.limitGB}
                  onChange={(e) =>
                    set({ limitGB: e.target.value.replace(/[^\d.]/g, "") })
                  }
                />
                <span className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-xs font-semibold text-foreground">
                  GB
                </span>
              </div>
              {[50, 100, 500].map((g) => (
                <Button
                  key={g}
                  className="min-w-0 px-2"
                  size="sm"
                  variant="flat"
                  onPress={() => set({ limitGB: String(g) })}
                >
                  {g}G
                </Button>
              ))}
            </div>
            <div className="flex items-center justify-between gap-2 pt-1">
              <label className="text-sm" htmlFor="sub-reset-day">
                每月重置
              </label>
              <select
                className="h-9 rounded-lg border border-divider bg-content1 px-2 text-sm"
                id="sub-reset-day"
                value={draft.resetDay}
                onChange={(e) => set({ resetDay: Number(e.target.value) })}
              >
                <option value={0}>不重置</option>
                {Array.from({ length: 28 }, (_, i) => i + 1).map((d) => (
                  <option key={d} value={d}>
                    每月 {d} 号
                  </option>
                ))}
              </select>
            </div>
            <p className="text-xs font-semibold text-foreground">
              上传 +
              下载合计，用完自动断开；在落地节点上按用户统计，仅支持多用户协议
            </p>
          </div>
        </Section>

        <Section hint="中转线路会跟着它的落地一起出现" title="来源">
          <Segmented
            options={[
              ["all", "全部落地"],
              ["pick", "指定落地"],
            ]}
            value={draft.sourceAll ? "all" : "pick"}
            onChange={(v) => set({ sourceAll: v === "all" })}
          />
          {draft.sourceAll ? (
            <p className="text-xs font-semibold text-foreground">
              {/* F3 S-004：此前提示「用下面的过滤」指向不存在的位置；
                  选子集的实际入口是上方切换到「指定落地」 */}
              包括以后新增的落地；只想要一部分时，切换到「指定落地」逐个勾选
            </p>
          ) : (
            <div className="max-h-56 space-y-3 overflow-y-auto rounded-xl border border-divider p-2">
              {pickList("自建", selfBuilt)}
              {pickList("导入", imported)}
              {sources.length > 0 && (
                <div className="space-y-1">
                  <p className="text-xs font-semibold text-foreground">
                    订阅源（整个源，更新后的节点自动跟上）
                  </p>
                  {sources.map((src) => (
                    <Checkbox
                      key={src.id}
                      className="w-full"
                      isSelected={draft.sourceIds.includes(src.id)}
                      onValueChange={(on) =>
                        set({
                          sourceIds: on
                            ? [...draft.sourceIds, src.id]
                            : draft.sourceIds.filter((x) => x !== src.id),
                        })
                      }
                    >
                      <span className="text-sm">{src.name}</span>
                      <span className="ml-1 text-xs font-semibold text-foreground">
                        {src.nodeCount} 个节点
                      </span>
                    </Checkbox>
                  ))}
                </div>
              )}
            </div>
          )}
          {sharedPicked > 0 && (
            <p className="text-xs font-semibold text-warning-700">
              其中 {sharedPicked} 个是导入的节点或
              Shadowsocks，没法单独分配凭据：对方拿到共用的密码，停用后订阅失效，但已导入的配置仍能连接。
            </p>
          )}
        </Section>

        <Section
          collapsible
          defaultOpen={o.lines !== "" || relayCustom}
          hint={
            relayCustom
              ? "自定义＝只保留勾选的端点；「直连」对所选落地整体生效。勾空后保存将回到当前档位范围"
              : "全部＝直连和所有中转都包含；只要中转＝只保留经中转的线路；只要直连＝只保留直连线路；自定义＝逐个勾选端点"
          }
          summary={
            relayCustom
              ? `自定义 · 已选 ${(o.relayIds ?? []).length} 个端点`
              : o.lines === "relay"
                ? "只要中转"
                : o.lines === "direct"
                  ? "只要直连"
                  : "全部端点"
          }
          title="中转范围"
        >
          <Segmented
            options={[
              ["", "全部"],
              ["relay", "只要中转"],
              ["direct", "只要直连"],
              ["custom", "自定义"],
            ]}
            value={relayCustom ? "custom" : o.lines}
            onChange={setScope}
          />
          {relayCustom &&
            (scopeInbounds.length === 0 ? (
              <p className="text-xs font-semibold text-foreground">
                先在「来源」里选择落地，这里才能挑端点
              </p>
            ) : (
              <div className="max-h-56 space-y-3 overflow-y-auto rounded-xl border border-divider p-2">
                {scopeInbounds.map((inbound) => {
                  const relays = relaysByInbound[inbound.id] ?? [];

                  if (inbound.relayOnly && relays.length === 0) return null;

                  return (
                    <div key={inbound.id} className="space-y-1">
                      <p className="text-xs font-semibold text-foreground">
                        {inbound.name}
                        <span className="ml-1 text-foreground">
                          {inbound.protocol} · {inbound.nodeName}
                        </span>
                      </p>
                      {!inbound.relayOnly && (
                        <Checkbox
                          className="w-full"
                          isSelected={endpointChecked(0)}
                          onValueChange={() => toggleEndpoint(0)}
                        >
                          <span className="text-sm">直连</span>
                        </Checkbox>
                      )}
                      {relays.map((relay) => (
                        <Checkbox
                          key={relay.id}
                          className="w-full"
                          isSelected={endpointChecked(relay.id)}
                          onValueChange={() => toggleEndpoint(relay.id)}
                        >
                          <span className="text-sm">
                            {relay.name || `中转 ${relay.id}`}
                          </span>
                          <span className="ml-1 text-xs font-semibold text-foreground">
                            中转
                          </span>
                        </Checkbox>
                      ))}
                    </div>
                  );
                })}
              </div>
            ))}
        </Section>

        <Section
          collapsible
          defaultOpen={Object.keys(o.templates ?? {}).length > 0}
          summary={
            Object.keys(o.templates ?? {}).length
              ? `已选 ${Object.keys(o.templates ?? {}).length} 个`
              : "未设置"
          }
          title="订阅模板"
        >
          <div className="space-y-1.5">
            <p className="text-xs font-semibold">订阅模板</p>
            <p className="text-xs font-semibold text-foreground">
              选了模板的客户端按模板骨架下发（面板只填节点）；
              没选的照旧。在订阅页右上角「模板」里管理模板。
            </p>
            {(
              [
                ["clash", "Mihomo", "clash"],
                ["singbox", "sing-box", "singbox"],
                ["surge", "Surge", "surge"],
                ["stash", "Stash", "clash"],
                ["shadowrocket", "Shadowrocket", "clash"],
              ] as Array<[string, string, string]>
            ).map(([slot, label, kind]) => (
              <div
                key={slot}
                className="flex items-center justify-between gap-2"
              >
                <label className="text-sm" htmlFor={`sub-tpl-${slot}`}>
                  {label}
                </label>
                <select
                  className="h-9 max-w-56 rounded-lg border border-divider bg-content1 px-2 text-sm"
                  id={`sub-tpl-${slot}`}
                  value={o.templates?.[slot] ?? 0}
                  onChange={(e) => {
                    const id = Number(e.target.value);
                    const next = { ...(o.templates ?? {}) };

                    if (id > 0) next[slot] = id;
                    else delete next[slot];
                    setOpts({ templates: next });
                  }}
                >
                  <option value={0}>不用模板</option>
                  {templates
                    .filter((t) => t.kind === kind)
                    .map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name}
                      </option>
                    ))}
                </select>
              </div>
            ))}
          </div>
        </Section>

        <Section title="预览">
          <Button
            className="w-full"
            isLoading={previewing}
            variant="flat"
            onPress={runPreview}
          >
            看看结果
          </Button>
          {preview && (
            <>
              <p className="text-xs font-semibold text-foreground">
                共 {preview.items.length} 个节点
                {preview.dropped > 0 && `，未纳入 ${preview.dropped} 个`}
              </p>
              {preview.excludedCompanionIds &&
                preview.excludedCompanionIds.length > 0 && (
                  <p className="rounded-lg bg-warning-500/10 px-3 py-2 text-xs font-semibold text-warning-700">
                    另有 {preview.excludedCompanionIds.length}
                    个伴生入站被排除（ID：
                    {preview.excludedCompanionIds.join("、")}
                    ）：伴生入站不能作为订阅源，不会出现在订阅中。此排除不计入上面的未纳入数。
                  </p>
                )}
              <div className="max-h-64 overflow-y-auto rounded-xl border border-divider">
                {preview.items.length === 0 ? (
                  <p className="p-3 text-xs font-semibold text-foreground">没有节点</p>
                ) : (
                  preview.items.map((p, i) => (
                    <div
                      key={`${p.original}-${i}`}
                      className="flex items-center gap-2 border-b border-divider px-3 py-1.5 text-xs font-semibold last:border-b-0"
                    >
                      <span className="min-w-0 flex-1 truncate">
                        {p.name}
                        {p.name !== p.original && (
                          <span className="text-foreground">
                            {" "}
                            ← {p.original}
                          </span>
                        )}
                      </span>
                      {(p.own || p.sourceId) && (
                        <span className="max-w-28 shrink-0 truncate text-foreground">
                          {p.own
                            ? "自建"
                            : (sources.find((s) => s.id === p.sourceId)?.name ??
                              "订阅源")}
                        </span>
                      )}
                      <span className="shrink-0 text-foreground">
                        {p.line === "relay" ? "中转" : "直连"}
                      </span>
                    </div>
                  ))
                )}
              </div>
            </>
          )}
        </Section>

        <Input
          label="备注"
          placeholder="可选"
          value={draft.remark}
          variant="bordered"
          onChange={(e) => set({ remark: e.target.value })}
        />
      </ModalBody>
      <ModalFooter>
        <Button variant="light" onPress={onClose}>
          取消
        </Button>
        <Button color="primary" isLoading={saving} onPress={onSave}>
          保存
        </Button>
      </ModalFooter>
    </>
  );
}

// R10-02：删除墓碑行（后端 subscriptionView.pendingDelete）——列表呈
// 「删除中」态且开关禁用，避免管理员对残留行再点启用。后端共享类型
// 在 api/index.ts，此处只做页面局部扩展，不改共享定义。
type SubscriptionRow = SubscriptionItem & { pendingDelete?: boolean };

export default function SubscriptionPage() {
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState<"manage" | "assign">("manage");
  const [templatesOpen, setTemplatesOpen] = useState(false);
  const [subs, setSubs] = useState<SubscriptionRow[]>([]);
  const [inbounds, setInbounds] = useState<InboundApiItem[]>([]);
  const [sources, setSources] = useState<RemoteSourceItem[]>([]);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [saving, setSaving] = useState(false);
  const [detail, setDetail] = useState<SubscriptionRow | null>(null);
  // 详情弹窗内展开的订阅二维码（按订阅 id 记，切换订阅自动收起）
  const [qrDetailId, setQrDetailId] = useState<number | null>(null);
  const [confirm, setConfirm] = useState<"reset" | "delete" | null>(null);
  const [toggling, setToggling] = useState<number | null>(null);
  const [resettingTraffic, setResettingTraffic] = useState(false);
  // 订阅前置地址（KU6）：选中的链接组装 base，"" = 面板域名。未配置
  // 前置时 fronts 为空、选择器不渲染，url() 与此前逐字一致。
  const fronts = useSubscriptionFrontURLs();
  const [frontBase, setFrontBase] = useState("");

  const load = useCallback(async () => {
    try {
      const [subRes, inRes, srcRes] = await Promise.all([
        getSubscriptions(),
        getInboundList(),
        getRemoteSources(),
      ]);

      if (srcRes.code === 0) setSources(srcRes.data ?? []);

      if (subRes.code === 0 && subRes.data) {
        setSubs(subRes.data.items ?? []);
      } else {
        toast.error(subRes.msg || "加载订阅失败");
      }
      if (inRes.code === 0) setInbounds(inRes.data ?? []);
      // 落地/订阅源是编辑器 picker 的数据源：失败静默装空会让管理员误判
      // 「什么都没选」甚至误改后保存（D5-11）。订阅本体 draft 内嵌列表项
      // 不受影响，此处只补失败提示，不改数据。
      if (inRes.code !== 0 || srcRes.code !== 0) {
        const failedParts = [
          inRes.code !== 0 ? "落地列表" : null,
          srcRes.code !== 0 ? "订阅源" : null,
        ].filter(Boolean);

        toast.error(
          `${failedParts.join("与")}加载失败，编辑器中的可选列表可能不完整，请刷新重试`,
        );
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const url = (s: SubscriptionItem, format = "") =>
    `${frontBase || panelOrigin()}${s.path}${format ? `?format=${format}` : ""}`;

  const showResult = (msg: string | undefined, ok: string) => {
    if (msg && msg !== "操作成功") toast(msg, { icon: "⚠️" });
    else toast.success(ok);
  };

  // R2X-004：订阅新建表单幂等键（同表单复用、成功轮换；更新不带键）。
  const { idemKey, rotateIdemKey } = useIdemKey();
  // C1-06：重置订阅（凭据轮换）的幂等键，按订阅目标绑定——超时
  // 重试复用同键，后端回放首轮回执（含新 token）不二次轮换。
  const {
    keyFor: resetKeyFor,
    clearTargetIdemKey: clearResetKey,
  } = useTargetIdemKey();

  // R3X-027：创建弹窗打开（上升沿且新建模式）时轮换幂等键；窗内重试仍复用同键。
  useRotateIdemKeyOnCreateOpen(
    Boolean(draft),
    Boolean(draft) && !draft?.id,
    rotateIdemKey,
  );

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const res = await saveSubscription(
        toPayload(draft),
        draft.id ? undefined : idemKey,
      );

      if (res.code !== 0) {
        toast.error(res.msg || "保存失败");

        return;
      }
      if (!draft.id) rotateIdemKey();
      showResult(
        res.msg,
        draft.id ? "已保存" : "已创建，复制链接导入客户端即可",
      );
      // RR-C5-03（B10-002）：保存校验排除的伴生入站在回执中点名，
      // 保存当刻必须可见——不能等重开编辑器才发现源缺失且无原因。
      // （启用/停用切换不经此路，避免对同一批既存排除反复告警。）
      const excluded = res.data?.excludedCompanionIds ?? [];

      if (excluded.length > 0) {
        toast(
          `有 ${excluded.length} 个伴生入站被排除（ID：${excluded.join("、")}）：伴生入站不能作为订阅源，未纳入此订阅`,
          { icon: "⚠️", duration: 8000 },
        );
      }
      setDraft(null);
      await load();
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (s: SubscriptionItem) => {
    setToggling(s.id);
    try {
      const res = await saveSubscription(
        toPayload({ ...draftFrom(s), enabled: !s.enabled }),
      );

      if (res.code !== 0) toast.error(res.msg || "操作失败");
      else showResult(res.msg, s.enabled ? "已停用" : "已启用");
      await load();
    } finally {
      setToggling(null);
    }
  };

  const resetTraffic = async () => {
    if (!detail) return;
    setResettingTraffic(true);
    try {
      const res = await resetSubscriptionTraffic(detail.id);

      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "操作失败");

        return;
      }
      toast.success(detail.overQuota ? "流量已重置，已恢复连接" : "流量已重置");
      setDetail(res.data);
      await load();
    } finally {
      setResettingTraffic(false);
    }
  };

  const runConfirm = async () => {
    if (!detail || !confirm || saving) return;
    setSaving(true);
    try {
      const res =
        confirm === "reset"
          ? await resetSubscription(detail.id, resetKeyFor(detail.id))
          : await deleteSubscription(detail.id);

      if (res.code !== 0) {
        toast.error(res.msg || "操作失败");

        return;
      }
      if (confirm === "reset") clearResetKey();
      showResult(
        res.msg,
        confirm === "reset" ? "已重置，旧链接立即失效" : "已删除",
      );
      setConfirm(null);
      setDetail(null);
      await load();
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <PageLoadingState message="正在加载..." />;

  const card = (s: SubscriptionRow) => (
    <div
      key={s.id}
      className="glass-card flex cursor-pointer items-center gap-3 rounded-xl px-3 py-2.5 transition-colors hover:bg-white/40"
      role="button"
      tabIndex={0}
      onClick={() => setDetail(s)}
      onKeyDown={(e) => {
        if (e.target !== e.currentTarget) return;
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          setDetail(s);
        }
      }}
    >
      <span
        className={`h-2 w-2 shrink-0 rounded-full ${s.active ? "bg-success" : "bg-default-300"}`}
      />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-1.5">
          <span className="truncate font-medium">{s.name}</span>
          {s.isDefault && (
            <Chip color="primary" size="sm" variant="flat">
              默认 · 个人链接
            </Chip>
          )}
          {s.meteringUnsupported && (
            <Chip color="warning" size="sm" variant="flat">
              流量永不计量
            </Chip>
          )}
          {s.pendingDelete ? (
            <Chip color="warning" size="sm" variant="flat">
              删除中
            </Chip>
          ) : !s.enabled ? (
            <Chip size="sm" variant="flat">
              已停用
            </Chip>
          ) : (
            !s.active && (
              <Chip color="danger" size="sm" variant="flat">
                {s.overQuota ? "流量用完" : "已过期"}
              </Chip>
            )
          )}
        </div>
        <div className="truncate text-xs font-semibold text-foreground">
          {s.nodeCount} 个节点
          {" · "}
          {s.sourceAll
            ? "全部落地"
            : [
                s.inboundIds.length ? `${s.inboundIds.length} 个落地` : "",
                s.sourceIds?.length ? `${s.sourceIds.length} 个订阅源` : "",
              ]
                .filter(Boolean)
                .join(" + ")}
          {s.expireTime ? ` · ${expiryLabel(s.expireTime)}` : ""}
          {s.remark ? ` · ${s.remark}` : ""}
        </div>
        <div className="mt-1.5">
          <Usage compact s={s} />
        </div>
      </div>
      <div
        className="flex shrink-0 items-center gap-2"
        role="presentation"
        onClick={(e) => e.stopPropagation()}
      >
        <Button
          isIconOnly
          aria-label={`复制 ${s.name} 的订阅链接`}
          size="sm"
          variant="flat"
          onPress={() => copyText(url(s), "订阅链接已复制")}
        >
          <Copy className="h-4 w-4" />
        </Button>
        <Switch
          aria-label={`${s.enabled ? "停用" : "启用"} ${s.name}`}
          isDisabled={toggling === s.id || !!s.pendingDelete}
          isSelected={s.enabled}
          size="sm"
          onValueChange={() => toggle(s)}
        />
      </div>
    </div>
  );

  return (
    <AnimatedPage className="space-y-5 px-3 py-6 lg:px-6">
      <div className="flex items-center justify-between gap-2">
        <div>
          <h1 className="text-lg font-semibold">订阅</h1>
          <p className="text-xs font-semibold text-foreground">
            {/* C3-02：副标题此前宣称「过滤、改名」——两项均为 2.1.2
                已删除的订阅加工功能，文案对齐现行功能面 */}
            每个订阅一个链接：选落地与中转范围、分配给用户，客户端自动识别格式
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button
            className="font-semibold"
            size="sm"
            variant="flat"
            onPress={() => setTemplatesOpen(true)}
          >
            模板
          </Button>
          <Button
            className="font-semibold"
            color="primary"
            size="sm"
            variant="solid"
            onPress={() => setDraft(draftFrom())}
          >
            新建订阅
          </Button>
        </div>
      </div>

      {/* 订阅管理 / 订阅分配 Tab */}
      <div className="glass-surface grid grid-cols-2 gap-1 rounded-xl p-1">
        {(
          [
            { key: "manage", label: "订阅管理" },
            { key: "assign", label: "订阅分配" },
          ] as const
        ).map((t) => (
          <button
            key={t.key}
            className={`rounded-lg px-3 py-2 text-sm font-medium transition-all ${
              tab === t.key
                ? "bg-tray shadow-sm"
                : "text-foreground hover:text-foreground"
            }`}
            onClick={() => setTab(t.key)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === "manage" && fronts.length > 0 && (
        <div className="flex justify-end">
          <SubFrontPicker
            fronts={fronts}
            value={frontBase}
            onChange={setFrontBase}
          />
        </div>
      )}

      {tab === "assign" ? <SubscriptionAssignSection /> : <></>}
      {tab === "manage" && (
        <>
          {inbounds.length === 0 && (
            <Card className="shadow-sm">
              <CardBody className="py-6 text-center text-sm text-foreground">
                还没有落地。先在“落地”里搭建或导入节点，订阅里才会有内容。
              </CardBody>
            </Card>
          )}

          {subs.length === 0 ? (
            <p className="rounded-xl border border-dashed border-divider px-3 py-4 text-center text-xs font-semibold text-foreground">
              每个订阅一个链接，有独立凭据：可以设到期和流量上限，停用后自动断开
            </p>
          ) : (
            <div className="grid grid-cols-1 gap-2 2xl:grid-cols-2">
              {subs.map(card)}
            </div>
          )}
        </>
      )}

      {/* Detail: import into a client, copy links, actions. */}
      <Modal
        backdrop="blur"
        classNames={{ base: modalBase }}
        isOpen={Boolean(detail) && !confirm}
        placement="center"
        scrollBehavior="inside"
        onOpenChange={(open) => !open && setDetail(null)}
      >
        <ModalContent>
          {(onClose) =>
            detail && (
              <>
                <ModalHeader className="flex flex-col gap-1">
                  <h2 className="text-lg font-bold break-all">{detail.name}</h2>
                  {detail.pendingDelete && (
                    <p className="text-small font-semibold text-warning-700">
                      删除中：凭据撤销下发尚未完成，完成后此订阅将自动消失
                    </p>
                  )}
                  {detail.meteringUnsupported && (
                    <p className="text-small font-semibold text-warning-700">
                      此订阅 ID
                      超出流量计量编码上限：其流量永不计量，流量限额也不会触发
                    </p>
                  )}
                  <p className="text-small font-normal text-foreground">
                    {detail.nodeCount} 个节点
                    {detail.expireTime
                      ? ` · ${expiryLabel(detail.expireTime)}`
                      : ""}
                  </p>
                </ModalHeader>
                <ModalBody className="space-y-3">
                  <div className="space-y-2 rounded-xl glass-inset p-3">
                    <Usage s={detail} />
                    <div className="flex items-center justify-between gap-2">
                      <span className="text-xs font-semibold text-foreground">
                        {detail.overQuota
                          ? "流量已用完，已从节点上断开"
                          : `${toDateInput(detail.trafficSince)} 起的上传 + 下载合计`}
                      </span>
                      <Button
                        isDisabled={
                          detail.uploadBytes + detail.downloadBytes === 0
                        }
                        isLoading={resettingTraffic}
                        size="sm"
                        variant="flat"
                        onPress={resetTraffic}
                      >
                        重置流量
                      </Button>
                    </div>
                  </div>
                  <div className="space-y-2">
                    <h3 className="text-sm font-semibold">流量统计</h3>
                    <SubTrafficStats id={detail.id} />
                  </div>
                  <div className="flex gap-2">
                    <Button
                      className="flex-1"
                      color="primary"
                      startContent={<Copy className="h-4 w-4" />}
                      variant="flat"
                      onPress={() => copyText(url(detail), "订阅链接已复制")}
                    >
                      复制订阅链接（自动识别客户端）
                    </Button>
                    <Button
                      aria-label={
                        qrDetailId === detail.id
                          ? "收起订阅二维码"
                          : "显示订阅二维码"
                      }
                      color={qrDetailId === detail.id ? "primary" : "default"}
                      isIconOnly
                      variant="flat"
                      onPress={() =>
                        setQrDetailId(
                          qrDetailId === detail.id ? null : detail.id,
                        )
                      }
                    >
                      <QrCode className="h-4 w-4" />
                    </Button>
                  </div>
                  {qrDetailId === detail.id && (
                    <div className="flex justify-center py-1">
                      <QrImage text={url(detail)} />
                    </div>
                  )}
                  <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                    {SUB_CLIENTS.map((c) => {
                      const link = url(detail, c.format);
                      const open = () => {
                        if (c.importURL) {
                          window.location.href = c.importURL(link, detail.name);
                        } else {
                          copyText(link, `${c.label}订阅已复制`);
                        }
                      };

                      return (
                        <div
                          key={c.key}
                          className="relative flex h-14 items-center rounded-xl border border-divider transition-colors hover:bg-white/40"
                        >
                          <button
                            className="h-full min-w-0 flex-1 truncate pl-3 pr-9 text-left text-sm font-medium"
                            title={
                              c.importURL ? `导入到 ${c.label}` : "复制通用订阅"
                            }
                            type="button"
                            onClick={open}
                          >
                            {c.label}
                          </button>
                          <button
                            aria-label={`复制 ${c.label} 订阅链接`}
                            className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-lg p-1.5 text-foreground hover:bg-white/40 hover:text-foreground"
                            type="button"
                            onClick={() =>
                              copyText(link, `${c.label} 订阅已复制`)
                            }
                          >
                            <Copy className="h-3.5 w-3.5" />
                          </button>
                        </div>
                      );
                    })}
                  </div>
                  <p className="text-center text-xs font-semibold text-foreground">
                    点客户端直接导入，右侧按钮复制链接
                  </p>
                  {detail.sharedIds.length > 0 && (
                    <p className="text-xs font-semibold text-warning-700">
                      其中 {detail.sharedIds.length}{" "}
                      个落地用的是共用凭据，停用后已导入的配置仍能连接
                    </p>
                  )}
                </ModalBody>
                <ModalFooter className="flex-wrap">
                  <Button
                    color="danger"
                    size="sm"
                    variant="light"
                    onPress={() => setConfirm("delete")}
                  >
                    删除
                  </Button>
                  <Button
                    color="warning"
                    size="sm"
                    variant="light"
                    onPress={() => setConfirm("reset")}
                  >
                    重置链接和凭据
                  </Button>
                  <Button
                    color="primary"
                    size="sm"
                    onPress={() => {
                      const s = detail;

                      onClose();
                      setDraft(draftFrom(s));
                    }}
                  >
                    编辑
                  </Button>
                </ModalFooter>
              </>
            )
          }
        </ModalContent>
      </Modal>

      <Modal
        backdrop="blur"
        classNames={{ base: modalBase }}
        isOpen={Boolean(draft)}
        placement="center"
        scrollBehavior="inside"
        size="2xl"
        onOpenChange={(open) => !open && setDraft(null)}
      >
        <ModalContent>
          {(onClose) =>
            draft && (
              <Editor
                draft={draft}
                inbounds={inbounds}
                saving={saving}
                setDraft={setDraft}
                sources={sources}
                onClose={onClose}
                onSave={save}
              />
            )
          }
        </ModalContent>
      </Modal>

      <Modal
        backdrop="blur"
        classNames={{ base: modalBase }}
        isOpen={Boolean(confirm)}
        placement="center"
        onOpenChange={(open) => !open && setConfirm(null)}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>
                <h2
                  className={`text-lg font-bold ${confirm === "delete" ? "text-danger-700" : ""}`}
                >
                  {confirm === "reset" ? "重置链接" : "删除订阅"}
                </h2>
              </ModalHeader>
              <ModalBody>
                <p className="text-sm text-foreground">
                  {confirm === "reset"
                    ? "旧链接和已导入的配置会立即失效，需要用新链接重新导入。"
                    : "订阅链接立即失效，无法恢复。"}
                </p>
                {confirm === "delete" && detail?.isDefault && (
                  <p className="mt-2 text-sm font-medium text-danger-700">
                    这是默认订阅，删除后所有用户的个人链接将切换到下一条订阅，旧链接立即失效。
                  </p>
                )}
                {/* X-P2-14（L-03-1/V-06）：sharedIds 即共用凭据落地
                    （SS/导入节点），其已导入配置不随重置失效。 */}
                {confirm === "reset" &&
                  detail &&
                  detail.sharedIds.length > 0 && (
                    <p className="mt-2 text-sm text-warning-700">
                      其中 {detail.sharedIds.length}{" "}
                      个落地用的是共用凭据（Shadowsocks、导入节点），重置不使它们已导入的配置失效，要断开只能改对应入站的密码。
                    </p>
                  )}
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  取消
                </Button>
                <Button
                  color={confirm === "delete" ? "danger" : "warning"}
                  isLoading={saving}
                  onPress={runConfirm}
                >
                  确认
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
      <SubscriptionTemplatesModal
        isOpen={templatesOpen}
        onClose={() => setTemplatesOpen(false)}
      />
    </AnimatedPage>
  );
}
