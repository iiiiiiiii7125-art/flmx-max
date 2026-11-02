import type {
  SubTemplateDraft,
  SubTemplateItem,
  SubTemplateKind,
  SubscriptionItem,
  TemplateBuilder,
  TemplateCheckResult,
  TemplateRuleCategoryItem,
} from "@/api";

import { useCallback, useEffect, useState } from "react";
import toast from "react-hot-toast";
import { Copy, Pencil, RefreshCw, Trash2 } from "lucide-react";

import {
  checkSubscriptionTemplate,
  deleteSubscriptionTemplate,
  getSubscriptions,
  getSubscriptionTemplates,
  getTemplateCatalog,
  refreshSubscriptionTemplate,
  saveSubscriptionTemplate,
} from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Input, Textarea } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";
import { Segmented } from "@/pages/subscription/controls";
import {
  TemplateBuilderForm,
  builderFromPreset,
} from "@/pages/subscription/template-builder";

export const TEMPLATE_KIND_LABELS: Record<SubTemplateKind, string> = {
  clash: "Mihomo",
  singbox: "sing-box",
  surge: "Surge",
};

const STARTERS: Record<SubTemplateKind, string> = {
  clash: `proxies: []
proxy-groups:
  - name: 🚀 节点选择
    type: select
    include-all: true
rules:
  - MATCH,🚀 节点选择
`,
  singbox: `{
  "outbounds": [
    {"type": "selector", "tag": "🚀 节点选择", "outbounds": ["{all}", "direct"]},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {"final": "🚀 节点选择"}
}
`,
  surge: `[General]
loglevel = notify

[Proxy]

[Proxy Group]
🚀 节点选择 = select, include-all-proxies=1

[Rule]
FINAL,🚀 节点选择
`,
};

type Mode = "builder" | "source" | "url";

interface EditState {
  id?: number;
  name: string;
  kind: SubTemplateKind;
  mode: Mode;
  content: string;
  sourceUrl: string;
  builder: TemplateBuilder | null;
}

const selectCls =
  "h-9 rounded-lg border border-divider bg-content1 px-2 text-sm";

/**
 * Subscription template manager: list, create and edit templates in a
 * modal. A template comes from the visual builder, pasted source, or a
 * URL the panel re-downloads once a day (failures keep the last good
 * copy and are shown on the row).
 */
