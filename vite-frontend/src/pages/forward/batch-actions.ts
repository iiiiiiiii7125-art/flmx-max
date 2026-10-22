import type { BatchOperationFailure, BatchOperationResult } from "@/api/types";

import {
  batchChangeTunnel,
  batchDeleteForwards,
  batchPauseForwards,
  batchRedeployForwards,
  batchResumeForwards,
} from "@/api";
import {
  buildBatchFailureMessage,
  extractBatchFailures,
  extractApiErrorMessage,
} from "@/api/error-message";

export interface ForwardBatchActionOutcome {
  toastVariant: "success" | "error";
  toastMessage: string;
  shouldRefresh: boolean;
  resultTitle?: string;
  resultSummary?: string;
  failureDetails?: BatchOperationFailure[];
  /** 后端批量回执的非致命告警（R3X-024）：随 outcome 带出，呈现并入 toastMessage/resultSummary。 */
  warnings?: string[];
  progressPercent?: number;
  progressLabel?: string;
  closeDeleteModal?: boolean;
  closeChangeTunnelModal?: boolean;
  resetTargetTunnel?: boolean;
}

const normalizeBatchResult = (value: unknown): BatchOperationResult => {
  const raw = (value ?? {}) as Partial<BatchOperationResult>;

  return {
    successCount: Number(raw.successCount ?? 0),
    failCount: Number(raw.failCount ?? 0),
    failures: extractBatchFailures(raw),
    // 待清理（节点不可达转 pending-delete）既非成功也非失败，必须单列
    // 保留，否则前端会把 pending 报成「成功删除 0 项」（A9-03）
    pendingCount: Number(raw.pendingCount ?? 0),
    pending: Array.isArray(raw.pending) ? raw.pending : [],
    // R3X-024：批量重下发/批量换隧道的非致命告警（data.warnings）必须透传，
    // 不得在归一化层丢弃（单条路径已按范式消费，批量独漏）。
    warnings: Array.isArray(raw.warnings)
      ? raw.warnings
          .map((item) => (typeof item === "string" ? item.trim() : ""))
          .filter((item) => item)
      : [],
  };
};

const buildBatchToast = (
  result: BatchOperationResult,
  successText: string,
  resultTitle: string,
): Pick<
  ForwardBatchActionOutcome,
  | "toastVariant"
  | "toastMessage"
  | "resultTitle"
  | "resultSummary"
  | "failureDetails"
  | "warnings"
> => {
  // R3X-024：warnings 按 ⚠️ 范式补报条数 + 首条，并入 toast 与结果摘要，
  // 计数口径（success/fail）不变；无 warnings 时文案与改前逐字一致。
  const warnings = result.warnings ?? [];
  const warningText =
    warnings.length > 0
      ? `另有 ${warnings.length} 条告警：${warnings[0]}`
      : "";
  const withWarnings = (text: string) =>
    warningText ? `${text}；⚠️ ${warningText}` : text;

  if (result.failCount === 0) {
    return {
      toastVariant: "success",
      toastMessage: withWarnings(successText),
      resultTitle,
      resultSummary: withWarnings(successText),
      failureDetails: [],
      warnings,
    };
  }

  return {
    toastVariant: "error",
    toastMessage: withWarnings(
      buildBatchFailureMessage(
        result,
        `成功 ${result.successCount} 项，失败 ${result.failCount} 项`,
      ),
    ),
    resultTitle,
    resultSummary: withWarnings(
      `成功 ${result.successCount} 项，失败 ${result.failCount} 项`,
    ),
    failureDetails: result.failures || [],
    warnings,
  };
};

