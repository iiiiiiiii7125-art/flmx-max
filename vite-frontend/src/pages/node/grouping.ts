/**
 * Node-page display grouping (pure presentation, user decision
 * 2026-11-09): each node carries at most one free-text group label
 * ("" = ungrouped). The page renders nodes in labelled sections;
 * section order comes from a manually arranged name list persisted
 * server-side as a panel config value, so it follows the panel (and
 * its backups), not the browser.
 */
export const NODE_GROUP_ORDER_CONFIG_KEY = "node_display_group_order";

/**
 * Collapsed-section list, persisted server-side through the same
 * panel-config channel as the section order (config key
 * `node_display_group_collapsed`, value = JSON array of labels).
 * The ungrouped section is recorded as the empty string "" — real
 * group labels are never empty after trimming, so the encoding is
 * unambiguous. Collapse is render state only: it never changes
 * membership, counts, selection or ordering.
 */
export const NODE_GROUP_COLLAPSED_CONFIG_KEY = "node_display_group_collapsed";

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

/**
 * Parses the persisted collapsed list (a JSON array of labels).
 * Unlike the order parser, a single "" entry is meaningful (the
 * ungrouped section) and is kept; other entries are trimmed,
 * empties dropped, duplicates removed. Anything malformed
 * degrades to an empty list — every section expanded — rather
 * than failing to render. Entries naming groups that no longer
 * exist are inert: they match no section, and re-creating a group
 * with the same name restores its collapsed state.
 */
export function parseNodeGroupCollapsed(
  raw: string | null | undefined,
): string[] {
  if (!raw) return [];
  try {
    const parsed: unknown = JSON.parse(raw);

    if (!Array.isArray(parsed)) return [];
    const seen = new Set<string>();
    const out: string[] = [];

    for (const item of parsed) {
      if (typeof item !== "string") continue;
      const label = item.trim();

      if (!seen.has(label)) {
        seen.add(label);
        out.push(label);
      }
    }

    return out;
  } catch {
    return [];
  }
}

/** Whether the section with this label ("" = ungrouped) is collapsed. */
export function isNodeGroupCollapsed(
  collapsed: string[],
  label: string,
): boolean {
  return collapsed.includes(label);
}

/**
 * The full set of groups the management surfaces work with: the
 * persisted registry (order list) first, in list order — including
 * registered-but-empty groups — then any in-use labels missing from
 * the registry (v1 free-text leftovers), by name. Saving this merged
 * list as the order materializes leftover labels into the registry.
 */
export function mergeNodeGroupRegistry(
  order: string[],
  inUse: string[],
): string[] {
  const seen = new Set<string>();
  const out: string[] = [];

  for (const label of [
    ...order,
    ...[...inUse].sort((a, b) => a.localeCompare(b)),
  ]) {
    const trimmed = label.trim();

    if (trimmed && !seen.has(trimmed)) {
      seen.add(trimmed);
      out.push(trimmed);
    }
  }

  return out;
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
