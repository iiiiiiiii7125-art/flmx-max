import { Select, SelectItem } from "@/shadcn-bridge/heroui/select";

/**
 * Base picker for subscription link assembly: the panel origin
 * (value "") or one of the configured front bases. Renders nothing
 * when no front base is configured, so surfaces without fronts keep
 * their exact previous layout and behavior.
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
      <span className="whitespace-nowrap text-xs text-default-500">
        链接地址
      </span>
      <Select
        aria-label="订阅链接地址"
        className="max-w-[280px]"
        selectedKeys={[value]}
        size="sm"
        onSelectionChange={(keys) => {
          const selected = Array.from(keys)[0] as string | undefined;

          onChange(selected ?? "");
        }}
      >
        <SelectItem key="">面板域名</SelectItem>
        {fronts.map((front) => (
          <SelectItem key={front}>{front}</SelectItem>
        ))}
      </Select>
    </div>
  );
}
