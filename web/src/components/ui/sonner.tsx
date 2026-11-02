import { CircleAlert, CircleCheck, Info, LoaderCircle, TriangleAlert } from "lucide-react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

function Toaster(props: ToasterProps) {
  return (
    <Sonner
      theme="system"
      position="top-center"
      offset={{ top: "calc(env(safe-area-inset-top, 0px) + 12px)" }}
      mobileOffset={{ top: "calc(env(safe-area-inset-top, 0px) + 8px)" }}
      icons={{
        success: <CircleCheck className="size-[1.125rem] text-success" />,
        info: <Info className="size-[1.125rem]" />,
        warning: <TriangleAlert className="size-[1.125rem] text-warning" />,
        error: <CircleAlert className="size-[1.125rem] text-destructive" />,
        loading: <LoaderCircle className="size-[1.125rem] animate-spin" />,
      }}
      toastOptions={{
        classNames: {
          toast:
            "!rounded-2xl !border-border/70 !bg-popover/90 !text-popover-foreground !shadow-xl !backdrop-blur-xl !font-sans !gap-3 !py-3.5",
          description: "!text-muted-foreground",
          title: "!font-medium",
        },
      }}
      {...props}
    />
  );
}

export { Toaster };
