import { Spinner } from "@/shadcn-bridge/heroui/spinner";

interface BaseStateProps {
  message: string;
  className?: string;
}

export const PageLoadingState = ({
  message,
  className = "h-64",
}: BaseStateProps) => {
  return (
    <div className={`flex items-center justify-center ${className}`}>
      <div className="flex items-center gap-3">
        <Spinner size="sm" />
        <span className="text-default-600">{message}</span>
      </div>
    </div>
  );
};

export const PageEmptyState = ({
  message,
  className = "h-48",
}: BaseStateProps) => {
  return (
    <div className={`flex items-center justify-center ${className}`}>
      <span className="text-foreground">{message}</span>
    </div>
  );
};

export const PageErrorState = ({
  message,
  className = "h-48",
}: BaseStateProps) => {
  return (
    <div className={`flex items-center justify-center ${className}`}>
      <span className="text-danger-700">{message}</span>
    </div>
  );
};
