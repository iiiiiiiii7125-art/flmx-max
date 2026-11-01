import type { AnnouncementData } from "@/api";

import { AnnouncementMarkdown } from "./announcement-markdown";

import { Card, CardBody } from "@/shadcn-bridge/heroui/card";

interface AnnouncementBannerProps {
  announcement: AnnouncementData;
}

export const AnnouncementBanner = ({
  announcement,
}: AnnouncementBannerProps) => {
  if (!announcement.content) {
    return null;
  }

  return (
    <Card className="mb-4 lg:mb-6 border-primary/20 shadow-sm [&>div]:bg-primary-500/10">
      <CardBody className="p-4 sm:p-5">
        <div className="flex items-center gap-3.5 sm:gap-4">
          <div className="w-10 h-10 sm:w-11 sm:h-11 bg-white dark:bg-primary-900 rounded-full flex-shrink-0 flex items-center justify-center shadow-sm border border-primary/20 dark:border-primary/25">
            <svg
              aria-hidden="true"
              className="w-5 h-5 sm:w-5 sm:h-5 text-primary-500 dark:text-primary-400"
              fill="currentColor"
              viewBox="0 0 20 20"
            >
              <path
                clipRule="evenodd"
                d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a1 1 0 000 2v3a1 1 0 001 1h1a1 1 0 100-2v-3a1 1 0 00-1-1H9z"
                fillRule="evenodd"
              />
            </svg>
          </div>
          <div className="flex-1 min-w-0">
            <h3 className="text-[15px] font-semibold text-blue-900 dark:text-blue-100 tracking-wide mb-1 sm:mb-1.5 mt-1.5">
              公告
            </h3>
            <div className="text-sm text-blue-800 dark:text-blue-100 break-words leading-relaxed">
              <AnnouncementMarkdown
                components={{
                  p: ({ children }) => (
                    <p className="mb-2 last:mb-0">{children}</p>
                  ),
                  a: ({ children, href }) => (
                    <a
                      className="underline decoration-primary-500/70 underline-offset-2 hover:text-primary-700 dark:hover:text-primary-100"
                      href={href}
                      rel="noopener noreferrer"
                      target="_blank"
                    >
                      {children}
                    </a>
                  ),
                  ul: ({ children }) => (
                    <ul className="list-disc pl-5 space-y-1 mb-2 last:mb-0">
                      {children}
                    </ul>
                  ),
                  ol: ({ children }) => (
                    <ol className="list-decimal pl-5 space-y-1 mb-2 last:mb-0">
                      {children}
                    </ol>
                  ),
                  code: ({ children }) => (
                    <code className="font-mono rounded bg-blue-100 dark:bg-blue-900 px-1 py-0.5 text-[0.92em]">
                      {children}
                    </code>
                  ),
                  pre: ({ children }) => (
                    <pre className="mb-2 font-mono overflow-x-auto rounded-md bg-blue-100 dark:bg-blue-900 p-2.5 text-xs leading-relaxed">
                      {children}
                    </pre>
                  ),
                  blockquote: ({ children }) => (
                    <blockquote className="mb-2 border-l-2 border-primary-300/80 dark:border-primary-500/60 pl-3 italic">
                      {children}
                    </blockquote>
                  ),
                }}
                content={announcement.content}
              />
            </div>
          </div>
        </div>
      </CardBody>
    </Card>
  );
};
