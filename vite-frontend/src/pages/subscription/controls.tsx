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
      className="glass-surface grid gap-1 rounded-xl p-1 text-sm"
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
                ? "bg-tray font-semibold shadow-sm"
                : disabled
                  ? "cursor-not-allowed text-foreground"
                  : "text-foreground"
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
        {hint && <span className="text-xs font-semibold text-foreground">{hint}</span>}
      </label>
    </div>
  );
}
