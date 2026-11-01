import type { ReactNode } from "react";

import { Card } from "@/shadcn-bridge/heroui/card";

interface MetricCardProps {
  title: string;
  value: string | number;
  iconClassName: string;
  icon: ReactNode;
  bottomContent?: ReactNode;
}

export const MetricCard = ({
  title,
  value,
  iconClassName,
  icon,
  bottomContent,
}: MetricCardProps) => {
  return (
    <Card className="h-48 flex flex-col overflow-hidden">
      {/* Round 5: the glass shell holds segmented trays (headline plate,
          bottom plate) with transparent gaps — no full-card plate. */}
      <div className="flex h-full flex-col gap-2">
        <div className="rounded-[14px] bg-tray px-4 pb-3.5 pt-4 md:px-5">
          <div className="flex items-center justify-between">
            <p className="text-sm font-medium text-default-600 truncate">
              {title}
            </p>
            <div className={`p-2 rounded-xl flex-shrink-0 ${iconClassName}`}>
              {icon}
            </div>
          </div>
          <p className="text-2xl lg:text-3xl font-bold text-foreground truncate mt-2">
            {value}
          </p>
        </div>
        {bottomContent && (
          <div className="mt-auto rounded-[14px] bg-tray px-4 py-3 md:px-5">
            {bottomContent}
          </div>
        )}
      </div>
    </Card>
  );
};
