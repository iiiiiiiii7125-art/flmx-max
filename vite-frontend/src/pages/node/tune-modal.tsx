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
  type NodeTuneFaceId,
  type NodeTuneProbeResult,
  type NodeTuneStatus,
} from "@/api";
import { normalizeNodeTuneStatus } from "@/api/node-tune-status";

/**
 * 节点调优（四面模型，tcp.vpsing.de 脚本谱系）：IPv4 优先解析、
 * BBR+FQ、生产级内核调优、网卡多队列均衡四面相互独立，每面一行
 * （名称 + 实时状态徽标 + 应用按钮），外加一键回退默认（删调优
 * 文件、拥塞算法回 cubic、队列回 pfifo_fast、清 MSS 钳制与 RPS）。
 * 内核面参数由 Agent 按机器总内存定尺寸，不再有带宽/延迟手填、
 * BDP 计算、自动/深度优化任务等旧模型入口。
 *
 * 线路探针与四面并列、互不混杂：参考主机一轮 10 次 TCP 连接探针
 * 序列（丢包率 / 抖动 / RTT 分布都出自这一腿的逐次样本）与 Agent
 * 内 sock_diag 采样（已建立连接的内核 RTT 中位数与分布；快照无
 * 发送计数与样本时序，不产丢包 / 抖动）两条腿；withSpeed/
 * speedtestURL 下载测速腿已退役（其唯一消费者是已弃用的 BDP
 * 尺寸推导）。
 */

interface FaceDef {
  id: NodeTuneFaceId;
  action: NodeTuneAction;
  name: string;
  desc: string;
}

const FACES: FaceDef[] = [
  {
    id: "ipv4",
    action: "apply-ipv4",
    name: "IPv4 优先解析",
    desc: "解决 IPv6 绕路导致的握手卡顿",
  },
  {
    id: "bbr",
    action: "apply-bbr",
    name: "BBR + FQ",
    desc: "降低跨境丢包影响，提升单线程速度（需要内核 4.9+）",
  },
  {
    id: "kernel",
    action: "apply-kernel",
    name: "生产级内核调优",
    // F2-18：本面键集前两项就是拥塞算法 BBR 与 fq 队列——与 BBR 面
    // 共用同一组运行时值，应用本面会同时点亮 BBR 面。此前文案只字
    // 不提，用户会误以为四面完全独立；在此明示耦合。
    desc: "缓冲区按总内存定尺寸、扩大连接队列与文件句柄、MSS 钳制；键集包含拥塞算法 BBR 与 fq 队列，应用后 BBR 面会随之激活（两面共用同一运行时值）",
  },
  {
    id: "nic",
    action: "apply-nic",
    name: "网卡多队列均衡",
    desc: "RPS 把软中断平摊到全部核心，消除单核瓶颈",
  },
];

/** RTT 分布条：sock_diag 逐样本分布的 min / 中位 / p95 / max 四个
 * 实测标记点，按最大值定标画在一条带上（样本即节点当前已建立
 * 连接的内核 RTT 采样；不虚构逐样本直方图）。 */
