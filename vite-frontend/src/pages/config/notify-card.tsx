import { useRef, useState } from "react";
import toast from "react-hot-toast";

import { testNotify } from "@/api";
import { Button } from "@/shadcn-bridge/heroui/button";
import { Card, CardBody, CardHeader } from "@/shadcn-bridge/heroui/card";
import { Checkbox } from "@/shadcn-bridge/heroui/checkbox";
import { Divider } from "@/shadcn-bridge/heroui/divider";
import { Input } from "@/shadcn-bridge/heroui/input";
import { Switch } from "@/shadcn-bridge/heroui/switch";

const NOTIFY_EVENTS = [
  {
    key: "node_offline",
    label: "节点掉线 / 恢复",
    description: "断开超过 90 秒才提醒，重启和升级不打扰",
  },
  {
    key: "inbound_down",
    label: "导入节点连不上 / 恢复",
    description: "连续两次检测失败（约 6 分钟）才提醒",
  },
  {
    key: "kernel_error",
    label: "内核运行出错",
    description: "Xray / sing-box / Snell 报错时提醒",
  },
  {
    key: "node_expiry",
    label: "节点即将到期",
    description: "到期前 3 天提醒一次",
  },
  {
    key: "latency_bad",
    label: "延迟 / 丢包异常",
    description:
      "某测试点连续 3 次检测（约 9 分钟）丢包 ≥ 20% 或延迟超阈值才提醒，恢复时再提醒一次；阈值可在下方「延迟 / 丢包阈值」调整",
  },
  {
    key: "route_change",
    label: "回程线路变化",
    description: "识别出的回程线路（如 CN2 GIA → 163）变化时提醒",
  },
  {
    key: "user_quota",
    label: "用户流量 / 到期",
    description:
      "转发用户流量用到 90%、用完或 3 天内到期；分享订阅流量用到 90%、用完或 3 天内到期",
  },
  {
    key: "backup_failed",
    label: "Gist 备份失败",
    description: "自动上传到 GitHub Gist 失败时提醒",
  },
  {
    key: "daily_report",
    label: "每日汇总",
    description: "每天定时发一条：节点在线、出错、流量、分享订阅",
  },
] as const;

const ALL_EVENT_KEYS = NOTIFY_EVENTS.map((e) => e.key) as string[];

interface NotifyCardProps {
  configs: Record<string, string>;
  originalConfigs: Record<string, string>;
  onChange: (key: string, value: string) => void;
}

function Field({
  label,
  description,
  children,
}: {
  label: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-2">
      <div className="flex flex-col gap-1">
        <label className="text-sm font-medium text-foreground">{label}</label>
        {description && (
          <p className="text-xs font-semibold text-foreground">{description}</p>
        )}
      </div>
      {children}
    </div>
  );
}

