import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";
import { panelOrigin } from "@/utils/panel-origin";

/**
 * Base picker for subscription link assembly: the panel origin
 * (value "") or one of the configured front bases. The default is
 * the panel origin itself, so there is no separate "panel domain"
 * option: the select's placeholder option carries the panel origin
 * text and doubles as the closed-state display for the default and
 * as the way back to it. Renders nothing when no front base is
 * configured, so surfaces without fronts keep their exact previous
 * layout and behavior.
 */
export function SubFrontPicker({
  fronts,
  value,
  onChange,
}: {
  fronts: string[];
  value: string;
  onChange: (base: string) => void;
}) {
  if (fronts.length === 0) return null;

  return (
    <div className="flex items-center gap-2">
      <span className="whitespace-nowrap text-xs font-semibold text-foreground">
        链接地址
      </span>
      <Select
        aria-label="订阅链接地址"
        className="max-w-[280px]"
        placeholder={panelOrigin()}
        selectedKeys={[value]}
        size="sm"
        onSelectionChange={(keys) => {
          const selected = Array.from(keys)[0] as string | undefined;

          onChange(selected ?? "");
        }}
      >
        {fronts.map((front) => (
          <SelectItem key={front}>{front}</SelectItem>
        ))}
      </Select>
    </div>
  );
}
