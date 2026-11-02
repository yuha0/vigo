import { ToggleGroup as ToggleGroupPrimitive } from "radix-ui";
import * as React from "react";

import { cn } from "@/lib/utils";

type SegmentedProps = {
  value: string;
  onValueChange: (value: string) => void;
  options: { value: string; label: React.ReactNode }[];
  disabled?: boolean;
  className?: string;
  "aria-label"?: string;
};

function Segmented({ value, onValueChange, options, disabled, className, ...rest }: SegmentedProps) {
  return (
    <ToggleGroupPrimitive.Root
      type="single"
      value={value}
      disabled={disabled}
      onValueChange={(v) => v && onValueChange(v)}
      aria-label={rest["aria-label"]}
      className={cn("inline-flex w-full rounded-full bg-muted p-1 data-[disabled]:opacity-45", className)}
    >
      {options.map((o) => (
        <ToggleGroupPrimitive.Item
          key={o.value}
          value={o.value}
          className="flex h-9 min-w-11 flex-1 items-center justify-center rounded-full px-3 text-sm font-medium text-muted-foreground transition-all duration-200 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 data-[state=on]:bg-card data-[state=on]:text-foreground data-[state=on]:shadow-[0_1px_3px_rgb(0_0_0/0.12)] [@media(pointer:coarse)]:h-[2.375rem]"
        >
          {o.label}
        </ToggleGroupPrimitive.Item>
      ))}
    </ToggleGroupPrimitive.Root>
  );
}

export { Segmented };
