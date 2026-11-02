import { Plus, Trash2 } from "lucide-react";

import { Button } from "@/shadcn-bridge/heroui/button";
import { Input } from "@/shadcn-bridge/heroui/input";
import { SUBSCRIPTION_FRONT_URLS_KEY } from "@/utils/subscription-front";

const MAX_FRONTS = 20;

// Editor rows keep in-progress text (including empty rows while
// typing); the backend validates the saved set and rejects invalid
// entries with the offending value named.
function parseRows(raw: string | undefined): string[] {
  if (!raw) return [];

  try {
    const list = JSON.parse(raw);

    return Array.isArray(list)
      ? list.filter((v): v is string => typeof v === "string")
      : [];
  } catch {
    return [];
  }
}

/** Subscription front base list editor, saved with the config page. */
export function SubFrontSettings({
  configs,
  onChange,
}: {
  configs: Record<string, string>;
  onChange: (key: string, value: string) => void;
}) {
  const rows = parseRows(configs[SUBSCRIPTION_FRONT_URLS_KEY]);

  const save = (next: string[]) =>
    onChange(
      SUBSCRIPTION_FRONT_URLS_KEY,
      next.length ? JSON.stringify(next) : "",
    );
  const update = (i: number, value: string) =>
    save(rows.map((row, j) => (j === i ? value : row)));
  const remove = (i: number) => save(rows.filter((_, j) => j !== i));
  const add = () => save([...rows, ""]);
  const full = rows.length >= MAX_FRONTS;

  return (
    <div className="space-y-3">
      <p className="text-xs font-semibold text-foreground">
        前置地址是订阅链接的备用入口：当面板域名被部分运营商阻断或烂路由时，用户可经前置地址拉取同一条订阅（前置只需把订阅路径反代回面板，token
        不变）。填协议 + 域名（可带端口），如
        https://sub.example.com，必须以 http:// 或 https:// 开头（推荐
        https://）、末尾不要斜杠、不要带路径。配置后，订阅管理、我的订阅等页面的链接旁会出现地址选择，可按前置地址复制链接；不配置则只用面板域名，与此前一致。
      </p>
      {rows.map((row, i) => (
        <div key={i} className="flex items-center gap-2">
          <Input
            aria-label={`前置地址 ${i + 1}`}
            className="flex-1"
            placeholder="https://sub.example.com"
            size="sm"
            value={row}
            variant="bordered"
            onChange={(e) => update(i, e.target.value)}
          />
          <Button
            isIconOnly
            aria-label={`删除前置地址 ${i + 1}`}
            size="sm"
            variant="light"
            onPress={() => remove(i)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      ))}
      <Button
        isDisabled={full}
        size="sm"
        startContent={<Plus className="h-4 w-4" />}
        variant="flat"
        onPress={add}
      >
        添加前置地址
      </Button>
      {full && (
        <p className="text-xs font-semibold text-foreground">
          最多填写 {MAX_FRONTS} 个前置地址
        </p>
      )}
    </div>
  );
}
