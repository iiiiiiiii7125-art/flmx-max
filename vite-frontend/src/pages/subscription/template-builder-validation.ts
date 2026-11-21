import type { TemplateBuilder } from "@/api";

/**
 * Pure validation for the visual template builder, mirroring the
 * backend TemplateBuilder.validate() (go-backend/internal/kernel/
 * sub_template_builder.go) rule for rule.
 *
 * R18-E2-05 / R18-R2-E2-01: the builder used to let through recipes
 * the backend refuses at save time (group names with surrounding
 * whitespace, over 50 runes, or forbidden characters; over-long or
 * comma/newline-bearing filters; invalid regexes; stale rule targets
 * after a group rename/delete; bad custom-rule values; over-count
 * recipes). These validators run inline in the form and gate saving
 * in templates.tsx, so a recipe the backend would refuse never
 * reaches the save call.
 *
 * Notes on parity: JS RegExp is an approximation of Go's regexp
 * (RE2) — a filter valid in JS but invalid in Go (e.g. lookahead)
 * is still caught by the backend at save time; the backend remains
 * the final gate. Length caps that the backend counts in bytes
 * (filter 200, keyword 253) are measured in UTF-8 bytes here via
 * TextEncoder. Group-name length is counted in runes
 * ([...name].length), matching Go's []rune.
 */

export const BUILDER_MAIN = "节点选择";

/** Reserved group names the backend refuses in a builder recipe
 * (TemplateBuilder.validate): the main group plus the client builtins.
 * Lowercase direct/reject are refused too — clients treat names
 * case-insensitively there, so the backend rejects both cases. */
const BUILDER_RESERVED_GROUP_NAMES = new Set([
  BUILDER_MAIN,
  "DIRECT",
  "REJECT",
  "direct",
  "reject",
]);

const BUILDER_GROUP_NAME_FORBIDDEN = ',=\r\n[]{}#"\\';

const BUILDER_PRESETS = new Set(["custom", "minimal", "balanced", "full"]);

const utf8Length = (s: string): number => new TextEncoder().encode(s).length;

/**
 * R18-E2-05 (extended by R18-R2-E2-01): builder group-name conflicts,
 * mirroring the backend TemplateBuilder.validate() name rule exactly,
 * in the backend's own check order: empty, surrounding whitespace,
 * over 50 runes, forbidden characters, reserved name, duplicate.
 * Returns one message per conflicting group row (keyed by group
 * index), so the form can mark the row inline and the page can gate
 * saving on the same verdict.
 */
export function builderGroupNameConflicts(
  builder: TemplateBuilder,
): Map<number, string> {
  const conflicts = new Map<number, string>();
  const seen = new Set<string>();

  builder.groups.forEach((g, i) => {
    if (g.name === "") {
      conflicts.set(i, "组名不能为空");

      return;
    }
    if (g.name !== g.name.trim()) {
      conflicts.set(i, `组名「${g.name}」首尾不能有空格，后端会拒绝保存`);

      return;
    }
    if ([...g.name].length > 50) {
      conflicts.set(i, `组名「${g.name}」最多 50 字，后端会拒绝保存`);

      return;
    }
    if ([...g.name].some((ch) => BUILDER_GROUP_NAME_FORBIDDEN.includes(ch))) {
      conflicts.set(
        i,
        '组名不能包含 , = [ ] { } # " \\ 或换行符，后端会拒绝保存',
      );

      return;
    }
    if (BUILDER_RESERVED_GROUP_NAMES.has(g.name)) {
      conflicts.set(i, `组名「${g.name}」是保留名，后端会拒绝保存`);

      return;
    }
    if (seen.has(g.name)) {
      conflicts.set(i, `组名「${g.name}」重复，后端会拒绝保存`);

      return;
    }
    seen.add(g.name);
  });

  return conflicts;
}

/**
 * Per-row node-filter conflicts, mirroring the backend's filter rule:
 * at most 200 bytes, no comma or newline, and a compilable regex.
 * (The backend caps bytes; a multi-byte filter hits the cap sooner
 * than its character count suggests.)
 */
