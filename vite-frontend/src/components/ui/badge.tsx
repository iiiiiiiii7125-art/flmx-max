import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-medium transition-colors duration-150 ease-refined",
  {
    variants: {
      variant: {
        default:
          "border-transparent bg-primary/10 text-primary-700 dark:text-primary-300",
        secondary:
          "border-transparent bg-secondary/10 text-secondary-700 dark:text-secondary-300",
        destructive:
          "border-transparent bg-danger/10 text-danger-700 dark:text-danger-300",
        outline: "border-default-300 text-foreground",
        success:
          "border-transparent bg-success/10 text-success-700 dark:text-success-300",
        warning:
          "border-transparent bg-warning/10 text-warning-700 dark:text-warning-300",
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
