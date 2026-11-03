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
  // R2-S1-02: the default solid used to rest on a translucent white
  // wash — over a dark wallpaper region its label fell to ~1.9, and
  // the wash family was never in the contrast gate. It now rests on
  // the opaque -800 neutral like every other solid rests on its -800
  // shade (white label, gate-pinned at 14.89), hovering one shade up.
  default: "bg-default-800 text-white shadow-sm hover:bg-default-700",
  // Round 6: bright success/warning/danger solids pair with white at
  // only ≈2.2/2.0/4.1 — rest on the -800 shades (≈7.1/7.1/6.9) and
  // hover one shade up.
  success: "bg-success-800 text-white shadow-sm hover:bg-success-700",
  warning: "bg-warning-800 text-white shadow-sm hover:bg-warning-700",
  primary:
    "bg-gradient-to-r from-primary-700 to-secondary-700 text-white shadow-sm hover:from-primary-800 hover:to-secondary-800",
  secondary:
    "bg-secondary text-secondary-foreground shadow-sm hover:bg-secondary/90",
  danger: "bg-danger-800 text-white shadow-sm hover:bg-danger-700",
};

// R2-S1-02: bordered labels used to ride a translucent white wash
// carrier that was never in the contrast gate — stacked over the
// card floor stop on a dark wallpaper region, four of six labels fell
// under the 4.5 floor, and over the bare page background all six did.
// They keep their outline identity (the coloured border) but the
// carrier is now the family's opaque tray — the same tray tokens as
// flat/light, hover included (tray pairs are gate-pinned, static and
// hover, >= 7). The tray class supplies the label colour; no text-*
// shade is set here.
const borderedColorClassMap: Record<HeroButtonColor, string> = {
  default: "border-default-300 tray-default",
  primary: "border-primary/50 tray-primary",
  secondary: "border-secondary/50 tray-secondary",
  success: "border-success/50 tray-success",
  warning: "border-warning/50 tray-warning",
  danger: "border-danger/50 tray-danger",
};

// Round 6 final verdict on light buttons: the glass-wash carrier was
// tried first, but rendered sampling over the wallpaper's dark streak
// put wash + semantic-label pairs at 2.2-4.4 local contrast — under the
// floor. Light buttons therefore take the user-sanctioned tray fallback
// (tinted solid tray + deepest label, >= 8 measured): the same tray
// tokens as flat. Button scale is chip scale, not a board. (The flat
// and solid defaults later left the wash family too — R2-S1-02.)
const lightColorClassMap: Record<HeroButtonColor, string> = {
  default: "tray-default shadow-sm",
  primary: "tray-primary shadow-sm",
  secondary: "tray-secondary shadow-sm",
  success: "tray-success shadow-sm",
  warning: "tray-warning shadow-sm",
  danger: "tray-danger shadow-sm",
};

const flatColorClassMap: Record<HeroButtonColor, string> = {
  // R2-S1-02: the default flat was the last wash carrier in this map —
  // it takes tray-default like every other flat colour takes its tray
  // (this is also the PageHeader secondary slot's carrier, which sits
  // directly on the page background, where the wash read ~1.9).
  default: "tray-default",
  primary: "tray-primary",
  secondary: "tray-secondary",
  success: "tray-success",
  warning: "tray-warning",
  danger: "tray-danger",
};

// The "shadow" variant adds the soft shadow-md layer on top of the
// solid colour map (mapColorClass falls through to solid for it).
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
