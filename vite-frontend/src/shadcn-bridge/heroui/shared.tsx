import * as React from "react";

import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

export interface FieldMetaProps {
  label?: React.ReactNode;
  description?: React.ReactNode;
  errorMessage?: React.ReactNode;
  isInvalid?: boolean;
  isRequired?: boolean;
}

interface FieldContainerProps extends FieldMetaProps {
  className?: string;
  id?: string;
  children: React.ReactNode;
}

export function FieldContainer({
  children,
  className,
  description,
  errorMessage,
  id,
  isInvalid,
  isRequired,
  label,
}: FieldContainerProps) {
  return (
    <div className={cn("w-full space-y-1.5", className)}>
      {label ? (
        <Label htmlFor={id}>
          {label}
          {isRequired ? <span className="ml-1 text-danger-700">*</span> : null}
        </Label>
      ) : null}
      {children}
      {description ? (
        <p className="text-xs font-semibold text-foreground">{description}</p>
      ) : null}
      {isInvalid && errorMessage ? (
        <p className="text-xs font-semibold text-danger-700">{errorMessage}</p>
      ) : null}
    </div>
  );
}

export function extractText(content: React.ReactNode): string {
  if (
    content === null ||
    content === undefined ||
    typeof content === "boolean"
  ) {
    return "";
  }
  if (typeof content === "string" || typeof content === "number") {
    return String(content);
  }
  if (Array.isArray(content)) {
    return content
      .map((item) => extractText(item))
      .join("")
      .trim();
  }
  if (React.isValidElement(content)) {
    return extractText(content.props.children);
  }

  return "";
}
