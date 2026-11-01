import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

type ChipColor =
  | "default"
  | "primary"
  | "secondary"
  | "success"
  | "warning"
  | "danger";
type ChipVariant = "solid" | "flat" | "light" | "bordered";
type ChipSize = "sm" | "md" | "lg";

export interface ChipProps extends React.ComponentProps<"span"> {
  color?: ChipColor;
  size?: ChipSize;
  variant?: ChipVariant;
}

function colorVariant(color: ChipColor) {
  if (color === "danger") {
    return "destructive" as const;
  }
  if (color === "success") {
    return "success" as const;
  }
  if (color === "warning") {
    return "warning" as const;
  }
  if (color === "secondary") {
    return "secondary" as const;
  }

  return "default" as const;
}

const solidColorClassMap: Record<ChipColor, string> = {
  default: "border-transparent bg-default-800 text-default-50",
  primary: "border-transparent bg-primary text-primary-foreground",
  secondary: "border-transparent bg-secondary text-secondary-foreground",
  success: "border-transparent bg-success text-white",
  warning: "border-transparent bg-warning text-white",
  danger: "border-transparent bg-danger text-white",
};

export function Chip({
  children,
  className,
  color = "default",
  size = "md",
  variant = "solid",
  ...props
}: ChipProps) {
  return (
    <Badge
      className={cn(
        variant === "solid" ? solidColorClassMap[color] : "",
        variant === "bordered" ? "border-current bg-tray" : "",
        size === "sm" ? "px-2 py-0 text-xs" : "",
        size === "lg" ? "px-3 py-1 text-sm" : "",
        className,
      )}
      variant={colorVariant(color)}
      {...props}
    >
      {children}
    </Badge>
  );
}
