import type { Components } from "react-markdown";

import ReactMarkdown from "react-markdown";
import rehypeSanitize from "rehype-sanitize";
import remarkGfm from "remark-gfm";

// 公告双渲染管线（横幅 / 进站弹窗）共用同一份插件链与链接行为（D5-10）：
// 此前弹窗管线缺 rehypeSanitize、链接同页跳转，与编辑侧「链接会在新
// 标签页打开」的文案不符（D5-09）。两处必须从这里取配置，禁止各自
// 再写一份插件链——将来引入 rehype-raw 或放宽 urlTransform 时两处同进退。
export const announcementRemarkPlugins = [remarkGfm];
export const announcementRehypePlugins = [rehypeSanitize];

// 默认链接组件：新标签页打开（与 config 页编辑侧文案一致）
export const announcementDefaultComponents: Components = {
  a: ({ children, href }) => (
    <a href={href} rel="noopener noreferrer" target="_blank">
      {children}
    </a>
  ),
};

export function AnnouncementMarkdown({
  content,
  components,
}: {
  content: string;
  components?: Components;
}) {
  return (
    <ReactMarkdown
      components={{ ...announcementDefaultComponents, ...components }}
      rehypePlugins={announcementRehypePlugins}
      remarkPlugins={announcementRemarkPlugins}
    >
      {content}
    </ReactMarkdown>
  );
}
