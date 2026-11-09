import type { BatchOperationFailure } from "@/api/types";

import { copyWithToast } from "@/utils/clipboard";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";
import { Alert } from "@/shadcn-bridge/heroui/alert";

interface BatchActionResultModalProps {
  failures: BatchOperationFailure[];
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  /** 待清理项（节点不可达，后台自动清理）：与失败分开呈现、单独标签（FT-7）。 */
  pending?: BatchOperationFailure[];
  summary: string;
  title: string;
}

const getFailureTitle = (
  failure: BatchOperationFailure,
  index: number,
): string => {
  const name = typeof failure.name === "string" ? failure.name.trim() : "";

  if (name) {
    return name;
  }

  if (typeof failure.id === "number" && Number.isFinite(failure.id)) {
    return `ID ${failure.id}`;
  }

  return `失败项 ${index + 1}`;
};

const getFailureReason = (failure: BatchOperationFailure): string => {
  const reason =
    typeof failure.reason === "string" ? failure.reason.trim() : "";

  return reason || "未知错误";
};

const buildFailureCopyText = (
  title: string,
  summary: string,
  failures: BatchOperationFailure[],
  pending: BatchOperationFailure[],
): string => {
  const lines: string[] = [title, summary, ""];

  if (failures.length > 0) {
    if (pending.length > 0) {
      lines.push("【失败】");
    }
    failures.forEach((failure, index) => {
      lines.push(
        `${index + 1}. ${getFailureTitle(failure, index)}\n${getFailureReason(failure)}`,
      );
    });
  }

  if (pending.length > 0) {
    if (failures.length > 0) {
      lines.push("");
    }
    lines.push("【待清理（节点恢复后后台自动清理，无需手工处理）】");
    pending.forEach((item, index) => {
      lines.push(
        `${index + 1}. ${getFailureTitle(item, index)}\n${getFailureReason(item)}`,
      );
    });
  }

  return lines.join("\n");
};

export function BatchActionResultModal({
  failures,
  isOpen,
  onOpenChange,
  pending = [],
  summary,
  title,
}: BatchActionResultModalProps) {
  const handleCopy = async () => {
    await copyWithToast(
      buildFailureCopyText(title, summary, failures, pending),
      failures.length > 0
        ? `已复制 ${failures.length} 项失败原因`
        : `已复制 ${pending.length} 项待清理清单`,
    );
  };

  const alertTitle =
    failures.length > 0 && pending.length > 0
      ? `共 ${failures.length} 项失败、${pending.length} 项待清理`
      : pending.length > 0
        ? `共 ${pending.length} 项待清理`
        : `共 ${failures.length} 项需要处理`;

  const renderItems = (
    items: BatchOperationFailure[],
    chipText: string,
    chipColor: "danger" | "warning",
    expandHint: string,
  ) => (
    <div className="space-y-3">
      {items.map((failure, index) => (
        <details
          key={`${chipText}-${failure.id ?? "unknown"}-${index}`}
          className="group rounded-xl border border-divider px-4 py-3"
        >
          <summary className="flex cursor-pointer list-none items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-foreground">
                {getFailureTitle(failure, index)}
              </p>
              <p className="mt-1 text-xs font-semibold text-foreground group-open:hidden">
                {expandHint}
              </p>
            </div>
            <Chip color={chipColor} size="sm" variant="flat">
              {chipText}
            </Chip>
          </summary>
          <div className="mt-3 rounded-lg glass-inset p-3 text-sm leading-6 text-foreground">
            {getFailureReason(failure)}
          </div>
        </details>
      ))}
    </div>
  );

  return (
    <Modal
      isOpen={isOpen}
      scrollBehavior="inside"
      size="2xl"
      onOpenChange={onOpenChange}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader>{title}</ModalHeader>
            <ModalBody className="space-y-4">
              <Alert
                color="warning"
                description={summary}
                title={alertTitle}
                variant="flat"
              />
              {failures.length > 0 &&
                renderItems(failures, "失败", "danger", "点击展开查看失败原因")}
              {pending.length > 0 && (
                <div className="space-y-2">
                  {failures.length > 0 && (
                    <p className="text-sm font-semibold text-foreground">
                      待清理（节点恢复后后台自动清理，无需手工处理）
                    </p>
                  )}
                  {renderItems(
                    pending,
                    "待清理",
                    "warning",
                    "点击展开查看待清理说明",
                  )}
                </div>
              )}
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={handleCopy}>
                {failures.length > 0 ? "复制失败原因" : "复制待清理清单"}
              </Button>
              <Button color="primary" onPress={onClose}>
                我知道了
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
