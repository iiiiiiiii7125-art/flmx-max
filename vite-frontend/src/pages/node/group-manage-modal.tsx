import { useEffect, useState } from "react";
import toast from "react-hot-toast";
import { ChevronDown, ChevronUp, GripVertical } from "lucide-react";
import {
  DndContext,
  KeyboardSensor,
  MouseSensor,
  TouchSensor,
  type DragEndEvent,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Input } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";

/**
 * Display-group management (v2, user decision 2026-11-09 23:34):
 * groups are first-class — created empty, filled by the batch
 * "移入分组" action, renamed/deleted here. Create/rename/delete hit
 * the server immediately (rename cascades member labels, delete
 * drops members back to ungrouped); only the section order is a
 * draft that 保存顺序 persists (panel config
 * `node_display_group_order`, the same list that registers groups).
 * Registered-but-empty groups are listed with 0 台: they exist, they
 * just render no section on the node page.
 */
export interface GroupOpResult {
  ok: boolean;
  msg?: string;
}

export interface NodeGroupActions {
  onCreate: (name: string) => Promise<GroupOpResult>;
  onRename: (oldName: string, newName: string) => Promise<GroupOpResult>;
  onDelete: (name: string) => Promise<GroupOpResult>;
  /** Persists the section order; resolves true when saved. */
  onSaveOrder: (order: string[]) => Promise<boolean>;
}

function SortableGroupRow({
  label,
  count,
  index,
  total,
  editing,
  confirmingDelete,
  busy,
  editValue,
  onEditValueChange,
  onMove,
  onStartRename,
  onCancelRename,
  onSaveRename,
  onStartDelete,
  onCancelDelete,
  onConfirmDelete,
}: {
  label: string;
  count: number;
  index: number;
  total: number;
  editing: boolean;
  confirmingDelete: boolean;
  busy: boolean;
  editValue: string;
  onEditValueChange: (value: string) => void;
  onMove: (index: number, delta: number) => void;
  onStartRename: () => void;
  onCancelRename: () => void;
  onSaveRename: () => void;
  onStartDelete: () => void;
  onCancelDelete: () => void;
  onConfirmDelete: () => void;
}) {
  const {
    attributes,
    listeners,
    setNodeRef,
    transform,
    transition,
    isDragging,
  } = useSortable({ id: label });

  return (
    <div
      ref={setNodeRef}
      className="flex items-center gap-2 rounded-xl border border-divider px-3 py-2"
      style={{
        transform: CSS.Transform.toString(transform),
        transition,
        opacity: isDragging ? 0.6 : 1,
      }}
    >
      <button
        aria-label={`拖动 ${label} 调整顺序`}
        className="shrink-0 cursor-grab touch-none text-foreground"
        type="button"
        {...attributes}
        {...listeners}
      >
        <GripVertical className="h-4 w-4" />
      </button>
      {editing ? (
        <>
          <Input
            aria-label="新分组名"
            className="min-w-0 flex-1"
            size="sm"
            value={editValue}
            variant="bordered"
            onChange={(e) => onEditValueChange(e.target.value)}
          />
          <Button
            color="primary"
            isDisabled={busy}
            size="sm"
            variant="flat"
            onPress={onSaveRename}
          >
            保存
          </Button>
          <Button
            isDisabled={busy}
            size="sm"
            variant="light"
            onPress={onCancelRename}
          >
            取消
          </Button>
        </>
      ) : confirmingDelete ? (
        <>
          <span className="min-w-0 flex-1 truncate text-sm font-semibold">
            删除「{label}」？
            {count > 0 ? `${count} 台节点将回未分组` : ""}
          </span>
          <Button
            color="danger"
            isDisabled={busy}
            size="sm"
            variant="flat"
            onPress={onConfirmDelete}
          >
            确认删除
          </Button>
          <Button
            isDisabled={busy}
            size="sm"
            variant="light"
            onPress={onCancelDelete}
          >
            取消
          </Button>
        </>
      ) : (
        <>
          <span className="min-w-0 flex-1 truncate text-sm font-semibold">
            {label}
            {/* F-W1-06：低透明前景字在玻璃载体上实测 3.48–4.11＜4.5，
                R6 口径此类为零——改全量前景色（与仓内 bg-tray＋
                text-foreground 已验组合一致）。 */}
            <span className="ml-2 text-xs font-semibold text-foreground">
              {count} 台
            </span>
          </span>
          <Button
            isDisabled={busy}
            size="sm"
            variant="light"
            onPress={onStartRename}
          >
            改名
          </Button>
          <Button
            color="danger"
            isDisabled={busy}
            size="sm"
            variant="light"
            onPress={onStartDelete}
          >
            删除
          </Button>
          <span className="flex shrink-0 items-center gap-1">
            <Button
              aria-label={`上移 ${label}`}
              isDisabled={index === 0 || busy}
              size="sm"
              variant="flat"
              onPress={() => onMove(index, -1)}
            >
              <ChevronUp className="h-4 w-4" />
            </Button>
            <Button
              aria-label={`下移 ${label}`}
              isDisabled={index === total - 1 || busy}
              size="sm"
              variant="flat"
              onPress={() => onMove(index, 1)}
            >
              <ChevronDown className="h-4 w-4" />
            </Button>
          </span>
        </>
      )}
    </div>
  );
}

