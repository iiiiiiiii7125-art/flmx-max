import type { ReactNode } from "react";

interface MetricCardProps {
  title: string;
  value: string | number;
  iconClassName: string;
  icon: ReactNode;
  bottomContent?: ReactNode;
}

/**
 * One cell of the dashboard headline strip: the four figures share a
 * single bordered block separated by hairlines (the parent grid paints
 * the divider lines via gap-px), instead of four free-standing cards.
 */
export const MetricCard = ({
  title,
  value,
  iconClassName,
  icon,
  bottomContent,
}: MetricCardProps) => {
  return (
    <div className="flex flex-col justify-between gap-3 glass-card p-4 lg:p-5">
      <div className="flex items-center justify-between">
        <p className="truncate text-sm font-medium text-foreground">
          {title}
        </p>
        <div className={`flex-shrink-0 rounded-lg p-1.5 ${iconClassName}`}>
          {icon}
        </div>
      </div>
      <p className="truncate text-2xl font-bold text-foreground lg:text-3xl">
        {value}
      </p>
      {bottomContent}
    </div>
  );
};
