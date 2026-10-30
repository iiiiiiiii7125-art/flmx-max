import { Switch } from "@/shadcn-bridge/heroui/switch";

/** Small controls shared by the subscription editors. */
export function Segmented<T extends string>({
  value,
  options,
  onChange,
  disabledOptions,
}: {
  value: T;
  options: Array<[T, string]>;
  onChange: (v: T) => void;
  /** 当前组合下非法的选项键：渲染为禁用态，不可点选。 */
  disabledOptions?: T[];
}) {
  return (
    <div
      className="grid gap-1 rounded-xl bg-default-100 p-1 text-sm"
      style={{
        gridTemplateColumns: `repeat(${options.length}, minmax(0, 1fr))`,
      }}
    >
      {options.map(([k, label]) => {
        const disabled = disabledOptions?.includes(k) ?? false;

        return (
          <button
            key={k}
            className={`rounded-lg px-2 py-1.5 transition-colors ${
              value === k
                ? "bg-background font-medium shadow-sm"
                : disabled
                  ? "cursor-not-allowed text-default-300"
                  : "text-default-500"
            }`}
            disabled={disabled}
            type="button"
            onClick={() => onChange(k)}
          >
            {label}
          </button>
        );
      })}
    </div>
  );
}

export function ToggleRow({
  id,
  label,
  hint,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  value: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <div className="flex items-start gap-3">
      <Switch
        aria-labelledby={`${id}-label`}
        id={id}
        isSelected={value}
        size="sm"
        onValueChange={onChange}
      />
      <label
        className="flex cursor-pointer flex-col"
        htmlFor={id}
        id={`${id}-label`}
      >
        <span className="text-sm">{label}</span>
        {hint && <span className="text-xs text-default-500">{hint}</span>}
      </label>
    </div>
  );
}

/**
 * 编辑器的页面标签：圆角分段条，标签等宽分布。在手机上不滚动，
 * 每页都是一触即达；标签保持简短（两个字）。
 * 移植自 kure29/flyx（订阅规则页用）。
 */
export function EditorTabs<T extends string>({
  value,
  tabs,
  onChange,
}: {
  value: T;
  tabs: Array<{ key: T; label: string; badge?: string | number }>;
  onChange: (v: T) => void;
}) {
  return (
    <div
      className="grid gap-1 rounded-xl bg-default-100/90 p-1 backdrop-blur-md"
      role="tablist"
      style={{ gridTemplateColumns: `repeat(${tabs.length}, minmax(0, 1fr))` }}
    >
      {tabs.map((t) => (
        <button
          key={t.key}
          aria-selected={value === t.key}
          className={`relative whitespace-nowrap rounded-lg px-1 py-1.5 text-sm transition-colors ${
            value === t.key
              ? "bg-content1 font-medium text-primary shadow-sm"
              : "text-default-500 hover:text-foreground"
          }`}
          role="tab"
          type="button"
          onClick={() => onChange(t.key)}
        >
          {t.label}
          {t.badge !== undefined && t.badge !== "" && (
            <span
              className={`absolute right-0.5 top-0.5 rounded-full px-1 text-[10px] leading-4 ${
                value === t.key
                  ? "bg-primary text-white"
                  : "bg-default-300 text-default-700"
              }`}
            >
              {t.badge}
            </span>
          )}
        </button>
      ))}
    </div>
  );
}
