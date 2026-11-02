import { Slider as SliderPrimitive } from "radix-ui";
import * as React from "react";

import { cn } from "@/lib/utils";

function Slider({ className, ...props }: React.ComponentProps<typeof SliderPrimitive.Root>) {
  const values = props.value ?? props.defaultValue ?? [props.min ?? 0];
  return (
    <SliderPrimitive.Root
      data-slot="slider"
      className={cn(
        "relative flex w-full touch-none items-center py-3 select-none data-[disabled]:opacity-45",
        className,
      )}
      {...props}
    >
      <SliderPrimitive.Track className="relative h-1.5 w-full grow overflow-hidden rounded-full bg-input">
        <SliderPrimitive.Range className="absolute h-full bg-primary" />
      </SliderPrimitive.Track>
      {values.map((_, i) => (
        <SliderPrimitive.Thumb
          key={i}
          aria-label={props["aria-label"]}
          className="relative block size-6 rounded-full border border-black/5 bg-white shadow-[0_2px_8px_rgb(0_0_0/0.18)] transition-[box-shadow,transform] outline-none before:absolute before:-inset-2.5 before:content-[''] hover:ring-4 hover:ring-ring/20 focus-visible:ring-4 focus-visible:ring-ring/40 active:scale-110"
        />
      ))}
    </SliderPrimitive.Root>
  );
}

export { Slider };
