import type {
  TemplateBuilder,
  TemplateBuilderRule,
  TemplateRuleCategoryItem,
} from "@/api";

import { useMemo } from "react";
import { ArrowDown, ArrowUp, Plus, Trash2 } from "lucide-react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Input } from "@/shadcn-bridge/heroui/input";
import { Segmented, ToggleRow } from "@/pages/subscription/controls";

export const BUILDER_MAIN = "节点选择";

export const PRESET_OPTIONS: Array<[string, string]> = [
  ["minimal", "极简"],
  ["balanced", "均衡"],
  ["full", "完整"],
  ["custom", "自定义"],
];

/** Quick-add region url-test groups for the builder. */
export const REGION_GROUP_PRESETS: Array<{ name: string; filter: string }> = [
  { name: "🇭🇰 香港", filter: "港|HK|Hong Kong" },
  { name: "🇯🇵 日本", filter: "日|JP|Japan" },
  { name: "🇺🇸 美国", filter: "美|US|United States" },
  { name: "🇸🇬 新加坡", filter: "新加坡|SG|Singapore" },
  { name: "🇹🇼 台湾", filter: "台|TW|Taiwan" },
  { name: "🇰🇷 韩国", filter: "韩|KR|Korea" },
];

const CUSTOM_RULE_TYPES: Array<[string, string]> = [
  ["domain", "完整域名"],
  ["domain_suffix", "域名后缀"],
  ["domain_keyword", "域名关键词"],
  ["ip_cidr", "IP CIDR"],
];

/** A fresh recipe for a preset: its categories in catalog order. */
export function builderFromPreset(
  catalog: TemplateRuleCategoryItem[],
  preset: string,
  groups: TemplateBuilder["groups"] = [],
): TemplateBuilder {
  return {
    version: 1,
    preset,
    groups,
    rules: catalog
      .filter((c) => c.presets.includes(preset))
      .map((c) => ({ type: "category", value: c.key, target: c.target })),
    final: BUILDER_MAIN,
  };
}

const selectCls =
  "h-9 rounded-lg border border-divider bg-content1 px-2 text-sm";

/**
 * The visual template builder: pick a rule preset, tick categories,
 * order the rules, add custom rules and region groups. Emits the
 * recipe the backend turns into a profile (see /subscription/template).
 */
