import { useEffect, useRef, useState } from "react";
import toast from "react-hot-toast";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Spinner } from "@/shadcn-bridge/heroui/spinner";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@/shadcn-bridge/heroui/modal";
import {
  nodeTune,
  type NodeTuneAction,
  type NodeTuneSample,
  type NodeTuneStatus,
} from "@/api";

const mb = (bytes: number) => `${Math.round((bytes / 1048576) * 10) / 10} MB`;

const fmtTime = (ms: number) => {
  if (!ms) return "未知";
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return String(ms);
  }
};

const fmtSample = (s?: NodeTuneSample) =>
  s ? `${Math.round(s.mbps * 10) / 10} Mbps（重传 ${s.retransmits}，丢包 ${Math.round(s.lossPercent * 100) / 100}%）` : "—";

const jobStateLabel: Record<string, string> = {
  running: "执行中",
  succeeded: "已完成",
  failed: "失败",
  cancelled: "已停止",
};

/**
 * 节点调优（上游 3.3.43 自动路线）：无需填写带宽和延迟，Agent 自动
 * 选择公共 iperf3 对端实测出口带宽、计算并应用参数、再验证吞吐，
 * 全程后台任务；关闭弹窗不中断，2 秒轮询看进度。
 *
 * 批次一说明：手填带宽/延迟输入框、分组勾选与一键测算已按上游删除
 * （后端 apply / probe 分支保留，但本弹窗不再发 apply/probe；一键
 * 测算随手动入口一并失去入口）。深度优化按钮在本批禁用，随批次二
 * 提供；测速引擎（tcpfit + bridge）亦随批次二进，批次一的自动任务
 * 会在测速阶段以明确错误失败，不静默回落、不改系统参数。
 */
