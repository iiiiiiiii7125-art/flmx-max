import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const alertVariants = cva(
  "relative w-full rounded-lg border px-4 py-3 text-sm",
  {
    variants: {
      variant: {
        default: "border-default-200 bg-default-50 text-foreground",
        // Round 4: semantic alerts ride the tint trays (opaque in both
        // themes; the old -50 tints kept their light shade in dark mode
        // and washed the light text out).
        destructive: "border-danger-200 tray-danger dark:border-danger-800",
        success: "border-success-200 tray-success dark:border-success-800",
        warning: "border-warning-200 tray-warning dark:border-warning-800",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

function Alert({
  className,
  variant,
  ...props
}: React.ComponentProps<"div"> & VariantProps<typeof alertVariants>) {
  return (
    <div
      className={cn(alertVariants({ className, variant }))}
      data-slot="alert"
      role="alert"
      {...props}
    />
  );
}

function AlertTitle({
  className,
  children,
  ...props
}: React.ComponentProps<"h5">) {
  return (
    <h5
      className={cn("mb-1 font-medium leading-none tracking-tight", className)}
      data-slot="alert-title"
      {...props}
    >
      {children}
    </h5>
  );
}

function AlertDescription({
  className,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("text-sm opacity-90", className)}
      data-slot="alert-description"
      {...props}
    />
  );
}

export { Alert, AlertDescription, AlertTitle };
