import { ImageOff } from "lucide-react";
import { useState } from "react";

import { cn } from "@/lib/utils";

/** A thumbnail that fades in once loaded and degrades quietly when the TV or a source can't supply it. */
export function ArtImage({
  src,
  alt,
  className,
  imgClassName,
  eager = false,
}: {
  src: string;
  alt: string;
  className?: string;
  imgClassName?: string;
  eager?: boolean;
}) {
  const [state, setState] = useState<{ src: string; status: "loading" | "loaded" | "error" }>({ src, status: "loading" });
  const status = state.src === src ? state.status : "loading";

  return (
    <div className={cn("relative overflow-hidden bg-muted", className)}>
      {status === "error" ? (
        <div className="absolute inset-0 flex items-center justify-center text-muted-foreground">
          <ImageOff className="size-5" aria-hidden="true" />
          <span className="sr-only">{alt} (thumbnail unavailable)</span>
        </div>
      ) : (
        <img
          src={src}
          alt={alt}
          loading={eager ? "eager" : "lazy"}
          decoding="async"
          draggable={false}
          onLoad={() => setState({ src, status: "loaded" })}
          onError={() => setState({ src, status: "error" })}
          className={cn(
            "absolute inset-0 size-full object-cover transition-opacity duration-500 ease-out",
            status === "loaded" ? "opacity-100" : "opacity-0",
            imgClassName,
          )}
        />
      )}
    </div>
  );
}
