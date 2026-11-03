import type {
  NodeGroupDraft,
  NodeGroupItem,
  NodeGroupPipeline,
  RemoteSourceItem,
  SubPreviewItem,
} from "@/api";
import type { InboundApiItem } from "@/api/types";

import { useMemo, useState } from "react";
import toast from "react-hot-toast";
import { ArrowDown, ArrowUp, Pencil, Plus, Trash2 } from "lucide-react";

import {
  deleteNodeGroup,
  orderNodeGroups,
  previewNodeGroup,
  saveNodeGroup,
} from "@/api";
import { useIdemKey, useRotateIdemKeyOnCreateOpen } from "@/hooks/useIdemKey";
import { PROTOCOL_LABELS } from "@/pages/inbound/presets";
import { Segmented, ToggleRow } from "@/pages/subscription/controls";
import { RenameRules } from "@/pages/subscription/rename-rules";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody } from "@/shadcn-bridge/heroui/card";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Input, Textarea } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";

const modalBase =
  "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden";

export const EMPTY_GROUP_PIPELINE: NodeGroupPipeline = {
  lines: "",
  regions: [],
  protocols: [],
  include: "",
  exclude: "",
  rename: "",
  addFlag: false,
  prefix: "",
  dedupe: false,
  sort: "",
  script: "",
};

interface GroupDraft extends NodeGroupDraft {}

function draftFrom(g?: NodeGroupItem): GroupDraft {
  if (!g) {
    return {
      name: "",
      sourceAll: false,
      inboundIds: [],
      sourceIds: [],
      options: { ...EMPTY_GROUP_PIPELINE },
    };
  }

  return {
    id: g.id,
    name: g.name,
    sourceAll: g.sourceAll,
    inboundIds: g.inboundIds,
    sourceIds: g.sourceIds,
    options: { ...EMPTY_GROUP_PIPELINE, ...g.options },
  };
}

