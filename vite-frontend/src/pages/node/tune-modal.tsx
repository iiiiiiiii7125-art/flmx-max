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
 * 全程后台任务；关闭弹窗不中断，2 秒轮询看进度。深度优化在 24
 * 小时内自动测速结果的基础上扫描限速器拐点并应用 HTB/fq 整形，
 * 开始前显示预计流量与本次预算，勾选允许后才执行。单流探测未发现
 * 拐点时可填手动整形值跳过扫描直接应用（F-05：拐点可能高于单流
 * 探测上限，手动值走同一条持久化与整形后验证）。
 *
 * 手填带宽/延迟输入框、分组勾选与一键测算已按上游删除（后端
 * apply / probe 分支保留，但本弹窗不再发 apply/probe；一键测算
 * 随手动入口一并失去入口，见批次一报告 §5）。
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
  const [deepAgreed, setDeepAgreed] = useState(false);
  // F-05：手动整形速率（Mbps）。单流探测未发现拐点时可用：填入用多流
  // sweep 实测的拐点附近值，Agent 跳过扫描、直接应用并验证吞吐。
  const [manualRate, setManualRate] = useState("");
  const manualMbps = Number.parseInt(manualRate, 10);
  const manualValid =
    manualRate.trim() !== "" &&
    Number.isInteger(manualMbps) &&
    manualMbps >= 1 &&
    manualMbps <= 100000;
  const aliveRef = useRef(true);
  const prevRunningRef = useRef(false);

  const job = status?.job;
  const jobRunning = job?.state === "running";
  // 能力探测（上游 3.3.43 automatic 字段）：旧 agent 无此字段。
  const automatic = status?.automatic === true;
  const detection = status?.detection;
  // 深度优化前置：已应用且 24 小时内的自动测速结果（agent 侧
  // startTuneJob 同样校验；此处只决定弹窗展示哪一支流程）。
  const deepReady =
    Boolean(detection?.applied) &&
    Boolean(detection?.measuredAt) &&
    Date.now() - (detection?.measuredAt ?? 0) <= 24 * 3600 * 1000;
  // 本次预算 = min(1000, ceil(预计 × 1.25 × 10) / 10) GB，与上游同款。
  const deepBudget = detection
    ? Math.min(1000, Math.ceil(detection.estimatedGB * 1.25 * 10) / 10)
    : 0;
  // 残留 tuning-result.json 的 0 值按缺失处理：bb66240 自产结果恒 ≥ 0.1 GB，
  // 0 只来自批次一 agent 的残留文件（PORT-REVIEW-L4-001）。0 值下不展示
  // “预计 0 GB / 预算 0 GB”，按钮禁用、文案改报无法估算。

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
    setDeepAgreed(false);
    setManualRate("");
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

  // 任务刚结束时再拉一次状态：最后一次轮询可能停在 running 态，
  // 而完成的测速结果（深度优化的前置）要靠这次刷新才出现。
  // restoreAuto 的完成 toast 只在此派发一次：成功报“已恢复”，失败报失败；
  // auto/deep 的既有行为不动（只有“已开始” toast，无完成 toast）。
  // job.mode === "restore" 即 restoreAuto 任务（后端 TuneStart mode "restore"，
  // 旧 restore 动作走 TuneApply 不建任务，故不会误判）。
  useEffect(() => {
    if (prevRunningRef.current && !jobRunning && node) {
      if (job?.mode === "restore") {
        if (job.state === "succeeded") toast.success("已恢复调优前的配置");
        else if (job.state === "failed") toast.error("恢复原配置失败");
      }
      void refresh(node.id).catch(() => {
        // 刷新失败不报错，下次打开弹窗再读。
      });
    }
    prevRunningRef.current = jobRunning;
  }, [node, jobRunning]);

  const run = async (
    action: NodeTuneAction,
    maxTrafficGB?: number,
    manualShapeMbps?: number,
  ) => {
    if (!node) return;
    setBusy(action);
    try {
      const res = await nodeTune(
        node.id,
        action,
        [],
        undefined,
        maxTrafficGB,
        manualShapeMbps,
      );
      if (!aliveRef.current) return;
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "调优失败");
        return;
      }
      setStatus(res.data);
      (res.data.warnings || []).forEach((w) => toast(w, { icon: "⚠️" }));
      if (action === "auto") toast.success("自动调优已开始，后台执行中");
      if (action === "deep") {
        toast.success("深度优化已开始，后台执行中");
        setDeepAgreed(false);
      }
      if (action === "cancel") toast.success("已停止任务");
      // restoreAuto 是后台任务：code 0 只表示已启动，完成与否看任务终态，
      // 成功 toast 改在任务结束分支（上）派发，此处只报“已开始”
      // （PORT-REVIEW-L4-002；与 auto/deep 的“已开始”口径一致）。
      if (action === "restoreAuto")
        toast.success("恢复任务已开始，后台执行中");
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
                    提供的公共对端发送流量，千兆线路基础测速约消耗 4–7
                    GB（含短测排序）；公共对端忙或不可达时会报错，不会用假定的千兆带宽代替实测。
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

                  {/* 深度优化 */}
                  <div className="space-y-1.5 rounded-xl border border-divider px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">深度优化</span>
                      {deepReady ? (
                        <Button
                          className="ml-auto"
                          color="primary"
                          isDisabled={
                            jobRunning ||
                            busy !== "" ||
                            !deepAgreed ||
                            !detection ||
                            detection.estimatedGB <= 0 ||
                            detection.estimatedGB > 1000
                          }
                          isLoading={busy === "deep"}
                          size="sm"
                          variant="bordered"
                          onPress={() => void run("deep", deepBudget)}
                        >
                          {jobRunning ? "调优执行中…" : "深度优化"}
                        </Button>
                      ) : (
                        <Button
                          className="ml-auto"
                          color="primary"
                          isDisabled={jobRunning || busy !== ""}
                          isLoading={busy === "auto"}
                          size="sm"
                          variant="bordered"
                          onPress={() => void run("auto")}
                        >
                          自动调优并继续
                        </Button>
                      )}
                    </div>
                    {deepReady && detection ? (
                      <div className="space-y-1.5">
                        {detection.estimatedGB <= 0 ? (
                          <p className="text-xs text-warning-600">
                            无法估算流量，请先跑自动调优。
                          </p>
                        ) : (
                          <p className="text-xs text-default-500">
                            按当前测速结果（{detection.bandwidthMbps}{" "}
                            Mbps），预计流量约 {detection.estimatedGB}{" "}
                            GB，本次预算 {deepBudget}{" "}
                            GB。扫描限速器拐点后应用 HTB/fq
                            整形并验证吞吐；扫描可能需要数分钟，单个任务最长
                            25 分钟，达到预算即停并恢复扫描前的队列。
                          </p>
                        )}
                        {detection.estimatedGB <= 0 ? (
                          <p className="text-xs text-default-500">
                            测速结果中没有有效的流量估算（可能是旧版 Agent
                            留下的结果），请先跑一次自动调优。
                          </p>
                        ) : detection.estimatedGB > 1000 ? (
                          <p className="text-xs text-warning-600">
                            预计流量超过 1000 GB 上限，不会启动扫描。
                          </p>
                        ) : (
                          <>
                            <label className="flex cursor-pointer items-start gap-2 text-xs text-default-600">
                              <input
                                checked={deepAgreed}
                                className="mt-0.5"
                                type="checkbox"
                                onChange={(e) =>
                                  setDeepAgreed(e.target.checked)
                                }
                              />
                              <span>
                                允许本次测速使用上述流量（预计约{" "}
                                {detection.estimatedGB} GB，本次预算{" "}
                                {deepBudget} GB）
                              </span>
                            </label>
                            <div className="space-y-1 rounded-lg bg-default-100 p-2">
                              <p className="text-xs text-default-600">
                                单流探测未发现拐点时可用：填入你用多流
                                sweep 实测的拐点附近整形值（如 4710
                                Mbps），Agent
                                跳过扫描、直接应用 HTB/fq
                                整形并验证吞吐，只消耗两次验证测速的流量。整形值本身不验证是否为真实拐点；可随时恢复原配置。
                              </p>
                              <div className="flex flex-wrap items-center gap-2">
                                <input
                                  aria-label="手动整形速率（Mbps）"
                                  className="w-32 rounded-md border border-divider bg-background px-2 py-1 text-xs"
                                  inputMode="numeric"
                                  placeholder="如 4710"
                                  type="number"
                                  value={manualRate}
                                  onChange={(e) =>
                                    setManualRate(e.target.value)
                                  }
                                />
                                <span className="text-xs text-default-500">
                                  Mbps
                                </span>
                                <Button
                                  isDisabled={
                                    jobRunning ||
                                    busy !== "" ||
                                    !deepAgreed ||
                                    !manualValid ||
                                    !detection ||
                                    detection.estimatedGB > 1000
                                  }
                                  isLoading={busy === "deep"}
                                  size="sm"
                                  variant="bordered"
                                  onPress={() =>
                                    void run(
                                      "deep",
                                      deepBudget,
                                      manualMbps,
                                    )
                                  }
                                >
                                  手动应用整形
                                </Button>
                              </div>
                            </div>
                          </>
                        )}
                      </div>
                    ) : (
                      <p className="text-xs text-default-500">
                        深度优化（限速器拐点扫描 + HTB/fq
                        整形）需要 24 小时内的自动调优测速结果。
                        {detection
                          ? "上次测速结果已超过 24 小时，请重新测速。"
                          : "还没有测速结果。"}
                        点「自动调优并继续」，完成后会自动显示预计流量和本次流量预算。
                      </p>
                    )}
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