export function NodeTuneModal({
  node,
  onClose,
}: {
  node: { id: number; name: string } | null;
  onClose: () => void;
}) {
  const [status, setStatus] = useState<NodeTuneStatus | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<"" | NodeTuneAction>("");
  const [detail, setDetail] = useState<string | null>(null);
  const aliveRef = useRef(true);

  const job = status?.job;
  const jobRunning = job?.state === "running";
  // 能力探测（上游 3.3.43 automatic 字段）：旧 agent 无此字段。
  const automatic = status?.automatic === true;
  const detection = status?.detection;

  const refresh = async (id: number) => {
    const res = await nodeTune(id, "status");
    if (!aliveRef.current) return;
    if (res.code !== 0 || !res.data) {
      setError(res.msg || "读取节点状态失败");
      return;
    }
    setStatus(res.data);
  };

  useEffect(() => {
    if (!node) return;
    aliveRef.current = true;
    setStatus(null);
    setError("");
    setDetail(null);
    setBusy("");
    refresh(node.id).catch(() => {
      if (aliveRef.current) setError("读取节点状态失败");
    });
    return () => {
      aliveRef.current = false;
    };
  }, [node]);

  // 任务执行中时 2 秒轮询 TuneStatus 看进度；关闭弹窗不中断任务。
  useEffect(() => {
    if (!node || !jobRunning) return;
    const timer = window.setInterval(() => {
      void refresh(node.id).catch(() => {
        // 轮询失败不断任务，下轮再试。
      });
    }, 2000);
    return () => window.clearTimeout(timer);
  }, [node, jobRunning]);

  const run = async (action: NodeTuneAction) => {
    if (!node) return;
    setBusy(action);
    try {
      const res = await nodeTune(node.id, action);
      if (!aliveRef.current) return;
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "调优失败");
        return;
      }
      setStatus(res.data);
      (res.data.warnings || []).forEach((w) => toast(w, { icon: "⚠️" }));
      if (action === "auto") toast.success("自动调优已开始，后台执行中");
      if (action === "cancel") toast.success("已停止任务");
      if (action === "restoreAuto") toast.success("已恢复调优前的配置");
    } catch {
      if (aliveRef.current) toast.error("调优失败");
    } finally {
      if (aliveRef.current) setBusy("");
    }
  };

  return (
    <Modal
      backdrop="blur"
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={Boolean(node)}
      placement="center"
      scrollBehavior="inside"
      size="2xl"
      onOpenChange={(open) => !open && onClose()}
    >
      <ModalContent>
        {() => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h2 className="text-xl font-bold">节点调优</h2>
              <p className="text-sm font-normal text-default-500">
                {node?.name}
                {status &&
                  ` · 内核 ${status.kernel || "未知"} · 拥塞控制 ${status.congestion || "未知"}`}
              </p>
            </ModalHeader>
            <ModalBody>
              {error ? (
                <p className="py-6 text-center text-sm text-danger">{error}</p>
              ) : !status ? (
                <div className="flex justify-center py-10">
                  <Spinner />
                </div>
              ) : !automatic ? (
                <div className="space-y-2 py-4">
                  <p className="text-sm text-warning-600">
                    这台服务器的 Agent
                    不支持自动调优，请先升级服务器 Agent。
                  </p>
                  <p className="text-xs text-default-500">
                    自动调优与深度优化要求节点 Agent
                    为调优移植版；旧 Agent 只能看到本提示，不会悄悄沿用固定带宽。
                  </p>
                </div>
              ) : (
                <div className="space-y-2">
                  <p className="text-xs text-default-500">
                    无需填写带宽和延迟：Agent
                    自动选择公共 iperf3 对端，实测四并发吞吐，读取服务器内存后计算并应用参数，最后验证单连接和多连接吞吐。任务在
                    Agent 后台执行，关闭窗口不会中断，再次打开可继续查看。
                  </p>
                  <p className="text-xs text-default-500">
                    测速会向 Leaseweb、Clouvider 或 OVH
                    提供的公共对端发送流量，千兆线路基础测速约消耗 4
                    GB；公共对端忙或不可达时会报错，不会用假定的千兆带宽代替实测。
                  </p>

                  {/* 自动调优 / 任务进度 */}
                  <div className="space-y-1.5 rounded-xl border border-divider px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">自动调优</span>
                      {job && (
                        <Chip
                          color={
                            job.state === "succeeded"
                              ? "success"
                              : job.state === "failed"
                                ? "danger"
                                : "primary"
                          }
                          size="sm"
                          variant="flat"
                        >
                          {jobStateLabel[job.state] ?? job.state}
                        </Chip>
                      )}
                      <Button
                        className="ml-auto"
                        color="primary"
                        isDisabled={jobRunning || busy !== ""}
                        isLoading={busy === "auto"}
                        size="sm"
                        onPress={() => void run("auto")}
                      >
                        {jobRunning ? "调优执行中…" : "自动调优"}
                      </Button>
                      {jobRunning && (
                        <Button
                          isDisabled={busy !== ""}
                          isLoading={busy === "cancel"}
                          size="sm"
                          variant="bordered"
                          onPress={() => void run("cancel")}
                        >
                          停止任务
                        </Button>
                      )}
                    </div>
                    {job && (
                      <div className="space-y-1">
                        <div className="h-1.5 overflow-hidden rounded-full bg-default-100">
                          <div
                            className="h-full rounded-full bg-primary transition-all"
                            style={{ width: `${job.percent}%` }}
                          />
                        </div>
                        <p className="text-xs text-default-500">
                          {job.percent}% · {job.message}
                        </p>
                        {job.logs && (
                          <pre className="max-h-32 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-default-100 p-2 font-mono text-[11px] text-default-600">
                            {job.logs}
                          </pre>
                        )}
                      </div>
                    )}
                  </div>

                  {/* 测速结果 */}
                  {detection && (
                    <div className="space-y-1 rounded-xl border border-divider px-3 py-2.5">
                      <p className="text-sm font-medium">测速结果</p>
                      <div className="space-y-0.5 text-xs text-default-600">
                        <p>
                          实测带宽 {detection.bandwidthMbps} Mbps
                          （{detection.bandwidthSource}）· 出口{" "}
                          {detection.interface} · 对端 {detection.peer}:
                          {detection.port} · 测量时间{" "}
                          {fmtTime(detection.measuredAt)}
                        </p>
                        <p>
                          缓冲区按 {detection.bandwidthMbps} Mbps ×{" "}
                          {detection.rttSource}计算
                          {status.sizes &&
                            ` · 内存 ${status.sizes.ramMb} MB · 缓冲区上限 ${mb(status.sizes.bufMax)}（${status.sizes.bufReason}）`}
                        </p>
                        {detection.before && (
                          <p>测速样本：{fmtSample(detection.before)}</p>
                        )}
                        {detection.single && (
                          <p>单连接验证：{fmtSample(detection.single)}</p>
                        )}
                        {detection.parallel && (
                          <p>多连接验证：{fmtSample(detection.parallel)}</p>
                        )}
                        {detection.deepSummary && (
                          <p>深度优化：{detection.deepSummary}</p>
                        )}
                        {(detection.warnings || []).map((w, i) => (
                          <p key={i}>⚠️ {w}</p>
                        ))}
                      </div>
                    </div>
                  )}

                  {/* 深度优化（批次二） */}
                  <div className="space-y-1.5 rounded-xl border border-divider px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">深度优化</span>
                      <Button
                        isDisabled
                        size="sm"
                        variant="bordered"
                        onPress={() => {}}
                      >
                        深度优化
                      </Button>
                    </div>
                    <p className="text-xs text-default-500">
                      深度优化（限速器拐点扫描 + HTB/fq
                      整形）随批次二提供。开始前会显示预计流量与本次预算，勾选允许后才执行。
                      {detection && detection.estimatedGB > 0
                        ? `按当前测速结果，预计流量约 ${detection.estimatedGB} GB。`
                        : "需要 24 小时内的自动调优测速结果作为前置。"}
                    </p>
                  </div>

                  {/* 已应用分组（只读） */}
                  {status.groups.map((g) => {
                    const supported = g.settings.some((s) => s.supported);
                    return (
                      <div
                        key={g.id}
                        className="rounded-xl border border-divider px-3 py-2.5"
                      >
                        <div className="flex items-start gap-2">
                          <div className="min-w-0 flex-1">
                            <div className="flex flex-wrap items-center gap-1.5">
                              <span className="text-sm font-medium">
                                {g.name}
                              </span>
                              {g.applied ? (
                                <Chip color="success" size="sm" variant="flat">
                                  已生效
                                </Chip>
                              ) : !supported ? (
                                <Chip size="sm" variant="flat">
                                  系统不支持
                                </Chip>
                              ) : null}
                              <button
                                className="ml-auto text-xs text-primary"
                                type="button"
                                onClick={() =>
                                  setDetail(detail === g.id ? null : g.id)
                                }
                              >
                                {detail === g.id ? "收起" : "参数"}
                              </button>
                            </div>
                            <p className="text-xs text-default-500">{g.desc}</p>
                            {detail === g.id && (
                              <div className="mt-2 space-y-0.5 font-mono text-[11px] text-default-600">
                                {g.settings.map((s) => (
                                  <div key={s.key} className="break-all">
                                    {s.key}:{" "}
                                    {s.supported ? (
                                      <>
                                        <span
                                          className={s.ok ? "text-success" : ""}
                                        >
                                          {s.current}
                                        </span>
                                        {!s.ok && ` → ${s.target}`}
                                      </>
                                    ) : (
                                      <span className="text-default-400">
                                        不支持
                                      </span>
                                    )}
                                  </div>
                                ))}
                              </div>
                            )}
                          </div>
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </ModalBody>
            <ModalFooter>
              {status?.automatic && (status?.hasBackup || detection?.applied) && (
                <Button
                  className="mr-auto"
                  isDisabled={busy !== "" || jobRunning}
                  isLoading={busy === "restoreAuto"}
                  variant="light"
                  onPress={() => void run("restoreAuto")}
                >
                  恢复原配置
                </Button>
              )}
              <Button variant="light" onPress={onClose}>
                关闭
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
