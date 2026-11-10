import type { Node } from "@/pages/node/types";

import { useEffect, useRef, useState } from "react";
import toast from "react-hot-toast";

import {
  getNodeSwapInfo,
  setNodeSwap,
  type NodeSwapInfo,
  type NodeSwapSetResult,
} from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Input } from "@/shadcn-bridge/heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";

const PRESET_GB = [1, 2, 4, 8];
const MAX_CUSTOM_GB = 32;

/** MB → 人读：满 1 GB 用 GB（整数不带小数），否则 MB 原值。 */
const formatMB = (mb: number): string =>
  mb >= 1024
    ? `${Number((mb / 1024).toFixed(1))} GB`
    : `${Math.round(mb)} MB`;

/**
 * 节点页「设置 Swap」（用户 2026-11-10 令）：弹窗打开经 agent 拉节点
 * 本机现状，用户选大小、确认后面板下发 agent 执行（管理 /swapfile：
 * 建/调大小、fstab 幂等）；非面板管理的既存 swap 只报不动、另建叠加。
 * 执行失败原因（后端 msg）常驻弹窗内原文呈现，不许只 toast 一闪。
 */
export function SwapModal({
  node,
  onClose,
}: {
  /** Node being set; null closes the dialog. */
  node: Node | null;
  onClose: () => void;
}) {
  const [info, setInfo] = useState<NodeSwapInfo | null>(null);
  // IRL-3：现状拉取失败必须常驻标记＋重试，不许按「无 swap」假空渲染。
  const [swapInfoLoadFailed, setSwapInfoLoadFailed] = useState(false);
  const [infoError, setInfoError] = useState("");
  const [loading, setLoading] = useState(false);
  const [choice, setChoice] = useState(2);
  const [custom, setCustom] = useState(false);
  const [customText, setCustomText] = useState("");
  const [executing, setExecuting] = useState(false);
  const [execError, setExecError] = useState("");
  const [result, setResult] = useState<NodeSwapSetResult | null>(null);
  const seqRef = useRef(0);
  const open = node !== null;

  const loadInfo = async () => {
    if (!node) return;
    const seq = ++seqRef.current;

    setLoading(true);
    setSwapInfoLoadFailed(false);
    setInfoError("");
    try {
      const res = await getNodeSwapInfo(node.id);

      if (seqRef.current !== seq) return;
      if (
        res.code === 0 &&
        res.data &&
        typeof res.data.totalMB === "number" &&
        Array.isArray(res.data.entries)
      ) {
        setInfo(res.data);
      } else {
        setInfo(null);
        setSwapInfoLoadFailed(true);
        setInfoError(res.msg || "");
      }
    } catch {
      if (seqRef.current !== seq) return;
      setInfo(null);
      setSwapInfoLoadFailed(true);
    } finally {
      if (seqRef.current === seq) setLoading(false);
    }
  };

  useEffect(() => {
    if (!open) return;
    setInfo(null);
    setSwapInfoLoadFailed(false);
    setInfoError("");
    setChoice(2);
    setCustom(false);
    setCustomText("");
    setExecError("");
    setResult(null);
    void loadInfo();
    // loadInfo 以 node/open 为界重置并拉取，seqRef 防旧响应覆盖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, node]);

  const customNum = Number(customText);
  const customValid =
    customText.trim() !== "" &&
    Number.isInteger(customNum) &&
    customNum >= 1 &&
    customNum <= MAX_CUSTOM_GB;
  const selectedGB = custom ? (customValid ? customNum : null) : choice;

  // 还需占用的磁盘：托管文件是调大小——只补差额；非托管是另建——全额。
  const managedSizeMB = info?.managedFilePresent
    ? (info.entries.find((e) => e.name === "/swapfile")?.sizeMB ?? 0)
    : 0;
  const requiredMB =
    info && selectedGB !== null
      ? Math.max(0, selectedGB * 1024 - managedSizeMB)
      : 0;
  const shortMB = info ? Math.max(0, requiredMB - info.diskFreeMB) : 0;

  const confirmDisabled =
    !info ||
    swapInfoLoadFailed ||
    executing ||
    result !== null ||
    selectedGB === null ||
    shortMB > 0;

  const execute = async () => {
    if (!node || selectedGB === null) return;
    setExecuting(true);
    setExecError("");
    try {
      const res = await setNodeSwap({ nodeId: node.id, sizeGB: selectedGB });

      if (res.code !== 0 || !res.data) {
        // 后端 msg 是给用户看的中文原因（空间不足/swapoff 风险等），原文常驻。
        setExecError(res.msg || "执行失败");

        return;
      }
      setResult(res.data);
      setInfo((prev) =>
        prev
          ? {
              ...prev,
              totalMB: res.data.after.totalMB,
              usedMB: res.data.after.usedMB,
            }
          : prev,
      );
      toast.success(`已设置 Swap 为 ${res.data.sizeGB} GB`);
      // 回执 warnings 是非致命残留提示（现仅旧 swap 备份删除失败一源）：逐条
      // 按全仓 envelope warning 形态追加 ⚠️ toast，不许只报成功吞掉。
      for (const warning of res.data.warnings ?? []) {
        toast(warning, { icon: "⚠️", duration: 12000 });
      }
    } catch {
      setExecError("执行失败：与面板的连接中断，请重试");
    } finally {
      setExecuting(false);
    }
  };

  const presetButton = (gb: number) => {
    const on = !custom && choice === gb;

    return (
      <button
        key={gb}
        className={`flex-1 rounded-md px-3 py-1.5 text-sm transition-colors ${
          on
            ? "bg-tray font-semibold shadow-sm"
            : "text-foreground hover:text-default-800"
        }`}
        type="button"
        onClick={() => {
          setCustom(false);
          setChoice(gb);
          setExecError("");
        }}
      >
        {gb} GB
      </button>
    );
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
          <h2 className="text-lg font-bold">设置 Swap</h2>
          <p className="truncate text-xs font-semibold text-foreground">
            {node?.name}
          </p>
        </ModalHeader>
        <ModalBody className="space-y-4">
          {swapInfoLoadFailed ? (
            <div className="rounded-xl bg-danger-50 px-3 py-3">
              <p className="text-sm font-semibold text-danger-700">
                节点 Swap 现状加载失败
              </p>
              {infoError && (
                <p className="mt-1 text-xs font-semibold text-danger-700">
                  {infoError}
                </p>
              )}
              <p className="mt-1 text-xs font-semibold text-danger-700">
                无法确认节点现状时不提供执行，避免在未知状态下改动节点的 swap。
              </p>
              <Button
                className="mt-2"
                isLoading={loading}
                size="sm"
                variant="flat"
                onPress={() => void loadInfo()}
              >
                重试
              </Button>
            </div>
          ) : loading && !info ? (
            <p className="py-4 text-center text-sm text-foreground">
              正在读取节点 Swap 现状…
            </p>
          ) : info ? (
            <>
              <div className="grid grid-cols-2 gap-3 text-sm">
                <div className="rounded-xl bg-default-500/10 px-3 py-2">
                  <p className="text-xs font-semibold text-foreground">
                    当前 Swap
                  </p>
                  <p className="mt-0.5 font-semibold">
                    {formatMB(info.totalMB)}
                    <span className="ml-1 text-xs font-semibold text-foreground">
                      已用 {formatMB(info.usedMB)}
                    </span>
                  </p>
                </div>
                <div className="rounded-xl bg-default-500/10 px-3 py-2">
                  <p className="text-xs font-semibold text-foreground">
                    内存总量
                  </p>
                  <p className="mt-0.5 font-semibold">
                    {formatMB(info.memTotalMB)}
                  </p>
                </div>
                <div className="rounded-xl bg-default-500/10 px-3 py-2">
                  <p className="text-xs font-semibold text-foreground">
                    磁盘可用
                  </p>
                  <p className="mt-0.5 font-semibold">
                    {formatMB(info.diskFreeMB)}
                  </p>
                </div>
                <div className="rounded-xl bg-default-500/10 px-3 py-2">
                  <p className="text-xs font-semibold text-foreground">
                    现有条目
                  </p>
                  {info.entries.length === 0 ? (
                    <p className="mt-0.5 font-semibold">无</p>
                  ) : (
                    info.entries.map((e) => (
                      <p
                        key={e.name}
                        className="mt-0.5 truncate font-mono text-xs font-semibold"
                        title={e.name}
                      >
                        {e.name} · {formatMB(e.sizeMB)}
                      </p>
                    ))
                  )}
                </div>
              </div>

              {info.managedFilePresent ? (
                <p className="text-xs font-semibold text-foreground">
                  已有面板管理的 /swapfile，执行将调整为所选大小（总量随之变化），并同步更新
                  fstab。
                </p>
              ) : info.totalMB > 0 ? (
                <p className="text-xs font-semibold text-foreground">
                  现有 swap 非面板管理，面板不会动它；执行将另建 /swapfile，总量叠加。
                </p>
              ) : (
                <p className="text-xs font-semibold text-foreground">
                  当前没有 swap，执行将新建 /swapfile 并写入 fstab（重启不丢）。
                </p>
              )}

              <div className="space-y-2">
                <p className="text-sm font-semibold">选择大小</p>
                <div className="flex gap-1 rounded-lg bg-default-500/10 p-1">
                  {PRESET_GB.map(presetButton)}
                  <button
                    className={`flex-1 rounded-md px-3 py-1.5 text-sm transition-colors ${
                      custom
                        ? "bg-tray font-semibold shadow-sm"
                        : "text-foreground hover:text-default-800"
                    }`}
                    type="button"
                    onClick={() => {
                      setCustom(true);
                      setExecError("");
                    }}
                  >
                    自定义
                  </button>
                </div>
                {custom && (
                  <Input
                    description={`整数，1–${MAX_CUSTOM_GB} GB`}
                    errorMessage={
                      !customValid
                        ? `请输入 1–${MAX_CUSTOM_GB} 的整数 GB`
                        : undefined
                    }
                    isInvalid={!customValid}
                    label="自定义大小（GB）"
                    max={MAX_CUSTOM_GB}
                    min={1}
                    type="number"
                    value={customText}
                    variant="bordered"
                    onChange={(e) => {
                      setCustomText(e.target.value);
                      setExecError("");
                    }}
                  />
                )}
                {shortMB > 0 && selectedGB !== null && (
                  <p className="text-xs font-semibold text-danger-700">
                    磁盘空间不足：设置 {selectedGB} GB 还需{" "}
                    {formatMB(requiredMB)}，当前可用{" "}
                    {formatMB(info.diskFreeMB)}，还差 {formatMB(shortMB)}
                    。请先清理节点磁盘或改选更小的大小。
                  </p>
                )}
              </div>

              {execError && (
                <div className="rounded-xl bg-danger-50 px-3 py-2">
                  <p className="text-xs font-semibold text-danger-700">
                    {execError}
                  </p>
                </div>
              )}

              {result && (
                <div className="rounded-xl bg-success-50 px-3 py-2">
                  <p className="text-xs font-semibold text-success-700">
                    执行完成：Swap {formatMB(result.before.totalMB)}（已用{" "}
                    {formatMB(result.before.usedMB)}）→{" "}
                    {formatMB(result.after.totalMB)}（已用{" "}
                    {formatMB(result.after.usedMB)}）
                  </p>
                </div>
              )}
            </>
          ) : null}
        </ModalBody>
        <ModalFooter>
          <Button variant="light" onPress={onClose}>
            {result ? "完成" : "取消"}
          </Button>
          {!result && (
            <Button
              color="primary"
              isDisabled={confirmDisabled}
              isLoading={executing}
              onPress={() => void execute()}
            >
              确认执行
            </Button>
          )}
        </ModalFooter>
      </ModalContent>
    </Modal>
  );
}
