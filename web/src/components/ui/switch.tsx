import { Switch as SwitchPrimitive } from "radix-ui";
import * as React from "react";

import { cn } from "@/lib/utils";

function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        "peer relative inline-flex h-[1.875rem] w-[3.125rem] shrink-0 items-center rounded-full p-[2px] transition-colors duration-200 outline-none",
        "before:absolute before:-inset-2 before:content-['']",
        "focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-45",
        "data-[state=checked]:bg-success data-[state=unchecked]:bg-input",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className="pointer-events-none block size-[1.625rem] rounded-full bg-white shadow-[0_2px_6px_rgb(0_0_0/0.2)] ring-0 transition-transform duration-200 ease-[var(--ease-gallery)] data-[state=checked]:translate-x-5 data-[state=unchecked]:translate-x-0"
      />
    </SwitchPrimitive.Root>
  );
}

export { Switch };