/** Telegram / Webhook notification settings, saved with the page's other configs. */
export function NotifyCard({
  configs,
  originalConfigs,
  onChange,
}: NotifyCardProps) {
  const [testing, setTesting] = useState(false);
  const testInFlightRef = useRef(false);
  const enabled = configs.notify_enabled === "true";
  const rawEvents = (configs.notify_events ?? "").trim();
  // Empty means every event, so new installs get all of them.
  const selected = rawEvents
    ? rawEvents
        .split(",")
        .map((e) => e.trim())
        .filter(Boolean)
    : ALL_EVENT_KEYS;

  const changedClass = (key: string) =>
    configs[key] !== originalConfigs[key]
      ? "border-warning-300 data-[hover=true]:border-warning-400"
      : "";

  const toggleEvent = (key: string, on: boolean) => {
    const next = on
      ? [...new Set([...selected, key])]
      : selected.filter((e) => e !== key);

    onChange(
      "notify_events",
      next.length === ALL_EVENT_KEYS.length ? "" : next.join(",") || "none",
    );
  };

  const runTest = async () => {
    // F-L6-01 自复扫补遗：每次下发都真实发送一条外部通知，双发即重复
    // 发送；此前只有 testing 渲染状态，补 ref 入口复核。
    if (testInFlightRef.current) return;
    testInFlightRef.current = true;
    setTesting(true);
    try {
      const res = await testNotify({
        telegramToken: configs.notify_telegram_bot_token ?? "",
        telegramChatId: configs.notify_telegram_chat_id ?? "",
        telegramApiBase: configs.notify_telegram_api_base ?? "",
        webhookUrl: configs.notify_webhook_url ?? "",
      });

      if (res.code === 0) {
        toast.success("测试通知已发送，请查收");
      } else {
        toast.error(res.msg || "发送失败");
      }
    } catch {
      toast.error("发送失败");
    } finally {
      testInFlightRef.current = false;
      setTesting(false);
    }
  };

  const input = (key: string, placeholder: string, type = "text") => (
    <Input
      classNames={{ input: "text-sm", inputWrapper: changedClass(key) }}
      placeholder={placeholder}
      size="md"
      type={type}
      value={configs[key] ?? ""}
      variant="bordered"
      onChange={(e) => onChange(key, e.target.value.trim())}
    />
  );

  return (
    <Card className="mt-6 shadow-md">
      <CardHeader className="pb-6">
        <div>
          <h2 className="text-xl font-semibold">通知</h2>
          <p className="text-sm text-foreground">
            节点掉线、导入节点连不上、内核出错、节点快到期时推送到 Telegram 或
            Webhook
          </p>
        </div>
      </CardHeader>
      <Divider />
      <CardBody className="space-y-6 pt-8">
        <Field label="启用通知">
          <div className="flex items-center gap-3">
            <Switch
              aria-label="启用通知"
              isSelected={enabled}
              size="sm"
              onValueChange={(on) =>
                onChange("notify_enabled", on ? "true" : "false")
              }
            />
            <span className="text-sm text-foreground">
              {enabled ? "已启用" : "已禁用"}
            </span>
          </div>
        </Field>

        <Divider />
        <Field
          description="找 @BotFather 创建机器人拿到 Token；给机器人发一条消息后，用 @userinfobot 查自己的 Chat ID（群组 ID 以 -100 开头）"
          label="Telegram Bot Token"
        >
          {input("notify_telegram_bot_token", "123456:ABC-DEF…", "password")}
        </Field>
        <Field label="Telegram Chat ID">
          {input("notify_telegram_chat_id", "例如 123456789")}
        </Field>
        <Field
          description="面板服务器连不上 Telegram 时，填一个反代地址；留空使用官方地址"
          label="Telegram API 地址（可选）"
        >
          {input("notify_telegram_api_base", "https://api.telegram.org")}
        </Field>

        <Divider />
        <Field
          description='以 POST JSON 发送：{"event","title","message","text","time","app"}，可对接企业微信、钉钉、Bark 等的转发服务'
          label="Webhook 地址"
        >
          {input("notify_webhook_url", "https://example.com/hook")}
        </Field>

        <Divider />
        <Field label="通知哪些事件">
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {NOTIFY_EVENTS.map((e) => (
              <Checkbox
                key={e.key}
                isSelected={selected.includes(e.key)}
                onValueChange={(on) => toggleEvent(e.key, on)}
              >
                <div className="flex flex-col">
                  <span className="text-sm">{e.label}</span>
                  <span className="text-xs font-semibold text-foreground">
                    {e.description}
                  </span>
                </div>
              </Checkbox>
            ))}
          </div>
        </Field>

        {selected.includes("latency_bad") && (
          <Field
            description="连续 3 次检测（约 9 分钟）触发提醒；留空使用默认（丢包 20%、延迟不判断）"
            label="延迟 / 丢包阈值"
          >
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="flex items-center gap-2">
                <span className="shrink-0 text-sm text-foreground">丢包率 ≥</span>
                <Input
                  classNames={{
                    input: "text-sm",
                    inputWrapper: changedClass("notify_latency_loss_pct"),
                  }}
                  endContent="%"
                  max={100}
                  min={1}
                  placeholder="20"
                  size="md"
                  type="number"
                  value={configs.notify_latency_loss_pct ?? ""}
                  variant="bordered"
                  onChange={(e) =>
                    onChange("notify_latency_loss_pct", e.target.value)
                  }
                />
              </div>
              <div className="flex items-center gap-2">
                <span className="shrink-0 text-sm text-foreground">延迟 ≥</span>
                <Input
                  classNames={{
                    input: "text-sm",
                    inputWrapper: changedClass("notify_latency_ms"),
                  }}
                  endContent="ms"
                  min={0}
                  placeholder="0"
                  size="md"
                  type="number"
                  value={configs.notify_latency_ms ?? ""}
                  variant="bordered"
                  onChange={(e) =>
                    onChange("notify_latency_ms", e.target.value)
                  }
                />
              </div>
            </div>
          </Field>
        )}

        {selected.includes("daily_report") && (
          <Field
            description="按面板服务器的时区（安装脚本默认 Asia/Shanghai），0–23 点"
            label="每日汇总发送时间"
          >
            <Input
              classNames={{
                input: "text-sm",
                inputWrapper: changedClass("notify_daily_hour"),
              }}
              endContent="点"
              max={23}
              min={0}
              placeholder="9"
              size="md"
              type="number"
              value={configs.notify_daily_hour ?? ""}
              variant="bordered"
              onChange={(e) => onChange("notify_daily_hour", e.target.value)}
            />
          </Field>
        )}

        <div className="flex flex-wrap items-center justify-end gap-3 border-t border-divider/50 pt-6">
          <span className="text-xs font-semibold text-foreground">
            测试会用当前填写的内容，不用先保存
          </span>
          <Button
            color="primary"
            isLoading={testing}
            variant="flat"
            onPress={runTest}
          >
            发送测试
          </Button>
        </div>
      </CardBody>
    </Card>
  );
}
