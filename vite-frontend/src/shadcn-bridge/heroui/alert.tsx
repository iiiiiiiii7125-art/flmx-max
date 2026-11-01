import * as React from "react";

import {
  Alert as BaseAlert,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";

type AlertColor = "default" | "success" | "warning" | "danger" | "primary";
type AlertVariant = "solid" | "flat" | "faded" | "bordered";

interface AlertProps
  extends Omit<React.ComponentProps<"div">, "color" | "title"> {
  color?: AlertColor;
  description?: React.ReactNode;
  title?: React.ReactNode;
  variant?: AlertVariant;
}

function mapVariant(color: AlertColor) {
  if (color === "danger") {
    return "destructive" as const;
  }
  if (color === "success") {
    return "success" as const;
  }
  if (color === "warning") {
    return "warning" as const;
  }

  return "default" as const;
}

export function Alert({
  children,
  color = "default",
  description,
  title,
  // Accepted for HeroUI API compatibility; Round 4 alerts are always
  // rendered on the opaque tray, so the variant no longer alters the fill.
  variant: _variant,
  ...props
}: AlertProps) {
  return (
    <BaseAlert
      // Round 4: the old flat variant forced a 15% bg opacity (translucent
      // alert text over glass); alerts always ride the opaque tray now.
      className={undefined}
      variant={mapVariant(color)}
      {...props}
    >
      {title ? <AlertTitle>{title}</AlertTitle> : null}
      {description ? <AlertDescription>{description}</AlertDescription> : null}
      {!description && !title ? children : null}
    </BaseAlert>
  );
}
