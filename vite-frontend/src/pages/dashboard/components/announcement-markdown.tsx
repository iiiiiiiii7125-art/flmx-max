import type { Components } from "react-markdown";

import { lazy, Suspense } from "react";

// 公告 Markdown 渲染器懒加载：react-markdown 全链（解析器 + remark/rehype
// 插件）体积可观，而公告只在平台真有公告时才需要渲染。渲染实现拆到
// announcement-markdown-impl.tsx 独立 chunk，本包装层只做 lazy + Suspense
// 兜底——横幅/弹窗仍从这里导入 AnnouncementMarkdown，插件链仍只有 impl
// 里一份，两处同进退的纪律不变（D5-10）。加载完成前先以纯文本呈现内容，
// 避免公告位闪空。
const AnnouncementMarkdownImpl = lazy(() =>
  import("./announcement-markdown-impl").then((module) => ({
    default: module.AnnouncementMarkdown,
  })),
);

export function AnnouncementMarkdown({
  content,
  components,
}: {
  content: string;
  components?: Components;
}) {
  return (
    <Suspense
      fallback={<span className="whitespace-pre-wrap">{content}</span>}
    >
      <AnnouncementMarkdownImpl components={components} content={content} />
    </Suspense>
  );
}
