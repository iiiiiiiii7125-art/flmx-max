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

// Flat solid buttons (2026-11-03 flat-surface directive): opaque fills,
// 1px borders where the variant calls for one, no gradient, no blur, no
// shadow. Semantic solids rest on the -700/-800 shades so white labels
// keep >= 6.9 contrast; bordered and flat defaults ride solid white /
// the lightest neutral fill with the deepest label shade.
const solidColorClassMap: Partial<Record<HeroButtonColor, string>> = {
  default: "bg-white border border-divider hover:bg-default-50 text-default-800",
  success: "bg-success-800 text-white hover:bg-success-700",
  warning: "bg-warning-800 text-white hover:bg-warning-700",
  primary: "bg-primary-700 text-white hover:bg-primary-800",
  secondary: "bg-secondary-700 text-white hover:bg-secondary-800",
  danger: "bg-danger-800 text-white hover:bg-danger-700",
};

const borderedColorClassMap: Record<HeroButtonColor, string> = {
  default:
    "border-default-300 bg-white text-default-800 hover:bg-default-50",
  primary:
    "border-primary/50 bg-white text-primary-800 hover:bg-primary-50",
  secondary:
    "border-secondary/50 bg-white text-secondary-800 hover:bg-secondary-50",
  success:
    "border-success/50 bg-white text-success-800 hover:bg-success-50",
  warning:
    "border-warning/50 bg-white text-warning-800 hover:bg-warning-50",
  danger:
    "border-danger/50 bg-white text-danger-800 hover:bg-danger-50",
};

// Light buttons ride the tinted solid trays (tray tokens pair each tint
// with its deepest label shade, >= 8 measured). Button scale is chip
// scale, not a board.
const lightColorClassMap: Record<HeroButtonColor, string> = {
  default: "tray-default",
  primary: "tray-primary",
  secondary: "tray-secondary",
  success: "tray-success",
  warning: "tray-warning",
  danger: "tray-danger",
};

const flatColorClassMap: Record<HeroButtonColor, string> = {
  default: "bg-default-100 border border-divider hover:bg-default-200 text-default-800",
  primary: "tray-primary",
  secondary: "tray-secondary",
  success: "tray-success",
  warning: "tray-warning",
  danger: "tray-danger",
};

// The "shadow" variant name is kept for API compatibility; under the
// flat-surface directive it paints no shadow and matches solid.
const shadowColorClassMap: Record<HeroButtonColor, string> = {
  default: "",
  primary: "",
  secondary: "",
  success: "",
  warning: "",
  danger: "",
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
