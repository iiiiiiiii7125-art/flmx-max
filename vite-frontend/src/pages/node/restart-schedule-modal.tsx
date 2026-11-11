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
const MIN_INTERVAL_MINUTES = 15;
const MAX_INTERVAL_MINUTES = 10080;
const INTERVAL_PRESETS: { label: string; minutes: number }[] = [
  { label: "30 分钟", minutes: 30 },
  { label: "1 小时", minutes: 60 },
  { label: "2 小时", minutes: 120 },
  { label: "6 小时", minutes: 360 },
  { label: "12 小时", minutes: 720 },
];

/**
 * Per-node scheduled agent restart (a fuse, off by default). Two
 * cadences: daily at a fixed local time, or every N minutes. The
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
  const [mode, setMode] = useState<"daily" | "interval">("daily");
  const [interval, setInterval] = useState("60");
  const [saving, setSaving] = useState(false);
  // C6-01：agent 下发失败（pushError）时不关弹窗——失败原因必须在
  // 弹窗内常驻呈现，直到用户读完手动关闭；旧实现只 toast 一闪，
  // 随后列表照常显示「已启用」，用户无从知道计时器根本没落到节点。
  const [pushFailed, setPushFailed] = useState("");
  const open = node !== null;

  useEffect(() => {
    if (!open || !node) return;
    setEnabled(node.restartEnabled === 1);
    setTime(node.restartTime || "04:00");
    setJitter(String(node.restartJitterMinutes ?? 30));
    setMode(node.restartMode === "interval" ? "interval" : "daily");
    setInterval(String(node.restartIntervalMinutes ?? 60));
    setPushFailed("");
  }, [open, node]);

  const jitterNum = Number(jitter);
  const jitterValid =
    jitter.trim() !== "" &&
    Number.isInteger(jitterNum) &&
    jitterNum >= 0 &&
    jitterNum <= MAX_JITTER_MINUTES;
  const timeValid = /^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(time);
  const intervalNum = Number(interval);
  const intervalValid =
    interval.trim() !== "" &&
    Number.isInteger(intervalNum) &&
    intervalNum >= MIN_INTERVAL_MINUTES &&
    intervalNum <= MAX_INTERVAL_MINUTES;
  const valid =
    !enabled ||
    (jitterValid && (mode === "daily" ? timeValid : intervalValid));

  const save = async () => {
    if (!node) return;
    setSaving(true);
    try {
      const res = await setNodeRestartSchedule({
        id: node.id,
        enabled: enabled ? 1 : 0,
        time,
        jitterMinutes: jitterValid ? jitterNum : 0,
        mode,
        intervalMinutes: intervalValid ? intervalNum : 60,
      });

      if (res.code !== 0) {
        toast.error(res.msg || "保存失败");

        return;
      }
      if (res.data?.pushError) {
        // C6-01：设置已落库但 agent 没接受（如无 systemd 环境直接
        // 拒绝持久化）。刷新列表（开关会显示已启用——那只是面板侧
        // 设置），但弹窗保持打开并常驻失败原因，讲清「已启用≠会
        // 生效」，由用户读完后手动关闭；不再 toast 一闪了事。
        setPushFailed(res.data.pushError);
        onSaved();

        return;
      }
      if (res.data?.pushed) {
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
              onValueChange={(v) => {
                setEnabled(v);
                setPushFailed("");
              }}
            />
            <div className="text-sm">
              <span className="font-semibold">定时重启 agent 服务</span>
              <p className="text-xs font-semibold text-foreground">
                重启的是节点上的 agent 服务（flmx_agent），不是整机重启；转发会短暂中断，服务恢复后自动重连。
              </p>
            </div>
          </div>

          <div className="flex items-center gap-2">
            <Button
              color="primary"
              isDisabled={!enabled}
              size="sm"
              variant={mode === "daily" ? "solid" : "bordered"}
              onPress={() => setMode("daily")}
            >
              每天定时
            </Button>
            <Button
              color="primary"
              isDisabled={!enabled}
              size="sm"
              variant={mode === "interval" ? "solid" : "bordered"}
              onPress={() => setMode("interval")}
            >
              按间隔
            </Button>
          </div>

          {mode === "daily" ? (
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
          ) : (
            <div className="space-y-3">
              <div className="flex flex-wrap items-center gap-2">
                {INTERVAL_PRESETS.map((p) => (
                  <Button
                    key={p.minutes}
                    color="primary"
                    isDisabled={!enabled}
                    size="sm"
                    variant={
                      intervalNum === p.minutes ? "solid" : "bordered"
                    }
                    onPress={() => setInterval(String(p.minutes))}
                  >
                    {p.label}
                  </Button>
                ))}
              </div>
              <div className="grid grid-cols-2 gap-3">
                <Input
                  description="每隔该分钟数重启一次；首次在节点开机/计时器启用后一个间隔触发，停机期间错过的拍不补"
                  errorMessage={
                    enabled && !intervalValid
                      ? `请输入 ${MIN_INTERVAL_MINUTES}–${MAX_INTERVAL_MINUTES} 的整数分钟`
                      : undefined
                  }
                  isDisabled={!enabled}
                  isInvalid={enabled && !intervalValid}
                  label="重启间隔（分钟）"
                  max={MAX_INTERVAL_MINUTES}
                  min={MIN_INTERVAL_MINUTES}
                  type="number"
                  value={interval}
                  variant="bordered"
                  onChange={(e) => setInterval(e.target.value)}
                />
                <Input
                  description={`每次在名义触发时刻后 0–${MAX_JITTER_MINUTES} 分钟内随机延迟，避免多台节点同时重启`}
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
            </div>
          )}

          <p className="text-xs font-semibold text-foreground">
            计时器保存在节点本机（systemd timer），面板离线或面板暂时连不上时照常执行；关闭开关会删除节点上的计时器。默认关闭。
          </p>
          {/* C6-01 前置边界：持久化依赖节点上有 systemd。容器形态 /
              无 systemd 的节点开启时 agent 会直接拒绝，设置存进面板
              也不会生效——先讲清，免得用户对着「已启用」空等。 */}
          <p className="text-xs font-semibold text-foreground">
            前提：节点 agent 须以 systemd 服务方式运行。容器形态或无
            systemd 的节点无法持久化此设置，开启时 agent 会拒绝下发，面板里显示「已启用」也不代表会执行。
          </p>
          {pushFailed && (
            <div className="rounded-xl bg-danger-50 px-3 py-2">
              <p className="text-xs font-semibold text-danger-700">
                {pushFailed}
              </p>
              <p className="mt-1 text-xs font-semibold text-danger-700">
                在 agent 实际接受前，该节点的定时重启不会生效；节点重连时面板会自动补发，但同一原因（如无
                systemd）会再次失败。请处理节点环境后重新保存。
              </p>
            </div>
          )}
          {enabled && mode === "daily" && !timeValid && (
            <p className="text-xs font-semibold text-danger-700">
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