export function SubscriptionTemplatesModal({
  isOpen,
  onClose,
}: {
  isOpen: boolean;
  onClose: () => void;
}) {
  const [items, setItems] = useState<SubTemplateItem[]>([]);
  const [catalog, setCatalog] = useState<TemplateRuleCategoryItem[]>([]);
  const [subs, setSubs] = useState<SubscriptionItem[]>([]);
  const [edit, setEdit] = useState<EditState | null>(null);
  const [check, setCheck] = useState<TemplateCheckResult | null>(null);
  const [checkSub, setCheckSub] = useState(0);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    const [listRes, catRes, subRes] = await Promise.all([
      getSubscriptionTemplates(),
      getTemplateCatalog(),
      getSubscriptions(),
    ]);
    if (listRes.code === 0) setItems(listRes.data ?? []);
    else toast.error(listRes.msg || "加载模板失败");
    if (catRes.code === 0) setCatalog(catRes.data ?? []);
    if (subRes.code === 0 && subRes.data) setSubs(subRes.data.items ?? []);
  }, []);

  useEffect(() => {
    if (isOpen) {
      setEdit(null);
      setCheck(null);
      void load();
    }
  }, [isOpen, load]);

  const openNew = () =>
    setEdit({
      name: "",
      kind: "clash",
      mode: "builder",
      content: "",
      sourceUrl: "",
      builder: builderFromPreset(catalog, "balanced"),
    });

  const openEdit = (t: SubTemplateItem) => {
    setCheck(null);
    setEdit({
      id: t.id,
      name: t.name,
      kind: t.kind,
      mode: t.builder ? "builder" : t.sourceUrl ? "url" : "source",
      content: t.content,
      sourceUrl: t.sourceUrl,
      builder: t.builder ?? null,
    });
  };

  const draft = (): SubTemplateDraft | null => {
    if (!edit) return null;
    const d: SubTemplateDraft = {
      id: edit.id,
      name: edit.name,
      kind: edit.kind,
    };
    if (edit.mode === "builder") {
      if (!edit.builder) return null;
      d.builder = edit.builder;
    } else if (edit.mode === "url") {
      d.sourceUrl = edit.sourceUrl;
    } else {
      d.content = edit.content;
    }
    return d;
  };

  const runCheck = async () => {
    const d = draft();
    if (!d) return;
    setBusy(true);
    try {
      const res = await checkSubscriptionTemplate({
        ...d,
        subscriptionId: checkSub || undefined,
      });
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "检查失败");
        return;
      }
      setCheck(res.data);
    } finally {
      setBusy(false);
    }
  };

  const save = async () => {
    const d = draft();
    if (!d) return;
    if (!d.name.trim()) {
      toast.error("请填写模板名称");
      return;
    }
    setBusy(true);
    try {
      const res = await saveSubscriptionTemplate(d);
      if (res.code !== 0) {
        toast.error(res.msg || "保存失败");
        return;
      }
      toast.success("模板已保存");
      setEdit(null);
      setCheck(null);
      await load();
    } finally {
      setBusy(false);
    }
  };

  const refresh = async (t: SubTemplateItem) => {
    setBusy(true);
    try {
      const res = await refreshSubscriptionTemplate(t.id);
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "刷新失败");
        return;
      }
      if (res.data.lastError)
        toast.error(`刷新失败，继续用上次内容：${res.data.lastError}`);
      else toast.success("已刷新");
      await load();
    } finally {
      setBusy(false);
    }
  };

  const copyLocal = async (t: SubTemplateItem) => {
    setBusy(true);
    try {
      const res = await saveSubscriptionTemplate({
        name: `${t.name} 副本`,
        kind: t.kind,
        content: t.content,
      });
      if (res.code !== 0) {
        toast.error(res.msg || "复制失败");
        return;
      }
      toast.success("已复制为本地模板");
      await load();
    } finally {
      setBusy(false);
    }
  };

  const remove = async (t: SubTemplateItem) => {
    setBusy(true);
    try {
      const res = await deleteSubscriptionTemplate(t.id);
      if (res.code !== 0) {
        toast.error(res.msg || "删除失败");
        return;
      }
      toast.success("模板已删除");
      await load();
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      backdrop="blur"
      isOpen={isOpen}
      placement="center"
      scrollBehavior="inside"
      size="4xl"
      onOpenChange={(open) => !open && onClose()}
    >
      <ModalContent>
        {() => (
          <>
            <ModalHeader>
              <h2 className="text-lg font-bold">
                {edit ? (edit.id ? "编辑模板" : "新建模板") : "订阅模板"}
              </h2>
            </ModalHeader>
            <ModalBody>
              {!edit && (
                <div className="space-y-3">
                  <p className="text-xs font-semibold text-foreground">
                    用自己的 Mihomo / sing-box / Surge 配置作骨架下发订阅，面板只把节点填进策略组。
                    网址模板每天自动刷新一次，失败继续用上次内容。在订阅编辑的「规则与脚本」里给客户端选用模板。
                  </p>
                  {items.length === 0 && (
                    <p className="text-sm text-foreground">还没有模板。</p>
                  )}
                  {items.map((t) => (
                    <div
                      key={t.id}
                      className="glass-surface flex flex-wrap items-center gap-2 rounded-xl px-3 py-2.5"
                    >
                      <div className="min-w-40 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-semibold">{t.name}</span>
                          <Chip size="sm" variant="flat">
                            {TEMPLATE_KIND_LABELS[t.kind]}
                          </Chip>
                          <Chip size="sm" variant="flat">
                            {t.builder ? "选择规则" : t.sourceUrl ? "网址" : "源码"}
                          </Chip>
                        </div>
                        <p className="text-xs font-semibold text-foreground">
                          {t.groups} 个策略组（{t.autoGroups} 个自动收节点）·{" "}
                          {t.rules} 条规则 · {t.usedBy} 个订阅在用
                          {t.sourceUrl && t.lastFetch > 0 &&
                            ` · 上次刷新 ${new Date(t.lastFetch).toLocaleString()}`}
                        </p>
                        {t.lastError && (
                          <p className="text-xs font-semibold text-warning-700">
                            刷新失败，继续用上次内容：{t.lastError}
                          </p>
                        )}
                      </div>
                      {t.sourceUrl && (
                        <>
                          <Button
                            isIconOnly
                            isLoading={busy}
                            size="sm"
                            title="立即刷新"
                            variant="light"
                            onPress={() => refresh(t)}
                          >
                            <RefreshCw className="h-4 w-4" />
                          </Button>
                          <Button
                            isIconOnly
                            isLoading={busy}
                            size="sm"
                            title="复制为本地模板"
                            variant="light"
                            onPress={() => copyLocal(t)}
                          >
                            <Copy className="h-4 w-4" />
                          </Button>
                        </>
                      )}
                      <Button
                        isIconOnly
                        size="sm"
                        title="编辑"
                        variant="light"
                        onPress={() => openEdit(t)}
                      >
                        <Pencil className="h-4 w-4" />
                      </Button>
                      <Button
                        isIconOnly
                        color="danger"
                        isDisabled={t.usedBy > 0}
                        size="sm"
                        title={
                          t.usedBy > 0
                            ? "有订阅在用，先在订阅里换掉"
                            : "删除"
                        }
                        variant="light"
                        onPress={() => remove(t)}
                      >
                        <Trash2 className="h-4 w-4" />
                      </Button>
                    </div>
                  ))}
                </div>
              )}

              {edit && (
                <div className="space-y-4">
                  <div className="flex flex-wrap items-end gap-2">
                    <Input
                      className="w-56"
                      label="名称"
                      size="sm"
                      value={edit.name}
                      variant="bordered"
                      onChange={(e) =>
                        setEdit({ ...edit, name: e.target.value })
                      }
                    />
                    <div className="space-y-1">
                      <p className="text-xs font-semibold">格式</p>
                      <select
                        className={selectCls}
                        value={edit.kind}
                        onChange={(e) =>
                          setEdit({
                            ...edit,
                            kind: e.target.value as SubTemplateKind,
                          })
                        }
                      >
                        {(
                          Object.keys(TEMPLATE_KIND_LABELS) as SubTemplateKind[]
                        ).map((k) => (
                          <option key={k} value={k}>
                            {TEMPLATE_KIND_LABELS[k]}
                          </option>
                        ))}
                      </select>
                    </div>
                    <div className="flex-1" />
                    <Segmented<Mode>
                      options={[
                        ["builder", "选择规则"],
                        ["source", "源码编辑"],
                        ["url", "从网址获取"],
                      ]}
                      value={edit.mode}
                      onChange={(mode) =>
                        setEdit({
                          ...edit,
                          mode,
                          builder:
                            mode === "builder" && !edit.builder
                              ? builderFromPreset(catalog, "balanced")
                              : edit.builder,
                        })
                      }
                    />
                  </div>

                  {edit.mode === "builder" &&
                    (edit.builder ? (
                      <TemplateBuilderForm
                        catalog={catalog}
                        value={edit.builder}
                        onChange={(builder) => setEdit({ ...edit, builder })}
                      />
                    ) : (
                      <p className="text-sm">目录加载中…</p>
                    ))}

                  {edit.mode === "source" && (
                    <div className="space-y-1.5">
                      <Textarea
                        className="font-mono text-xs"
                        minRows={14}
                        placeholder="粘贴完整的客户端配置；节点列表留空（如 proxies: []），面板会填入"
                        value={edit.content}
                        variant="bordered"
                        onChange={(e) =>
                          setEdit({ ...edit, content: e.target.value })
                        }
                      />
                      {edit.content.trim() === "" && (
                        <Button
                          size="sm"
                          variant="flat"
                          onPress={() =>
                            setEdit({ ...edit, content: STARTERS[edit.kind] })
                          }
                        >
                          用示例开始
                        </Button>
                      )}
                    </div>
                  )}

                  {edit.mode === "url" && (
                    <div className="space-y-1.5">
                      <Input
                        label="模板网址"
                        placeholder="https://…（raw 链接）"
                        size="sm"
                        value={edit.sourceUrl}
                        variant="bordered"
                        onChange={(e) =>
                          setEdit({ ...edit, sourceUrl: e.target.value })
                        }
                      />
                      <p className="text-xs font-semibold text-foreground">
                        面板每天下载一次；下载或校验失败时继续用上次成功的内容，不会把订阅打挂。网址模板不能直接改内容，可在列表里「复制为本地模板」后编辑。
                      </p>
                      {edit.id !== undefined && edit.content && (
                        <Textarea
                          readOnly
                          className="font-mono text-xs"
                          label="当前内容（上次成功下载）"
                          minRows={8}
                          value={edit.content}
                          variant="bordered"
                        />
                      )}
                    </div>
                  )}

                  <div className="space-y-2 border-t border-divider pt-3">
                    <div className="flex flex-wrap items-center gap-2">
                      <select
                        className={selectCls}
                        value={checkSub}
                        onChange={(e) => setCheckSub(Number(e.target.value))}
                      >
                        <option value={0}>全部节点</option>
                        {subs.map((s) => (
                          <option key={s.id} value={s.id}>
                            {s.name}
                          </option>
                        ))}
                      </select>
                      <Button
                        isLoading={busy}
                        size="sm"
                        variant="flat"
                        onPress={runCheck}
                      >
                        检查并预览策略组
                      </Button>
                      {check && (
                        <span className="text-xs font-semibold text-foreground">
                          样本：{check.sample} · {check.nodes} 个节点 ·{" "}
                          {check.summary.rules} 条规则
                        </span>
                      )}
                    </div>
                    {check?.summary.warnings.map((w) => (
                      <p key={w} className="text-xs font-semibold text-warning-700">
                        {w}
                      </p>
                    ))}
                    {check?.summary.groups.map((g) => (
                      <details key={g.name} className="text-sm">
                        <summary className="cursor-pointer">
                          {g.name}
                          <span className="ml-2 text-xs font-semibold text-foreground">
                            {g.type}
                            {g.auto
                              ? ` · 自动收节点 ${g.nodes?.length ?? 0} 个`
                              : ` · 固定成员 ${g.proxies.length} 个`}
                            {g.filterError ? " · 过滤条件无法预览" : ""}
                          </span>
                        </summary>
                        {g.auto && (
                          <p className="pl-4 text-xs font-semibold text-foreground">
                            {(g.nodes ?? []).join("、") || "（空组）"}
                          </p>
                        )}
                      </details>
                    ))}
                    {check && edit.mode === "builder" && (
                      <details className="text-sm">
                        <summary className="cursor-pointer">
                          生成的配置
                        </summary>
                        <Textarea
                          readOnly
                          className="mt-1 font-mono text-xs"
                          minRows={10}
                          value={check.content}
                          variant="bordered"
                        />
                      </details>
                    )}
                  </div>
                </div>
              )}
            </ModalBody>
            <ModalFooter>
              {!edit ? (
                <>
                  <Button variant="light" onPress={onClose}>
                    关闭
                  </Button>
                  <Button color="primary" onPress={openNew}>
                    新建模板
                  </Button>
                </>
              ) : (
                <>
                  <Button
                    variant="light"
                    onPress={() => {
                      setEdit(null);
                      setCheck(null);
                    }}
                  >
                    返回列表
                  </Button>
                  <Button color="primary" isLoading={busy} onPress={save}>
                    保存模板
                  </Button>
                </>
              )}
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
