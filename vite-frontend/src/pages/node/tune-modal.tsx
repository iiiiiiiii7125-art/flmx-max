import { useEffect, useRef, useState } from "react";
import toast from "react-hot-toast";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Chip } from "@/shadcn-bridge/heroui/chip";
import { Input } from "@/shadcn-bridge/heroui/input";
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
  probeNodeTune,
  type NodeTuneLine,
  type NodeTuneStatus,
} from "@/api";

const mb = (bytes: number) => `${Math.round((bytes / 1048576) * 10) / 10} MB`;

/** The typed line, or null while it is not a valid one (upstream parseLine rule). */
function parseLine(bw: string, rtt: string): NodeTuneLine | null {
  const b = Number(bw);
  const r = Number(rtt);

  if (!Number.isInteger(b) || !Number.isInteger(r)) return null;
  if (b < 1 || b > 100000 || r < 1 || r > 2000) return null;

  return { bandwidthMbps: b, rttMs: r };
}

/** A probe outcome kept for display / explicit adoption; never applied. */
interface ProbeSuggestion {
  bandwidthMbps?: number;
  rttMs?: number;
  panelRttMs?: number;
  /** True when the probe failed and the values are the T-5 defaults. */
  fromFallback: boolean;
  speedRequested: boolean;
  /** Agent-side leg warnings (e.g. why the median was not measured). */
  warnings?: string[];
}

/** Clamp a measured value into the input's legal range (integers only). */
const clampLine = (value: number, min: number, max: number) =>
  Math.min(max, Math.max(min, Math.round(value)));

/** T-5 defaults, used only as the probe-failure fallback suggestion. */
const FALLBACK_LINE = { bandwidthMbps: 1000, rttMs: 150 };

