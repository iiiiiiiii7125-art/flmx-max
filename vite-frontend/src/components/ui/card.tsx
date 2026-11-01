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
        // Round 4 "壳透字托" put one opaque tray plate under the whole
        // card interior; Round 5 segments it: the shell stays clear
        // glass and the tray moves down to the section components
        // (CardHeader / CardContent / CardFooter below), so a card
        // reads as a glass frame holding separate text plates with
        // transparent gaps — never one solid board edge to edge.
        "glass-card rounded-2xl p-2 text-card-foreground shadow-sm",
        className,
      )}
      data-slot="card"
      style={{
        ...style,
      }}
      {...props}
    >
      <div className="flex flex-1 flex-col gap-2">{children}</div>
    </div>
  );
}

function CardHeader({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex flex-col gap-1.5 rounded-[14px] bg-tray p-6",
        className,
      )}
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
      className={cn("text-sm text-default-600", className)}
      data-slot="card-description"
      {...props}
    />
  );
}

function CardContent({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("rounded-[14px] bg-tray p-6", className)}
      data-slot="card-content"
      {...props}
    />
  );
}

function CardFooter({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn("flex items-center rounded-[14px] bg-tray p-6", className)}
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
