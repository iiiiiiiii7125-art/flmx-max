import type { Dispatch, SetStateAction } from "react";
import { useEffect, useState } from "react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@/shadcn-bridge/heroui/modal";
import { Node } from "@/pages/node/types";

/**
 * 政策点（2026-11-03 定版）：删除服务器时「同时卸载服务器上的
 * agent」默认勾选——删服务器通常意味着退役该机器。改默认只改这一
 * 处常量，单删与批量删共用。
 */
export const DELETE_NODE_UNINSTALL_DEFAULT = true;

/** 每次打开删除确认窗时把卸载勾选重置为政策默认。 */
function useUninstallChoice(open: boolean) {
  const [uninstall, setUninstall] = useState(DELETE_NODE_UNINSTALL_DEFAULT);

  useEffect(() => {
    if (open) setUninstall(DELETE_NODE_UNINSTALL_DEFAULT);
  }, [open ]);

  return [uninstall, setUninstall] as const;
}

function UninstallAgentOption({
  isSelected,
  onValueChange,
}: {
  isSelected: boolean;
  onValueChange: (value: boolean) => void;
}) {
  return (
    <div className="space-y-1 rounded-xl border border-default-200 bg-default-50 px-3 py-2.5">
      <Checkbox
        isSelected={isSelected}
        size="sm"
        onValueChange={onValueChange}
      >
        同时卸载服务器上的 agent
      </Checkbox>
      <p className="pl-6 text-xs font-semibold text-foreground">
        停止并删除 agent、内核和 nftables
        规则，恢复调优前的系统参数。服务器离线时，会在它下次连接面板时自动卸载。只想把服务器移到另一个面板时请取消勾选。
      </p>
    </div>
  );
}

export interface RollbackNodeModalProps {
  confirmRollback: () => Promise<void>;
  nodeToRollback: Node | null;
  rollbackModalOpen: boolean;
  setRollbackModalOpen: Dispatch<SetStateAction<boolean>>;
}

/** Confirm rolling a node's agent back to the previous version. */
export function RollbackNodeModal({
  confirmRollback,
  nodeToRollback,
  rollbackModalOpen,
  setRollbackModalOpen,
}: RollbackNodeModalProps) {
  return (
    <Modal
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={rollbackModalOpen}
      placement="center"
      scrollBehavior="inside"
      size="2xl"
      onOpenChange={setRollbackModalOpen}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h2 className="text-xl font-bold">确认回退</h2>
            </ModalHeader>
            <ModalBody>
              <p>
                确定要将节点 <strong>&quot;{nodeToRollback?.name}&quot;</strong>{" "}
                回退到上一个版本吗？
              </p>
              <p className="text-small text-foreground">
                节点将执行版本回退并自动重启，期间会导致节点短暂离线。
              </p>
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={onClose}>
                取消
              </Button>
              <Button color="secondary" onPress={confirmRollback}>
                确认回退
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}

export interface DeleteNodeModalProps {
  /** R2X-045: 跨节点引用清单（删除将打断这些引用），确认前点名。 */
  brokenReferrers?: string[];
  confirmDelete: (uninstall: boolean) => Promise<void>;
  deleteLoading: boolean;
  deleteModalOpen: boolean;
  nodeToDelete: Node | null;
  setDeleteModalOpen: Dispatch<SetStateAction<boolean>>;
}

/** Confirm deleting one node. */
export function DeleteNodeModal({
  brokenReferrers,
  confirmDelete,
  deleteLoading,
  deleteModalOpen,
  nodeToDelete,
  setDeleteModalOpen,
}: DeleteNodeModalProps) {
  const [uninstall, setUninstall] = useUninstallChoice(deleteModalOpen);

  return (
    <Modal
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={deleteModalOpen}
      placement="center"
      scrollBehavior="inside"
      size="2xl"
      onOpenChange={setDeleteModalOpen}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h2 className="text-xl font-bold">确认删除</h2>
            </ModalHeader>
            <ModalBody>
              <p>
                确定要删除节点 <strong>&quot;{nodeToDelete?.name}&quot;</strong>{" "}
                吗？
              </p>
              {brokenReferrers && brokenReferrers.length > 0 ? (
                <p className="text-small text-danger-700">
                  删除将打断 {brokenReferrers.length} 个跨节点引用：
                  {brokenReferrers.join("、")}
                  。这些入站经本节点出去的流量将被丢弃，请先解除引用或确认继续。
                </p>
              ) : null}
              <UninstallAgentOption
                isSelected={uninstall}
                onValueChange={setUninstall}
              />
              <p className="text-small text-foreground">
                此操作不可恢复，请谨慎操作。
              </p>
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={onClose}>
                取消
              </Button>
              <Button
                color="danger"
                isLoading={deleteLoading}
                onPress={() => confirmDelete(uninstall)}
              >
                {deleteLoading ? "删除中..." : "确认删除"}
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}

export interface BatchDeleteNodesModalProps {
  batchDeleteModalOpen: boolean;
  batchLoading: boolean;
  /** R2X-045: 跨节点引用清单（删除将打断这些引用），确认前点名。 */
  brokenReferrers?: string[];
  handleBatchDelete: (uninstall: boolean) => Promise<void>;
  selectedIds: Set<number>;
  setBatchDeleteModalOpen: Dispatch<SetStateAction<boolean>>;
}

/** Confirm deleting the selected nodes. */
export function BatchDeleteNodesModal({
  batchDeleteModalOpen,
  batchLoading,
  brokenReferrers,
  handleBatchDelete,
  selectedIds,
  setBatchDeleteModalOpen,
}: BatchDeleteNodesModalProps) {
  const [uninstall, setUninstall] = useUninstallChoice(batchDeleteModalOpen);

  return (
    <Modal
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={batchDeleteModalOpen}
      placement="center"
      scrollBehavior="inside"
      size="md"
      onOpenChange={setBatchDeleteModalOpen}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h2 className="text-xl font-bold">确认删除</h2>
            </ModalHeader>
            <ModalBody>
              <p>
                确定要删除选中的 <strong>{selectedIds.size}</strong> 个节点吗？
              </p>
              {brokenReferrers && brokenReferrers.length > 0 ? (
                <p className="text-small text-danger-700">
                  删除将打断 {brokenReferrers.length} 个跨节点引用：
                  {brokenReferrers.join("、")}
                  。这些入站经被删节点出去的流量将被丢弃，请先解除引用或确认继续。
                </p>
              ) : null}
              <UninstallAgentOption
                isSelected={uninstall}
                onValueChange={setUninstall}
              />
              <p className="text-small text-foreground">
                此操作不可恢复，请谨慎操作。
              </p>
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={onClose}>
                取消
              </Button>
              <Button
                color="danger"
                isLoading={batchLoading}
                onPress={() => handleBatchDelete(uninstall)}
              >
                {batchLoading ? "删除中..." : "确认删除"}
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
