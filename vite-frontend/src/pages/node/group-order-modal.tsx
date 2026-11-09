import { useEffect, useState } from "react";
import toast from "react-hot-toast";
import { ChevronDown, ChevronUp } from "lucide-react";

import { Button } from "@/shadcn-bridge/heroui/button";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";

/**
 * Manual ordering for the node page's display-group sections. The
 * draft lives in the dialog: arrows only rearrange the draft, 保存
 * persists it (panel config `node_display_group_order`), 取消
 * discards it. Only labels currently in use are listed — the
 * ungrouped section is pinned last by rule and never appears here.
 */
export function GroupOrderModal({
  labels,
  onClose,
  onSave,
  open,
}: {
  /** In-use group labels in their currently effective order. */
  labels: string[];
  onClose: () => void;
  /** Persists the new order; resolves true when saved. */
  onSave: (order: string[]) => Promise<boolean>;
  open: boolean;
}) {
  const [draft, setDraft] = useState<string[]>([]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (open) setDraft(labels);
    // Re-seed the draft only when the dialog opens; `labels` changing
    // underneath an open dialog must not clobber the user's edits.
  }, [open]);

  const move = (index: number, delta: number) => {
    setDraft((prev) => {
      const next = [...prev];
      const target = index + delta;

      if (target < 0 || target >= next.length) return prev;
      [next[index], next[target]] = [next[target], next[index]];

      return next;
    });
  };

  const save = async () => {
    setSaving(true);
    try {
      if (await onSave(draft)) {
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
          <h2 className="text-lg font-bold">分组排序</h2>
          <p className="text-xs font-semibold text-foreground">
            调整节点页各分组的展示顺序；未分组恒在最后，不在此列。
          </p>
        </ModalHeader>
        <ModalBody>
          <div className="space-y-2">
            {draft.map((label, index) => (
              <div
                key={label}
                className="flex items-center justify-between gap-3 rounded-xl border border-divider px-3 py-2"
              >
                <span className="min-w-0 truncate text-sm font-semibold">
                  {label}
                </span>
                <span className="flex shrink-0 items-center gap-1">
                  <Button
                    aria-label={`上移 ${label}`}
                    isDisabled={index === 0}
                    size="sm"
                    variant="flat"
                    onPress={() => move(index, -1)}
                  >
                    <ChevronUp className="h-4 w-4" />
                  </Button>
                  <Button
                    aria-label={`下移 ${label}`}
                    isDisabled={index === draft.length - 1}
                    size="sm"
                    variant="flat"
                    onPress={() => move(index, 1)}
                  >
                    <ChevronDown className="h-4 w-4" />
                  </Button>
                </span>
              </div>
            ))}
            {draft.length === 0 && (
              <p className="text-sm text-foreground">
                当前没有在用的分组标签。
              </p>
            )}
          </div>
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            isLoading={saving}
            onPress={() => void save()}
          >
            保存
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
