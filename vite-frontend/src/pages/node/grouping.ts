/**
 * Node-page display grouping (pure presentation, user decision
 * 2026-11-09): each node carries at most one free-text group label
 * ("" = ungrouped). The page renders nodes in labelled sections;
 * section order comes from a manually arranged name list persisted
 * server-side as a panel config value, so it follows the panel (and
 * its backups), not the browser.
 */
export const NODE_GROUP_ORDER_CONFIG_KEY = "node_display_group_order";

export const UNGROUPED_LABEL = "未分组";

/** A node's effective label: trimmed, "" when ungrouped. */
export function nodeGroupLabel(node: { displayGroup?: string | null }): string {
  return (node.displayGroup ?? "").trim();
}

/** Distinct labels currently in use, in first-appearance order. */
export function collectNodeGroupLabels(
  nodes: Array<{ displayGroup?: string | null }>,
): string[] {
  const seen = new Set<string>();
  const out: string[] = [];

  for (const node of nodes) {
    const label = nodeGroupLabel(node);

    if (label && !seen.has(label)) {
      seen.add(label);
      out.push(label);
    }
  }

  return out;
}

/**
 * Parses the persisted order value (a JSON array of label strings).
 * Anything malformed degrades to an empty order — the page then
 * falls back to name order rather than failing to render.
 */
export function parseNodeGroupOrder(raw: string | null | undefined): string[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);

    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    const out: string[] = [];

    for (const item of parsed) {
      if (typeof item !== "string") continue;
      const label = item.trim();

      if (label && !seen.has(label)) {
        seen.add(label);
        out.push(label);
      }
    }

    return out;
  } catch {
    return [];
  }
}

export interface NodeGroupSection<T> {
  /** Group label; "" is the ungrouped section (always last). */
  label: string;
  nodes: T[];
}

/**
 * Orders the in-use labels: labels named in `order` first (in list
 * order; stale entries with no nodes are ignored), remaining labels
 * by name, and the ungrouped bucket always last.
 */
export function orderNodeGroupLabels(
  inUse: string[],
  order: string[],
): string[] {
  const inUseSet = new Set(inUse);
  const head = order.filter((label) => inUseSet.has(label));
  const headSet = new Set(head);
  const tail = inUse
    .filter((label) => !headSet.has(label))
    .sort((a, b) => a.localeCompare(b));

  return [...head, ...tail];
}

/**
 * Segments an already-filtered, already-sorted node list into
 * display sections. Group membership never reorders nodes inside a
 * section: each section keeps the incoming sequence. Sections with
 * no nodes never appear; the ungrouped section appears only when it
 * has nodes, titled 「未分组」.
 */
export function segmentNodesByGroup<T extends { displayGroup?: string | null }>(
  nodes: T[],
  order: string[],
): Array<NodeGroupSection<T>> {
  const buckets = new Map<string, T[]>();

  for (const node of nodes) {
    const label = nodeGroupLabel(node);
    const bucket = buckets.get(label);

    if (bucket) bucket.push(node);
    else buckets.set(label, [node]);
  }
  const labels = orderNodeGroupLabels(
    [...buckets.keys()].filter((label) => label !== ""),
    order,
  );
  const sections: Array<NodeGroupSection<T>> = labels.map((label) => ({
    label,
    nodes: buckets.get(label) ?? [],
  }));
  const ungrouped = buckets.get("");

  if (ungrouped && ungrouped.length > 0) {
    sections.push({ label: "", nodes: ungrouped });
  }

  return sections;
}
