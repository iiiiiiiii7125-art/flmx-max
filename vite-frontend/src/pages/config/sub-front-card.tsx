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
        不变）。填协议 + 域名（可带端口），如 https://sub.example.com，必须以
        http:// 或 https:// 开头（推荐
        https://）、末尾不要斜杠、不要带路径。配置后，订阅管理、我的订阅等页面的链接旁会出现地址选择，可按前置地址复制链接；不配置则只用面板域名，与此前一致。
      </p>
      {/* AP-1：Cloudflare 侧配置必须写到字段级（2026-11-01 立规）——此前
          本页只字未提，管理员只能去翻文档，选错字段规则会静默失效。 */}
      <div className="space-y-1.5 rounded-lg border border-divider p-3">
        <p className="text-xs font-semibold text-foreground">
          用 Cloudflare 橙云代理前置域名时，还须在 Cloudflare 侧配缓存绕过：
        </p>
        <p className="text-xs font-semibold text-foreground">
          ① 进入该域名 →「缓存 → Cache Rules → 创建规则」：匹配字段选「URI
          路径」，运算符选「通配符」、值填 /api/v1/open_api/sub/*
          （或运算符选「以…开头」、值填
          /api/v1/open_api/sub/，两者等效）；「缓存资格」选「绕过缓存（Bypass
          cache）」后保存部署。⚠️ 不要选「URI 完整」字段：它按整条 URL
          全串匹配、永不命中，规则会静默失效。
        </p>
        <p className="text-xs font-semibold text-foreground">
          ② 前置域名必须是一级子域名（如 sub.example.com）：二级子域名超出
          Cloudflare
          免费版边缘证书覆盖范围，橙云后证书不匹配，任何网络下都拉不到订阅。
        </p>
        <p className="text-xs font-semibold text-foreground">
          ③ DNS 的 A 记录先保持灰云（仅
          DNS），等面板机证书签发完成（https://前置域名
          能打开）后再改橙云（已代理）；面板机可运行 panel_install.sh 菜单
          6「配置订阅前置域名（自动 HTTPS）」自动追加 Caddy 站点块并申请证书。
        </p>
        <p className="text-xs font-semibold text-foreground">
          ④ 自检：对一条前置订阅链接执行 curl -sI 链接 | grep -i
          cf-cache-status，应显示 BYPASS。
        </p>
      </div>
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
