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
  default:
    "bg-default-200/70 text-default-800 shadow-none hover:bg-default-200",
  success: "bg-success text-white shadow-sm hover:bg-success/90",
  warning: "bg-warning text-white shadow-sm hover:bg-warning/90",
  primary:
    "bg-gradient-to-r from-primary-500 to-secondary-500 text-white shadow-sm hover:from-primary-600 hover:to-secondary-600",
  secondary:
    "bg-secondary text-secondary-foreground shadow-sm hover:bg-secondary/90",
  danger: "bg-danger text-white shadow-sm hover:bg-danger/90",
};

const borderedColorClassMap: Record<HeroButtonColor, string> = {
  default:
    "border-default-300 text-default-700 hover:bg-default-100 dark:border-default-200 dark:text-default-600 dark:hover:bg-default-100",
  primary:
    "border-primary/50 text-primary hover:bg-primary/5 dark:text-primary-300 dark:hover:bg-primary/10",
  secondary:
    "border-secondary/50 text-secondary hover:bg-secondary/5 dark:text-secondary-300 dark:hover:bg-secondary/10",
  success:
    "border-success/50 text-success hover:bg-success/5 dark:text-success-300 dark:hover:bg-success/10",
  warning:
    "border-warning/50 text-warning-700 hover:bg-warning/5 dark:text-warning-300 dark:hover:bg-warning/10",
  danger:
    "border-danger/50 text-danger hover:bg-danger/5 dark:text-danger-300 dark:hover:bg-danger/10",
};

const lightColorClassMap: Record<HeroButtonColor, string> = {
  default: "text-default-700 hover:bg-default-100 dark:text-default-600",
  primary:
    "text-primary hover:bg-primary/10 dark:text-primary-300 dark:hover:bg-primary/15",
  secondary:
    "text-secondary hover:bg-secondary/10 dark:text-secondary-300 dark:hover:bg-secondary/15",
  success:
    "text-success hover:bg-success/10 dark:text-success-300 dark:hover:bg-success/15",
  warning:
    "text-warning-700 hover:bg-warning/10 dark:text-warning-300 dark:hover:bg-warning/15",
  danger:
    "text-danger hover:bg-danger/10 dark:text-danger-300 dark:hover:bg-danger/15",
};

const flatColorClassMap: Record<HeroButtonColor, string> = {
  default: "bg-default-100 text-default-700 hover:bg-default-200",
  primary:
    "bg-primary/10 text-primary-700 hover:bg-primary/15 dark:bg-primary/15 dark:text-primary-300 dark:hover:bg-primary/20",
  secondary:
    "bg-secondary/10 text-secondary-700 hover:bg-secondary/15 dark:bg-secondary/15 dark:text-secondary-300 dark:hover:bg-secondary/20",
  success:
    "bg-success/10 text-success-700 hover:bg-success/15 dark:bg-success/15 dark:text-success-300 dark:hover:bg-success/20",
  warning:
    "bg-warning/10 text-warning-700 hover:bg-warning/15 dark:bg-warning/15 dark:text-warning-300 dark:hover:bg-warning/20",
  danger:
    "bg-danger/10 text-danger-700 hover:bg-danger/15 dark:bg-danger/15 dark:text-danger-300 dark:hover:bg-danger/20",
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
