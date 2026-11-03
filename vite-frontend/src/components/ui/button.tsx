import * as React from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";

import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center whitespace-nowrap rounded-lg text-sm font-medium transition-colors duration-200 ease-refined focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        destructive: "bg-danger-800 text-white hover:bg-danger-700",
        outline:
          "border border-input bg-content1 hover:bg-white/40 hover:text-foreground",
        secondary:
          "bg-secondary text-secondary-foreground hover:bg-secondary/80",
        // btn-ghost is a marker only: it carries no styles of its own.
        // In glass / wallpaper contexts globals.css gives it the opaque
        // tray-default carrier (see GLASS-TEXT-OVERRIDES there); outside
        // those contexts the variant renders exactly as before —
        // transparent at rest, white/40 wash on hover.
        ghost: "btn-ghost hover:bg-white/40 hover:text-foreground",
        link: "text-primary-700 underline-offset-4 hover:underline",
        // flat/light carry no background here: the bridge colour maps own
        // the (Round 4 tray) background so text never lands on glass.
        light: "",
        flat: "",
      },
      size: {
        default: "h-9 px-4 py-2",
        sm: "h-8 rounded-lg px-3 text-xs font-semibold",
        lg: "h-10 rounded-lg px-8",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

type ButtonProps = React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
  };

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ asChild = false, className, size, variant, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";

    return (
      <Comp
        ref={ref}
        className={cn(buttonVariants({ className, size, variant }))}
        data-slot="button"
        {...props}
      />
    );
  },
);

Button.displayName = "Button";

export { Button, buttonVariants };
