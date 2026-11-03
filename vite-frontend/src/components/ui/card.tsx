import * as React from "react";

import { cn } from "@/lib/utils";

function Card({
  className,
  style,
  children,
  ...props
}: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "relative flex flex-col transition-[transform,box-shadow] duration-200 ease-refined hover:-translate-y-px hover:shadow-md",
        // A card is ONE matte-glass surface and its content sits directly
        // on the glass — no opaque tray plate under card interiors.
        // Readability comes from the matte stops (floor 0.60) plus
        // deepest-shade foreground colors; solid trays survive only on
        // small elements (pills, chips, badges, primary buttons).
        "glass-card rounded-2xl text-card-foreground shadow-sm",
        className,
      )}
      data-slot="card"
      style={{
        ...style,
      }}
      {...props}
    >
      <div className="flex flex-1 flex-col">{children}</div>
    </div>
  );
}

function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("flex flex-col gap-1.5 p-6", className)}
      data-slot="card-header"
      {...props}
    />
  );
}

function CardTitle({
  className,
  children,
  ...props
}: React.ComponentProps<"h3">) {
  return (
    <h3
      className={cn(
        "text-lg font-semibold leading-none tracking-tight",
        className,
      )}
      data-slot="card-title"
      {...props}
    >
      {children}
    </h3>
  );
}

function CardDescription({ className, ...props }: React.ComponentProps<"p">) {
  return (
    <p
      className={cn("text-sm font-medium text-foreground", className)}
      data-slot="card-description"
      {...props}
    />
  );
}

function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div className={cn("p-6", className)} data-slot="card-content" {...props} />
  );
}

function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("flex items-center p-6 pt-0", className)}
      data-slot="card-footer"
      {...props}
    />
  );
}

export {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
};