export function GroupManageModal({
  counts,
  groups,
  onClose,
  onCreate,
  onDelete,
  onRename,
  onSaveOrder,
  open,
}: NodeGroupActions & {
  /** Member count per group label (0 for registered-but-empty). */
  counts: Record<string, number>;
  /** All known groups (registry ∪ in-use) in effective order. */
  groups: string[];
  onClose: () => void;
  open: boolean;
}) {
  const [draft, setDraft] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);
  const [busy, setBusy] = useState(false);
  const [newName, setNewName] = useState("");
  const [editing, setEditing] = useState<string | null>(null);
  const [editValue, setEditValue] = useState("");
  const [confirmingDelete, setConfirmingDelete] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setDraft(groups);
      setNewName("");
      setEditing(null);
      setConfirmingDelete(null);
    }
    // Re-seed only when the dialog opens; `groups` changing underneath
    // an open dialog must not clobber the user's draft.
  }, [open]);

  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 8 } }),
    useSensor(TouchSensor, {
      activationConstraint: { delay: 250, tolerance: 8 },
    }),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  const move = (index: number, delta: number) => {
    setDraft((prev) => {
      const target = index + delta;

      if (target < 0 || target >= prev.length) return prev;

      return arrayMove(prev, index, target);
    });
  };

  const handleDragEnd = (event: DragEndEvent) => {
    const { active, over } = event;

    if (!over || active.id === over.id) return;
    setDraft((prev) => {
      const from = prev.indexOf(String(active.id));
      const to = prev.indexOf(String(over.id));

      if (from < 0 || to < 0) return prev;

      return arrayMove(prev, from, to);
    });
  };

  const create = async () => {
    const name = newName.trim();

    if (!name) {
      toast.error("分组名称不能为空");

      return;
    }
    setBusy(true);
    try {
      const result = await onCreate(name);

      if (result.ok) {
        toast.success(`分组「${name}」已创建`);
        setDraft((prev) => (prev.includes(name) ? prev : [...prev, name]));
        setNewName("");
      } else {
        toast.error(result.msg || "创建失败");
      }
    } finally {
      setBusy(false);
    }
  };

  const saveRename = async (label: string) => {
    const name = editValue.trim();

    if (!name) {
      toast.error("分组名称不能为空");

      return;
    }
    setBusy(true);
    try {
      const result = await onRename(label, name);

      if (result.ok) {
        toast.success(result.msg || `已改名为「${name}」`);
        setDraft((prev) => prev.map((g) => (g === label ? name : g)));
        setEditing(null);
      } else {
        toast.error(result.msg || "改名失败");
      }
    } finally {
      setBusy(false);
    }
  };

  const confirmDelete = async (label: string) => {
    setBusy(true);
    try {
      const result = await onDelete(label);

      if (result.ok) {
        toast.success(result.msg || `分组「${label}」已删除`);
        setDraft((prev) => prev.filter((g) => g !== label));
        setConfirmingDelete(null);
      } else {
        toast.error(result.msg || "删除失败");
      }
    } finally {
      setBusy(false);
    }
  };

  const saveOrder = async () => {
    setSaving(true);
    try {
      if (await onSaveOrder(draft)) {
        toast.success("分组顺序已保存");
        onClose();
      } else {
        toast.error("保存失败");
      }
    } catch {
      toast.error("保存失败");
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={open}
      placement="center"
      scrollBehavior="inside"
      size="lg"
      onOpenChange={(v) => !v && onClose()}
    >
      <ModalContent>
        <ModalHeader className="flex flex-col gap-0.5">
          <h2 className="text-lg font-bold">分组管理</h2>
          <p className="text-xs font-semibold text-foreground">
            拖动调整展示顺序（点「保存顺序」生效）；新建、改名、删除立即生效，改名会同步成员节点，删除后成员回未分组。未分组恒在最后，不在此列。
          </p>
        </ModalHeader>
        <ModalBody>
          <div className="mb-3 flex items-center gap-2">
            <Input
              aria-label="新分组名"
              className="min-w-0 flex-1"
              maxLength={100}
              placeholder="新分组名"
              size="sm"
              value={newName}
              variant="bordered"
              onChange={(e) => setNewName(e.target.value)}
            />
            <Button
              className="shrink-0"
              color="primary"
              isDisabled={busy}
              size="sm"
              variant="flat"
              onPress={() => void create()}
            >
              新建分组
            </Button>
          </div>
          <DndContext sensors={sensors} onDragEnd={handleDragEnd}>
            <SortableContext
              items={draft}
              strategy={verticalListSortingStrategy}
            >
              <div className="space-y-2">
                {draft.map((label, index) => (
                  <SortableGroupRow
                    key={label}
                    busy={busy}
                    confirmingDelete={confirmingDelete === label}
                    count={counts[label] ?? 0}
                    editValue={editing === label ? editValue : ""}
                    editing={editing === label}
                    index={index}
                    label={label}
                    total={draft.length}
                    onCancelDelete={() => setConfirmingDelete(null)}
                    onCancelRename={() => setEditing(null)}
                    onConfirmDelete={() => void confirmDelete(label)}
                    onEditValueChange={setEditValue}
                    onMove={move}
                    onSaveRename={() => void saveRename(label)}
                    onStartDelete={() => {
                      setEditing(null);
                      setConfirmingDelete(label);
                    }}
                    onStartRename={() => {
                      setConfirmingDelete(null);
                      setEditing(label);
                      setEditValue(label);
                    }}
                  />
                ))}
                {draft.length === 0 && (
                  <p className="text-sm text-foreground">
                    还没有分组。在上方输入名称新建，或用工具栏的「新建分组」。
                  </p>
                )}
              </div>
            </SortableContext>
          </DndContext>
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            isLoading={saving}
            onPress={() => void saveOrder()}
          >
            保存顺序
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}

/** Standalone create-group dialog behind the toolbar 「新建分组」 button. */
export function GroupCreateModal({
  onClose,
  onCreate,
  open,
}: {
  onClose: () => void;
  onCreate: (name: string) => Promise<GroupOpResult>;
  open: boolean;
}) {
  const [name, setName] = useState("");
  const [creating, setCreating] = useState(false);

  useEffect(() => {
    if (open) setName("");
  }, [open]);

  const create = async () => {
    const trimmed = name.trim();

    if (!trimmed) {
      toast.error("分组名称不能为空");

      return;
    }
    setCreating(true);
    try {
      const result = await onCreate(trimmed);

      if (result.ok) {
        toast.success(`分组「${trimmed}」已创建，勾选节点后用「移入分组」加入`);
        onClose();
      } else {
        toast.error(result.msg || "创建失败");
      }
    } finally {
      setCreating(false);
    }
  };

  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={open}
      placement="center"
      scrollBehavior="inside"
      size="md"
      onOpenChange={(v) => !v && onClose()}
    >
      <ModalContent>
        <ModalHeader className="flex flex-col gap-0.5">
          <h2 className="text-lg font-bold">新建分组</h2>
          <p className="text-xs font-semibold text-foreground">
            分组是纯展示标签：创建后勾选节点、用批量操作里的「移入分组」把节点加进来。
          </p>
        </ModalHeader>
        <ModalBody>
          <Input
            aria-label="分组名"
            label="分组名"
            maxLength={100}
            placeholder="例如：落地节点"
            value={name}
            variant="bordered"
            onChange={(e) => setName(e.target.value)}
          />
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            isLoading={creating}
            onPress={() => void create()}
          >
            创建
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
