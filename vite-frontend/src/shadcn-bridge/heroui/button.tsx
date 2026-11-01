import * as React from "react";
import { Loader2Icon } from "lucide-react";

import { Button as BaseButton } from "@/components/ui/button";
import { cn } from "@/lib/utils";

type HeroButtonColor =
  | "default"
  | "primary"
  | "secondary"
  | "success"
  | "warning"
  | "danger";
type HeroButtonVariant =
  | "solid"
  | "light"
  | "flat"
  | "ghost"
  | "bordered"
  | "shadow";
type HeroButtonSize = "sm" | "md" | "lg";

export interface ButtonProps
  extends Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "color"> {
  color?: HeroButtonColor;
  endContent?: React.ReactNode;
  isIconOnly?: boolean;
  isLoading?: boolean;
  onPress?: (event: React.MouseEvent<HTMLButtonElement>) => void;
  size?: HeroButtonSize;
  startContent?: React.ReactNode;
  variant?: HeroButtonVariant;
}

function mapVariant(
  color: HeroButtonColor,
  variant: HeroButtonVariant,
):
  | "default"
  | "destructive"
  | "secondary"
  | "outline"
  | "ghost"
  | "light"
  | "flat" {
  if (variant === "bordered") {
    return "outline";
  }
  if (variant === "ghost") {
    return "ghost";
  }
  if (variant === "light") {
    return "light";
  }
  if (variant === "flat") {
    return "flat";
  }
  if (color === "danger") {
    return "destructive";
  }
  if (color === "secondary") {
    return "secondary";
  }

  return "default";
}

function mapSize(
  size: HeroButtonSize,
  isIconOnly: boolean,
): "default" | "sm" | "lg" | "icon" {
  if (isIconOnly) {
    return "icon";
  }
  if (size === "sm") {
    return "sm";
  }
  if (size === "lg") {
    return "lg";
  }

  return "default";
}

const solidColorClassMap: Partial<Record<HeroButtonColor, string>> = {
  default: "bg-white/30 backdrop-blur-[3px] border border-white/50 shadow-sm hover:bg-white/45 text-default-800",
  // Round 6: bright success/warning/danger solids pair with white at
  // only ≈2.2/2.0/4.1 — rest on the -800 shades (≈7.1/7.1/6.9) and
  // hover one shade up. The default rests on the glass wash instead:
  // the old opaque 200-stop slab read as a pale board on the wallpaper.
  success: "bg-success-800 text-white shadow-sm hover:bg-success-700",
  warning: "bg-warning-800 text-white shadow-sm hover:bg-warning-700",
  primary:
    "bg-gradient-to-r from-primary-700 to-secondary-700 text-white shadow-sm hover:from-primary-800 hover:to-secondary-800",
  secondary:
    "bg-secondary text-secondary-foreground shadow-sm hover:bg-secondary/90",
  danger: "bg-danger-800 text-white shadow-sm hover:bg-danger-700",
};

// Round 6: bordered labels sat directly on glass at -700, which the
// rendered sampling put at ~2.5 over the wallpaper's dark streak. They
// keep their outline identity but gain a white/25 blurred carrier and
// the -800 label shade; hover lifts the carrier, not a colour wash.
const borderedColorClassMap: Record<HeroButtonColor, string> = {
  default:
    "border-default-300 bg-white/25 backdrop-blur-[3px] text-default-800 hover:bg-white/40",
  primary:
    "border-primary/50 bg-white/25 backdrop-blur-[3px] text-primary-800 hover:bg-white/40",
  secondary:
    "border-secondary/50 bg-white/25 backdrop-blur-[3px] text-secondary-800 hover:bg-white/40",
  success:
    "border-success/50 bg-white/25 backdrop-blur-[3px] text-success-800 hover:bg-white/40",
  warning:
    "border-warning/50 bg-white/25 backdrop-blur-[3px] text-warning-800 hover:bg-white/40",
  danger:
    "border-danger/50 bg-white/25 backdrop-blur-[3px] text-danger-800 hover:bg-white/40",
};

// Round 6 final verdict on light buttons: the glass-wash carrier was
// tried first, but rendered sampling over the wallpaper's dark streak
// put wash + semantic-label pairs at 2.2-4.4 local contrast — under the
// floor. Light buttons therefore take the user-sanctioned tray fallback
// (tinted solid tray + deepest label, >= 8 measured): the same tray
// tokens as flat. Button scale is chip scale, not a board. The flat and
// solid defaults keep the glass wash (near-black label, verified).
const lightColorClassMap: Record<HeroButtonColor, string> = {
  default: "tray-default shadow-sm",
  primary: "tray-primary shadow-sm",
  secondary: "tray-secondary shadow-sm",
  success: "tray-success shadow-sm",
  warning: "tray-warning shadow-sm",
  danger: "tray-danger shadow-sm",
};

const flatColorClassMap: Record<HeroButtonColor, string> = {
  default: "bg-white/30 backdrop-blur-[3px] border border-white/50 shadow-sm hover:bg-white/45 text-default-800",
  primary: "tray-primary",
  secondary: "tray-secondary",
  success: "tray-success",
  warning: "tray-warning",
  danger: "tray-danger",
};

const shadowColorClassMap: Record<HeroButtonColor, string> = {
  default: "shadow-md",
  primary: "shadow-md",
  secondary: "shadow-md",
  success: "shadow-md",
  warning: "shadow-md",
  danger: "shadow-md",
};

function mapColorClass(
  color: HeroButtonColor,
  variant: HeroButtonVariant,
): string {
  if (variant === "bordered") {
    return borderedColorClassMap[color];
  }
  if (variant === "light") {
    return lightColorClassMap[color];
  }
  if (variant === "flat") {
    return flatColorClassMap[color];
  }
  if (variant === "solid" || variant === "shadow") {
    return solidColorClassMap[color] ?? "";
  }

  return "";
}

function mapShadowClass(
  color: HeroButtonColor,
  variant: HeroButtonVariant,
): string {
  if (variant !== "shadow") {
    return "";
  }

  return shadowColorClassMap[color];
}

export const Button = React.forwardRef<
  HTMLButtonElement,
  ButtonProps & {
    isDisabled?: boolean;
  }
>(
  (
    {
      children,
      className,
      color = "default",
      disabled,
      endContent,
      isIconOnly = false,
      isLoading = false,
      isDisabled,
      onClick,
      onPress,
      size = "md",
      startContent,
      type = "button",
      variant = "solid",
      ...props
    },
    ref,
  ) => {
    const resolvedVariant = mapVariant(color, variant);
    const resolvedSize = mapSize(size, isIconOnly);
    const resolvedDisabled = Boolean(disabled || isDisabled || isLoading);
    const resolvedColorClass = mapColorClass(color, variant);
    const resolvedShadowClass = mapShadowClass(color, variant);

    const handleClick = (event: React.MouseEvent<HTMLButtonElement>) => {
      onClick?.(event);
      onPress?.(event);
    };

    return (
      <BaseButton
        ref={ref}
        className={cn(
          isIconOnly ? "p-0" : "",
          resolvedColorClass,
          resolvedShadowClass,
          className,
        )}
        disabled={resolvedDisabled}
        size={resolvedSize}
        type={type}
        variant={resolvedVariant}
        onClick={handleClick}
        {...props}
      >
        {isLoading ? (
          <Loader2Icon className="mr-2 h-4 w-4 animate-spin" />
        ) : null}
        {startContent}
        {isIconOnly ? null : children}
        {isIconOnly ? children : null}
        {endContent}
      </BaseButton>
    );
  },
);

Button.displayName = "HeroBridgeButton";
