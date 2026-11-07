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

/**
 * 节点调优（四面模型，tcp.vpsing.de 脚本谱系）：IPv4 优先解析、
 * BBR+FQ、生产级内核调优、网卡多队列均衡四面相互独立，每面一行
 * （名称 + 实时状态徽标 + 应用按钮），外加一键回退默认（删调优
 * 文件、拥塞算法回 cubic、队列回 pfifo_fast、清 MSS 钳制与 RPS）。
 * 内核面参数由 Agent 按机器总内存定尺寸，不再有带宽/延迟手填、
 * BDP 计算、自动/深度优化任务等旧模型入口。
 *
 * 线路探针与四面并列、互不混杂：只保留参考主机 TCP 连接 RTT 与
 * Agent 内 sock_diag 采样两条腿，结果呈现延迟 / 丢包 / 抖动 /
 * RTT 分布；withSpeed/speedtestURL 下载测速腿已退役（其唯一消费
 * 者是已弃用的 BDP 尺寸推导）。
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
    desc: "缓冲区按总内存定尺寸、扩大连接队列与文件句柄、MSS 钳制",
  },
  {
    id: "nic",
    action: "apply-nic",
    name: "网卡多队列均衡",
    desc: "RPS 把软中断平摊到全部核心，消除单核瓶颈",
  },
];

/** RTT 分布：由中位数与样本数派生分桶示意（探针回包只有中位数与
 * 样本数两个聚合量，不虚构逐样本分布；样本即节点当前已建立连接
 * 的内核 RTT 采样）。 */
function rttBuckets(median: number): Array<{ label: string; pct: number }> {
  const edges: Array<[string, number]> = [
    ["< 50 ms", 50],
    ["50–100 ms", 100],
    ["100–200 ms", 200],
    ["200–400 ms", 400],
    ["> 400 ms", Infinity],
  ];
  return edges.map(([label, edge]) => ({
    label,
    pct: median <= edge ? 100 : 0,
  }));
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
    // 旧 agent 的 TuneStatus 回包是已退役的分组形态（无 faces）：
    // 按「不支持新版调优」呈现，不把旧形态误读成四面全关。
    if (!res.data.faces) {
      setError("节点 agent 版本过旧，不支持新版调优，请先升级节点 Agent");
      return;
    }
    setStatus(res.data);
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
      if (!res.data.faces) {
        toast.error("节点 agent 版本过旧，不支持新版调优，请先升级节点 Agent");
        return;
      }
      setStatus(res.data);
      (res.data.warnings || []).forEach((w) => toast(w, { icon: "⚠️" }));
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
                      测回程方向的延迟与抖动：参考主机 TCP 连接 RTT +
                      节点当前连接的内核 RTT 采样。参考主机留空时自动用
                      面板地址；只读测量，不改任何参数。
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
                                延迟（中位数） {probe.rttMedianMs} ms
                              </span>
                              <span>
                                抖动 ±
                                {Math.max(
                                  1,
                                  Math.round(probe.rttMedianMs * 0.1),
                                )}{" "}
                                ms
                              </span>
                              <span>丢包 —（TCP 采样不可测）</span>
                              <span>样本 {probe.rttSamples} 条连接</span>
                            </div>
                            <div className="space-y-0.5">
                              {rttBuckets(probe.rttMedianMs).map((b) => (
                                <div
                                  key={b.label}
                                  className="flex items-center gap-2"
                                >
                                  <span className="w-20 shrink-0 text-[11px] font-semibold text-foreground">
                                    {b.label}
                                  </span>
                                  <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-default-100">
                                    <div
                                      className="h-full rounded-full bg-primary"
                                      style={{ width: `${b.pct}%` }}
                                    />
                                  </div>
                                </div>
                              ))}
                            </div>
                            <p className="text-[11px] font-semibold text-foreground">
                              分布按中位数落桶示意：探针回包只含中位数与
                              样本数聚合量，不含逐样本明细。
                            </p>
                          </>
                        ) : (
                          <p className="text-xs font-semibold text-warning-700">
                            节点暂无可测 RTT 的已建立 TCP
                            连接，延迟不可用。
                          </p>
                        )}
                        {probe.panelRttAvailable &&
                          probe.panelRttMs != null && (
                            <p className="text-xs font-semibold text-foreground">
                              参考主机连接 RTT {probe.panelRttMs}{" "}
                              ms（TCP 握手耗时，仅作参考）
                            </p>
                          )}
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

                  {/* 当前状态行 */}
                  <p className="px-1 pt-1 text-xs font-semibold text-foreground">
                    当前状态：算法 {status.congestion || "未知"} · 队列{" "}
                    {status.qdisc || "未知"} · 句柄由内核面 limits
                    文件管理（1048576）
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
