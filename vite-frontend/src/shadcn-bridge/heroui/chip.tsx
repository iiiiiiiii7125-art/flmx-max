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

// Round 6: the bright success/warning solids measured only ≈2.2/2.0
// against white text — the -800 shades keep the solid-chip identity and
// pair with white at ≈7.1 (target tier). Danger stays on the saturated
// carrier (white ≈4.1, the sanctioned solid family).
const solidColorClassMap: Record<ChipColor, string> = {
  default: "border-transparent bg-default-800 text-default-50",
  primary: "border-transparent bg-primary text-primary-foreground",
  secondary: "border-transparent bg-secondary text-secondary-foreground",
  success: "border-transparent bg-success-800 text-white",
  warning: "border-transparent bg-warning-800 text-white",
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
