import { tv } from "tailwind-variants";

export const title = tv({
  base: "tracking-tight inline font-semibold",
  variants: {
    color: {
      violet: "text-secondary-700",
      yellow: "text-warning-700",
      blue: "text-primary-700",
      cyan: "text-sky-700",
      green: "text-success-700",
      pink: "text-pink-700",
      foreground: "text-foreground",
    },
    size: {
      sm: "text-3xl lg:text-4xl",
      md: "text-[2.3rem] lg:text-5xl leading-9",
      lg: "text-4xl lg:text-6xl",
    },
    fullWidth: {
      true: "w-full block",
    },
  },
  defaultVariants: {
    size: "md",
  },
});
