import { useMemo } from "react";
import { encode } from "uqr";

/** Renders a QR code as crisp SVG, entirely client-side. */
export function QrCode({ value, label, className }: { value: string; label: string; className?: string }) {
  const path = useMemo(() => {
    const { data, size } = encode(value, { ecc: "M", border: 0 });
    let d = "";
    for (let y = 0; y < size; y++) {
      for (let x = 0; x < size; x++) if (data[y][x]) d += `M${x} ${y}h1v1h-1z`;
    }
    return { d, size };
  }, [value]);

  return (
    <svg viewBox={`-2 -2 ${path.size + 4} ${path.size + 4}`} role="img" aria-label={label} className={className} shapeRendering="crispEdges">
      <rect x="-2" y="-2" width={path.size + 4} height={path.size + 4} fill="#fff" />
      <path d={path.d} fill="#111" />
    </svg>
  );
}