/**
 * 节点调优: shows the node's current network parameters next to the
 * recommended ones; the checked groups are applied by the agent (sysctl,
 * kept across reboots), unchecked ones go back to their original values.
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
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState<"" | "apply" | "restore">("");
  const [detail, setDetail] = useState<string | null>(null);
  // Bandwidth (Mbps) and RTT (ms) as typed; the agent sizes buffers for them.
  const [bw, setBw] = useState("");
  const [rtt, setRtt] = useState("");
  // Per-field: true once the user has typed in that field. A later status
  // refill or probe suggestion must not overwrite a field they typed in.
  const bwEditedRef = useRef(false);
  const rttEditedRef = useRef(false);
  // One-click probe state. The probe only produces suggestion values; it
  // never calls apply and never submits anything by itself.
  const [probing, setProbing] = useState(false);
  const [withSpeed, setWithSpeed] = useState(false);
  const [probeError, setProbeError] = useState("");
  const [probeSuggestion, setProbeSuggestion] =
    useState<ProbeSuggestion | null>(null);
  // Monotonic token: bumped on node switch and on each probe click, so a
  // late probe response for a previous node (or an older probe) is dropped.
  const probeRunRef = useRef(0);
  const line = parseLine(bw, rtt);
  const lineSupported = Boolean(status?.params);
  const lineBw = line ? line.bandwidthMbps : undefined;
  const lineRtt = line ? line.rttMs : undefined;
  const statusBw = status?.params?.bandwidthMbps;
  const statusRtt = status?.params?.rttMs;

  useEffect(() => {
    if (!node) return;
    // 归属守卫：切节点后旧节点的迟到响应不得落 state，
    // 否则 run() 会把 A 的调优勾选提交给 B
    let alive = true;

    setStatus(null);
    setError("");
    setDetail(null);
    setChosen(new Set());
    setBw("");
    setRtt("");
    bwEditedRef.current = false;
    rttEditedRef.current = false;
    // Invalidate any in-flight probe for the previous node.
    probeRunRef.current += 1;
    setProbing(false);
    setProbeError("");
    setProbeSuggestion(null);
    nodeTune(node.id, "status")
      .then((res) => {
        if (!alive) return;
        if (res.code !== 0 || !res.data) {
          setError(res.msg || "读取节点状态失败");

          return;
        }
        setStatus(res.data);
        // Refill only on first load / node switch, per field, and only
        // if the user has not typed in that field (the inputs are not
        // rendered before status arrives, so this cannot clobber an
        // in-progress edit either).
        if (res.data.params) {
          if (!bwEditedRef.current)
            setBw(String(res.data.params.bandwidthMbps));
          if (!rttEditedRef.current) setRtt(String(res.data.params.rttMs));
        }
        // Everything checked by default, as recommended.
        setChosen(new Set(res.data.groups.map((g) => g.id)));
      })
      .catch(() => {
        if (alive) setError("读取节点状态失败");
      });

    return () => {
      alive = false;
    };
  }, [node]);

  // A changed line changes the targets: ask the agent again, after typing
  // settles. Same alive guard as above: a late response after switching
  // nodes must not land on the new node's state.
  useEffect(() => {
    if (!node || !line || !status?.params) return;
    const p = status.params;

    if (p.bandwidthMbps === line.bandwidthMbps && p.rttMs === line.rttMs)
      return;
    let alive = true;
    const timer = window.setTimeout(() => {
      void nodeTune(node.id, "status", [], line)
        .then((res) => {
          if (!alive) return;
          if (res.code === 0 && res.data) setStatus(res.data);
        })
        .catch(() => {
          // Keep the previous sizes on a failed recalculation.
        });
    }, 500);

    return () => {
      alive = false;
      window.clearTimeout(timer);
    };
  }, [node, lineBw, lineRtt, statusBw, statusRtt]);

  const run = async (action: "apply" | "restore") => {
    if (!node) return;
    setBusy(action);
    try {
      // Apply carries the line only when the agent supports it; restore
      // never carries it (the panel strips it for restore in any case).
      const res = await nodeTune(
        node.id,
        action,
        [...chosen],
        action === "apply" && lineSupported && line ? line : undefined,
      );

      if (res.code !== 0 || !res.data) {
        toast.error(res.msg || "调优失败");

        return;
      }
      setStatus(res.data);
      (res.data.warnings || []).forEach((w) => toast(w, { icon: "⚠️" }));
      toast.success(action === "apply" ? "已应用，立即生效" : "已恢复默认");
      if (action === "restore") {
        setChosen(new Set());
      }
    } catch {
      toast.error("调优失败");
    } finally {
      setBusy("");
    }
  };

  /**
   * One-click probe: fetches a suggestion only. It never calls apply and
   * never submits the line. Suggestion refill rule (per field): a field
   * the user has typed in keeps its value; a field they have not typed
   * in takes the measured value. On probe failure the suggestion falls
   * back to the T-5 defaults, but a failure never overwrites a non-empty
   * field — the user adopts the fallback explicitly if wanted. The
   * fallback is still shown as an explicit result row (marked 回落值,
   * with the agent's leg warnings as the reason), even when it equals
   * the current inputs: a silent fallback is no result at all.
   */
  const runProbe = async () => {
    if (!node || probing) return;
    const runId = ++probeRunRef.current;
    const nodeId = node.id;
    const speedRequested = withSpeed;

    setProbing(true);
    setProbeError("");
    setProbeSuggestion(null);
    try {
      const res = await probeNodeTune(nodeId, speedRequested);

      // Alive guard for the probe: a late response after switching
      // nodes (or after a newer probe) must not land on the wrong node.
      if (runId !== probeRunRef.current) return;
      const medianMs = res.data?.rttMedianMs;
      const warnings = res.data?.warnings ?? [];

      if (res.code !== 0 || !res.data || medianMs == null) {
        setProbeError(res.msg || "测算失败或探测不可用");
        setProbeSuggestion({
          ...FALLBACK_LINE,
          fromFallback: true,
          speedRequested,
          warnings,
        });
        if (bw === "") setBw(String(FALLBACK_LINE.bandwidthMbps));
        if (rtt === "") setRtt(String(FALLBACK_LINE.rttMs));

        return;
      }
      const d = res.data;
      const suggestion: ProbeSuggestion = {
        rttMs: clampLine(medianMs, 1, 2000),
        panelRttMs:
          d.panelRttMs == null ? undefined : clampLine(d.panelRttMs, 1, 2000),
        bandwidthMbps:
          d.bandwidthMbps == null
            ? undefined
            : clampLine(d.bandwidthMbps, 1, 100000),
        fromFallback: false,
        speedRequested,
        warnings,
      };

      setProbeSuggestion(suggestion);
      if (!rttEditedRef.current && suggestion.rttMs != null)
        setRtt(String(suggestion.rttMs));
      if (!bwEditedRef.current && suggestion.bandwidthMbps != null)
        setBw(String(suggestion.bandwidthMbps));
    } catch {
      if (runId !== probeRunRef.current) return;
      setProbeError("测算失败或探测不可用");
      setProbeSuggestion({
        ...FALLBACK_LINE,
        fromFallback: true,
        speedRequested,
      });
      if (bw === "") setBw(String(FALLBACK_LINE.bandwidthMbps));
      if (rtt === "") setRtt(String(FALLBACK_LINE.rttMs));
    } finally {
      if (runId === probeRunRef.current) setProbing(false);
    }
  };

  /** Explicit adoption of a shown suggestion (e.g. a field kept by hand). */
  const adoptProbe = () => {
    if (!probeSuggestion) return;
    if (probeSuggestion.bandwidthMbps != null)
      setBw(String(probeSuggestion.bandwidthMbps));
    if (probeSuggestion.rttMs != null) setRtt(String(probeSuggestion.rttMs));
  };

  // A suggestion value differs from the current input and therefore can
  // still be adopted explicitly (auto-refill skipped it because the user
  // typed in that field, or because the probe failed).
  const probeAdoptable =
    probeSuggestion != null &&
    ((probeSuggestion.bandwidthMbps != null &&
      String(probeSuggestion.bandwidthMbps) !== bw) ||
      (probeSuggestion.rttMs != null && String(probeSuggestion.rttMs) !== rtt));

  const toggle = (id: string, on: boolean) =>
    setChosen((prev) => {
      const next = new Set(prev);

      if (on) next.add(id);
      else next.delete(id);

      return next;
    });

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
              ) : (
                <div className="space-y-2">
                  <p className="text-xs text-default-500">
                    勾选的项目写入系统配置并立即生效，重启后保持；取消勾选的会恢复成调优前的值。
                  </p>
                  {lineSupported ? (
                    <div className="space-y-1.5 rounded-xl border border-divider px-3 py-2.5">
                      <div className="grid grid-cols-2 gap-2">
                        <Input
                          endContent={
                            <span className="text-xs text-default-400">
                              Mbps
                            </span>
                          }
                          inputMode="numeric"
                          label="带宽"
                          size="sm"
                          value={bw}
                          variant="bordered"
                          onChange={(e) => {
                            bwEditedRef.current = true;
                            setBw(e.target.value.replace(/\D/g, ""));
                          }}
                        />
                        <Input
                          endContent={
                            <span className="text-xs text-default-400">ms</span>
                          }
                          inputMode="numeric"
                          label="延迟"
                          size="sm"
                          value={rtt}
                          variant="bordered"
                          onChange={(e) => {
                            rttEditedRef.current = true;
                            setRtt(e.target.value.replace(/\D/g, ""));
                          }}
                        />
                      </div>
                      {!line ? (
                        <p className="text-xs text-danger">
                          带宽填 1–100000 Mbps，延迟填 1–2000 ms
                        </p>
                      ) : (
                        status.sizes && (
                          <p className="text-xs text-default-500">
                            按节点的套餐带宽和到主要用户的延迟（比如跨境约
                            150ms）计算缓冲区。内存 {status.sizes.ramMb} MB ·
                            缓冲区上限 {mb(status.sizes.bufMax)}（
                            {status.sizes.bufReason}）
                          </p>
                        )
                      )}
                      {/* 一键测算：与下方「应用」分离，只取建议值，绝不提交。 */}
                      <div className="flex flex-wrap items-center gap-2 pt-1">
                        <Checkbox
                          isDisabled={probing}
                          isSelected={withSpeed}
                          size="sm"
                          onValueChange={setWithSpeed}
                        >
                          <span className="text-xs">
                            同时测带宽（短时测速）
                          </span>
                        </Checkbox>
                        <Button
                          className="ml-auto"
                          isDisabled={probing || busy !== ""}
                          isLoading={probing}
                          size="sm"
                          variant="bordered"
                          onPress={() => void runProbe()}
                        >
                          一键测算
                        </Button>
                      </div>
                      {probing && (
                        <p className="text-xs text-default-500">
                          测算中…
                          {withSpeed
                            ? "含短时测速，稍候。"
                            : "默认只测延迟，未测速。"}
                        </p>
                      )}
                      {probeError && (
                        <p className="text-xs text-danger">
                          {probeError}，已回落默认值 1000 Mbps / 150
                          ms（仅为建议，未自动应用；点「应用」才下发）。
                        </p>
                      )}
                      {probeSuggestion && (
                        <p className="text-xs text-default-500">
                          {probeSuggestion.fromFallback ? (
                            <>
                              测算结果（回落值，未采到实测中位数）：延迟{" "}
                              {probeSuggestion.rttMs} ms · 带宽{" "}
                              {probeSuggestion.bandwidthMbps} Mbps（默认值）
                              {probeSuggestion.warnings?.[0] &&
                                `；${probeSuggestion.warnings[0]}`}
                              。已按“未手动改过才回填”填入输入框，点「应用」才下发。
                            </>
                          ) : (
                            <>
                              测算结果：延迟中位数 {probeSuggestion.rttMs} ms
                              {probeSuggestion.panelRttMs != null &&
                                `（到面板参考 ${probeSuggestion.panelRttMs} ms）`}
                              {probeSuggestion.bandwidthMbps != null
                                ? ` · 带宽 ${probeSuggestion.bandwidthMbps} Mbps`
                                : probeSuggestion.speedRequested
                                  ? " · 测速未返回带宽，带宽保持原值"
                                  : " · 未测速，带宽保持原值"}
                              。已按“未手动改过才回填”填入输入框，点「应用」才下发。
                            </>
                          )}
                        </p>
                      )}
                      {probeAdoptable && (
                        <div className="flex items-center gap-2">
                          <p className="text-xs text-default-500">
                            建议值：
                            {probeSuggestion?.bandwidthMbps != null &&
                              `带宽 ${probeSuggestion.bandwidthMbps} Mbps`}
                            {probeSuggestion?.bandwidthMbps != null &&
                              probeSuggestion?.rttMs != null &&
                              " / "}
                            {probeSuggestion?.rttMs != null &&
                              `延迟 ${probeSuggestion.rttMs} ms`}
                            （有字段是你手动改过的，未自动覆盖）
                          </p>
                          <button
                            className="ml-auto shrink-0 text-xs text-primary"
                            type="button"
                            onClick={adoptProbe}
                          >
                            填入建议值
                          </button>
                        </div>
                      )}
                    </div>
                  ) : (
                    <p className="text-xs text-warning-600">
                      这台节点的 agent
                      较旧，缓冲区用固定值；升级后可以按带宽和延迟计算
                    </p>
                  )}
                  {status.groups.map((g) => {
                    const supported = g.settings.some((s) => s.supported);

                    return (
                      <div
                        key={g.id}
                        className="rounded-xl border border-divider px-3 py-2.5"
                      >
                        <div className="flex items-start gap-2">
                          <Checkbox
                            isDisabled={!supported}
                            isSelected={chosen.has(g.id) && supported}
                            size="sm"
                            onValueChange={(on) => toggle(g.id, on)}
                          />
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
              {status?.hasBackup && (
                <Button
                  className="mr-auto"
                  isDisabled={busy !== ""}
                  isLoading={busy === "restore"}
                  variant="light"
                  onPress={() => run("restore")}
                >
                  恢复默认
                </Button>
              )}
              <Button variant="light" onPress={onClose}>
                关闭
              </Button>
              <Button
                color="primary"
                isDisabled={
                  !status ||
                  chosen.size === 0 ||
                  busy !== "" ||
                  (lineSupported && !line)
                }
                isLoading={busy === "apply"}
                onPress={() => run("apply")}
              >
                应用
              </Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