export function builderGroupFilterConflicts(
  builder: TemplateBuilder,
): Map<number, string> {
  const conflicts = new Map<number, string>();

  builder.groups.forEach((g, i) => {
    if (utf8Length(g.filter) > 200) {
      conflicts.set(i, "节点过滤条件最多 200 字节，后端会拒绝保存");

      return;
    }
    if (/[,\r\n]/.test(g.filter)) {
      conflicts.set(i, "节点过滤条件不能包含逗号或换行，后端会拒绝保存");

      return;
    }
    // R18-R3-E2-02: Go's regexp (RE2) accepts Python-style named
    // groups "(?P<name>...)", which JS RegExp rejects — normalize them
    // to the "(?<name>...)" form both engines accept before compiling,
    // so a filter the backend accepts is not wrongly flagged here.
    // (Other JS/RE2 gaps, e.g. lookahead, stay backend-gated as noted
    // in the module header.)
    try {
      new RegExp(g.filter.replace(/\(\?P</g, "(?<"));
    } catch {
      conflicts.set(i, `策略组「${g.name}」的正则表达式无效，后端会拒绝保存`);
    }
  });

  return conflicts;
}

/**
 * Recipe-level conflicts, mirroring the backend's recipe rules that
 * are not per-row: editor version, preset, group/rule counts, group
 * types, and the final (fallback) target. A stale final — e.g. its
 * group was renamed or deleted — is the reachable case here: the
 * form's final selector only offers live targets, but a rename or
 * delete after picking leaves a dangling value the backend refuses.
 */
export function builderRecipeConflicts(builder: TemplateBuilder): string[] {
  const problems: string[] = [];

  if (builder.version !== 1) {
    problems.push("不支持的规则编辑器版本");
  }
  if (!BUILDER_PRESETS.has(builder.preset)) {
    problems.push("请选择规则预设");
  }
  if (builder.groups.length > 20 || builder.rules.length > 500) {
    problems.push("最多 20 个策略组和 500 条规则");
  }
  for (const g of builder.groups) {
    if (g.type !== "select" && g.type !== "url-test") {
      problems.push("策略组仅支持手动选择或自动测速");
      break;
    }
  }
  const targets = new Set([
    BUILDER_MAIN,
    ...builder.groups.map((g) => g.name),
    "DIRECT",
    "REJECT",
  ]);

  if (!targets.has(builder.final) || builder.final === "REJECT") {
    problems.push("兜底去向请选择节点选择、策略组或直连");
  }

  return problems;
}

function isValidBuilderDomain(s: string): boolean {
  if (s.length === 0 || s.length > 253) {
    return false;
  }
  for (const label of s.split(".")) {
    if (
      label.length === 0 ||
      label.length > 63 ||
      label.startsWith("-") ||
      label.endsWith("-") ||
      !/^[A-Za-z0-9-]+$/.test(label)
    ) {
      return false;
    }
  }

  return true;
}

function isValidIPv4(ip: string): boolean {
  const parts = ip.split(".");

  if (parts.length !== 4) {
    return false;
  }

  return parts.every((p) => /^(0|[1-9]\d*)$/.test(p) && Number(p) <= 255);
}

function isValidIPv6(ip: string): boolean {
  if (ip.includes("%")) {
    return false; // no zone identifiers (netip.ParsePrefix rejects them)
  }
  if (ip === "::") {
    return true;
  }
  const halves = ip.split("::");

  if (halves.length > 2) {
    return false;
  }
  const checkGroups = (groups: string[]): { ok: boolean; count: number } => {
    let count = groups.length;

    for (let i = 0; i < groups.length; i++) {
      const g = groups[i];

      if (g.includes(".")) {
        // Embedded IPv4 occupies two groups and must be last.
        if (i !== groups.length - 1 || !isValidIPv4(g)) {
          return { ok: false, count: 0 };
        }
        count += 1;
      } else if (!/^[0-9a-fA-F]{1,4}$/.test(g)) {
        return { ok: false, count: 0 };
      }
    }

    return { ok: true, count };
  };
  const left = halves[0] === "" ? [] : halves[0].split(":");

  if (halves.length === 1) {
    const checked = checkGroups(left);

    return checked.ok && checked.count === 8;
  }
  const right = halves[1] === "" ? [] : halves[1].split(":");
  // R18-R3-E2-01: an embedded IPv4 address must be the last element
  // of the whole address — netip.ParsePrefix rejects anything after
  // it, including "::" itself ("192.168.1.1::/120" is invalid even
  // though the right half is empty). checkGroups below only requires
  // it to be last within its own half, so reject a dotted left half
  // outright here.
  if (halves[0].includes(".")) {
    return false;
  }
  const checkedLeft = checkGroups(left);
  const checkedRight = checkGroups(right);

  if (!checkedLeft.ok || !checkedRight.ok) {
    return false;
  }

  // "::" must compress at least one group.
  return checkedLeft.count + checkedRight.count <= 7;
}

function isValidCIDR(s: string): boolean {
  const slash = s.lastIndexOf("/");

  if (slash <= 0) {
    return false;
  }
  const ip = s.slice(0, slash);
  const bits = s.slice(slash + 1);

  // R18-R3-E2-01: netip.ParsePrefix rejects a leading zero in the
  // prefix length ("1.2.3.4/024", "1.2.3.4/00"), so require "0" or a
  // non-zero leading digit instead of any digit run.
  if (!/^(0|[1-9][0-9]*)$/.test(bits)) {
    return false;
  }
  const n = Number(bits);

  if (ip.includes(":")) {
    return n <= 128 && isValidIPv6(ip);
  }

  return n <= 32 && isValidIPv4(ip);
}

/**
 * Per-rule conflicts, mirroring the backend's rule loop: the target
 * must be a live group (renaming or deleting a group after rules
 * point at it leaves dangling targets the backend refuses), category
 * rules must name a known, unrepeated category, and custom-rule
 * values must satisfy their type's rule (domain, keyword, CIDR).
 * Returns one message per offending rule row (keyed by rule index).
 */
export function builderRuleConflicts(
  builder: TemplateBuilder,
  catalogKeys: Set<string>,
): Map<number, string> {
  const conflicts = new Map<number, string>();
  const targets = new Set([
    BUILDER_MAIN,
    ...builder.groups.map((g) => g.name),
    "DIRECT",
    "REJECT",
  ]);
  const seenCategories = new Set<string>();

  builder.rules.forEach((r, i) => {
    if (!targets.has(r.target)) {
      conflicts.set(i, `第 ${i + 1} 条规则的策略组不存在：${r.target}`);

      return;
    }
    switch (r.type) {
      case "category": {
        if (!catalogKeys.has(r.value) || seenCategories.has(r.value)) {
          conflicts.set(i, `规则分类不存在或重复：${r.value}`);

          return;
        }
        seenCategories.add(r.value);
        break;
      }
      case "domain":
      case "domain_suffix": {
        if (!isValidBuilderDomain(r.value)) {
          conflicts.set(
            i,
            `第 ${i + 1} 条规则请填写域名（不带协议、路径或通配符）`,
          );

          return;
        }
        break;
      }
      case "domain_keyword": {
        if (
          r.value === "" ||
          utf8Length(r.value) > 253 ||
          /[,\r\n \t"\\]/.test(r.value)
        ) {
          conflicts.set(i, `第 ${i + 1} 条规则的域名关键词无效`);

          return;
        }
        break;
      }
      case "ip_cidr": {
        if (!isValidCIDR(r.value)) {
          conflicts.set(i, `第 ${i + 1} 条规则请填写有效的 IPv4 / IPv6 CIDR`);

          return;
        }
        break;
      }
      default: {
        conflicts.set(i, `不支持的规则类型：${r.type}`);
      }
    }
  });

  return conflicts;
}