function GroupEditor({
  draft,
  setDraft,
  inbounds,
  sources,
  regions,
  saving,
  onSave,
  onClose,
}: {
  draft: GroupDraft;
  setDraft: (d: GroupDraft) => void;
  inbounds: InboundApiItem[];
  sources: RemoteSourceItem[];
  regions: string[];
  saving: boolean;
  onSave: () => void;
  onClose: () => void;
}) {
  const [preview, setPreview] = useState<{
    items: SubPreviewItem[];
    total: number;
    dropped: number;
    scriptError?: string;
  } | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const o = draft.options;
  const setOpts = (patch: Partial<NodeGroupPipeline>) => {
    setDraft({ ...draft, options: { ...o, ...patch } });
    setPreview(null);
  };
  const set = (patch: Partial<GroupDraft>) => {
    setDraft({ ...draft, ...patch });
    setPreview(null);
  };

  const protocols = useMemo(
    () =>
      [...new Set(inbounds.map((i) => i.protocol))].map(
        (p) => [p, PROTOCOL_LABELS[p] ?? p] as [string, string],
      ),
    [inbounds],
  );
  const selfBuilt = inbounds.filter((i) => !i.external);
  const imported = inbounds.filter((i) => i.external && !i.sourceId);

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
            <span className="ml-1 text-xs font-semibold text-foreground">
              {PROTOCOL_LABELS[i.protocol] ?? i.protocol} · {i.nodeName}
            </span>
          </Checkbox>
        ))}
      </div>
    );

  const runPreview = async () => {
    setPreviewing(true);
    try {
      const res = await previewNodeGroup(draft);

      if (res.code === 0 && res.data) setPreview(res.data);
      else toast.error(res.msg || "预览失败");
    } finally {
      setPreviewing(false);
    }
  };

  return (
    <Modal
      classNames={{ base: modalBase }}
      isOpen={true}
      placement="center"
      scrollBehavior="inside"
      onOpenChange={(open) => !open && onClose()}
    >
      <ModalContent>
        {(onCloseModal) => (
          <>
            <ModalHeader>
              <h2 className="text-lg font-bold">
                {draft.id ? "编辑节点组" : "新建节点组"}
              </h2>
            </ModalHeader>
            <ModalBody className="space-y-5">
              <Input
                label="名称"
                placeholder="例如 香港组"
                value={draft.name}
                variant="bordered"
                onChange={(e) => set({ name: e.target.value })}
              />

              <div className="space-y-2">
                <p className="text-sm font-semibold">来源</p>
                <Segmented
                  options={[
                    ["pick", "指定落地"],
                    ["all", "全部落地"],
                  ]}
                  value={draft.sourceAll ? "all" : "pick"}
                  onChange={(v) => set({ sourceAll: v === "all" })}
                />
                {draft.sourceAll ? (
                  <p className="text-xs font-semibold text-foreground">
                    包括以后新增的落地；只想要一部分可以用下面的过滤
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
                                  : draft.sourceIds.filter(
                                      (x) => x !== src.id,
                                    ),
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
              </div>

              <div className="space-y-2">
                <p className="text-sm font-semibold">节点处理</p>
                <div className="space-y-1.5">
                  <p className="text-xs font-semibold">线路</p>
                  <Segmented
                    options={[
                      ["", "全部"],
                      ["direct", "直连"],
                      ["relay", "中转"],
                    ]}
                    value={o.lines}
                    onChange={(lines) => setOpts({ lines })}
                  />
                </div>
                {regions.length > 0 && (
                  <div className="space-y-1.5">
                    <p className="text-xs font-semibold">地区（不选 = 全部）</p>
                    <div className="flex flex-wrap gap-1">
                      {regions.map((rg) => (
                        <Checkbox
                          key={rg}
                          isSelected={o.regions.includes(rg)}
                          onValueChange={(on) =>
                            setOpts({
                              regions: on
                                ? [...o.regions, rg]
                                : o.regions.filter((x) => x !== rg),
                            })
                          }
                        >
                          <span className="text-sm">{rg}</span>
                        </Checkbox>
                      ))}
                    </div>
                  </div>
                )}
                {protocols.length > 0 && (
                  <div className="space-y-1.5">
                    <p className="text-xs font-semibold">协议（不选 = 全部）</p>
                    <div className="flex flex-wrap gap-1">
                      {protocols.map(([p, label]) => (
                        <Checkbox
                          key={p}
                          isSelected={o.protocols.includes(p)}
                          onValueChange={(on) =>
                            setOpts({
                              protocols: on
                                ? [...o.protocols, p]
                                : o.protocols.filter((x) => x !== p),
                            })
                          }
                        >
                          <span className="text-sm">{label}</span>
                        </Checkbox>
                      ))}
                    </div>
                  </div>
                )}
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                  <Input
                    description="多个关键词用 | 分隔，或 /正则/"
                    label="包含"
                    value={o.include}
                    variant="bordered"
                    onChange={(e) => setOpts({ include: e.target.value })}
                  />
                  <Input
                    description="多个关键词用 | 分隔，或 /正则/"
                    label="排除"
                    value={o.exclude}
                    variant="bordered"
                    onChange={(e) => setOpts({ exclude: e.target.value })}
                  />
                </div>
                <RenameRules
                  value={o.rename}
                  onChange={(rename) => setOpts({ rename })}
                />
                <Input
                  description="加在处理后的名字前面"
                  label="前缀"
                  value={o.prefix}
                  variant="bordered"
                  onChange={(e) => setOpts({ prefix: e.target.value })}
                />
                <ToggleRow
                  hint="名字里没有国旗时按地区补上"
                  id="group-add-flag"
                  label="加国旗"
                  value={o.addFlag}
                  onChange={(addFlag) => setOpts({ addFlag })}
                />
                <ToggleRow
                  hint="地址、端口、协议都相同的只保留第一个"
                  id="group-dedupe"
                  label="去重"
                  value={o.dedupe}
                  onChange={(dedupe) => setOpts({ dedupe })}
                />
                <div className="space-y-1.5">
                  <p className="text-xs font-semibold">排序</p>
                  <Segmented
                    options={[
                      ["", "面板顺序"],
                      ["name", "按名称"],
                    ]}
                    value={o.sort}
                    onChange={(sort) => setOpts({ sort })}
                  />
                </div>
                <Textarea
                  description="function operator(nodes)，在其他步骤后运行"
                  label="脚本"
                  minRows={3}
                  value={o.script}
                  variant="bordered"
                  onChange={(e) => setOpts({ script: e.target.value })}
                />
              </div>

              <div className="space-y-2">
                <div className="flex items-center justify-between">
                  <p className="text-sm font-semibold">预览</p>
                  <Button
                    isLoading={previewing}
                    size="sm"
                    variant="flat"
                    onPress={runPreview}
                  >
                    预览节点
                  </Button>
                </div>
                {preview && (
                  <div className="space-y-1 rounded-xl border border-divider p-2">
                    <p className="text-xs font-semibold text-foreground">
                      共 {preview.total} 个节点，处理后 {preview.items.length}{" "}
                      个
                      {preview.dropped > 0 &&
                        `，过滤掉 ${preview.dropped} 个`}
                    </p>
                    {preview.scriptError && (
                      <p className="text-xs font-semibold text-danger-700">
                        脚本出错：{preview.scriptError}
                      </p>
                    )}
                    <div className="max-h-40 overflow-y-auto">
                      {preview.items.map((it, i) => (
                        <p key={i} className="truncate text-sm">
                          {it.name}
                        </p>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            </ModalBody>
            <ModalFooter>
              <Button variant="flat" onPress={onCloseModal}>
                取消
              </Button>
              <Button
                color="primary"
                isLoading={saving}
                onPress={onSave}
              >
                保存
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}

/**
 * Node group management: reusable source + pipeline recipes that
 * subscriptions splice into their node lists. Editing a group here
 * changes every subscription referencing it.
 */
export default function NodeGroupsSection({
  groups,
  inbounds,
  sources,
  regions,
  onChanged,
}: {
  groups: NodeGroupItem[];
  inbounds: InboundApiItem[];
  sources: RemoteSourceItem[];
  regions: string[];
  onChanged: () => Promise<void> | void;
}) {
  const [draft, setDraft] = useState<GroupDraft | null>(null);
  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState<NodeGroupItem | null>(
    null,
  );
  const [busy, setBusy] = useState(false);

  // R10-06：节点组新建表单幂等键（同表单复用、成功轮换；更新不带键）。
  const { idemKey, rotateIdemKey } = useIdemKey();

  // 创建弹窗打开（上升沿且新建模式）时轮换幂等键；窗内重试仍复用同键。
  useRotateIdemKeyOnCreateOpen(
    Boolean(draft),
    Boolean(draft) && !draft?.id,
    rotateIdemKey,
  );

  const save = async () => {
    if (!draft) return;
    setSaving(true);
    try {
      const res = await saveNodeGroup(draft, draft.id ? undefined : idemKey);

      if (res.code !== 0) {
        toast.error(res.msg || "保存失败");

        return;
      }
      if (!draft.id) rotateIdemKey();
      if (res.msg && res.msg !== "操作成功") toast(res.msg, { icon: "⚠️" });
      else toast.success("已保存");
      setDraft(null);
      await onChanged();
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!confirmDelete) return;
    setBusy(true);
    try {
      const res = await deleteNodeGroup(confirmDelete.id);

      if (res.code !== 0) {
        toast.error(res.msg || "删除失败");

        return;
      }
      toast.success("已删除");
      setConfirmDelete(null);
      await onChanged();
    } finally {
      setBusy(false);
    }
  };

  const move = async (index: number, delta: number) => {
    const next = [...groups];
    const target = index + delta;

    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]];
    const res = await orderNodeGroups(next.map((g) => g.id));

    if (res.code !== 0) toast.error(res.msg || "排序失败");
    else await onChanged();
  };

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs font-semibold text-foreground">
          把一组来源和一套过滤 / 改名 /
          排序规则存成组，多个订阅共用；改一处，所有引用它的订阅一起变。
        </p>
        <Button
          className="shrink-0 font-semibold"
          color="primary"
          size="sm"
          startContent={<Plus size={14} />}
          variant="solid"
          onPress={() => setDraft(draftFrom())}
        >
          新建节点组
        </Button>
      </div>

      {groups.length === 0 ? (
        <p className="rounded-xl border border-dashed border-divider px-3 py-4 text-center text-xs font-semibold text-foreground">
          还没有节点组。订阅多、规则重复时，把共用的规则存成组只维护一份。
        </p>
      ) : (
        <div className="grid grid-cols-1 gap-2 2xl:grid-cols-2">
          {groups.map((g, i) => (
            <Card key={g.id} className="shadow-sm">
              <CardBody className="space-y-2">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="truncate text-sm font-semibold">{g.name}</p>
                    <p className="text-xs font-semibold text-foreground">
                      {g.nodeCount} 个节点
                      {g.sourceAll
                        ? " · 全部落地"
                        : ` · ${g.inboundIds.length} 个落地 / ${g.sourceIds.length} 个订阅源`}
                    </p>
                  </div>
                  <div className="flex shrink-0 gap-1">
                    <Button
                      isIconOnly
                      aria-label="上移"
                      isDisabled={i === 0}
                      size="sm"
                      variant="flat"
                      onPress={() => move(i, -1)}
                    >
                      <ArrowUp size={14} />
                    </Button>
                    <Button
                      isIconOnly
                      aria-label="下移"
                      isDisabled={i === groups.length - 1}
                      size="sm"
                      variant="flat"
                      onPress={() => move(i, 1)}
                    >
                      <ArrowDown size={14} />
                    </Button>
                    <Button
                      isIconOnly
                      aria-label="编辑"
                      size="sm"
                      variant="flat"
                      onPress={() => setDraft(draftFrom(g))}
                    >
                      <Pencil size={14} />
                    </Button>
                    <Button
                      isIconOnly
                      aria-label="删除"
                      color="danger"
                      size="sm"
                      variant="flat"
                      onPress={() => setConfirmDelete(g)}
                    >
                      <Trash2 size={14} />
                    </Button>
                  </div>
                </div>
                {g.subscriptions.length > 0 && (
                  <p className="text-xs font-semibold text-foreground">
                    被引用：
                    {g.subscriptions.map((s) => s.name).join("、")}
                  </p>
                )}
              </CardBody>
            </Card>
          ))}
        </div>
      )}

      {draft && (
        <GroupEditor
          draft={draft}
          inbounds={inbounds}
          regions={regions}
          saving={saving}
          setDraft={setDraft}
          sources={sources}
          onClose={() => setDraft(null)}
          onSave={save}
        />
      )}

      <Modal
          classNames={{ base: modalBase }}
        isOpen={Boolean(confirmDelete)}
        placement="center"
        onOpenChange={(open) => !open && setConfirmDelete(null)}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>删除节点组</ModalHeader>
              <ModalBody>
                <p className="text-sm">
                  删除「{confirmDelete?.name}」？
                  {confirmDelete && confirmDelete.subscriptions.length > 0
                    ? `有 ${confirmDelete.subscriptions.length} 个订阅在引用它，删除后这些订阅将不再包含这个组的节点。`
                    : "没有订阅在引用它。"}
                </p>
              </ModalBody>
              <ModalFooter>
                <Button variant="flat" onPress={onClose}>
                  取消
                </Button>
                <Button color="danger" isLoading={busy} onPress={remove}>
                  删除
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    </div>
  );
}