function rttRangeMarks(
  min: number,
  median: number,
  p95: number,
  max: number,
): Array<{ label: string; pct: number }> {
  const scale = Math.max(max, 1);
  const at = (v: number) => Math.min(100, Math.max(0, (v / scale) * 100));
  return [
    { label: `最小 ${min}`, pct: at(min) },
    { label: `中位 ${median}`, pct: at(median) },
    { label: `p95 ${p95}`, pct: at(p95) },
    { label: `最大 ${max}`, pct: at(max) },
  ];
}

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
  const [probeHost, setProbeHost] = useState("");
  const [probe, setProbe] = useState<NodeTuneProbeResult | null>(null);
  const aliveRef = useRef(true);
  // 启动类动作双发不可收敛（应用/回退/探针都会在节点上落状态），
  // 下发瞬间同步复核（F-L6-01 同款口径）。
  const runInFlightRef = useRef(false);

  const faces = status?.faces;
  const anyFaceOn = Boolean(
    faces && (faces.ipv4 || faces.bbr || faces.kernel || faces.nic),
  );

  const refresh = async (id: number) => {
    const res = await nodeTune(id, "status");
    if (!aliveRef.current) return;
    if (res.code !== 0 || !res.data) {
      setError(res.msg || "读取节点状态失败");
      return;
    }
    // 回包先归一：2.1.0 agent 的扁平形状与规范 faces 形状都认。
    // 归一后仍为空的才是旧 agent 的已退役分组形态，按「不支持
    // 新版调优」呈现，不把旧形态误读成四面全关。
    const data = normalizeNodeTuneStatus(res.data);
    if (!data) {
      setError("节点 agent 版本过旧，不支持新版调优，请先升级节点 Agent");
      return;
    }
    setStatus(data);
  };

  useEffect(() => {
    if (!node) return;
    aliveRef.current = true;
    setStatus(null);
    setError("");
    setBusy("");
    setProbe(null);
    setProbeHost("");
    refresh(node.id).catch(() => {
      if (aliveRef.current) setError("读取节点状态失败");
    });
    return () => {
      aliveRef.current = false;
    };
  }, [node]);

  const run = async (action: NodeTuneAction) => {
    if (!node || runInFlightRef.current) return;
    runInFlightRef.current = true;
    setBusy(action);
    try {
      const res = await nodeTune(node.id, action);
      if (!aliveRef.current) return;
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "调优失败");
        return;
      }
      const data = normalizeNodeTuneStatus(res.data);
      if (!data) {
        toast.error("节点 agent 版本过旧，不支持新版调优，请先升级节点 Agent");
        return;
      }
      setStatus(data);
      (data.warnings || []).forEach((w) => toast(w, { icon: "⚠️" }));
      if (action === "rollback") toast.success("已回退到默认设置");
      else toast.success("已应用");
    } catch {
      if (aliveRef.current) toast.error("调优失败");
    } finally {
      runInFlightRef.current = false;
      if (aliveRef.current) setBusy("");
    }
  };

  const runProbe = async () => {
    if (!node || runInFlightRef.current) return;
    runInFlightRef.current = true;
    setBusy("probe");
    try {
      const res = await nodeTune<NodeTuneProbeResult>(
        node.id,
        "probe",
        probeHost.trim() || undefined,
      );
      if (!aliveRef.current) return;
      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "线路探针失败");
        return;
      }
      setProbe(res.data);
      (res.data.warnings || []).forEach((w) => toast(w, { icon: "⚠️" }));
    } catch {
      if (aliveRef.current) toast.error("线路探针失败");
    } finally {
      runInFlightRef.current = false;
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
              <p className="text-sm font-normal text-foreground">
                {node?.name}
                {status &&
                  ` · 拥塞控制 ${status.congestion || "未知"} · 队列 ${status.qdisc || "未知"}`}
              </p>
            </ModalHeader>
            <ModalBody>
              {error ? (
                <p className="py-6 text-center text-sm text-danger-700">
                  {error}
                </p>
              ) : !status || !faces ? (
                <div className="flex justify-center py-10">
                  <Spinner />
                </div>
              ) : (
                <div className="space-y-2">
                  {/* 四面开关：每面一行 = 名称 + 状态徽标 + 应用按钮 */}
                  {FACES.map((face) => (
                    <div
                      key={face.id}
                      className="rounded-xl border border-divider px-3 py-2.5"
                    >
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="text-sm font-medium">
                          {face.name}
                        </span>
                        {faces[face.id] ? (
                          <Chip color="success" size="sm" variant="flat">
                            已激活
                          </Chip>
                        ) : (
                          <Chip size="sm" variant="flat">
                            未开启
                          </Chip>
                        )}
                        <Button
                          className="ml-auto"
                          color="primary"
                          isDisabled={busy !== ""}
                          isLoading={busy === face.action}
                          size="sm"
                          variant={faces[face.id] ? "bordered" : "solid"}
                          onPress={() => void run(face.action)}
                        >
                          {faces[face.id] ? "重新应用" : "应用"}
                        </Button>
                      </div>
                      <p className="mt-1 text-xs font-semibold text-foreground">
                        {face.desc}
                      </p>
                      {/* F2-18：BBR 已激活且内核面也在位时，点名 BBR
                          可能由内核面键集一并设置，不假装两面独立。 */}
                      {face.id === "bbr" && faces.bbr && faces.kernel && (
                        <p className="mt-1 text-xs font-semibold text-warning-700">
                          注：内核面的键集同样包含 BBR 与 fq，当前 BBR
                          可能由内核面一并设置；两面共用拥塞算法运行时值，不是完全独立的两个开关。
                        </p>
                      )}
                    </div>
                  ))}

                  {/* 一键回退默认 */}
                  <div className="rounded-xl border border-divider px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">回退默认</span>
                      <Button
                        className="ml-auto"
                        color="danger"
                        isDisabled={busy !== "" || !anyFaceOn}
                        isLoading={busy === "rollback"}
                        size="sm"
                        variant="bordered"
                        onPress={() => void run("rollback")}
                      >
                        一键回退到默认设置
                      </Button>
                    </div>
                    <p className="mt-1 text-xs font-semibold text-foreground">
                      清理全部调优文件，拥塞算法恢复 cubic、队列恢复
                      pfifo_fast，并移除 MSS 钳制与网卡队列设置。
                    </p>
                  </div>

                  {/* 线路探针（与四面并列的独立面板） */}
                  <div className="space-y-1.5 rounded-xl border border-divider px-3 py-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="text-sm font-medium">线路探针</span>
                      <Button
                        className="ml-auto"
                        color="primary"
                        isDisabled={busy !== ""}
                        isLoading={busy === "probe"}
                        size="sm"
                        variant="bordered"
                        onPress={() => void runProbe()}
                      >
                        开始探针
                      </Button>
                    </div>
                    <p className="text-xs font-semibold text-foreground">
                      测回程方向的延迟 / 丢包 / 抖动 / RTT
                      分布：参考主机一轮 10 次 TCP
                      连接探针给出丢包率、抖动与分布，节点已建立连接的
                      内核 RTT 采样给出延迟中位数与分布。参考主机留空时
                      自动用面板地址；只读测量，不改任何参数。
                    </p>
                    <div className="flex flex-wrap items-center gap-2">
                      <input
                        aria-label="参考主机（host:port，可留空）"
                        className="w-56 rounded-md border border-divider bg-content1 px-2 py-1 text-xs font-semibold"
                        placeholder="参考主机 host:port（可留空）"
                        value={probeHost}
                        onChange={(e) => setProbeHost(e.target.value)}
                      />
                      <span className="text-xs font-semibold text-foreground">
                        仅公网地址（面板与 Agent 双重校验）
                      </span>
                    </div>
                    {probe && (
                      <div className="space-y-1.5 pt-1">
                        {probe.rttAvailable && probe.rttMedianMs != null ? (
                          <>
                            <div className="flex flex-wrap gap-x-4 gap-y-0.5 text-xs font-semibold text-foreground">
                              <span>
                                内核采样延迟（中位数） {probe.rttMedianMs}{" "}
                                ms
                              </span>
                              {probe.rttMinMs != null &&
                                probe.rttP95Ms != null &&
                                probe.rttMaxMs != null && (
                                  <span>
                                    分布 最小 {probe.rttMinMs} · p95{" "}
                                    {probe.rttP95Ms} · 最大 {probe.rttMaxMs}{" "}
                                    ms
                                  </span>
                                )}
                              <span>样本 {probe.rttSamples} 条连接</span>
                              <span>
                                丢包 / 抖动见参考腿（内核快照无发送计数与
                                样本时序，不可测）
                              </span>
                            </div>
                            {probe.rttMinMs != null &&
                              probe.rttP95Ms != null &&
                              probe.rttMaxMs != null && (
                                <div className="relative h-5">
                                  <div className="absolute inset-x-0 top-1/2 h-1.5 -translate-y-1/2 rounded-full bg-default-100" />
                                  {rttRangeMarks(
                                    probe.rttMinMs,
                                    probe.rttMedianMs,
                                    probe.rttP95Ms,
                                    probe.rttMaxMs,
                                  ).map((m) => (
                                    <span
                                      key={m.label}
                                      className="absolute top-1/2 h-3 w-[3px] -translate-x-1/2 -translate-y-1/2 rounded bg-primary"
                                      style={{ left: `${m.pct}%` }}
                                      title={`${m.label} ms`}
                                    />
                                  ))}
                                </div>
                              )}
                            <p className="text-[11px] font-semibold text-foreground">
                              分布条标记为最小 / 中位 / p95 /
                              最大四个实测点（按最大值定标），均为
                              sock_diag 逐样本计算，不含虚构直方图。
                            </p>
                          </>
                        ) : (
                          <p className="text-xs font-semibold text-warning-700">
                            节点暂无可测 RTT 的已建立 TCP
                            连接，延迟不可用。
                          </p>
                        )}
                        {probe.panelRttSent != null &&
                          probe.panelRttSent > 0 &&
                          (probe.panelRttReceived === 0 ? (
                            <p className="text-xs font-semibold text-warning-700">
                              参考腿：发送 {probe.panelRttSent} · 收到 0 ·
                              丢包 100%（整轮无响应，延迟 / 抖动不可测）
                            </p>
                          ) : (
                            <div className="flex flex-wrap gap-x-4 gap-y-0.5 text-xs font-semibold text-foreground">
                              <span>
                                参考腿延迟（中位数） {probe.panelRttMs} ms
                              </span>
                              {probe.panelRttLossRate != null && (
                                <span>
                                  丢包{" "}
                                  {Math.round(probe.panelRttLossRate * 100)}
                                  %（发送 {probe.panelRttSent} · 收到{" "}
                                  {probe.panelRttReceived}）
                                </span>
                              )}
                              {probe.panelRttJitterMs != null ? (
                                <span>
                                  抖动（相邻差均值）{" "}
                                  {probe.panelRttJitterMs.toFixed(1)} ms
                                </span>
                              ) : (
                                <span>抖动不可测（成功样本不足）</span>
                              )}
                              {probe.panelRttMinMs != null &&
                                probe.panelRttMaxMs != null && (
                                  <span>
                                    分布 最小 {probe.panelRttMinMs} ·{" "}
                                    {probe.panelRttP95Ms != null
                                      ? `p95 ${probe.panelRttP95Ms} · `
                                      : ""}
                                    最大 {probe.panelRttMaxMs} ms
                                  </span>
                                )}
                            </div>
                          ))}
                        {(probe.warnings || []).map((w, i) => (
                          <p
                            key={i}
                            className="text-xs font-semibold text-warning-700"
                          >
                            ⚠️ {w}
                          </p>
                        ))}
                      </div>
                    )}
                  </div>

                  {/* 当前状态行。C3-03：句柄数字必须用 agent 实回的
                      nofile（进程 soft/hard 上限），旧实现写死
                      「1048576」与实测拥塞/队列混排冒充实测；回包
                      无 nofile 时整段省略，不给任何数字。 */}
                  <p className="px-1 pt-1 text-xs font-semibold text-foreground">
                    当前状态：算法 {status.congestion || "未知"} · 队列{" "}
                    {status.qdisc || "未知"}
                    {status.nofile
                      ? ` · agent 句柄 ${status.nofile.soft}/${status.nofile.hard}${status.nofile.limitsFile ? "（limits 文件在位）" : ""}`
                      : ""}
                  </p>
                </div>
              )}
            </ModalBody>
            <ModalFooter>
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
