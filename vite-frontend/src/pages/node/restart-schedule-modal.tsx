import type { Node } from "@/pages/node/types";

import { useEffect, useState } from "react";
import toast from "react-hot-toast";

import { setNodeRestartSchedule } from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Input } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";
import { Switch } from "@/shadcn-bridge/heroui/switch";

const MAX_JITTER_MINUTES = 720;

/**
 * Per-node scheduled agent restart (a fuse, off by default). The
 * panel stores the setting and pushes it to the agent, which
 * persists it as a systemd timer on the node itself — the restart
 * therefore fires even while the panel is unreachable. Only the
 * agent service restarts, never the whole machine.
 */
export function RestartScheduleModal({
  node,
  onClose,
  onSaved,
}: {
  /** Node being set; null closes the dialog. */
  node: Node | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [enabled, setEnabled] = useState(false);
  const [time, setTime] = useState("04:00");
  const [jitter, setJitter] = useState("30");
  const [saving, setSaving] = useState(false);
  const open = node !== null;

  useEffect(() => {
    if (!open || !node) return;
    setEnabled(node.restartEnabled === 1);
    setTime(node.restartTime || "04:00");
    setJitter(String(node.restartJitterMinutes ?? 30));
  }, [open, node]);

  const jitterNum = Number(jitter);
  const jitterValid =
    jitter.trim() !== "" &&
    Number.isInteger(jitterNum) &&
    jitterNum >= 0 &&
    jitterNum <= MAX_JITTER_MINUTES;
  const timeValid = /^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(time);
  const valid = !enabled || (timeValid && jitterValid);

  const save = async () => {
    if (!node) return;
    setSaving(true);
    try {
      const res = await setNodeRestartSchedule({
        id: node.id,
        enabled: enabled ? 1 : 0,
        time,
        jitterMinutes: jitterValid ? jitterNum : 0,
      });

      if (res.code !== 0) {
        toast.error(res.msg || "保存失败");

        return;
      }
      if (res.data?.pushError) {
        toast.error(res.data.pushError);
      } else if (res.data?.pushed) {
        toast.success("已保存并下发到节点");
      } else {
        toast.success("已保存，节点上线后自动下发");
      }
      onSaved();
      onClose();
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
          <h2 className="text-lg font-bold">定时重启</h2>
          <p className="truncate text-xs font-semibold text-foreground">
            {node?.name}
          </p>
        </ModalHeader>
        <ModalBody className="space-y-4">
          <div className="flex items-center gap-3">
            <Switch
              isSelected={enabled}
              size="sm"
              onValueChange={setEnabled}
            />
            <div className="text-sm">
              <span className="font-semibold">每天定时重启 agent 服务</span>
              <p className="text-xs font-semibold text-foreground">
                重启的是节点上的 agent 服务（flmx_agent），不是整机重启；转发会短暂中断，服务恢复后自动重连。
              </p>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-3">
            <Input
              description="节点本机时间，每天到点触发"
              isDisabled={!enabled}
              label="重启时刻"
              type="time"
              value={time}
              variant="bordered"
              onChange={(e) => setTime(e.target.value)}
            />
            <Input
              description={`在重启时刻后 0–${MAX_JITTER_MINUTES} 分钟内随机触发，避免多台节点同时重启`}
              errorMessage={
                enabled && !jitterValid
                  ? `请输入 0–${MAX_JITTER_MINUTES} 的整数分钟`
                  : undefined
              }
              isDisabled={!enabled}
              isInvalid={enabled && !jitterValid}
              label="随机抖动（分钟）"
              max={MAX_JITTER_MINUTES}
              min={0}
              type="number"
              value={jitter}
              variant="bordered"
              onChange={(e) => setJitter(e.target.value)}
            />
          </div>

          <p className="text-xs font-semibold text-foreground">
            计时器保存在节点本机（systemd timer），面板离线或面板暂时连不上时照常执行；关闭开关会删除节点上的计时器。默认关闭。
          </p>
          {enabled && !timeValid && (
            <p className="text-xs font-semibold text-danger">
              重启时刻格式无效，请用 24 小时制时间。
            </p>
          )}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            取消
          </Button>
          <Button
            color="primary"
            isDisabled={!valid}
            isLoading={saving}
            onPress={save}
          >
            保存
          </Button>
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
