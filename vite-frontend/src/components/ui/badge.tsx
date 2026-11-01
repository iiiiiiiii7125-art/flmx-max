import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors duration-150 ease-refined",
  {
    variants: {
      variant: {
        // Round 4: tint trays (opaque semantic plates), not /10 washes —
        // badges carry status text and must read on any surface.
        default: "border-transparent tray-primary",
        secondary: "border-transparent tray-secondary",
        destructive: "border-transparent tray-danger",
        outline: "border-default-300 bg-tray text-foreground",
        success: "border-transparent tray-success",
        warning: "border-transparent tray-warning",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

function Badge({
  className,
  variant,
  ...props
}: React.ComponentProps<"span"> & VariantProps<typeof badgeVariants>) {
  return (
    <span
      className={cn(badgeVariants({ className, variant }))}
      data-slot="badge"
      {...props}
    />
  );
}

export { Badge, badgeVariants };