export const executeForwardBatchDelete = async (
  ids: number[],
): Promise<ForwardBatchActionOutcome> => {
  try {
    const response = await batchDeleteForwards(ids);

    if (response.code !== 0) {
      return {
        toastVariant: "error",
        toastMessage: response.msg || "删除失败",
        shouldRefresh: false,
      };
    }

    const summary = normalizeBatchResult(response.data);
    const pendingCount = summary.pendingCount ?? 0;

    if (pendingCount > 0) {
      // 有待清理项：结论里单列 pending 数，明细并入结果弹窗逐条呈现
      // （原因文案为后端下发的「已加入待清理队列，后台将自动重试」）。
      const summaryText =
        summary.failCount > 0
          ? `成功删除 ${summary.successCount} 项，失败 ${summary.failCount} 项，另有 ${pendingCount} 项待节点恢复后清理`
          : `成功删除 ${summary.successCount} 项，另有 ${pendingCount} 项待节点恢复后清理`;

      return {
        toastVariant: summary.failCount > 0 ? "error" : "success",
        toastMessage: summaryText,
        resultTitle: "批量删除结果",
        resultSummary: summaryText,
        failureDetails: [
          ...(summary.failures || []),
          ...(summary.pending || []),
        ],
        warnings: summary.warnings ?? [],
        shouldRefresh: true,
        progressPercent: 100,
        progressLabel: `删除完成：成功 ${summary.successCount} 项，待清理 ${pendingCount} 项`,
        closeDeleteModal: true,
      };
    }

    return {
      ...buildBatchToast(
        summary,
        `成功删除 ${summary.successCount} 项`,
        "批量删除结果",
      ),
      shouldRefresh: true,
      progressPercent: 100,
      progressLabel: `删除完成：成功 ${summary.successCount} 项`,
      closeDeleteModal: true,
    };
  } catch (error) {
    return {
      toastVariant: "error",
      toastMessage: extractApiErrorMessage(error, "删除失败"),
      shouldRefresh: false,
    };
  }
};

export const executeForwardBatchToggleService = async (
  ids: number[],
  enable: boolean,
): Promise<ForwardBatchActionOutcome> => {
  const fallback = enable ? "启用失败" : "停用失败";

  try {
    const response = enable
      ? await batchResumeForwards(ids)
      : await batchPauseForwards(ids);

    if (response.code !== 0) {
      return {
        toastVariant: "error",
        toastMessage: response.msg || fallback,
        shouldRefresh: false,
      };
    }

    const summary = normalizeBatchResult(response.data);

    return {
      ...buildBatchToast(
        summary,
        enable
          ? `成功启用 ${summary.successCount} 项`
          : `成功停用 ${summary.successCount} 项`,
        enable ? "批量启用结果" : "批量停用结果",
      ),
      shouldRefresh: true,
      progressPercent: 100,
      progressLabel: `${enable ? "启用" : "停用"}完成：成功 ${summary.successCount} 项`,
    };
  } catch (error) {
    return {
      toastVariant: "error",
      toastMessage: extractApiErrorMessage(error, fallback),
      shouldRefresh: false,
    };
  }
};

export const executeForwardBatchRedeploy = async (
  ids: number[],
): Promise<ForwardBatchActionOutcome> => {
  try {
    const response = await batchRedeployForwards(ids);

    if (response.code !== 0) {
      return {
        toastVariant: "error",
        toastMessage: response.msg || "下发失败",
        shouldRefresh: false,
      };
    }

    const summary = normalizeBatchResult(response.data);

    return {
      ...buildBatchToast(
        summary,
        `成功重新下发 ${summary.successCount} 项`,
        "批量下发结果",
      ),
      shouldRefresh: true,
      progressPercent: 100,
      progressLabel: `重新下发完成：成功 ${summary.successCount} 项`,
    };
  } catch (error) {
    return {
      toastVariant: "error",
      toastMessage: extractApiErrorMessage(error, "下发失败"),
      shouldRefresh: false,
    };
  }
};

export const executeForwardBatchChangeTunnel = async (
  ids: number[],
  targetTunnelId: number,
): Promise<ForwardBatchActionOutcome> => {
  try {
    const response = await batchChangeTunnel({
      forwardIds: ids,
      targetTunnelId,
    });

    if (response.code !== 0) {
      return {
        toastVariant: "error",
        toastMessage: response.msg || "隧道失败",
        shouldRefresh: false,
      };
    }

    const summary = normalizeBatchResult(response.data);

    return {
      ...buildBatchToast(
        summary,
        `成功换隧道 ${summary.successCount} 项`,
        "批量换隧道结果",
      ),
      shouldRefresh: true,
      progressPercent: 100,
      progressLabel: `批量换隧道完成：成功 ${summary.successCount} 项`,
      closeChangeTunnelModal: true,
      resetTargetTunnel: true,
    };
  } catch (error) {
    return {
      toastVariant: "error",
      toastMessage: extractApiErrorMessage(error, "隧道失败"),
      shouldRefresh: false,
    };
  }
};