export function TemplateBuilderForm({
  catalog,
  value,
  onChange,
}: {
  catalog: TemplateRuleCategoryItem[];
  value: TemplateBuilder;
  onChange: (b: TemplateBuilder) => void;
}) {
  const catByKey = useMemo(
    () => new Map(catalog.map((c) => [c.key, c])),
    [catalog],
  );
  const targets = useMemo(
    () => [BUILDER_MAIN, ...value.groups.map((g) => g.name), "DIRECT", "REJECT"],
    [value.groups],
  );
  const picked = useMemo(
    () =>
      new Map(
        value.rules
          .filter((r) => r.type === "category")
          .map((r) => [r.value, r]),
      ),
    [value.rules],
  );

  const setRules = (rules: TemplateBuilderRule[], preset = "custom") =>
    onChange({ ...value, rules, preset: value.preset === preset ? value.preset : preset });

  const pickPreset = (preset: string) => {
    if (preset === "custom") {
      onChange({ ...value, preset });
      return;
    }
    // Switching presets keeps custom rules and groups (upstream behavior).
    const customs = value.rules.filter((r) => r.type !== "category");
    const base = builderFromPreset(catalog, preset, value.groups);
    onChange({ ...value, preset, rules: [...base.rules, ...customs] });
  };

  const toggleCategory = (key: string, on: boolean) => {
    if (on) {
      const cat = catByKey.get(key);
      if (!cat) return;
      setRules([
        ...value.rules,
        { type: "category", value: key, target: cat.target },
      ]);
    } else {
      setRules(
        value.rules.filter((r) => !(r.type === "category" && r.value === key)),
      );
    }
  };

  const patchRule = (idx: number, patch: Partial<TemplateBuilderRule>) => {
    const rules = value.rules.map((r, i) => (i === idx ? { ...r, ...patch } : r));
    setRules(rules, value.preset);
  };

  const moveRule = (idx: number, dir: -1 | 1) => {
    const j = idx + dir;
    if (j < 0 || j >= value.rules.length) return;
    const rules = [...value.rules];
    [rules[idx], rules[j]] = [rules[j], rules[idx]];
    setRules(rules, value.preset);
  };

  const addCustomRule = () =>
    // New rules go first, like the backend guide says.
    setRules([
      { type: "domain_suffix", value: "", target: BUILDER_MAIN },
      ...value.rules,
    ]);

  const patchGroup = (idx: number, patch: Partial<TemplateBuilder["groups"][number]>) =>
    onChange({
      ...value,
      groups: value.groups.map((g, i) => (i === idx ? { ...g, ...patch } : g)),
    });

  return (
    <div className="space-y-5">
      <div className="space-y-1.5">
        <p className="text-xs font-semibold">规则预设</p>
        <Segmented
          options={PRESET_OPTIONS}
          value={value.preset}
          onChange={pickPreset}
        />
        <p className="text-xs font-semibold text-foreground">
          极简只分国内/国外；均衡多分 AI、视频、通讯等常用服务；完整包含全部{" "}
          {catalog.length} 类。勾选变化会自动切到自定义。
        </p>
      </div>

      <div className="space-y-1.5">
        <p className="text-xs font-semibold">规则分类</p>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          {catalog.map((c) => (
            <Checkbox
              key={c.key}
              isSelected={picked.has(c.key)}
              onValueChange={(on) => toggleCategory(c.key, on)}
            >
              <span className="text-sm">{c.name}</span>
            </Checkbox>
          ))}
        </div>
      </div>

      <div className="space-y-1.5">
        <div className="flex items-center justify-between">
          <p className="text-xs font-semibold">规则顺序（从上往下匹配）</p>
          <Button size="sm" variant="flat" onPress={addCustomRule}>
            <Plus className="h-3.5 w-3.5" /> 自定义规则
          </Button>
        </div>
        {value.rules.length === 0 && (
          <p className="text-xs font-semibold text-foreground">
            还没有规则，全部流量走下面的兜底去向。
          </p>
        )}
        {value.rules.map((r, i) => (
          <div key={i} className="flex flex-wrap items-center gap-1.5">
            {r.type === "category" ? (
              <span className="min-w-28 text-sm font-medium">
                {catByKey.get(r.value)?.name ?? r.value}
              </span>
            ) : (
              <>
                <select
                  className={selectCls}
                  value={r.type}
                  onChange={(e) => patchRule(i, { type: e.target.value })}
                >
                  {CUSTOM_RULE_TYPES.map(([k, label]) => (
                    <option key={k} value={k}>
                      {label}
                    </option>
                  ))}
                </select>
                <Input
                  className="w-44"
                  placeholder="example.com / 192.0.2.0/24"
                  size="sm"
                  value={r.value}
                  variant="bordered"
                  onChange={(e) => patchRule(i, { value: e.target.value })}
                />
              </>
            )}
            <span className="text-xs font-semibold text-foreground">→</span>
            <select
              className={selectCls}
              value={r.target}
              onChange={(e) => patchRule(i, { target: e.target.value })}
            >
              {targets.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
            <Button
              isIconOnly
              size="sm"
              variant="light"
              onPress={() => moveRule(i, -1)}
            >
              <ArrowUp className="h-3.5 w-3.5" />
            </Button>
            <Button
              isIconOnly
              size="sm"
              variant="light"
              onPress={() => moveRule(i, 1)}
            >
              <ArrowDown className="h-3.5 w-3.5" />
            </Button>
            <Button
              isIconOnly
              color="danger"
              size="sm"
              variant="light"
              onPress={() =>
                setRules(
                  value.rules.filter((_, j) => j !== i),
                  value.preset,
                )
              }
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        ))}
        <div className="flex items-center gap-1.5">
          <span className="min-w-28 text-sm font-medium">兜底（未命中）</span>
          <span className="text-xs font-semibold text-foreground">→</span>
          <select
            className={selectCls}
            value={value.final}
            onChange={(e) => onChange({ ...value, final: e.target.value })}
          >
            {targets
              .filter((t) => t !== "REJECT")
              .map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
          </select>
        </div>
        <ToggleRow
          hint="每个分类生成一个同名选择组，可在客户端里单独选节点；关掉则规则直接指向去向"
          id="tpl-service-groups"
          label="按服务生成独立策略组"
          value={value.serviceGroups !== false}
          onChange={(on) => onChange({ ...value, serviceGroups: on })}
        />
      </div>

      <div className="space-y-1.5">
        <p className="text-xs font-semibold">地区策略组（按名称自动收节点）</p>
        <div className="flex flex-wrap gap-1.5">
          {REGION_GROUP_PRESETS.map((g) => (
            <Button
              key={g.name}
              isDisabled={value.groups.some((x) => x.name === g.name)}
              size="sm"
              variant="flat"
              onPress={() =>
                onChange({
                  ...value,
                  groups: [
                    ...value.groups,
                    { name: g.name, type: "url-test", filter: g.filter },
                  ],
                })
              }
            >
              {g.name}
            </Button>
          ))}
          <Button
            size="sm"
            variant="flat"
            onPress={() =>
              onChange({
                ...value,
                groups: [...value.groups, { name: "", type: "select", filter: "" }],
              })
            }
          >
            <Plus className="h-3.5 w-3.5" /> 自定义组
          </Button>
        </div>
        {value.groups.map((g, i) => (
          <div key={i} className="flex flex-wrap items-center gap-1.5">
            <Input
              className="w-36"
              placeholder="组名"
              size="sm"
              value={g.name}
              variant="bordered"
              onChange={(e) => patchGroup(i, { name: e.target.value })}
            />
            <select
              className={selectCls}
              value={g.type}
              onChange={(e) => patchGroup(i, { type: e.target.value })}
            >
              <option value="select">手动选择</option>
              <option value="url-test">自动测速</option>
            </select>
            <Input
              className="w-52"
              placeholder="节点名过滤（正则，可空）"
              size="sm"
              value={g.filter}
              variant="bordered"
              onChange={(e) => patchGroup(i, { filter: e.target.value })}
            />
            <Button
              isIconOnly
              color="danger"
              size="sm"
              variant="light"
              onPress={() =>
                onChange({
                  ...value,
                  groups: value.groups.filter((_, j) => j !== i),
                })
              }
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        ))}
      </div>
    </div>
  );
}
