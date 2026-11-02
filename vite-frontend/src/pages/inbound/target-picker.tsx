import type { InboundApiItem } from "@/api/types";

import { useState } from "react";
import {
  Ban,
  Check,
  ChevronLeft,
  ChevronRight,
  Search,
  Waypoints,
} from "lucide-react";

import {
  Modal,
  ModalBody,
  ModalContent,
  ModalHeader,
} from "@/shadcn-bridge/heroui/modal";

/** Where traffic goes: "direct", "block" or an inbound id. */
export type TargetKey = string;

const SEARCH_FROM = 8;

export function targetSource(i: InboundApiItem): string {
  if (!i.external) return i.nodeName || "节点";

  return i.sourceName ? `订阅源 · ${i.sourceName}` : "导入";
}

function TargetIcon({ k }: { k: TargetKey }) {
  if (k === "block") return <Ban className="h-4 w-4 text-danger-700" />;
  if (k === "direct") return <Waypoints className="h-4 w-4 text-success-700" />;

  return null;
}

/** The current choice, shown as a tappable row. */
export function TargetButton({
  value,
  targets,
  missing,
  onPress,
}: {
  value: TargetKey;
  targets: InboundApiItem[];
  missing?: boolean;
  onPress: () => void;
}) {
  const t = targets.find((i) => String(i.id) === value);
  const title =
    value === "direct"
      ? "直连"
      : value === "block"
        ? "阻断"
        : missing
          ? "已删除的节点"
          : (t?.name ?? "选择出站节点");

  return (
    <button
      className={`flex w-full items-center gap-2.5 rounded-xl border px-3 py-2.5 text-left transition-colors active:bg-default-100 ${
        missing ? "border-danger/50 panel-card" : "panel-card"
      }`}
      type="button"
      onClick={onPress}
    >
      <TargetIcon k={value} />
      <span className="min-w-0 flex-1">
        <span
          className={`block truncate text-sm ${
            !value ? "text-foreground" : missing ? "text-danger-700" : ""
          }`}
        >
          {title}
        </span>
        {t && (
          <span className="block truncate text-xs font-semibold text-foreground">
            {targetSource(t)}
          </span>
        )}
      </span>
      <ChevronRight className="h-4 w-4 shrink-0 text-foreground" />
    </button>
  );
}

/** A list of exits to pick from, opened over the 分流 dialog. */
export function TargetPicker({
  isOpen,
  title,
  value,
  targets,
  withBlock,
  onPick,
  onClose,
}: {
  isOpen: boolean;
  title: string;
  value: TargetKey;
  targets: InboundApiItem[];
  withBlock?: boolean;
  onPick: (k: TargetKey) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const q = query.trim().toLowerCase();
  const shown = q
    ? targets.filter((t) =>
        `${t.name} ${targetSource(t)}`.toLowerCase().includes(q),
      )
    : targets;
  const groups = [...new Set(shown.map(targetSource))];

  const close = () => {
    setQuery("");
    onClose();
  };

  const row = (k: TargetKey, name: string, sub?: string) => (
    <button
      key={k}
      className="flex w-full items-center gap-2.5 rounded-xl px-3 py-2.5 text-left hover:bg-default-100 active:bg-default-100"
      type="button"
      onClick={() => {
        onPick(k);
        close();
      }}
    >
      <TargetIcon k={k} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm">{name}</span>
        {sub && (
          <span className="block truncate text-xs font-semibold text-foreground">{sub}</span>
        )}
      </span>
      {value === k && <Check className="h-4 w-4 shrink-0 text-primary-700" />}
    </button>
  );

  return (
    <Modal
      classNames={{
        base: "!w-[calc(100%-32px)] !mx-auto sm:!w-full rounded-2xl overflow-hidden",
      }}
      isOpen={isOpen}
      placement="center"
      scrollBehavior="inside"
      size="md"
      onOpenChange={(open) => !open && close()}
    >
      <ModalContent>
        {/* The list can outgrow a phone screen: keep a way back and the
            search box above it, not inside the scrolled body. */}
        <ModalHeader className="flex items-center gap-2">
          <button
            aria-label="返回"
            className="rounded-lg p-1 text-foreground hover:bg-default-100"
            type="button"
            onClick={close}
          >
            <ChevronLeft className="h-5 w-5" />
          </button>
          <h2 className="text-lg font-bold">{title}</h2>
        </ModalHeader>
        {targets.length >= SEARCH_FROM && (
          <label className="flex shrink-0 items-center gap-2 rounded-xl border border-divider bg-content1 px-3 py-2">
            <Search className="h-4 w-4 text-foreground" />
            <input
              className="min-w-0 flex-1 bg-transparent text-sm outline-none"
              placeholder="搜索节点"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </label>
        )}
        <ModalBody className="space-y-1 pb-4">
          {!q && (
            <div className="rounded-xl panel-inset">
              {row("direct", "直连", "从本节点出去")}
              {withBlock && row("block", "阻断", "丢弃匹配的流量")}
            </div>
          )}
          {groups.map((g) => (
            <div key={g} className="pt-2">
              <p className="px-3 pb-1 text-xs font-semibold text-foreground">{g}</p>
              <div className="rounded-xl panel-inset">
                {shown
                  .filter((t) => targetSource(t) === g)
                  .map((t) => row(String(t.id), t.name))}
              </div>
            </div>
          ))}
          {q && shown.length === 0 && (
            <p className="py-6 text-center text-sm text-foreground">
              没有匹配的节点
            </p>
          )}
        </ModalBody>
      </ModalContent>
    </Modal>
  );
}
