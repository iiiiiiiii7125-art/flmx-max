import { Button } from "@/shadcn-bridge/heroui/button";

// 备份导入/恢复回执的披露面（R2-C5-01＝R2-F6-01）：后端回执在
// importWarnings 里逐项点名被忽略/跳过的数据（含已移除的
// tunnelGroups/userGroups/permissions/nodeGroups 段与订阅 groupIds），
// 并带删除屏蔽恢复数、SSH 配置恢复节点清单与转发重推失败节点清单。
// 此前 UI 只报警告条数、Gist 恢复整份回执零消费，点名内容在面板里
// 不可达。本组件把回执关键面全文呈现，本地导入与 Gist 恢复共用。

const asNumber = (v: unknown): number =>
  typeof v === "number" && Number.isFinite(v) ? v : 0;

const asStringList = (v: unknown): string[] =>
  Array.isArray(v)
    ? v.filter((x): x is string => typeof x === "string" && x !== "")
    : [];

const asNumberList = (v: unknown): number[] =>
  Array.isArray(v)
    ? v.filter(
        (x): x is number => typeof x === "number" && Number.isFinite(x),
      )
    : [];

export function BackupImportReceiptPanel({
  data,
  title,
  onClose,
}: {
  data: Record<string, unknown>;
  title: string;
  onClose?: () => void;
}) {
  const importedTotal = Object.entries(data).reduce(
    (sum, [k, v]) =>
      k.endsWith("Imported") && typeof v === "number" ? sum + v : sum,
    0,
  );
  const warnings = asStringList(data.importWarnings);
  const skippedRows =
    data.skippedRows && typeof data.skippedRows === "object"
      ? (data.skippedRows as Record<string, unknown>)
      : undefined;
  const skippedEntries = skippedRows
    ? Object.entries(skippedRows)
        .map(([k, v]) => [k, asNumber(v)] as const)
        .filter(([, v]) => v > 0)
    : [];
  const userSubscriptionsSkipped = asNumber(data.userSubscriptionsSkipped);
  const skippedTotal =
    userSubscriptionsSkipped +
    skippedEntries.reduce((sum, [, v]) => sum + v, 0);
  const shieldsImported = asNumber(data.inboundShieldsImported);
  const sshConfigsImported = asNumber(data.nodeSSHConfigsImported);
  const peerSharesImported = asNumber(data.peerSharesImported);
  const restoredSSHNodeIds = asNumberList(data.restoredSSHNodeIds);
  const repushFailedNodeIds = asNumberList(data.repushFailedNodeIds);

  const notes: string[] = [];

  if (shieldsImported > 0) {
    notes.push(
      `已随备份恢复删除屏蔽登记 ${shieldsImported} 条：手工删除过的导入节点不会被后续同步复活`,
    );
  }
  if (sshConfigsImported > 0) {
    notes.push(`已恢复 ${sshConfigsImported} 个节点的 SSH 配置`);
  }
  if (peerSharesImported > 0) {
    notes.push(`已恢复面板共享定义 ${peerSharesImported} 条`);
  }

  return (
    <div className="space-y-3 rounded-lg border border-divider glass-inset px-4 py-3">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {onClose && (
          <Button size="sm" variant="light" onPress={onClose}>
            收起
          </Button>
        )}
      </div>
      <p className="text-xs font-semibold text-foreground">
        共导入 {importedTotal} 条记录
        {skippedTotal > 0 ? `，跳过 ${skippedTotal} 条` : ""}
        {warnings.length > 0 ? `，${warnings.length} 条警告（全文如下）` : ""}
      </p>

      {skippedEntries.length > 0 || userSubscriptionsSkipped > 0 ? (
        <div className="text-xs font-semibold text-foreground">
          <p>跳过明细：</p>
          <ul className="mt-1 list-disc space-y-1 pl-4">
            {skippedEntries.map(([segment, count]) => (
              <li key={segment}>
                {segment} 段 {count} 行
              </li>
            ))}
            {userSubscriptionsSkipped > 0 && (
              <li>订阅分配 {userSubscriptionsSkipped} 行（目标订阅不存在）</li>
            )}
          </ul>
        </div>
      ) : null}

      {notes.length > 0 && (
        <ul className="list-disc space-y-1 pl-4 text-xs font-semibold text-foreground">
          {notes.map((note) => (
            <li key={note}>{note}</li>
          ))}
        </ul>
      )}

      {restoredSSHNodeIds.length > 0 && (
        <p className="text-xs font-semibold text-foreground">
          已恢复 SSH 配置的节点（其数据面不随备份恢复，将在启动对账或下一次下发时重新校验）：节点 ID{" "}
          {restoredSSHNodeIds.join("、")}
        </p>
      )}

      {repushFailedNodeIds.length > 0 && (
        <div className="rounded-lg border border-danger-300 bg-danger-500/10 px-3 py-2">
          <p className="text-xs font-semibold text-danger-800">
            转发重推失败的节点：ID {repushFailedNodeIds.join("、")}
            （导入本身已成功；这些节点重连时会自动全量重布收敛，请留意其转发是否恢复）
          </p>
        </div>
      )}

      {warnings.length > 0 && (
        <details className="rounded-lg border border-warning-300 bg-warning-500/10 px-3 py-2" open>
          <summary className="cursor-pointer text-xs font-semibold text-warning-800">
            导入警告（{warnings.length} 条）
          </summary>
          <ul className="mt-2 max-h-48 list-disc space-y-1 overflow-auto pl-4 text-xs font-semibold text-warning-800">
            {warnings.map((warning, i) => (
              <li key={`${i}-${warning.slice(0, 24)}`}>{warning}</li>
            ))}
          </ul>
        </details>
      )}
    </div>
  );
}
